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
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// OperatorNativeWG manages tunnels via Keenetic native WireGuard + awg_proxy.ko.
type OperatorNativeWG struct {
	rci    *rci.Client
	kmod   *KmodManager
	ndms   ndms.Client
	log    *logger.Logger
	appLog *logging.ScopedLogger
}

// NewOperator creates a new NativeWG operator.
func NewOperator(log *logger.Logger, ndmsClient ndms.Client, rciClient *rci.Client, appLogger logging.AppLogger) *OperatorNativeWG {
	return &OperatorNativeWG{
		rci:    rciClient,
		kmod:   NewKmodManager(log),
		ndms:   ndmsClient,
		log:    log,
		appLog: logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubOps),
	}
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

	// Import via RCI — NDMS creates the interface and parses all params
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
	batch := rci.NewBatch()
	batch.InterfaceDescription(ndmsName, stored.Name)
	batch.InterfaceSecurityLevel(ndmsName, "public")
	batch.InterfaceIPGlobal(ndmsName, true)
	batch.InterfaceAdjustMSS(ndmsName, true)
	batch.Save()

	if err := batch.Execute(ctx, o.rci); err != nil {
		// Cleanup on failure
		cleanup := rci.NewBatch()
		cleanup.InterfaceDelete(ndmsName)
		cleanup.Save()
		_ = cleanup.Execute(ctx, o.rci)
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
	endpointIP, endpointPort, err := resolveEndpointIP(stored.Peer.Endpoint)
	if err != nil {
		return 0, fmt.Errorf("resolve endpoint: %w", err)
	}

	batch := rci.NewBatch()
	batch.InterfaceCreate(ndmsName)
	batch.InterfaceDescription(ndmsName, stored.Name)
	batch.InterfaceSecurityLevel(ndmsName, "public")
	batch.InterfaceIPAddress(ndmsName, extractIPv4(stored.Interface.Address), "255.255.255.255")
	batch.InterfaceMTU(ndmsName, stored.Interface.MTU)
	batch.InterfaceAdjustMSS(ndmsName, true)
	batch.InterfaceIPGlobal(ndmsName, true)
	batch.WireguardPrivateKey(ndmsName, stored.Interface.PrivateKey)

	// DNS
	if stored.Interface.DNS != "" {
		var servers []string
		for _, dns := range strings.Split(stored.Interface.DNS, ",") {
			if d := strings.TrimSpace(dns); d != "" {
				servers = append(servers, d)
			}
		}
		if len(servers) > 0 {
			batch.InterfaceDNS(ndmsName, servers)
		}
	}

	// IPv6 if present
	ipv6Addr := extractIPv6(stored.Interface.Address)
	if ipv6Addr != "" {
		batch.InterfaceIPv6Address(ndmsName, ipv6Addr)
	}

	// Peer
	peerCfg := rci.PeerConfig{
		PublicKey: stored.Peer.PublicKey,
		Endpoint:  fmt.Sprintf("%s:%d", endpointIP, endpointPort),
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
	batch.WireguardPeer(ndmsName, peerCfg)
	batch.Save()

	if err := batch.Execute(ctx, o.rci); err != nil {
		// Cleanup on failure
		cleanup := rci.NewBatch()
		cleanup.InterfaceDelete(ndmsName)
		cleanup.Save()
		_ = cleanup.Execute(ctx, o.rci)
		return 0, fmt.Errorf("create batch: %w", err)
	}

	// Set AWG obfuscation params via RCI (firmware >= 5.1Alpha4).
	// Non-fatal: kmod proxy handles actual obfuscation regardless.
	if ndmsinfo.SupportsWireguardASC() {
		if ascJSON, err := buildASCJSON(&stored.Interface); err == nil && ascJSON != nil {
			if err := o.ndms.SetASCParams(ctx, ndmsName, ascJSON); err != nil {
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
		if err := o.ndms.SetASCParams(ctx, names.NDMSName, ascJSON); err != nil {
			o.log.Warnf("nwg: sync ASC params on start for %s: %v", names.NDMSName, err)
		}
	}

	// Resolve endpoint (fallback to cached IP if DNS unavailable at boot)
	endpointIP, endpointPort, err := resolveEndpointIP(stored.Peer.Endpoint)
	if err != nil {
		endpointIP, endpointPort, err = o.fallbackResolve(stored, err)
		if err != nil {
			return err
		}
	}
	o.appLog.Full("start", stored.Name, fmt.Sprintf("Resolving endpoint %s -> %s:%d", stored.Peer.Endpoint, endpointIP, endpointPort))

	realEndpoint := fmt.Sprintf("%s:%d", endpointIP, endpointPort)

	// Batch: set endpoint + connect via + sync + up
	batch := rci.NewBatch()
	batch.WireguardPeerEndpoint(names.NDMSName, pubkey, realEndpoint)
	batch.WireguardPeerConnect(names.NDMSName, pubkey, stored.ISPInterface)

	// Sync address/MTU from storage
	if err := o.SyncAddressMTU(ctx, stored); err != nil {
		o.log.Warnf("nwg: sync address/mtu on start: %v", err)
	}

	// Register DNS servers with the router's DNS proxy
	o.applyDNS(ctx, names.NDMSName, stored)

	o.appLog.Full("start", stored.Name, "Setting peer endpoint, interface up")
	batch.InterfaceUp(names.NDMSName, true)

	if err := batch.Execute(ctx, o.rci); err != nil {
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
	endpointIP, endpointPort, err := resolveEndpointIP(stored.Peer.Endpoint)
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
	bindIface := o.resolveBindIface(ctx, stored)

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

	// Batch: set proxy endpoint + connect + up
	batch := rci.NewBatch()
	batch.WireguardPeerEndpoint(names.NDMSName, pubkey, proxyEndpoint)
	batch.WireguardPeerConnect(names.NDMSName, pubkey, stored.ISPInterface)

	// Sync address/MTU from storage
	if err := o.SyncAddressMTU(ctx, stored); err != nil {
		o.log.Warnf("nwg: sync address/mtu on start: %v", err)
	}

	// Register DNS servers with the router's DNS proxy
	o.applyDNS(ctx, names.NDMSName, stored)

	batch.InterfaceUp(names.NDMSName, true)

	if err := batch.Execute(ctx, o.rci); err != nil {
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

// Stop stops a NativeWG tunnel: interface down -> deactivate peer -> kmod remove (proxy only).
func (o *OperatorNativeWG) Stop(ctx context.Context, stored *storage.AWGTunnel) error {
	names := NewNWGNames(stored.NWGIndex)
	pubkey := stored.Peer.PublicKey

	o.appLog.Full("stop", stored.Name, "Interface down, peer deactivated")

	batch := rci.NewBatch()
	batch.InterfaceUp(names.NDMSName, false)
	batch.WireguardPeerConnect(names.NDMSName, pubkey, "") // reset binding
	_ = batch.Execute(ctx, o.rci)

	// Clear DNS servers from the router's DNS proxy
	o.clearDNS(ctx, names.NDMSName, stored)
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
	_, _ = o.rci.Post(ctx, rci.CmdInterfaceDelete(names.NDMSName))

	// 4. Persist
	_, _ = o.rci.Post(ctx, rci.CmdSave())

	o.log.Infof("nwg: deleted %s", names.NDMSName)
	return nil
}

// applyDNS registers DNS servers from the tunnel config with the router's DNS proxy.
// This tells the router to forward DNS queries arriving through this interface to these servers.
func (o *OperatorNativeWG) applyDNS(ctx context.Context, ndmsName string, stored *storage.AWGTunnel) {
	servers := parseDNSServers(stored.Interface.DNS)
	if len(servers) == 0 {
		return
	}
	if err := o.ndms.SetDNS(ctx, ndmsName, servers); err != nil {
		o.log.Warnf("nwg: set DNS for %s: %v", ndmsName, err)
	}
}

// clearDNS removes DNS servers from the router's DNS proxy for this interface.
func (o *OperatorNativeWG) clearDNS(ctx context.Context, ndmsName string, stored *storage.AWGTunnel) {
	servers := parseDNSServers(stored.Interface.DNS)
	if len(servers) == 0 {
		return
	}
	_ = o.ndms.ClearDNS(ctx, ndmsName, servers)
}

// parseDNSServers splits a comma-separated DNS string into a slice of trimmed, non-empty IPs.
func parseDNSServers(dns string) []string {
	if dns == "" {
		return nil
	}
	var servers []string
	for _, s := range strings.Split(dns, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			servers = append(servers, s)
		}
	}
	return servers
}

// GetState returns the state of a NativeWG tunnel via RCI.
// KmodManager does NOT participate in state detection — RCI is the single source of truth.
func (o *OperatorNativeWG) GetState(ctx context.Context, stored *storage.AWGTunnel) tunnel.StateInfo {
	names := NewNWGNames(stored.NWGIndex)

	body, err := o.rci.GetRaw(ctx, "/show/interface/"+names.NDMSName)
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
	if err := o.ndms.ConfigurePingCheck(ctx, profile, ifaceName, cfg); err != nil {
		o.log.Warnf("pingcheck: configure failed: %v", err)
		return err
	}
	return nil
}

// RemovePingCheck removes the ping-check profile for a tunnel.
func (o *OperatorNativeWG) RemovePingCheck(ctx context.Context, stored *storage.AWGTunnel) error {
	profile := pingCheckProfile(stored.ID)
	ifaceName := NewNWGNames(stored.NWGIndex).NDMSName
	return o.ndms.RemovePingCheck(ctx, profile, ifaceName)
}

// GetPingCheckStatus returns the current ping-check status for a tunnel.
func (o *OperatorNativeWG) GetPingCheckStatus(ctx context.Context, stored *storage.AWGTunnel) (*ndms.PingCheckStatus, error) {
	profile := pingCheckProfile(stored.ID)
	status, err := o.ndms.ShowPingCheck(ctx, profile)
	if err != nil {
		o.log.Warnf("pingcheck: show %s: %v", profile, err)
		// Return exists=false without error so API doesn't break
		return &ndms.PingCheckStatus{Exists: false}, nil
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
	bindIface := o.resolveBindIface(ctx, stored)

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
	_, err = o.rci.Post(ctx, rci.CmdWireguardPeerEndpoint(names.NDMSName, stored.Peer.PublicKey, proxyEndpoint))
	if err != nil {
		o.log.Warnf("nwg: restored kmod but failed to update endpoint to %s: %v", proxyEndpoint, err)
	}

	return nil
}

// SyncAddressMTU pushes the stored address and MTU to the NDMS interface.
// Called on Start (to override any changes made via the router UI)
// and on Update (to hot-apply changes to a running tunnel).
func (o *OperatorNativeWG) SyncAddressMTU(ctx context.Context, stored *storage.AWGTunnel) error {
	ndmsName := NewNWGNames(stored.NWGIndex).NDMSName
	ipv4 := extractIPv4(stored.Interface.Address)

	if err := o.ndms.SetAddress(ctx, ndmsName, ipv4); err != nil {
		return fmt.Errorf("sync address: %w", err)
	}

	// Sync IPv6 address if present
	ipv6 := extractIPv6(stored.Interface.Address)
	if ipv6 != "" {
		if err := o.ndms.SetIPv6Address(ctx, ndmsName, ipv6); err != nil {
			o.log.Warnf("nwg: sync ipv6 address on %s: %v", ndmsName, err)
		}
	} else {
		// Clear IPv6 if removed from config
		o.ndms.ClearIPv6Address(ctx, ndmsName)
	}

	if err := o.ndms.SetMTU(ctx, ndmsName, stored.Interface.MTU); err != nil {
		return fmt.Errorf("sync mtu: %w", err)
	}

	if err := o.ndms.Save(ctx); err != nil {
		o.log.Warnf("nwg: save after address/mtu sync: %v", err)
	}

	o.log.Infof("nwg: synced address=%s ipv6=%s mtu=%d on %s", ipv4, ipv6, stored.Interface.MTU, ndmsName)
	return nil
}

// UpdateDescription updates the NDMS interface description.
func (o *OperatorNativeWG) UpdateDescription(ctx context.Context, stored *storage.AWGTunnel, name string) error {
	return o.ndms.SetDescription(ctx, NewNWGNames(stored.NWGIndex).NDMSName, name)
}

// KmodManager returns the kmod manager (for shutdown hook).
func (o *OperatorNativeWG) KmodManager() *KmodManager {
	return o.kmod
}

// resolveBindIface reads the peer "via" field from RCI and resolves
// the NDMS WAN name to a kernel interface name for SO_BINDTODEVICE.
// Returns empty string if no "via" is set (= default routing).
func (o *OperatorNativeWG) resolveBindIface(ctx context.Context, stored *storage.AWGTunnel) string {
	names := NewNWGNames(stored.NWGIndex)

	body, err := o.rci.GetRaw(ctx, "/show/interface/"+names.NDMSName)
	if err != nil {
		return ""
	}
	rciState, err := parseRCIInterfaceResponse(body)
	if err != nil || !rciState.Exists || rciState.PeerVia == "" {
		return ""
	}
	sysName := o.ndms.GetSystemName(ctx, rciState.PeerVia)
	o.log.Infof("nwg: %s peer via %s -> bind %s", names.NDMSName, rciState.PeerVia, sysName)
	return sysName
}

// nextFreeIndex finds the next available Wireguard index via RCI.
func (o *OperatorNativeWG) nextFreeIndex(ctx context.Context) (int, error) {
	body, err := o.rci.GetRaw(ctx, "/show/interface/")
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
	ip, port, err := resolveEndpointIP(stored.Peer.Endpoint)
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
		H1: stored.Interface.H1, H2: stored.Interface.H2,
		H3: stored.Interface.H3, H4: stored.Interface.H4,
		S1: stored.Interface.S1, S2: stored.Interface.S2,
		S3: stored.Interface.S3, S4: stored.Interface.S4,
		Jc: stored.Interface.Jc, Jmin: stored.Interface.Jmin, Jmax: stored.Interface.Jmax,
		PubServerHex: pubKeyToHex(stored.Peer.PublicKey),
		PubClientHex: pubKeyToHex(clientPubKeyFromPrivate(stored.Interface.PrivateKey)),
		I1: stored.Interface.I1, I2: stored.Interface.I2,
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

// resolveEndpointIP parses an endpoint string (host:port) and resolves hostname -> IP.
func resolveEndpointIP(endpoint string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, fmt.Errorf("split endpoint %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}

	// If already an IP, return directly.
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), port, nil
	}

	// Resolve hostname.
	ips, err := net.LookupHost(host)
	if err != nil {
		return "", 0, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return "", 0, fmt.Errorf("no IPs for %q", host)
	}
	return ips[0], port, nil
}

// extractIPv4 extracts the IPv4 address from a WireGuard Address field
// which may contain comma-separated IPv4 and IPv6 (e.g. "172.16.0.2, 2606::1/128").
// Returns the IPv4 with /32 CIDR suffix.
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
		return host + "/32"
	}
	return addr + "/32"
}

// extractIPv6 extracts the IPv6 address from a WireGuard Address field
// which may contain comma-separated IPv4 and IPv6 (e.g. "172.16.0.2, 2606::1/128").
// Returns the bare IPv6 address WITHOUT CIDR suffix (ndms.SetIPv6Address adds /128).
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

// buildASCJSON builds a json.RawMessage for ndms.SetASCParams from stored interface fields.
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
