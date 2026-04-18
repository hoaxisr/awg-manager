package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

const (
	interfaceReadyTimeout = 10 * time.Second
	socketReadyTimeout    = 5 * time.Second
)

// OperatorOS4Impl is the Operator implementation for Keenetic OS 4.x.
// Uses ip commands directly instead of NDMS.
// Routing is NOT managed by the operator — OS4 handles routing externally.
type OperatorOS4Impl struct {
	queries  *query.Queries
	commands *command.Commands
	wg       wg.Client
	backend  backend.Backend
	firewall firewall.Manager
	log      *logger.Logger
	appLog   *logging.ScopedLogger

	// Resolved ISP tracking (tunnelID -> WAN interface name)
	// Tracks which WAN each tunnel is bound to for WAN event matching.
	resolvedISP   map[string]string
	resolvedISPMu sync.RWMutex

	// DNS tracking (tunnelID -> DNS servers applied via NDMS)
	appliedDNS   map[string][]string
	appliedDNSMu sync.RWMutex
}

// NewOperatorOS4 creates a new OS4 operator.
func NewOperatorOS4(
	queries *query.Queries,
	commands *command.Commands,
	wgClient wg.Client,
	backendImpl backend.Backend,
	firewallMgr firewall.Manager,
	log *logger.Logger,
) *OperatorOS4Impl {
	return &OperatorOS4Impl{
		queries:     queries,
		commands:    commands,
		wg:          wgClient,
		backend:     backendImpl,
		firewall:    firewallMgr,
		log:         log,
		resolvedISP: make(map[string]string),
		appliedDNS:  make(map[string][]string),
	}
}

// Create is a no-op on OS4 (interface created by process).
func (o *OperatorOS4Impl) Create(ctx context.Context, cfg tunnel.Config) error {
	// On OS4, interface is created when process starts
	return nil
}

// ColdStart on OS4 is the same as Start — no NDMS/OpkgTun lifecycle.
func (o *OperatorOS4Impl) ColdStart(ctx context.Context, cfg tunnel.Config) error {
	return o.Start(ctx, cfg)
}

// Start starts a tunnel on OS 4.x.
// Sequence: process → wait ready → ip config → WG config → interface up → firewall
// Routing is not managed here — OS4 handles it externally.
func (o *OperatorOS4Impl) Start(ctx context.Context, cfg tunnel.Config) error {
	// On OS4, interface name is the tunnel ID (e.g., "awgm0")
	ifaceName := cfg.ID

	// Validate config
	if err := cfg.Validate(); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "", err)
	}

	// === Phase 1: Start backend process ===
	if err := o.backend.Start(ctx, ifaceName); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "backend", err)
	}

	// Wait for interface and socket to be ready
	if err := o.backend.WaitReady(ctx, ifaceName, interfaceReadyTimeout); err != nil {
		o.backend.Stop(ctx, ifaceName)
		return tunnel.NewOpError("start", cfg.ID, "backend", fmt.Errorf("wait ready: %w", err))
	}

	o.logInfo("start", cfg.ID, "Backend process started")

	// === Phase 2: Configure interface IP ===
	if err := o.configureIP(ctx, ifaceName, cfg.Address); err != nil {
		o.backend.Stop(ctx, ifaceName)
		return tunnel.NewOpError("start", cfg.ID, "ip", err)
	}

	// Configure IPv6 if present
	if cfg.AddressIPv6 != "" {
		if err := o.configureIPv6(ctx, ifaceName, cfg.AddressIPv6); err != nil {
			o.logWarn("start", cfg.ID, "Failed to configure IPv6: "+err.Error())
		}
	}

	o.logInfo("start", cfg.ID, "IP configured")

	// === Phase 3: Apply WireGuard configuration ===
	if err := o.wg.SetConf(ctx, ifaceName, cfg.ConfPath); err != nil {
		o.backend.Stop(ctx, ifaceName)
		o.deleteInterface(ctx, ifaceName)
		return tunnel.NewOpError("start", cfg.ID, "wg", err)
	}

	o.logInfo("start", cfg.ID, "WireGuard config applied")

	// === Phase 4: Bring interface up and set MTU ===
	if result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", ifaceName); err != nil {
		o.backend.Stop(ctx, ifaceName)
		o.deleteInterface(ctx, ifaceName)
		return tunnel.NewOpError("start", cfg.ID, "ip", fmt.Errorf("bring up: %w", exec.FormatError(result, err)))
	}

	if result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "set", "dev", ifaceName, "mtu", fmt.Sprintf("%d", cfg.MTU)); err != nil {
		o.logWarn("start", cfg.ID, "Failed to set MTU: "+exec.FormatError(result, err).Error())
	}

	// Set txqueuelen (kernel backend only)
	if o.backend.Type() == backend.TypeKernel {
		if result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "set", "dev", ifaceName, "txqueuelen", "1000"); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set txqueuelen: "+exec.FormatError(result, err).Error())
		}
	}

	o.logInfo("start", cfg.ID, "Interface up with MTU")

	// === Phase 5: Add firewall rules ===
	if err := o.firewall.AddRules(ctx, ifaceName); err != nil {
		o.logWarn("start", cfg.ID, "Failed to add firewall rules: "+err.Error())
		o.appLog.Warn("start", cfg.ID, "Правила файрвола: "+err.Error())
	} else {
		o.appLog.Info("start", cfg.ID, "Правила файрвола добавлены для "+ifaceName)
	}

	// Apply DNS servers via NDMS (RCI works on OS4 too)
	if len(cfg.DNS) > 0 {
		if err := o.commands.Interfaces.SetDNS(ctx, ifaceName, cfg.DNS); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set DNS: "+err.Error())
		} else {
			o.appliedDNSMu.Lock()
			o.appliedDNS[cfg.ID] = cfg.DNS
			o.appliedDNSMu.Unlock()
		}
	}

	// Track resolved ISP for WAN event matching
	if cfg.ISPInterface != "" {
		o.resolvedISPMu.Lock()
		o.resolvedISP[cfg.ID] = cfg.ISPInterface
		o.resolvedISPMu.Unlock()
	}

	o.logInfo("start", cfg.ID, "Tunnel started successfully")
	return nil
}

// Stop stops a tunnel on OS 4.x.
func (o *OperatorOS4Impl) Stop(ctx context.Context, tunnelID string) error {
	ifaceName := tunnelID

	// Remove firewall rules
	_ = o.firewall.RemoveRules(ctx, ifaceName)

	// Stop backend process (this will remove the interface)
	if err := o.backend.Stop(ctx, ifaceName); err != nil {
		o.logWarn("stop", tunnelID, "Failed to stop backend: "+err.Error())
	}

	// Wait for interface removal
	o.waitForInterfaceRemoval(ctx, ifaceName, 5*time.Second)

	// Clear DNS servers
	o.appliedDNSMu.Lock()
	dnsServers := o.appliedDNS[tunnelID]
	delete(o.appliedDNS, tunnelID)
	o.appliedDNSMu.Unlock()
	if len(dnsServers) > 0 {
		_ = o.commands.Interfaces.ClearDNS(ctx, ifaceName, dnsServers)
	}

	// Clear resolved ISP tracking
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	o.logInfo("stop", tunnelID, "Tunnel stopped")
	return nil
}

// SetDefaultRoute is a no-op on OS4 (no NDMS route management).
func (o *OperatorOS4Impl) SetDefaultRoute(ctx context.Context, tunnelID string) error {
	return nil
}

// RemoveDefaultRoute is a no-op on OS4 (no NDMS route management).
func (o *OperatorOS4Impl) RemoveDefaultRoute(ctx context.Context, tunnelID string) error {
	return nil
}

// Delete completely removes a tunnel.
func (o *OperatorOS4Impl) Delete(ctx context.Context, stored *storage.AWGTunnel) error {
	// On OS4, stop and delete are the same
	return o.Stop(ctx, stored.ID)
}

// Recover attempts to bring a broken tunnel into a consistent state.
// Stops the backend and force-removes the interface to reach a clean state.
func (o *OperatorOS4Impl) Recover(ctx context.Context, tunnelID string, state tunnel.StateInfo) error {
	ifaceName := tunnelID

	o.logInfo("recover", tunnelID, fmt.Sprintf("Recovering from state: %s", state.State))

	// Stop via backend
	_ = o.backend.Stop(ctx, ifaceName)

	// Force-remove interface at kernel level
	o.deleteInterface(ctx, ifaceName)

	// Clean up DNS entries
	o.appliedDNSMu.Lock()
	dnsServers := o.appliedDNS[tunnelID]
	delete(o.appliedDNS, tunnelID)
	o.appliedDNSMu.Unlock()
	if len(dnsServers) > 0 {
		_ = o.commands.Interfaces.ClearDNS(ctx, ifaceName, dnsServers)
	}

	o.logInfo("recover", tunnelID, "Recovery complete")
	return nil
}

// Suspend on OS4 is a no-op — OS4 has no NDMS layer.
// WAN down on OS4 is handled by kernel routing automatically.
func (o *OperatorOS4Impl) Suspend(ctx context.Context, tunnelID string) error {
	return nil
}

// Resume on OS4 is a no-op — see Suspend.
func (o *OperatorOS4Impl) Resume(ctx context.Context, tunnelID string) error {
	return nil
}

// Reconcile re-applies system configuration around an already-running process.
// Assumes: process is running, interface exists. Re-applies WG config, IP, firewall.
func (o *OperatorOS4Impl) Reconcile(ctx context.Context, cfg tunnel.Config) error {
	ifaceName := cfg.ID

	o.logInfo("reconcile", cfg.ID, "Reconciling state around running process")

	// Apply WireGuard configuration
	if err := o.wg.SetConf(ctx, ifaceName, cfg.ConfPath); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "wg", err)
	}

	// Bring interface up
	if result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", ifaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "ip", fmt.Errorf("bring up: %w", exec.FormatError(result, err)))
	}

	// Set MTU
	if result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "set", "dev", ifaceName, "mtu", fmt.Sprintf("%d", cfg.MTU)); err != nil {
		o.logWarn("reconcile", cfg.ID, "Failed to set MTU: "+exec.FormatError(result, err).Error())
	}

	// Add firewall rules
	if err := o.firewall.AddRules(ctx, ifaceName); err != nil {
		o.logWarn("reconcile", cfg.ID, "Failed to add firewall rules: "+err.Error())
	}

	// Re-apply DNS servers
	if len(cfg.DNS) > 0 {
		if err := o.commands.Interfaces.SetDNS(ctx, ifaceName, cfg.DNS); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to re-apply DNS: "+err.Error())
		} else {
			o.appliedDNSMu.Lock()
			o.appliedDNS[cfg.ID] = cfg.DNS
			o.appliedDNSMu.Unlock()
		}
	}

	o.logInfo("reconcile", cfg.ID, "Reconciliation complete")
	return nil
}

// ApplyConfig applies a new WireGuard config to a running tunnel.
func (o *OperatorOS4Impl) ApplyConfig(ctx context.Context, tunnelID, configPath string) error {
	if err := o.wg.SetConf(ctx, tunnelID, configPath); err != nil {
		return tunnel.NewOpError("apply_config", tunnelID, "wg", err)
	}
	return nil
}

// SetupEndpointRoute is a no-op on OS4 (routing not managed by operator).
func (o *OperatorOS4Impl) SetupEndpointRoute(ctx context.Context, tunnelID, endpoint, _, _ string) (string, error) {
	return "", nil
}

// CleanupEndpointRoute is a no-op on OS4 (routing not managed by operator).
func (o *OperatorOS4Impl) CleanupEndpointRoute(ctx context.Context, tunnelID string) error {
	return nil
}

// RestoreEndpointTracking restores resolved ISP tracking on daemon restart.
// Routing is not managed by OS4, but resolvedISP is needed for WAN event matching.
func (o *OperatorOS4Impl) RestoreEndpointTracking(ctx context.Context, tunnelID, endpoint, ispInterface string) (string, error) {
	if ispInterface != "" {
		o.resolvedISPMu.Lock()
		o.resolvedISP[tunnelID] = ispInterface
		o.resolvedISPMu.Unlock()
	}
	return "", nil
}

// GetTrackedEndpointIP is a no-op on OS4 (routing not managed by operator).
func (o *OperatorOS4Impl) GetTrackedEndpointIP(tunnelID string) string {
	return ""
}

// GetDefaultGatewayInterface returns the current default gateway interface via ip route.
func (o *OperatorOS4Impl) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show", "default")
	if err != nil {
		return "", fmt.Errorf("ip route show default: %w", err)
	}
	// Parse: "default via 192.168.1.1 dev eth0"
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("no default gateway found")
}

// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
func (o *OperatorOS4Impl) GetResolvedISP(tunnelID string) string {
	o.resolvedISPMu.RLock()
	defer o.resolvedISPMu.RUnlock()
	return o.resolvedISP[tunnelID]
}

// SetMTU sets MTU on a running tunnel interface via ip link.
func (o *OperatorOS4Impl) SetMTU(ctx context.Context, tunnelID string, mtu int) error {
	if _, err := exec.Run(ctx, "/opt/sbin/ip", "link", "set", "dev", tunnelID, "mtu", fmt.Sprintf("%d", mtu)); err != nil {
		return tunnel.NewOpError("set_mtu", tunnelID, "ip", err)
	}
	o.logInfo("set_mtu", tunnelID, fmt.Sprintf("MTU set to %d", mtu))
	return nil
}

// SyncDNS is a no-op on OS4 (DNS managed differently).
func (o *OperatorOS4Impl) SyncDNS(ctx context.Context, tunnelID string, dns []string) error {
	return nil
}

// SyncAddress is a no-op on OS4 (address managed by process).
func (o *OperatorOS4Impl) SyncAddress(ctx context.Context, tunnelID string, address, ipv6 string) error {
	return nil
}

// UpdateDescription is a no-op on OS4 (no NDMS interface descriptions).
func (o *OperatorOS4Impl) UpdateDescription(ctx context.Context, tunnelID, description string) error {
	return nil
}

// configureIP configures IPv4 address on the interface.
func (o *OperatorOS4Impl) configureIP(ctx context.Context, iface, address string) error {
	result, err := exec.Run(ctx, "/opt/sbin/ip", "address", "add", "dev", iface, address+"/32")
	return exec.FormatError(result, err)
}

// configureIPv6 configures IPv6 address on the interface.
func (o *OperatorOS4Impl) configureIPv6(ctx context.Context, iface, address string) error {
	result, err := exec.Run(ctx, "/opt/sbin/ip", "-6", "address", "add", "dev", iface, address+"/128")
	return exec.FormatError(result, err)
}

// deleteInterface force-deletes a network interface.
func (o *OperatorOS4Impl) deleteInterface(ctx context.Context, iface string) {
	exec.Run(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", iface)
	exec.Run(ctx, "/opt/sbin/ip", "link", "del", iface)
}

// waitForInterfaceRemoval waits for interface to be removed.
func (o *OperatorOS4Impl) waitForInterfaceRemoval(ctx context.Context, iface string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Timeout - force delete
			o.deleteInterface(context.Background(), iface)
			return
		case <-ticker.C:
			if !o.interfaceExists(iface) {
				return
			}
		}
	}
}

// interfaceExists checks if interface exists.
func (o *OperatorOS4Impl) interfaceExists(iface string) bool {
	result, err := exec.Run(context.Background(), "/opt/sbin/ip", "link", "show", iface)
	return err == nil && result != nil && result.ExitCode == 0
}

// logInfo logs an info message.
func (o *OperatorOS4Impl) logInfo(action, target, message string) {
	if o.log != nil {
		o.log.Infof("[%s] %s: %s", action, target, message)
	}
}

// logWarn logs a warning message.
func (o *OperatorOS4Impl) logWarn(action, target, message string) {
	if o.log != nil {
		o.log.Warnf("[%s] %s: %s", action, target, message)
	}
}

// HasWANIPv6 checks if a WAN interface has IPv6 connectivity via NDMS RCI.
func (o *OperatorOS4Impl) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	if o.queries == nil {
		return false
	}
	return o.queries.Interfaces.HasIPv6Global(ctx, ifaceName)
}

// GetSystemName on OS4 returns ndmsID as-is (no system-name RCI on OS4;
// GetDefaultGatewayInterface already returns kernel names from ip route).
func (o *OperatorOS4Impl) GetSystemName(_ context.Context, ndmsID string) string {
	return ndmsID
}

// SetAppLogger sets the web UI logger.
func (o *OperatorOS4Impl) SetAppLogger(logger logging.AppLogger) {
	o.appLog = logging.NewScopedLogger(logger, logging.GroupTunnel, logging.SubOps)
}

// Ensure OperatorOS4Impl implements Operator interface.
var _ Operator = (*OperatorOS4Impl)(nil)
