package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
	*clientRouteOps // provides the 5 client-route Operator methods

	queries  *query.Queries
	commands *command.Commands
	wg       wg.Client
	backend  Backend
	firewall firewall.Manager
	ipRun    ipRunFunc // ip command runner (mockable in tests)
	appLog   *logging.ScopedLogger

	// DNS tracking (tunnelID -> DNS servers applied via NDMS)
	appliedDNS   map[string][]string
	appliedDNSMu sync.RWMutex
}

// NewOperatorOS4 creates a new OS4 operator.
func NewOperatorOS4(
	queries *query.Queries,
	commands *command.Commands,
	wgClient wg.Client,
	backendImpl Backend,
	firewallMgr firewall.Manager,
) *OperatorOS4Impl {
	o := &OperatorOS4Impl{
		queries:    queries,
		commands:   commands,
		wg:         wgClient,
		backend:    backendImpl,
		firewall:   firewallMgr,
		ipRun:      exec.Run,
		appliedDNS: make(map[string][]string),
	}
	// Wire clientRouteOps after o is built — it captures o.ipRun and
	// o.logWarn (bound to o) as the runner and warn-logger.
	o.clientRouteOps = newClientRouteOps(
		func(ctx context.Context, name string, args ...string) (*exec.Result, error) {
			return o.ipRun(ctx, name, args...)
		},
		o.logWarn,
	)
	return o
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
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", ifaceName); err != nil {
		o.backend.Stop(ctx, ifaceName)
		o.deleteInterface(ctx, ifaceName)
		return tunnel.NewOpError("start", cfg.ID, "ip", fmt.Errorf("bring up: %w", exec.FormatError(result, err)))
	}

	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", ifaceName, "mtu", fmt.Sprintf("%d", cfg.MTU)); err != nil {
		o.logWarn("start", cfg.ID, "Failed to set MTU: "+exec.FormatError(result, err).Error())
	}

	// Set txqueuelen
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", ifaceName, "txqueuelen", "1000"); err != nil {
		o.logWarn("start", cfg.ID, "Failed to set txqueuelen: "+exec.FormatError(result, err).Error())
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
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", ifaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "ip", fmt.Errorf("bring up: %w", exec.FormatError(result, err)))
	}

	// Set MTU
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", ifaceName, "mtu", fmt.Sprintf("%d", cfg.MTU)); err != nil {
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

// RestoreEndpointTracking на OS4 не делает ничего: маршрутизацию оператор здесь
// не ведёт, а соответствие туннеля и WAN оркестратор берёт из записи (ActiveWAN).
func (o *OperatorOS4Impl) RestoreEndpointTracking(ctx context.Context, tunnelID, endpoint string) (string, error) {
	return "", nil
}

// GetTrackedEndpointIP is a no-op on OS4 (routing not managed by operator).
func (o *OperatorOS4Impl) GetTrackedEndpointIP(tunnelID string) string {
	return ""
}

// GetDefaultGatewayInterface returns the current default gateway interface via ip route.
func (o *OperatorOS4Impl) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "show", "default")
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

// SetMTU sets MTU on a running tunnel interface via ip link.
func (o *OperatorOS4Impl) SetMTU(ctx context.Context, tunnelID string, mtu int) error {
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", tunnelID, "mtu", fmt.Sprintf("%d", mtu)); err != nil {
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
func (o *OperatorOS4Impl) SyncAddress(ctx context.Context, tunnelID string, address string, prefix int, ipv6 string) error {
	return nil
}

// UpdateDescription is a no-op on OS4 (no NDMS interface descriptions).
func (o *OperatorOS4Impl) UpdateDescription(ctx context.Context, tunnelID, description string) error {
	return nil
}

// configureIP configures IPv4 address on the interface.
func (o *OperatorOS4Impl) configureIP(ctx context.Context, iface, address string) error {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "address", "add", "dev", iface, address+"/32")
	return exec.FormatError(result, err)
}

// configureIPv6 configures IPv6 address on the interface.
func (o *OperatorOS4Impl) configureIPv6(ctx context.Context, iface, address string) error {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "add", "dev", iface, address+"/128")
	return exec.FormatError(result, err)
}

// deleteInterface force-deletes a network interface.
func (o *OperatorOS4Impl) deleteInterface(ctx context.Context, iface string) {
	o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", iface)
	o.ipRun(ctx, "/opt/sbin/ip", "link", "del", iface)
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
	result, err := o.ipRun(context.Background(), "/opt/sbin/ip", "link", "show", iface)
	return err == nil && result != nil && result.ExitCode == 0
}

// logInfo logs an info message via the UI-visible scoped logger.
func (o *OperatorOS4Impl) logInfo(action, target, message string) {
	o.appLog.Info(action, target, message)
}

// logWarn logs a warning message via the UI-visible scoped logger.
func (o *OperatorOS4Impl) logWarn(action, target, message string) {
	o.appLog.Warn(action, target, message)
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
