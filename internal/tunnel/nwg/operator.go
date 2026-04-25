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
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

// OperatorNativeWG manages tunnels via Keenetic native WireGuard + awg_proxy.ko.
type OperatorNativeWG struct {
	queries   *query.Queries
	commands  *command.Commands
	transport *transport.Client
	// Legacy rci.Client retained only for ImportWireguardConfig (multipart upload).
	// Will move to new layer in cleanup phase.
	rci          *rci.Client
	kmod         *KmodManager
	log          *logger.Logger
	appLog       *logging.ScopedLogger
	hookNotifier tunnel.HookNotifier
}

// NewOperator creates a new NativeWG operator.
func NewOperator(log *logger.Logger, queries *query.Queries, commands *command.Commands, tr *transport.Client, rciClient *rci.Client, appLogger logging.AppLogger) *OperatorNativeWG {
	return &OperatorNativeWG{
		queries:   queries,
		commands:  commands,
		transport: tr,
		rci:       rciClient,
		kmod:      NewKmodManager(log),
		log:       log,
		appLog:    logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubOps),
	}
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
	ndmsName, err := o.rci.ImportWireguardConfig(ctx, []byte(confData), stored.Name+".conf")
	if err != nil {
		return 0, fmt.Errorf("import wireguard config: %w", err)
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
		rci.CmdInterfaceDescription(ndmsName, stored.Name),
		rci.CmdInterfaceSecurityLevel(ndmsName, "public"),
		rci.CmdInterfaceIPGlobal(ndmsName, true),
		rci.CmdInterfaceAdjustMSS(ndmsName, true),
		rci.CmdSave(),
	}

	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		// Cleanup on failure
		cleanup := []any{
			rci.CmdInterfaceDelete(ndmsName),
			rci.CmdSave(),
		}
		_, _ = o.transport.PostBatch(ctx, cleanup)
		return 0, fmt.Errorf("post-import settings: %w", err)
	}

	o.appLog.Full("create", stored.Name, fmt.Sprintf("Created NDMS interface %s via import", ndmsName))
	o.log.Infof("nwg: created %s (import path)", ndmsName)
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
	// the actual proxy endpoint is set at Start time)
	endpointIP, endpointPort, err := netutil.ResolveEndpoint(stored.Peer.Endpoint)
	if err != nil {
		return 0, fmt.Errorf("resolve endpoint: %w", err)
	}

	cmds := []any{
		rci.CmdInterfaceCreate(ndmsName),
		rci.CmdInterfaceDescription(ndmsName, stored.Name),
		rci.CmdInterfaceSecurityLevel(ndmsName, "public"),
		rci.CmdInterfaceIPAddress(ndmsName, extractIPv4(stored.Interface.Address), "255.255.255.255"),
		rci.CmdInterfaceMTU(ndmsName, stored.Interface.MTU),
		rci.CmdInterfaceAdjustMSS(ndmsName, true),
		rci.CmdInterfaceIPGlobal(ndmsName, true),
		rci.CmdWireguardPrivateKey(ndmsName, stored.Interface.PrivateKey),
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
			cmds = append(cmds, rci.CmdInterfaceDNS(ndmsName, servers))
		}
	}

	// IPv6 if present
	ipv6Addr := extractIPv6(stored.Interface.Address)
	if ipv6Addr != "" {
		cmds = append(cmds, rci.CmdInterfaceIPv6Address(ndmsName, ipv6Addr))
	}

	// Peer
	peerCfg := rci.PeerConfig{
		PublicKey:   stored.Peer.PublicKey,
		Endpoint:    fmt.Sprintf("%s:%d", endpointIP, endpointPort),
		AllowedIPv4: []rci.AllowedIP{{Address: "0.0.0.0", Mask: "0"}},
	}
	if hasIPv6AllowedIPs(stored.Peer.AllowedIPs) {
		peerCfg.AllowedIPv6 = []rci.AllowedIP{{Address: "::", Mask: "0"}}
	}
	if stored.Peer.PersistentKeepalive > 0 {
		peerCfg.KeepaliveInterval = stored.Peer.PersistentKeepalive
	}
	if stored.Peer.PresharedKey != "" {
		peerCfg.PresharedKey = stored.Peer.PresharedKey
	}
	cmds = append(cmds, rci.CmdWireguardPeer(ndmsName, peerCfg), rci.CmdSave())

	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		// Cleanup on failure
		cleanup := []any{
			rci.CmdInterfaceDelete(ndmsName),
			rci.CmdSave(),
		}
		_, _ = o.transport.PostBatch(ctx, cleanup)
		return 0, fmt.Errorf("create batch: %w", err)
	}

	// Set AWG obfuscation params via RCI (firmware >= 5.1Alpha4).
	// Non-fatal: kmod proxy handles actual obfuscation regardless.
	if ndmsinfo.SupportsWireguardASC() {
		if ascJSON, err := buildASCJSON(&stored.Interface); err == nil && ascJSON != nil {
			if err := o.commands.Wireguard.SetASCParams(ctx, ndmsName, ascJSON); err != nil {
				o.log.Warnf("nwg: SetASCParams via RCI failed (non-fatal): %v", err)
			}
		}
	}

	o.appLog.Full("create", stored.Name, fmt.Sprintf("Creating NDMS interface %s", ndmsName))
	o.log.Infof("nwg: created %s", ndmsName)
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
			o.log.Warnf("nwg: sync ASC params on start for %s: %v", names.NDMSName, err)
		}
	}

	// Resolve endpoint (fallback to cached IP if DNS unavailable at boot)
	endpointIP, endpointPort, err := netutil.ResolveEndpoint(stored.Peer.Endpoint)
	if err != nil {
		endpointIP, endpointPort, err = o.fallbackResolve(stored, err)
		if err != nil {
			return err
		}
	}
	o.appLog.Full("start", stored.Name, fmt.Sprintf("Resolving endpoint %s -> %s:%d", stored.Peer.Endpoint, endpointIP, endpointPort))

	realEndpoint := fmt.Sprintf("%s:%d", endpointIP, endpointPort)

	// Sync address/MTU from storage
	if err := o.SyncAddressMTU(ctx, stored); err != nil {
		o.log.Warnf("nwg: sync address/mtu on start: %v", err)
	}

	// Register DNS servers with the router's DNS proxy
	if err := o.SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS)); err != nil {
		o.log.Warnf("nwg: apply DNS: %v", err)
	}

	o.appLog.Full("start", stored.Name, "Setting peer endpoint, interface up")
	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(names.NDMSName, "running")
	}

	// Batch: set endpoint + connect via + up
	cmds := []any{
		rci.CmdWireguardPeerEndpoint(names.NDMSName, pubkey, realEndpoint),
		rci.CmdWireguardPeerConnect(names.NDMSName, pubkey, stored.ISPInterface),
		rci.CmdInterfaceUp(names.NDMSName, true),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		return fmt.Errorf("start native: %w", err)
	}

	viaInfo := ""
	if stored.ISPInterface != "" {
		viaInfo = " via " + stored.ISPInterface
	}
	o.log.Infof("nwg: started %s (native ASC, endpoint %s%s)", names.NDMSName, realEndpoint, viaInfo)
	return nil
}

// startProxy starts a tunnel on older firmware via awg_proxy.ko.
// Peer endpoint is redirected to 127.0.0.1:proxy_port.
func (o *OperatorNativeWG) startProxy(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)
	pubkey := stored.Peer.PublicKey

	// Resolve endpoint — kmod proxy connects to this IP
	// Fallback to cached IP if DNS unavailable at boot
	endpointIP, endpointPort, err := netutil.ResolveEndpoint(stored.Peer.Endpoint)
	if err != nil {
		endpointIP, endpointPort, err = o.fallbackResolve(stored, err)
		if err != nil {
			return err
		}
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
	o.appLog.Full("start", stored.Name, fmt.Sprintf("Adding tunnel to kmod, listen port %d", result.ListenPort))
	o.appLog.Debug("start", stored.Name, fmt.Sprintf("Kmod proxy %s:%d -> 127.0.0.1:%d, bind=%s", endpointIP, endpointPort, result.ListenPort, bindIface))

	proxyEndpoint := fmt.Sprintf("127.0.0.1:%d", result.ListenPort)

	// Sync address/MTU from storage
	if err := o.SyncAddressMTU(ctx, stored); err != nil {
		o.log.Warnf("nwg: sync address/mtu on start: %v", err)
	}

	// Register DNS servers with the router's DNS proxy
	if err := o.SyncDNS(ctx, stored, nil, tunnel.ParseDNSList(stored.Interface.DNS)); err != nil {
		o.log.Warnf("nwg: apply DNS: %v", err)
	}

	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(names.NDMSName, "running")
	}

	// Batch: set proxy endpoint + connect + up
	cmds := []any{
		rci.CmdWireguardPeerEndpoint(names.NDMSName, pubkey, proxyEndpoint),
		rci.CmdWireguardPeerConnect(names.NDMSName, pubkey, stored.ISPInterface),
		rci.CmdInterfaceUp(names.NDMSName, true),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		_ = o.kmod.RemoveTunnel(stored.ID)
		return fmt.Errorf("start proxy: %w", err)
	}

	viaInfo := ""
	if stored.ISPInterface != "" {
		viaInfo = " via " + stored.ISPInterface
	}
	o.log.Infof("nwg: started %s (proxy %s -> %s:%d%s)", names.NDMSName, proxyEndpoint, endpointIP, endpointPort, viaInfo)
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
		rci.CmdWireguardPeerDisconnect(names.NDMSName, pubkey),
	}
	if _, err := o.transport.PostBatch(ctx, cmds); err != nil {
		o.log.Warnf("nwg: suspend proxy %s: peer disconnect: %v", names.NDMSName, err)
		return fmt.Errorf("peer disconnect: %w", err)
	}

	o.appLog.Info("suspend", stored.Name, "Proxy suspended (WAN down)")
	o.log.Infof("nwg: suspended proxy %s", names.NDMSName)
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
		rci.CmdInterfaceUp(names.NDMSName, false),
		rci.CmdSave(),
	}
	_, _ = o.transport.PostBatch(ctx, cmds)

	// Clear DNS servers from the router's DNS proxy
	if err := o.SyncDNS(ctx, stored, tunnel.ParseDNSList(stored.Interface.DNS), nil); err != nil {
		o.log.Warnf("nwg: clear DNS: %v", err)
	}
	o.appLog.Full("stop", stored.Name, "DNS cleared")

	// Only remove kmod proxy entry on older firmware
	if !ndmsinfo.SupportsWireguardASC() {
		_ = o.kmod.RemoveTunnel(stored.ID)
	}

	o.log.Infof("nwg: stopped %s", names.NDMSName)
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
	_, _ = o.transport.Post(ctx, rci.CmdInterfaceDelete(names.NDMSName))

	// 4. Persist
	_, _ = o.transport.Post(ctx, rci.CmdSave())

	o.log.Infof("nwg: deleted %s", names.NDMSName)
	return nil
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

	// State matrix (simplified — no proxy/kmod tracking needed):
	//   ConfLayer==running && PeerOnline     -> StateRunning
	//   ConfLayer==running && !PeerOnline    -> StateStarting
	//   ConfLayer==disabled                  -> StateStopped
	//   !Exists                              -> StateNotCreated
	switch {
	case rciState.ConfLayer == "running" && rciState.PeerOnline:
		info.State = tunnel.StateRunning
	case rciState.ConfLayer == "running" && !rciState.PeerOnline:
		info.State = tunnel.StateStarting
	case rciState.ConfLayer == "disabled":
		info.State = tunnel.StateStopped
	default:
		info.State = tunnel.StateUnknown
	}

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
	o.log.Infof("pingcheck: configure profile=%s iface=%s host=%s mode=%s", profile, ifaceName, cfg.Host, cfg.Mode)
	if err := o.commands.PingCheck.ConfigureProfile(ctx, profile, ifaceName, cfg); err != nil {
		o.log.Warnf("pingcheck: configure failed: %v", err)
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
		o.log.Warnf("pingcheck: list profiles: %v", perr)
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
			o.log.Warnf("pingcheck: list statuses: %v", serr)
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
	o.log.Infof("pingcheck: show %s -> exists=%v host=%s status=%s", profile, status.Exists, status.Host, status.Status)
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

	kmodCfg, err := buildKmodConfig(stored, bindIface)
	if err != nil {
		return fmt.Errorf("build kmod config: %w", err)
	}
	result, err := o.kmod.AddTunnel(stored.ID, kmodCfg)
	if err != nil {
		return err
	}

	// Update NDMS peer endpoint to proxy address
	names := NewNWGNames(stored.NWGIndex)
	proxyEndpoint := fmt.Sprintf("127.0.0.1:%d", result.ListenPort)
	_, err = o.transport.Post(ctx, rci.CmdWireguardPeerEndpoint(names.NDMSName, stored.Peer.PublicKey, proxyEndpoint))
	if err != nil {
		o.log.Warnf("nwg: restored kmod but failed to update endpoint to %s: %v", proxyEndpoint, err)
	}

	return nil
}

// KmodManager returns the kmod manager (for shutdown hook).
func (o *OperatorNativeWG) KmodManager() *KmodManager {
	return o.kmod
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
		o.log.Warnf("nwg: %s peer via %s: could not resolve kernel name, using default route", names.NDMSName, rciState.PeerVia)
		return ""
	}
	o.log.Infof("nwg: %s peer via %s -> kernel %s", names.NDMSName, rciState.PeerVia, sysName)
	return sysName
}

// nextFreeIndex finds the next available Wireguard index via RCI.
func (o *OperatorNativeWG) nextFreeIndex(ctx context.Context) (int, error) {
	body, err := o.transport.GetRaw(ctx, "/show/interface/")
	if err != nil {
		return 0, fmt.Errorf("list wireguard interfaces: %w", err)
	}

	existing, err := parseRCIInterfaceList(body)
	if err != nil {
		return 0, fmt.Errorf("parse interface list: %w", err)
	}

	used := make(map[int]bool)
	for _, name := range existing {
		// Extract index from "WireguardN"
		if idx, _, err := ParseNDMSCreatedName(`"` + name + `" interface created`); err == nil {
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

// buildKmodConfig resolves the endpoint and builds a KmodConfig.
// Used by RestoreKmodTunnel where we don't need the resolved IP separately.
func buildKmodConfig(stored *storage.AWGTunnel, bindIface string) (KmodConfig, error) {
	ip, port, err := netutil.ResolveEndpoint(stored.Peer.Endpoint)
	if err != nil {
		return KmodConfig{}, fmt.Errorf("resolve endpoint: %w", err)
	}
	return buildKmodConfigResolved(stored, ip, port, bindIface)
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
	o.log.Warnf("nwg: DNS failed for %s, using cached IP %s", stored.Peer.Endpoint, stored.ResolvedEndpointIP)
	return stored.ResolvedEndpointIP, port, nil
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
