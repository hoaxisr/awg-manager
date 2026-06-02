// Package nwg provides OperatorNativeWG — manages tunnels via Keenetic's
// native WireGuard interface + awg_proxy.ko kernel module for obfuscation.
//
// Architecture: NDMS creates/manages the WireGuard interface natively.
// awg_proxy.ko creates a per-tunnel UDP proxy: WG sends to 127.0.0.1:proxy_port,
// the proxy transforms packets and forwards to the real AWG server (and vice versa).
package nwg

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/payloads"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

const (
	resolveAttempts       = 3
	resolveAttemptTimeout = 1500 * time.Millisecond
	resolveRetryGap       = 300 * time.Millisecond
)

// OperatorNativeWG manages tunnels via Keenetic native WireGuard + awg_proxy.ko.
type OperatorNativeWG struct {
	queries      *query.Queries
	commands     *command.Commands
	transport    *transport.Client
	kmod         *KmodManager
	appLog       *logging.ScopedLogger
	hookNotifier tunnel.HookNotifier

	// resolveFn resolves "host:port" to (ip, port). Defaults to
	// netutil.ResolveEndpoint; overridable in tests.
	resolveFn func(endpoint string) (string, int, error)

	// trackedIP holds the last freshly-resolved endpoint IP per tunnel ID
	// (NOT cache fallbacks). The orchestrator reads it via GetTrackedEndpointIP
	// to persist storage.AWGTunnel.ResolvedEndpointIP.
	trackedMu sync.RWMutex
	trackedIP map[string]string

	// supportsASC reports native ASC firmware support. Default:
	// ndmsinfo.SupportsWireguardASC; overridable in tests.
	supportsASC func() bool
	// hasProxySlot reports a live kmod proxy slot on a listen port. Default:
	// kmod.HasSlotListening; overridable in tests.
	hasProxySlot func(listenPort int) bool
}

// NewOperator creates a new NativeWG operator.
func NewOperator(queries *query.Queries, commands *command.Commands, tr *transport.Client, appLogger logging.AppLogger) *OperatorNativeWG {
	op := &OperatorNativeWG{
		queries:     queries,
		commands:    commands,
		transport:   tr,
		kmod:        NewKmodManager(appLogger),
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubOps),
		resolveFn:   netutil.ResolveEndpoint,
		supportsASC: ndmsinfo.SupportsWireguardASC,
	}
	op.hasProxySlot = op.kmod.HasSlotListening
	return op
}

// SetHookNotifier sets the hook notifier for registering expected NDMS hooks.
func (o *OperatorNativeWG) SetHookNotifier(hn tunnel.HookNotifier) {
	o.hookNotifier = hn
}

// Create creates a NativeWG tunnel in NDMS.
// Returns the assigned NWGIndex.
// Accepts both AWG and plain WireGuard configs — plain WG can be edited later
// to add obfuscation params, but Start() will block until they are set.
func (o *OperatorNativeWG) Create(ctx context.Context, stored *storage.AWGTunnel) (index int, err error) {
	if ndmsinfo.SupportsHRanges() {
		return o.createViaImport(ctx, stored)
	}
	return o.createViaBatch(ctx, stored)
}

// createViaImport creates a tunnel by importing a .conf file (firmware >= 5.01.A.3).
// NDMS fully parses AWG params (Jc, Jmin, S1, H1 etc.) from the .conf file.
func (o *OperatorNativeWG) createViaImport(ctx context.Context, stored *storage.AWGTunnel) (int, error) {
	// Generate .conf with all AWG params
	confData := config.GenerateForExport(stored)

	// Import via RCI — NDMS creates the interface and parses all params.
	// ImportWireguardConfig is a multipart-upload helper with no new-layer equivalent yet.
	res, err := o.commands.Wireguard.ImportWireguardConfig(ctx, []byte(confData), stored.Name+".conf")
	if err != nil {
		return 0, fmt.Errorf("import wireguard config: %w", err)
	}
	ndmsName := res.Created

	// The router may create the interface yet report that its config collides
	// with an existing one (same keys) — surface it so the context is not lost.
	if res.Intersects != "" {
		o.appLog.Warn("create", stored.Name,
			fmt.Sprintf("imported %s intersects existing %s; status: %s",
				ndmsName, res.Intersects, strings.Join(res.Messages, "; ")))
	}

	// Extract index from "WireguardN"
	idx, _, err := ParseNDMSCreatedName(`"` + ndmsName + `" interface created`)
	if err != nil {
		// Try direct parse: "Wireguard0" -> 0
		numStr := strings.TrimPrefix(ndmsName, "Wireguard")
		idx, err = strconv.Atoi(numStr)
		if err != nil {
			return 0, fmt.Errorf("parse imported interface name %q: %w", ndmsName, err)
		}
	}

	// Post-import settings that aren't in .conf
	cmds := []any{
		payloads.CmdInterfaceDescription(ndmsName, stored.Name),
		payloads.CmdInterfaceSecurityLevel(ndmsName, "public"),
		payloads.CmdInterfaceIPGlobal(ndmsName, true),
		payloads.CmdInterfaceAdjustMSS(ndmsName, true),
		payloads.CmdSave(),
	}

	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		// Cleanup on failure
		cleanup := []any{
			payloads.CmdInterfaceDelete(ndmsName),
			payloads.CmdSave(),
		}
		_, _ = o.transport.PostBatch(ctx, cleanup)
		return 0, fmt.Errorf("post-import settings: %w", err)
	}

	// Keep the interface cache coherent with the freshly-imported interface
	// (same rationale as the batch path — issue #255).
	o.queries.Interfaces.Invalidate(ndmsName)

	o.appLog.Full("create", stored.Name, fmt.Sprintf("Created NDMS interface %s via import", ndmsName))
	o.appLog.Info("create", ndmsName, "via import path")
	return idx, nil
}

// createViaBatch creates a tunnel via RCI batch commands (firmware < 5.01.A.3).
func (o *OperatorNativeWG) createViaBatch(ctx context.Context, stored *storage.AWGTunnel) (int, error) {
	idx, err := o.nextFreeIndex(ctx)
	if err != nil {
		return 0, fmt.Errorf("find free index: %w", err)
	}

	names := NewNWGNames(idx)
	ndmsName := names.NDMSName

	// Resolve endpoint hostname -> IP (for validation only at create time;
	// the actual proxy endpoint is set at Start time). Uses retry + cache fallback.
	endpointIP, endpointPort, err := o.resolveEndpointWithFallback(stored)
	if err != nil {
		return 0, fmt.Errorf("resolve endpoint: %w", err)
	}

	cmds := []any{
		payloads.CmdInterfaceCreate(ndmsName),
		payloads.CmdInterfaceDescription(ndmsName, stored.Name),
		payloads.CmdInterfaceSecurityLevel(ndmsName, "public"),
		payloads.CmdInterfaceIPAddress(ndmsName, extractIPv4(stored.Interface.Address), "255.255.255.255"),
		payloads.CmdInterfaceMTU(ndmsName, stored.Interface.MTU),
		payloads.CmdInterfaceAdjustMSS(ndmsName, true),
		payloads.CmdInterfaceIPGlobal(ndmsName, true),
		payloads.CmdWireguardPrivateKey(ndmsName, stored.Interface.PrivateKey),
	}

	// DNS
	if stored.Interface.DNS != "" {
		var servers []string
		for _, dns := range strings.Split(stored.Interface.DNS, ",") {
			if d := strings.TrimSpace(dns); d != "" {
				servers = append(servers, d)
			}
		}
		if len(servers) > 0 {
			cmds = append(cmds, payloads.CmdInterfaceDNS(ndmsName, servers))
		}
	}

	// IPv6 if present
	ipv6Addr := extractIPv6(stored.Interface.Address)
	if ipv6Addr != "" {
		cmds = append(cmds, payloads.CmdInterfaceIPv6Address(ndmsName, ipv6Addr))
	}

	// Peer
	peerCfg := payloads.PeerConfig{
		PublicKey:   stored.Peer.PublicKey,
		Endpoint:    fmt.Sprintf("%s:%d", endpointIP, endpointPort),
		AllowedIPv4: []payloads.AllowedIP{{Address: "0.0.0.0", Mask: "0"}},
	}
	if hasIPv6AllowedIPs(stored.Peer.AllowedIPs) {
		peerCfg.AllowedIPv6 = []payloads.AllowedIP{{Address: "::", Mask: "0"}}
	}
	if stored.Peer.PersistentKeepalive > 0 {
		peerCfg.KeepaliveInterval = stored.Peer.PersistentKeepalive
	}
	if stored.Peer.PresharedKey != "" {
		peerCfg.PresharedKey = stored.Peer.PresharedKey
	}
	cmds = append(cmds, payloads.CmdWireguardPeer(ndmsName, peerCfg), payloads.CmdSave())

	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		// Cleanup on failure
		cleanup := []any{
			payloads.CmdInterfaceDelete(ndmsName),
			payloads.CmdSave(),
		}
		_, _ = o.transport.PostBatch(ctx, cleanup)
		return 0, fmt.Errorf("create batch: %w", err)
	}

	// Set AWG obfuscation params via RCI (firmware >= 5.1Alpha4).
	// Non-fatal: kmod proxy handles actual obfuscation regardless.
	if ndmsinfo.SupportsWireguardASC() {
		if ascJSON, err := buildASCJSON(&stored.Interface); err == nil && ascJSON != nil {
			if err := o.commands.Wireguard.SetASCParams(ctx, ndmsName, ascJSON); err != nil {
				o.appLog.Warn("set-asc-params", "", "RCI failed (non-fatal): "+err.Error())
			}
		}
	}

	// Refresh the interface cache so the next nextFreeIndex sees this slot
	// as occupied. Without it, back-to-back creates (no Start in between)
	// re-read the stale map and allocate the same index — issue #255.
	o.queries.Interfaces.OnCreated(ctx, ndmsName)

	o.appLog.Full("create", stored.Name, fmt.Sprintf("Creating NDMS interface %s", ndmsName))
	o.appLog.Info("create", ndmsName, "interface created")
	return idx, nil
}

// Start starts a NativeWG tunnel.
//
// Requires AWG obfuscation parameters to be set — plain WireGuard configs
// must be edited first to add Jc/H/S/I values before starting.
//
// On firmware >= 5.01.A.4 (native ASC): peer endpoint is set to the real server
// address — NDMS handles obfuscation natively. ASC params are synced from storage
// on every start (they may have been added/changed via the edit form after Create).
//
// On older firmware: awg_proxy.ko creates a local UDP proxy, peer endpoint is
// set to 127.0.0.1:proxy_port, and the proxy forwards obfuscated traffic.
func (o *OperatorNativeWG) Start(ctx context.Context, stored *storage.AWGTunnel) error {
	// Block plain WireGuard configs — user must add AWG obfuscation params first
	if !config.IsAWGObfuscated(&stored.Interface) {
		return tunnel.ErrNotObfuscated
	}

	if ndmsinfo.SupportsWireguardASC() {
		return o.startNative(ctx, stored)
	}
	return o.startProxy(ctx, stored)
}

// startNative starts a tunnel on firmware with native ASC support (>= 5.01.A.4).
// No awg_proxy needed — NDMS handles obfuscation via ASC params.
func (o *OperatorNativeWG) startNative(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)
	pubkey := stored.Peer.PublicKey

	// Sync ASC params from storage to NDMS — they may have been added/changed
	// via the edit form after the initial Create (e.g. imported as plain WG, then edited).
	o.appLog.Full("start", stored.Name, "Syncing ASC params to NDMS")
	if ascJSON, err := buildASCJSON(&stored.Interface); err == nil && ascJSON != nil {
		if err := o.commands.Wireguard.SetASCParams(ctx, names.NDMSName, ascJSON); err != nil {
			o.appLog.Warn("sync-asc", names.NDMSName, err.Error())
		}
	}

	// Resolve endpoint (retry + cached IP fallback if DNS unavailable at boot)
	endpointIP, endpointPort, err := o.resolveEndpointWithFallback(stored)
	if err != nil {
		return err
	}
	o.appLog.Full("start", stored.Name, fmt.Sprintf("Resolving endpoint %s -> %s:%d", stored.Peer.Endpoint, endpointIP, endpointPort))

	realEndpoint := fmt.Sprintf("%s:%d", endpointIP, endpointPort)

	// Sync address/MTU from storage
	if err := o.SyncAddressMTU(ctx, stored); err != nil {
		o.appLog.Warn("sync-address-mtu", names.NDMSName, "on start: "+err.Error())
	}

	// Register DNS servers with the router's DNS proxy
	if err := o.SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS)); err != nil {
		o.appLog.Warn("apply-dns", names.NDMSName, err.Error())
	}

	o.appLog.Full("start", stored.Name, "Setting peer endpoint, interface up")
	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(names.NDMSName, "running")
	}

	// Batch: set endpoint + connect via + up
	cmds := []any{
		payloads.CmdWireguardPeerEndpoint(names.NDMSName, pubkey, realEndpoint),
		payloads.CmdWireguardPeerConnect(names.NDMSName, pubkey, stored.ISPInterface),
		payloads.CmdInterfaceUp(names.NDMSName, true),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		return fmt.Errorf("start native: %w", err)
	}

	viaInfo := ""
	if stored.ISPInterface != "" {
		viaInfo = " via " + stored.ISPInterface
	}
	o.appLog.Info("start", names.NDMSName, fmt.Sprintf("native ASC, endpoint %s%s", realEndpoint, viaInfo))
	return nil
}

// startProxy starts a tunnel on older firmware via awg_proxy.ko.
// Peer endpoint is redirected to 127.0.0.1:proxy_port.
func (o *OperatorNativeWG) startProxy(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)
	pubkey := stored.Peer.PublicKey

	// Resolve endpoint — kmod proxy connects to this IP.
	// Retry + cached IP fallback if DNS unavailable at boot.
	endpointIP, endpointPort, err := o.resolveEndpointWithFallback(stored)
	if err != nil {
		return err
	}

	// Ensure kernel module is loaded
	o.appLog.Full("start", stored.Name, "Loading kmod proxy")
	if err := o.kmod.EnsureLoaded(); err != nil {
		return fmt.Errorf("kmod: %w", err)
	}

	// Read peer "via" from RCI (NDMS WAN binding) -> resolve to kernel iface
	bindIface := o.ResolveActiveWAN(ctx, stored)

	// Add tunnel to kernel module -> creates proxy, returns listen_port
	kmodCfg, err := buildKmodConfigResolved(stored, endpointIP, endpointPort, bindIface)
	if err != nil {
		return fmt.Errorf("build kmod config: %w", err)
	}
	result, err := o.kmod.AddTunnel(stored.ID, kmodCfg)
	if err != nil {
		return fmt.Errorf("kmod add: %w", err)
	}
	if result.Adopted {
		o.appLog.Full("start", stored.Name, fmt.Sprintf("Using existing kmod proxy, listen port %d", result.ListenPort))
	} else {
		o.appLog.Full("start", stored.Name, fmt.Sprintf("Adding tunnel to kmod, listen port %d", result.ListenPort))
	}
	o.appLog.Debug("start", stored.Name, fmt.Sprintf("Kmod proxy %s:%d -> 127.0.0.1:%d, bind=%s", endpointIP, endpointPort, result.ListenPort, bindIface))

	proxyEndpoint := fmt.Sprintf("127.0.0.1:%d", result.ListenPort)

	// Sync address/MTU from storage
	if err := o.SyncAddressMTU(ctx, stored); err != nil {
		o.appLog.Warn("sync-address-mtu", names.NDMSName, "on start: "+err.Error())
	}

	// Register DNS servers with the router's DNS proxy
	if err := o.SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS)); err != nil {
		o.appLog.Warn("apply-dns", names.NDMSName, err.Error())
	}

	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(names.NDMSName, "running")
	}

	// Batch: set proxy endpoint + connect + up
	cmds := []any{
		payloads.CmdWireguardPeerEndpoint(names.NDMSName, pubkey, proxyEndpoint),
		payloads.CmdWireguardPeerConnect(names.NDMSName, pubkey, stored.ISPInterface),
		payloads.CmdInterfaceUp(names.NDMSName, true),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		_ = o.kmod.RemoveTunnel(stored.ID)
		return fmt.Errorf("start proxy: %w", err)
	}

	viaInfo := ""
	if stored.ISPInterface != "" {
		viaInfo = " via " + stored.ISPInterface
	}
	o.appLog.Info("start", names.NDMSName, fmt.Sprintf("proxy %s -> %s:%d%s", proxyEndpoint, endpointIP, endpointPort, viaInfo))
	return nil
}

// SuspendProxy disconnects the peer and removes kmod proxy entry.
// Called on WAN down for firmware < 5.01.A.3 (proxy mode).
// Preserves NDMS intent (conf stays "running") — peer goes to link: pending.
// Does NOT call InterfaceDown (that would set conf: disabled, losing user intent).
// Does NOT clear DNS (interface stays up in NDMS, DNS binding is intact).
// Resume path: call Start() which routes through startProxy().
func (o *OperatorNativeWG) SuspendProxy(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)
	pubkey := stored.Peer.PublicKey

	// 1. Remove kmod proxy entry (socket is dead after WAN down anyway).
	// Module stays loaded — only the tunnel entry is removed.
	_ = o.kmod.RemoveTunnel(stored.ID)

	// 2. Disconnect peer — NDMS sets link: pending, connected: no.
	// conf stays "running" so NDMS knows the tunnel wants to be up.
	cmds := []any{
		payloads.CmdWireguardPeerDisconnect(names.NDMSName, pubkey),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		o.appLog.Warn("suspend", names.NDMSName, "peer disconnect: "+err.Error())
		return fmt.Errorf("peer disconnect: %w", err)
	}

	o.appLog.Info("suspend", stored.Name, "Proxy suspended (WAN down)")
	o.appLog.Info("suspend", names.NDMSName, "proxy suspended")
	return nil
}

// Stop stops a NativeWG tunnel: interface down -> kmod remove (proxy only).
// Peer binding is not reset here — Start sets it fresh via WireguardPeerConnect.
func (o *OperatorNativeWG) Stop(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)

	o.appLog.Full("stop", stored.Name, "Interface down")

	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(names.NDMSName, "disabled")
	}
	cmds := []any{
		payloads.CmdInterfaceUp(names.NDMSName, false),
		payloads.CmdSave(),
	}
	_, _ = o.transport.PostBatch(ctx, cmds)

	// Clear DNS servers from the router's DNS proxy
	if err := o.SyncDNS(ctx, stored, tunnel.ParseDNSList(stored.Interface.DNS), nil); err != nil {
		o.appLog.Warn("clear-dns", names.NDMSName, err.Error())
	}
	o.appLog.Full("stop", stored.Name, "DNS cleared")

	// Only remove kmod proxy entry on older firmware
	if !ndmsinfo.SupportsWireguardASC() {
		_ = o.kmod.RemoveTunnel(stored.ID)
	}

	o.appLog.Info("stop", names.NDMSName, "tunnel stopped")
	return nil
}

// Delete removes a NativeWG tunnel from NDMS completely.
func (o *OperatorNativeWG) Delete(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)

	// 1. Remove kmod proxy entry (older firmware only, before interface deletion)
	if !ndmsinfo.SupportsWireguardASC() {
		_ = o.kmod.RemoveTunnel(stored.ID)
	}

	// 2. Remove ping-check profile (before interface deletion)
	if stored.PingCheck != nil && stored.PingCheck.Enabled {
		_ = o.RemovePingCheck(ctx, stored)
	}

	// 3. Remove NDMS interface — cleans everything:
	//    peer, DNS (ip + ipv6 name-server), ASC params, kernel Wireguard interface
	_, _ = o.transport.Post(ctx, payloads.CmdInterfaceDelete(names.NDMSName))

	// 4. Persist
	_, _ = o.transport.Post(ctx, payloads.CmdSave())

	// 5. Free the slot in the interface cache so the index can be reused
	// without an AWGM restart — issue #255.
	o.queries.Interfaces.OnDestroyed(names.NDMSName)

	o.appLog.Info("delete", names.NDMSName, "tunnel deleted")
	return nil
}

// classifyNWGState decides the tunnel State for a NativeWG interface from parsed
// RCI state. For the awg_proxy path (no ASC), conf=running with an offline peer is
// StateBroken when the config is incoherent — NDMS peer not pointing at 127.0.0.1
// or no live kmod slot on the peer's remote-port — otherwise StateStarting.
// hasProxySlot is only consulted on the proxy path with an offline peer.
func classifyNWGState(rci NWGState, supportsASC bool, hasProxySlot func(listenPort int) bool) tunnel.State {
	switch {
	case rci.ConfLayer == "running" && rci.PeerOnline:
		return tunnel.StateRunning
	case rci.ConfLayer == "running" && !rci.PeerOnline:
		if supportsASC {
			return tunnel.StateStarting
		}
		if rci.PeerRemoteAddr != "127.0.0.1" || !hasProxySlot(rci.PeerRemotePort) {
			return tunnel.StateBroken
		}
		return tunnel.StateStarting
	case rci.ConfLayer == "disabled":
		return tunnel.StateStopped
	default:
		return tunnel.StateUnknown
	}
}

// GetState returns the state of a NativeWG tunnel via RCI.
// KmodManager does NOT participate in state detection — RCI is the single source of truth.
func (o *OperatorNativeWG) GetState(ctx context.Context, stored *storage.AWGTunnel) tunnel.StateInfo {
	names := NewNWGNames(stored.NWGIndex)

	body, err := o.transport.GetRaw(ctx, "/show/interface/"+names.NDMSName)
	if err != nil {
		return tunnel.StateInfo{State: tunnel.StateNotCreated}
	}

	rciState, err := parseRCIInterfaceResponse(body)
	if err != nil || !rciState.Exists {
		return tunnel.StateInfo{State: tunnel.StateNotCreated}
	}

	info := tunnel.StateInfo{
		OpkgTunExists: true,
		InterfaceUp:   rciState.LinkUp,
		HasPeer:       true, // always configured for nativewg
		RxBytes:       rciState.RxBytes,
		TxBytes:       rciState.TxBytes,
		BackendType:   "nativewg",
		ConnectedAt:   rciState.Connected,
		PeerVia:       rciState.PeerVia,
	}

	// Parse handshake: RCI returns seconds since last handshake, not unix timestamp.
	if rciState.LastHandshake > 0 && rciState.LastHandshake < neverHandshake {
		info.HasHandshake = true
		info.LastHandshake = time.Now().Add(-time.Duration(rciState.LastHandshake) * time.Second)
	}

	o.appLog.Debug("state", stored.Name, fmt.Sprintf("RCI state: conf=%s link=%v peer=%v", rciState.ConfLayer, rciState.LinkUp, rciState.PeerOnline))

	// State (see classifyNWGState):
	//   running & peer online                         -> Running
	//   running & peer offline & proxy & incoherent   -> Broken
	//   running & peer offline (coherent / ASC)        -> Starting
	//   disabled                                       -> Stopped
	info.State = classifyNWGState(rciState, o.supportsASC(), o.hasProxySlot)

	return info
}

// pingCheckProfile returns the profile name for a tunnel: "awgm-<tunnelID>".
func pingCheckProfile(tunnelID string) string {
	return "awgm-" + tunnelID
}

// ConfigurePingCheck creates/updates a ping-check profile for a tunnel.
func (o *OperatorNativeWG) ConfigurePingCheck(ctx context.Context, stored *storage.AWGTunnel, cfg ndms.PingCheckConfig) error {
	profile := pingCheckProfile(stored.ID)
	ifaceName := NewNWGNames(stored.NWGIndex).NDMSName
	o.appLog.Info("configure-pingcheck", profile, fmt.Sprintf("iface=%s host=%s mode=%s", ifaceName, cfg.Host, cfg.Mode))
	if err := o.commands.PingCheck.ConfigureProfile(ctx, profile, ifaceName, cfg); err != nil {
		o.appLog.Warn("configure-pingcheck", profile, err.Error())
		return err
	}
	return nil
}

// RemovePingCheck removes the ping-check profile for a tunnel.
func (o *OperatorNativeWG) RemovePingCheck(ctx context.Context, stored *storage.AWGTunnel) error {
	profile := pingCheckProfile(stored.ID)
	ifaceName := NewNWGNames(stored.NWGIndex).NDMSName
	return o.commands.PingCheck.RemoveProfile(ctx, profile, ifaceName)
}

// GetPingCheckStatus returns the current ping-check status for a tunnel.
//
// The return type is the legacy (nested, profile-oriented) PingCheckStatus
// kept for API compatibility with callers in api/pingcheck.go and
// pingcheck/facade.go. Internally we compose it from the new flattened
// query stores (PingCheckProfile.List + PingCheckStatus.List).
func (o *OperatorNativeWG) GetPingCheckStatus(ctx context.Context, stored *storage.AWGTunnel) (*ndms.PingCheckProfileStatus, error) {
	profile := pingCheckProfile(stored.ID)
	ifaceName := NewNWGNames(stored.NWGIndex).NDMSName

	status := &ndms.PingCheckProfileStatus{Exists: false}

	profiles, perr := o.queries.PingCheckProfile.List(ctx)
	if perr != nil {
		o.appLog.Warn("list-profiles", "", perr.Error())
	} else {
		for _, p := range profiles {
			if p.Profile == profile {
				status.Exists = true
				if len(p.Host) > 0 {
					status.Host = p.Host[0]
				}
				status.Mode = p.Mode
				status.Interval = p.UpdateInterval
				status.MaxFails = p.MaxFails
				status.MinSuccess = p.MinSuccess
				status.Timeout = p.Timeout
				status.Port = p.Port
				break
			}
		}
	}

	if status.Exists {
		statuses, serr := o.queries.PingCheckStatus.List(ctx)
		if serr != nil {
			o.appLog.Warn("list-statuses", "", serr.Error())
		} else {
			for _, s := range statuses {
				if s.Profile == profile && s.Interface == ifaceName {
					status.Bound = true
					status.Status = s.Status
					status.SuccessCount = s.SuccessCount
					status.FailCount = s.FailCount
					break
				}
			}
		}
	}

	// Restart and MinSuccess: use storage as source of truth.
	// NDMS /show/ping-check/ doesn't expose these fields in a reliable way
	// (min-success is simply omitted from the profile response even when
	// applied — confirmed on live router), and we already persist both
	// settings when configuring ping-check.
	//
	// When the NDMS profile doesn't exist (monitoring disabled), also
	// overlay the rest of the fields from storage so the settings modal
	// can pre-fill with the user's last-saved values on re-enable.
	// When Exists=true we leave NDMS values alone — it's the live source
	// of truth if anyone edited the profile via the router's web UI.
	if stored.PingCheck != nil {
		status.Restart = stored.PingCheck.Restart
		status.MinSuccess = stored.PingCheck.MinSuccess
		if !status.Exists {
			status.Host = stored.PingCheck.Target
			status.Mode = stored.PingCheck.Method
			status.Interval = stored.PingCheck.Interval
			status.MaxFails = stored.PingCheck.FailThreshold
			status.Timeout = stored.PingCheck.Timeout
			status.Port = stored.PingCheck.Port
		}
	}
	return status, nil
}

// EnsureKmodLoaded loads awg_proxy.ko (or reloads if version changed).
func (o *OperatorNativeWG) EnsureKmodLoaded() error {
	return o.kmod.EnsureLoaded()
}

// RestoreKmodTunnel adds a tunnel entry to the already-loaded kmod and updates
// the NDMS peer endpoint to use the proxy address (127.0.0.1:listen_port).
// Called at boot for enabled tunnels that are already running in NDMS.
func (o *OperatorNativeWG) RestoreKmodTunnel(ctx context.Context, stored *storage.AWGTunnel) error {
	bindIface := o.ResolveActiveWAN(ctx, stored)

	// Resolve with retry + cached IP fallback — boot DNS on the router may be
	// slow/uncached (this is the path that previously failed hard).
	endpointIP, endpointPort, err := o.resolveEndpointWithFallback(stored)
	if err != nil {
		return fmt.Errorf("build kmod config: %w", err)
	}
	kmodCfg, err := buildKmodConfigResolved(stored, endpointIP, endpointPort, bindIface)
	if err != nil {
		return fmt.Errorf("build kmod config: %w", err)
	}
	result, err := o.kmod.RestoreTunnel(stored.ID, kmodCfg)
	if err != nil {
		return err
	}

	// Update NDMS peer endpoint to proxy address
	names := NewNWGNames(stored.NWGIndex)
	proxyEndpoint := fmt.Sprintf("127.0.0.1:%d", result.ListenPort)
	_, err = o.transport.Post(ctx, payloads.CmdWireguardPeerEndpoint(names.NDMSName, stored.Peer.PublicKey, proxyEndpoint))
	if err != nil {
		o.appLog.Warn("restore-kmod", names.NDMSName, "failed to update endpoint to "+proxyEndpoint+": "+err.Error())
	}

	return nil
}

// SyncKmodSlot rebuilds the awg_proxy.ko slot from the freshly-stored
// config and pushes the resulting listen port to the NDMS peer endpoint.
// Used by Service.applyDiffNWG when an Update changes a kmod-shaping
// field (PrivateKey, Peer.PublicKey, Peer.Endpoint, obfuscation) on a
// running tunnel — without this the slot keeps the pre-Update params
// silently, and the next daemon-restart RestoreTunnel would adopt the
// stale slot. AddTunnel always installs a fresh slot, EEXIST'ing the
// existing one out of the way; that's the rebuild we need.
//
// No-op on ASC-native firmware (no kmod slot exists).
func (o *OperatorNativeWG) SyncKmodSlot(ctx context.Context, stored *storage.AWGTunnel) error {
	if o.supportsASC() {
		return nil
	}
	bindIface := o.ResolveActiveWAN(ctx, stored)

	endpointIP, endpointPort, err := o.resolveEndpointWithFallback(stored)
	if err != nil {
		return fmt.Errorf("resolve endpoint: %w", err)
	}
	kmodCfg, err := buildKmodConfigResolved(stored, endpointIP, endpointPort, bindIface)
	if err != nil {
		return fmt.Errorf("build kmod config: %w", err)
	}

	result, err := o.kmod.AddTunnel(stored.ID, kmodCfg)
	if err != nil {
		return fmt.Errorf("kmod add: %w", err)
	}

	// listen port likely changed on rebuild — push it to NDMS so the
	// kernel WG peer points at the new local proxy.
	names := NewNWGNames(stored.NWGIndex)
	proxyEndpoint := fmt.Sprintf("127.0.0.1:%d", result.ListenPort)
	if _, err := o.transport.Post(ctx, payloads.CmdWireguardPeerEndpoint(names.NDMSName, stored.Peer.PublicKey, proxyEndpoint)); err != nil {
		o.appLog.Warn("sync-kmod-slot", names.NDMSName, "update peer endpoint to "+proxyEndpoint+": "+err.Error())
	}

	o.appLog.Info("sync-kmod-slot", names.NDMSName, fmt.Sprintf("slot rebuilt → 127.0.0.1:%d", result.ListenPort))
	return nil
}

// ResolveActiveWAN reads the peer "via" field from RCI and resolves the
// NDMS WAN name (e.g. "PPPoE0") to a kernel interface name (e.g. "ppp0").
// Returns empty string if no peer.via is set (= default routing) or if
// the RCI query fails. Used both for SO_BINDTODEVICE in the kmod proxy
// and for ActiveWAN tracking in the orchestrator.
func (o *OperatorNativeWG) ResolveActiveWAN(ctx context.Context, stored *storage.AWGTunnel) string {
	names := NewNWGNames(stored.NWGIndex)

	body, err := o.transport.GetRaw(ctx, "/show/interface/"+names.NDMSName)
	if err != nil {
		return ""
	}
	rciState, err := parseRCIInterfaceResponse(body)
	if err != nil || !rciState.Exists || rciState.PeerVia == "" {
		return ""
	}
	sysName := o.queries.Interfaces.ResolveSystemName(ctx, rciState.PeerVia)
	if sysName == "" || sysName == rciState.PeerVia {
		// ResolveSystemName failed to translate (e.g. /show/interface/system-name
		// unavailable on firmware < 4.1). Return "" so the kmod proxy socket
		// uses the default route instead of crashing with ENODEV.
		o.appLog.Warn("resolve-wan", names.NDMSName, "peer via "+rciState.PeerVia+": could not resolve kernel name")
		return ""
	}
	o.appLog.Info("resolve-wan", names.NDMSName, fmt.Sprintf("peer via %s -> kernel %s", rciState.PeerVia, sysName))
	return sysName
}

// nextFreeIndex finds the next available Wireguard index via cached InterfaceStore.
// Раньше делал direct GetRaw + parseRCIInterfaceList; теперь идёт через
// query.InterfaceStore.List() который кэширует bootstrap. Cold path (only
// at tunnel creation) — миграция нужна для чистой архитектуры, не perf.
func (o *OperatorNativeWG) nextFreeIndex(ctx context.Context) (int, error) {
	ifaces, err := o.queries.Interfaces.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list wireguard interfaces: %w", err)
	}

	used := make(map[int]bool)
	for _, iface := range ifaces {
		if !strings.EqualFold(iface.Type, "Wireguard") {
			continue
		}
		// Extract index from "WireguardN"
		if idx, _, err := ParseNDMSCreatedName(`"` + iface.ID + `" interface created`); err == nil {
			used[idx] = true
		}
	}

	for i := 0; i < MaxTunnels; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("all %d Wireguard slots are occupied", MaxTunnels)
}

// buildKmodConfigResolved builds a KmodConfig with a pre-resolved endpoint IP.
// bindIface is the kernel interface name for SO_BINDTODEVICE (empty = no binding).
func buildKmodConfigResolved(stored *storage.AWGTunnel, endpointIP string, endpointPort int, bindIface string) (KmodConfig, error) {
	return KmodConfig{
		EndpointIP:   endpointIP,
		EndpointPort: endpointPort,
		H1:           stored.Interface.H1, H2: stored.Interface.H2,
		H3: stored.Interface.H3, H4: stored.Interface.H4,
		S1: stored.Interface.S1, S2: stored.Interface.S2,
		S3: stored.Interface.S3, S4: stored.Interface.S4,
		Jc: stored.Interface.Jc, Jmin: stored.Interface.Jmin, Jmax: stored.Interface.Jmax,
		PubServerHex: pubKeyToHex(stored.Peer.PublicKey),
		PubClientHex: pubKeyToHex(clientPubKeyFromPrivate(stored.Interface.PrivateKey)),
		I1:           stored.Interface.I1, I2: stored.Interface.I2,
		I3: stored.Interface.I3, I4: stored.Interface.I4, I5: stored.Interface.I5,
		BindIface: bindIface,
	}, nil
}

// fallbackResolve uses the cached ResolvedEndpointIP from storage when DNS is unavailable
// (e.g. at boot when another tunnel's default route breaks DNS).
func (o *OperatorNativeWG) fallbackResolve(stored *storage.AWGTunnel, resolveErr error) (string, int, error) {
	if stored.ResolvedEndpointIP == "" {
		return "", 0, fmt.Errorf("resolve endpoint: %w (no cached IP)", resolveErr)
	}
	_, portStr, err := net.SplitHostPort(stored.Peer.Endpoint)
	if err != nil {
		return "", 0, fmt.Errorf("resolve endpoint: %w", resolveErr)
	}
	port, _ := strconv.Atoi(portStr)
	o.appLog.Warn("resolve-endpoint", stored.Peer.Endpoint, "DNS failed, using cached IP "+stored.ResolvedEndpointIP)
	return stored.ResolvedEndpointIP, port, nil
}

// resolveEndpointWithFallback resolves the tunnel's endpoint with a short retry
// budget, then falls back to the cached ResolvedEndpointIP. On a fresh DNS
// success it records the IP via trackEndpointIP (cache fallbacks are NOT tracked).
func (o *OperatorNativeWG) resolveEndpointWithFallback(stored *storage.AWGTunnel) (string, int, error) {
	var lastErr error
	for attempt := 1; attempt <= resolveAttempts; attempt++ {
		ip, port, err := o.resolveOnce(stored.Peer.Endpoint, resolveAttemptTimeout)
		if err == nil {
			o.trackEndpointIP(stored.ID, ip)
			return ip, port, nil
		}
		lastErr = err
		o.appLog.Debug("resolve-endpoint", stored.Peer.Endpoint,
			fmt.Sprintf("attempt %d/%d failed: %v", attempt, resolveAttempts, err))
		if attempt < resolveAttempts {
			time.Sleep(resolveRetryGap)
		}
	}
	return o.fallbackResolve(stored, lastErr)
}

// resolveOnce runs the injected resolver under a per-attempt timeout. A resolver
// that outlives the timeout is abandoned (its goroutine finishes and the result
// is discarded) and the attempt is reported as a timeout failure.
func (o *OperatorNativeWG) resolveOnce(endpoint string, timeout time.Duration) (string, int, error) {
	type result struct {
		ip   string
		port int
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		ip, port, err := o.resolveFn(endpoint)
		ch <- result{ip, port, err}
	}()
	select {
	case r := <-ch:
		return r.ip, r.port, r.err
	case <-time.After(timeout):
		return "", 0, fmt.Errorf("resolve %s: timeout after %s", endpoint, timeout)
	}
}

// trackEndpointIP records a freshly-resolved endpoint IP for a tunnel.
// Lazy-inits the map so struct-literal construction (tests) is safe.
func (o *OperatorNativeWG) trackEndpointIP(tunnelID, ip string) {
	o.trackedMu.Lock()
	defer o.trackedMu.Unlock()
	if o.trackedIP == nil {
		o.trackedIP = make(map[string]string)
	}
	o.trackedIP[tunnelID] = ip
}

// GetTrackedEndpointIP returns the last freshly-resolved endpoint IP for a
// tunnel, or "" if none has been resolved this process lifetime.
func (o *OperatorNativeWG) GetTrackedEndpointIP(tunnelID string) string {
	o.trackedMu.RLock()
	defer o.trackedMu.RUnlock()
	return o.trackedIP[tunnelID]
}

// splitAddressMask splits a CIDR or bare IP into (address, mask).
// - "10.0.0.2/32" → ("10.0.0.2", "255.255.255.255")
// - "10.0.0.2"    → ("10.0.0.2", "255.255.255.255")  (defaults to /32)
// Returns the original input as-is with a /32 mask if parsing fails.
func splitAddressMask(addr string) (string, string) {
	if addr == "" {
		return "", ""
	}
	cidr := addr
	if !strings.Contains(cidr, "/") {
		cidr += "/32"
	}
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return addr, "255.255.255.255"
	}
	return ip.String(), net.IP(ipNet.Mask).String()
}

// extractIPv4 extracts the bare IPv4 address from a WireGuard Address field
// which may contain comma-separated IPv4 and IPv6 (e.g. "172.16.0.2/32, 2606::1/128").
// Returns the IPv4 without CIDR suffix (caller provides mask separately for RCI).
func extractIPv4(addr string) string {
	for _, part := range strings.Split(addr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Strip existing CIDR for the check
		host := part
		if idx := strings.Index(part, "/"); idx != -1 {
			host = part[:idx]
		}
		// Skip IPv6
		if strings.Contains(host, ":") {
			continue
		}
		return host
	}
	return addr
}

// extractIPv6 extracts the IPv6 address from a WireGuard Address field
// which may contain comma-separated IPv4 and IPv6 (e.g. "172.16.0.2, 2606::1/128").
// Returns the bare IPv6 address WITHOUT CIDR suffix (SetIPv6Address adds /128).
func extractIPv6(addr string) string {
	for _, part := range strings.Split(addr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Strip CIDR suffix
		host := part
		if idx := strings.Index(part, "/"); idx != -1 {
			host = part[:idx]
		}
		// IPv6 contains ":"
		if strings.Contains(host, ":") {
			return host
		}
	}
	return ""
}

// hasIPv6AllowedIPs checks if AllowedIPs contains any IPv6 entry (e.g. "::/0").
func hasIPv6AllowedIPs(allowedIPs []string) bool {
	for _, ip := range allowedIPs {
		if strings.Contains(ip, ":") {
			return true
		}
	}
	return false
}

// buildASCJSON builds a json.RawMessage for SetASCParams from stored interface fields.
// Returns nil if the config is plain WireGuard (no obfuscation).
func buildASCJSON(iface *storage.AWGInterface) (json.RawMessage, error) {
	if !config.IsAWGObfuscated(iface) {
		return nil, nil
	}

	ver := config.ClassifyAWGVersion(iface)
	if ver == "awg1.5" || ver == "awg2.0" {
		params := ndms.ASCParamsExtended{
			ASCParams: ndms.ASCParams{
				Jc: iface.Jc, Jmin: iface.Jmin, Jmax: iface.Jmax,
				S1: iface.S1, S2: iface.S2,
				H1: iface.H1, H2: iface.H2, H3: iface.H3, H4: iface.H4,
			},
			S3: iface.S3, S4: iface.S4,
			I1: iface.I1, I2: iface.I2, I3: iface.I3, I4: iface.I4, I5: iface.I5,
		}
		return json.Marshal(params)
	}

	params := ndms.ASCParams{
		Jc: iface.Jc, Jmin: iface.Jmin, Jmax: iface.Jmax,
		S1: iface.S1, S2: iface.S2,
		H1: iface.H1, H2: iface.H2, H3: iface.H3, H4: iface.H4,
	}
	return json.Marshal(params)
}

// clientPubKeyFromPrivate derives WireGuard public key from a base64 private key.
// Uses crypto/ecdh (Go 1.20+) with X25519.
func clientPubKeyFromPrivate(privKeyBase64 string) string {
	privBytes, err := base64.StdEncoding.DecodeString(privKeyBase64)
	if err != nil || len(privBytes) != 32 {
		return ""
	}

	curve := ecdh.X25519()
	privKey, err := curve.NewPrivateKey(privBytes)
	if err != nil {
		return ""
	}

	return base64.StdEncoding.EncodeToString(privKey.PublicKey().Bytes())
}
