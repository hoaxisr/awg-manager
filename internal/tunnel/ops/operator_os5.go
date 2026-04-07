package ops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// interfaceReadyTimeout and socketReadyTimeout are defined in operator_os4.go
// (shared between OS4 and OS5 implementations).

// ipRunFunc is the signature for running ip commands.
// Defaults to exec.Run; overridden in tests to avoid real /opt/sbin/ip calls.
type ipRunFunc func(ctx context.Context, name string, args ...string) (*exec.Result, error)

// OperatorOS5Impl is the Operator implementation for Keenetic OS 5.0+.
// Uses NDMS for interface management, kernel backend for tunnel interfaces.
type OperatorOS5Impl struct {
	ndms     ndms.Client
	wg       wg.Client
	backend  backend.Backend
	firewall firewall.Manager
	log      *logger.Logger
	ipRun    ipRunFunc // ip command runner (mockable in tests)

	appLog *logging.ScopedLogger

	// Endpoint route tracking (tunnelID -> endpointIP)
	endpointRoutes   map[string]string
	endpointRoutesMu sync.RWMutex

	// Resolved ISP tracking (tunnelID -> WAN interface name)
	// Tracks the actual WAN used for auto-mode tunnels.
	resolvedISP   map[string]string
	resolvedISPMu sync.RWMutex

	// DNS tracking (tunnelID -> DNS servers applied via NDMS)
	// Used to clean up DNS entries on Stop/Delete.
	appliedDNS   map[string][]string
	appliedDNSMu sync.RWMutex

	// hookNotifier registers expected NDMS hooks for self-triggered changes,
	// preventing infinite loops when our InterfaceUp/InterfaceDown calls
	// trigger hooks that would otherwise cause orchestrator to react.
	hookNotifier tunnel.HookNotifier
}

// NewOperatorOS5 creates a new OS5 operator.
func NewOperatorOS5(
	ndmsClient ndms.Client,
	wgClient wg.Client,
	backendImpl backend.Backend,
	firewallMgr firewall.Manager,
	log *logger.Logger,
) *OperatorOS5Impl {
	return &OperatorOS5Impl{
		ndms:           ndmsClient,
		wg:             wgClient,
		backend:        backendImpl,
		firewall:       firewallMgr,
		log:            log,
		ipRun:          exec.Run,
		endpointRoutes: make(map[string]string),
		resolvedISP:    make(map[string]string),
		appliedDNS:     make(map[string][]string),
	}
}

// SetHookNotifier sets the hook notifier for registering expected NDMS hooks.
// Must be called before any operations that change NDMS interface state.
func (o *OperatorOS5Impl) SetHookNotifier(hn tunnel.HookNotifier) {
	o.hookNotifier = hn
}

// expectHook registers an expected NDMS hook to filter self-triggered events.
// No-op if hookNotifier is not set.
func (o *OperatorOS5Impl) expectHook(ndmsName, level string) {
	if o.hookNotifier != nil {
		o.hookNotifier.ExpectHook(ndmsName, level)
	}
}

// Create creates a tunnel's NDMS resources without starting it.
// Sets address and MTU so NDMS has the full config from the start.
func (o *OperatorOS5Impl) Create(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	// Check if already exists
	if o.ndms.OpkgTunExists(ctx, names.NDMSName) {
		return tunnel.ErrAlreadyExists
	}

	// Create OpkgTun in NDMS
	if err := o.ndms.CreateOpkgTun(ctx, names.NDMSName, cfg.Name); err != nil {
		return tunnel.NewOpError("create", cfg.ID, "ndms", err)
	}

	// rollbackCreate is called on error: removes the partially-created OpkgTun.
	// Pre-registers a "disabled" hook because DeleteOpkgTun may trigger conf
	// state change before removal.
	rollbackCreate := func() {
		o.expectHook(names.NDMSName, "disabled")
		_ = o.ndms.DeleteOpkgTun(ctx, names.NDMSName)
	}

	// Configure address and MTU before Save so NDMS has the full config.
	// This is the only place we call SetAddress/SetMTU for new tunnels —
	// Start() skips NDMS config when OpkgTun already exists.
	if cfg.Address != "" {
		if err := o.ndms.SetAddress(ctx, names.NDMSName, cfg.Address); err != nil {
			rollbackCreate()
			return tunnel.NewOpError("create", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
		}
	}
	if cfg.AddressIPv6 != "" {
		if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
			rollbackCreate()
			return tunnel.NewOpError("create", cfg.ID, "ndms", fmt.Errorf("set ipv6 address: %w", err))
		}
	}
	if cfg.MTU > 0 {
		if err := o.ndms.SetMTU(ctx, names.NDMSName, cfg.MTU); err != nil {
			rollbackCreate()
			return tunnel.NewOpError("create", cfg.ID, "ndms", fmt.Errorf("set MTU: %w", err))
		}
	}

	// Mark interface as global AFTER address/MTU are set.
	// Setting ip global during CreateOpkgTun (atomically with security-level: public)
	// causes Keenetic's nginx to bind to the tunnel IP before the address exists.
	if err := o.ndms.SetIPGlobal(ctx, names.NDMSName); err != nil {
		o.logWarn("create", cfg.ID, "Failed to set ip global: "+err.Error())
	}

	// DNS is not applied in Create — Start handles it with proper tracking.
	// Applying here without tracking would leave orphaned entries on the router.

	// Set NDMS default route if enabled
	if cfg.DefaultRoute {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			o.logWarn("create", cfg.ID, "Failed to set NDMS default route: "+err.Error())
		}
	}

	// Save configuration
	if err := o.ndms.Save(ctx); err != nil {
		rollbackCreate()
		return tunnel.NewOpError("create", cfg.ID, "ndms", err)
	}

	o.logInfo("create", cfg.ID, "Created OpkgTun in NDMS (address + MTU configured)")
	return nil
}

// Start brings up an existing amneziawg interface after our Stop.
// Interface already exists with address and WG config — just bring it up.
// Sequence: ip link set up → InterfaceUp → routes → firewall → Save.
// Used for: Disabled (after our Stop), Dead (after PingCheck).
func (o *OperatorOS5Impl) Start(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	// Bring link up.
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
	}

	// NDMS InterfaceUp sets conf: running.
	o.expectHook(names.NDMSName, "running")
	if err := o.ndms.InterfaceUp(ctx, names.NDMSName); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("interface up: %w", err))
	}

	o.logInfo("start", cfg.ID, "Interface up")

	// Endpoint route.
	if cfg.Endpoint != "" {
		routeEndpoint := endpointWithResolvedIP(cfg.Endpoint, cfg.EndpointIP)
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.KernelDevice, cfg.ISPInterface); err != nil {
			o.logWarn("start", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
		}
	}

	// Track resolved ISP.
	if cfg.ISPInterface != "" {
		o.resolvedISPMu.Lock()
		o.resolvedISP[cfg.ID] = cfg.ISPInterface
		o.resolvedISPMu.Unlock()
	}

	// Default route.
	if cfg.DefaultRoute {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			o.logWarn("start", cfg.ID, "Default route failed (non-fatal): "+err.Error())
		}
	}

	// Firewall.
	if err := o.firewall.AddRules(ctx, names.IfaceName); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "firewall", err)
	}

	// Save.
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("start", cfg.ID, "Failed to save NDMS config: "+err.Error())
	}

	o.logInfo("start", cfg.ID, "Tunnel started (light — existing interface)")
	o.appLog.Info("start", cfg.ID, "Туннель запущен")
	return nil
}

// ColdStart creates a tunnel from scratch or recreates from wrong type (tun → amneziawg).
// Full sequence: OpkgTun → NDMS config → ip link del + ip link add amneziawg →
// ip addr add → wg setconf → ip link set up → InterfaceUp → routes → firewall → Save.
// Used for: BootReady, NotCreated, Broken.
func (o *OperatorOS5Impl) ColdStart(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	// Validate config
	if err := cfg.Validate(); err != nil {
		return tunnel.NewOpError("start", cfg.ID, "", err)
	}

	// === Phase 1: Ensure OpkgTun exists ===
	justCreated := false
	if !o.ndms.OpkgTunExists(ctx, names.NDMSName) {
		if err := o.ndms.CreateOpkgTun(ctx, names.NDMSName, cfg.Name); err != nil {
			return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("create OpkgTun: %w", err))
		}
		justCreated = true
		o.logInfo("start", cfg.ID, "Created OpkgTun in NDMS")
	}

	// === Phase 2: NDMS config ===
	// Always re-apply address/MTU — after ip link del + ip link add, NDMS
	// does not re-apply stored config to the new kernel interface.
	// SetAddress via RCI triggers NDMS to do "ip addr add" on the interface.
	if err := o.ndms.SetAddress(ctx, names.NDMSName, cfg.Address); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
	}

	if err := o.ndms.SetMTU(ctx, names.NDMSName, cfg.MTU); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("set MTU: %w", err))
	}

	if cfg.AddressIPv6 != "" {
		if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set NDMS IPv6 address: "+err.Error())
		}
	}

	// Ensure ip global is set — it's not part of CreateOpkgTun anymore
	// (split out to avoid premature nginx binding), so re-apply on every start.
	if err := o.ndms.SetIPGlobal(ctx, names.NDMSName); err != nil {
		o.logWarn("start", cfg.ID, "Failed to set ip global: "+err.Error())
	}

	o.logInfo("start", cfg.ID, "NDMS config applied (address + MTU + global)")

	if justCreated {
		// Save NDMS config so InterfaceUp works (import flow creates OpkgTun
		// inside Start, and NDMS refuses to bring up unsaved interfaces).
		if err := o.ndms.Save(ctx); err != nil {
			o.rollbackStart(ctx, cfg.ID, names, justCreated)
			return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("save: %w", err))
		}
	}

	// Apply DNS servers (idempotent, re-applied on every start)
	if len(cfg.DNS) > 0 {
		if err := o.ndms.SetDNS(ctx, names.NDMSName, cfg.DNS); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set DNS: "+err.Error())
		} else {
			o.appliedDNSMu.Lock()
			o.appliedDNS[cfg.ID] = cfg.DNS
			o.appliedDNSMu.Unlock()
		}
	}

	// === Phase 3: Start backend (ip link add type amneziawg) ===
	if err := o.backend.Start(ctx, names.IfaceName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "backend", err)
	}

	// Wait for interface to appear in /sys/class/net
	if err := o.backend.WaitReady(ctx, names.IfaceName, interfaceReadyTimeout); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "backend", fmt.Errorf("wait ready: %w", err))
	}

	o.logInfo("start", cfg.ID, fmt.Sprintf("Backend started (%s)", o.backend.Type()))
	o.appLog.Info("start", cfg.ID, fmt.Sprintf("Интерфейс создан (%s)", o.backend.Type()))

	// === Phase 4: Interface config + WireGuard configuration ===
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", names.IfaceName,
		"txqueuelen", "1000", "mtu", fmt.Sprintf("%d", mtu)); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "kernel", fmt.Errorf("configure interface: %w", err))
	}
	o.logInfo("start", cfg.ID, fmt.Sprintf("Kernel interface configured (mtu=%d, qlen=1000)", mtu))

	if err := o.wg.SetConf(ctx, names.IfaceName, cfg.ConfPath); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "wg", err)
	}

	o.logInfo("start", cfg.ID, "WireGuard config applied")

	// === Phase 5: Assign addresses + bring up ===
	// After ip link del + ip link add, this is OUR kernel interface —
	// NDMS does not manage it. We must apply all config ourselves.
	if cfg.Address != "" {
		addr := cfg.Address
		if !strings.Contains(addr, "/") {
			addr += "/32"
		}
		if _, err := o.ipRun(ctx, "/opt/sbin/ip", "address", "add", "dev", names.IfaceName, addr); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set IPv4 address: "+err.Error())
		}
	}
	if cfg.AddressIPv6 != "" {
		if _, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "add", "dev", names.IfaceName, cfg.AddressIPv6+"/128"); err != nil {
			o.logWarn("start", cfg.ID, "Failed to set IPv6 address: "+err.Error())
			o.appLog.Warn("start", cfg.ID, "IPv6 адрес: "+err.Error())
		}
	}

	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
	}

	// NDMS InterfaceUp sets conf: running (intent UP).
	// Always needed in Start: after Stop, InterfaceDown set conf: disabled.
	o.expectHook(names.NDMSName, "running")
	if err := o.ndms.InterfaceUp(ctx, names.NDMSName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("interface up: %w", err))
	}

	o.logInfo("start", cfg.ID, "Interface up")

	// === Phase 6: Set up routing ===
	// Endpoint route: always set up when endpoint is configured.
	// Needed for tunnel chaining (tunnel through tunnel) and routing loop prevention.
	endpointRouteOK := false
	if cfg.Endpoint != "" {
		// Use pre-resolved IP when available — avoids DNS re-resolution which
		// can fail right after start (awg show empty, Go DNS may not work on router).
		routeEndpoint := endpointWithResolvedIP(cfg.Endpoint, cfg.EndpointIP)
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.KernelDevice, cfg.ISPInterface); err != nil {
			o.logWarn("start", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
			o.appLog.Warn("start", cfg.ID, "Не удалось создать endpoint route: "+err.Error())
		} else {
			endpointRouteOK = true
		}
	} else {
		endpointRouteOK = true // no endpoint — nothing to route
	}

	// Track resolved ISP for dashboard display (NDMS name from service layer).
	// SetupEndpointRoute works in kernel namespace and doesn't track NDMS names.
	if cfg.ISPInterface != "" {
		o.resolvedISPMu.Lock()
		o.resolvedISP[cfg.ID] = cfg.ISPInterface
		o.resolvedISPMu.Unlock()
	}

	// Default route: only when DefaultRoute is enabled.
	// NDMS manages the route via the kernel backend.
	// Non-fatal: if NDMS is not ready (e.g. boot race), tunnel starts without
	// default route. HandleWANUp will retry when WAN stabilizes.
	if cfg.DefaultRoute {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			o.logWarn("start", cfg.ID, "Default route failed (non-fatal): "+err.Error())
			o.appLog.Warn("start", cfg.ID, "Не удалось установить маршрут по умолчанию — будет повторная попытка при WAN UP")
		} else {
			if cfg.AddressIPv6 != "" {
				if err := o.ndms.SetIPv6DefaultRoute(ctx, names.NDMSName); err != nil {
					o.logWarn("start", cfg.ID, "Failed to set IPv6 default route: "+err.Error())
				}
			}
			o.appLog.Info("start", cfg.ID, "Маршрут по умолчанию добавлен через "+names.IfaceName)
			if !endpointRouteOK {
				o.appLog.Warn("start", cfg.ID, "Default route установлен без endpoint route — возможны проблемы с маршрутизацией")
			}
		}
	}

	o.logInfo("start", cfg.ID, "Routing configured")

	// === Phase 7: Add firewall rules ===
	// Use kernel interface name (opkgtun0), not NDMS name (OpkgTun0)
	if err := o.firewall.AddRules(ctx, names.IfaceName); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "firewall", err)
	}

	o.logInfo("start", cfg.ID, "Firewall rules added")
	o.appLog.Info("start", cfg.ID, "Правила файрвола добавлены для "+names.IfaceName)

	// === Phase 8: Save NDMS configuration ===
	// Saves interface state (address, MTU, conf: running).
	// Routes are kernel-level volatile — re-created on every Start.
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("start", cfg.ID, "Failed to save NDMS config: "+err.Error())
	}

	o.logInfo("start", cfg.ID, "Tunnel started successfully")
	return nil
}

// TeardownForRestart removes firewall, routes, DNS, and sets link down
// WITHOUT calling InterfaceDown. NDMS intent stays "running" so no conf-layer
// hooks are fired. The amneziawg interface is preserved (only link toggled down)
// so that a light Start can bring it back up.
func (o *OperatorOS5Impl) TeardownForRestart(ctx context.Context, tunnelID string) {
	names := tunnel.NewNames(tunnelID)

	// Remove firewall rules (no-op if already absent).
	_ = o.firewall.RemoveRules(ctx, names.IfaceName)

	// Remove endpoint route from kernel + clear tracking.
	_ = o.CleanupEndpointRoute(ctx, tunnelID)

	// Clear DNS servers from NDMS (Start will re-apply).
	o.clearAppliedDNS(ctx, tunnelID, names)

	// Link down only — amneziawg interface stays, WG config stays loaded.
	// NO backend.Stop (ip link del) — that would destroy the device and
	// require ColdStart to recreate, which can fail on NDMS transient errors.
	o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName)

	// Clear in-memory tracking.
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	// NO InterfaceDown — NDMS intent stays "running".
	o.logInfo("teardown", tunnelID, "Teardown for restart (link down, device preserved)")
}

// Stop brings down a tunnel without destroying the interface.
// ip link set down + InterfaceDown (conf: disabled) + Save.
// NDMS handles routing/failover automatically when link goes down.
// Interface stays as amneziawg with WG config and address loaded.
func (o *OperatorOS5Impl) Stop(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)

	// Bring link down at kernel level.
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName); err != nil {
		o.logWarn("stop", tunnelID, "ip link set down: "+err.Error())
	}

	// InterfaceDown sets conf: disabled — NDMS won't bring it up on its own.
	o.interfaceDownBestEffort(ctx, tunnelID, names.NDMSName)

	// Save NDMS config so router UI reflects conf: disabled.
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("stop", tunnelID, "Failed to save NDMS config: "+err.Error())
	}

	// Clear resolved ISP tracking.
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	o.logInfo("stop", tunnelID, "Tunnel stopped (link down, conf: disabled)")
	o.appLog.Info("stop", tunnelID, "Туннель остановлен")
	return nil
}

// clearAppliedDNS removes DNS servers that were applied during Start and clears tracking.
func (o *OperatorOS5Impl) clearAppliedDNS(ctx context.Context, tunnelID string, names tunnel.Names) {
	o.appliedDNSMu.Lock()
	servers := o.appliedDNS[tunnelID]
	delete(o.appliedDNS, tunnelID)
	o.appliedDNSMu.Unlock()

	if len(servers) > 0 {
		_ = o.ndms.ClearDNS(ctx, names.NDMSName, servers)
		o.logInfo("stop", tunnelID, "DNS servers removed")
	}
}

// interfaceDownBestEffort tries to set NDMS conf: disabled.
// Retries up to 3 times for transient failures (NDMS busy/timeout).
// Exit 122 = NDMS permanent rejection (already down) — not an error.
func (o *OperatorOS5Impl) interfaceDownBestEffort(ctx context.Context, tunnelID, ndmsName string) {
	o.expectHook(ndmsName, "disabled")
	for attempt := 1; attempt <= 3; attempt++ {
		err := o.ndms.InterfaceDown(ctx, ndmsName)
		if err == nil {
			o.logInfo("stop", tunnelID, "Interface down (conf: disabled)")
			return
		}
		if strings.Contains(err.Error(), "exit status 122") {
			o.logInfo("stop", tunnelID, "InterfaceDown: already disabled (exit 122)")
			return
		}
		o.logWarn("stop", tunnelID, fmt.Sprintf("InterfaceDown attempt %d/3 failed: %s", attempt, err))
		if attempt < 3 {
			time.Sleep(1 * time.Second)
		}
	}
	// Not fatal — cleanup continues regardless.
	// The enabled/disabled state is tracked in the program's own JSON storage.
}

// Delete completely removes a tunnel.
func (o *OperatorOS5Impl) Delete(ctx context.Context, stored *storage.AWGTunnel) error {
	names := tunnel.NewNames(stored.ID)

	// 1. Remove endpoint route from kernel (host route to VPN server via WAN).
	//    Uses persisted IP. Fallback to DNS for old tunnels without stored IP.
	endpointIP := stored.ResolvedEndpointIP
	if endpointIP == "" && stored.Peer.Endpoint != "" {
		if ip, err := netutil.ResolveEndpointIP(stored.Peer.Endpoint); err == nil {
			endpointIP = ip
		}
	}
	if endpointIP != "" {
		o.delKernelHostRoute(ctx, endpointIP)
		_ = o.ndms.RemoveHostRoute(ctx, endpointIP)
	}

	// 2. Remove NDMS interface — cleans everything:
	//    address, MTU, security-level, ip global, default route, DNS name-servers
	// DeleteOpkgTun triggers conf: disabled hook before removal.
	o.expectHook(names.NDMSName, "disabled")
	if err := o.ndms.DeleteOpkgTun(ctx, names.NDMSName); err != nil {
		o.logWarn("delete", stored.ID, "DeleteOpkgTun: "+err.Error())
	}

	// 3. Remove kernel interface (our amneziawg — NDMS can't delete what we created)
	o.ipRun(ctx, "/opt/sbin/ip", "link", "del", "dev", names.IfaceName)

	// 4. Persist NDMS config
	_ = o.ndms.Save(ctx)

	// 5. Clear in-memory tracking
	o.endpointRoutesMu.Lock()
	delete(o.endpointRoutes, stored.ID)
	o.endpointRoutesMu.Unlock()
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, stored.ID)
	o.resolvedISPMu.Unlock()
	o.appliedDNSMu.Lock()
	delete(o.appliedDNS, stored.ID)
	o.appliedDNSMu.Unlock()

	o.logInfo("delete", stored.ID, "Tunnel deleted")
	o.appLog.Info("delete", stored.ID, "Туннель удалён")
	return nil
}

// Recover attempts to bring a broken tunnel into a consistent state.
// Stops the backend, removes the kernel interface, and brings down the
// NDMS interface to reach a clean state for restart.
func (o *OperatorOS5Impl) Recover(ctx context.Context, tunnelID string, state tunnel.StateInfo) error {
	names := tunnel.NewNames(tunnelID)

	o.logInfo("recover", tunnelID, fmt.Sprintf("Recovering from state: %s (%s)", state.State, state.Details))

	// 1. Stop via backend (removes kernel interface)
	if err := o.backend.Stop(ctx, names.IfaceName); err != nil {
		o.logWarn("recover", tunnelID, "Backend stop: "+err.Error())
	}

	// 2. Bring NDMS interface down but NEVER delete OpkgTun.
	// Deleting OpkgTun destroys Policy bindings that the user configured
	// through NDMS — these cannot be recreated automatically.
	// Start will re-configure NDMS via SetAddress + InterfaceUp (phase 4),
	// which re-associates OpkgTun with the newly created device.
	o.expectHook(names.NDMSName, "disabled")
	_ = o.ndms.InterfaceDown(ctx, names.NDMSName)

	// Clean up DNS entries
	o.clearAppliedDNS(ctx, tunnelID, names)

	o.logInfo("recover", tunnelID, "Recovery complete")
	return nil
}

// Reconcile re-applies NDMS/system configuration around an already-running process.
// Assumes: process is running, interface exists. Re-applies WG config, NDMS, routing, firewall.
func (o *OperatorOS5Impl) Reconcile(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	o.logInfo("reconcile", cfg.ID, "Reconciling NDMS state around running process")
	o.appLog.Info("reconcile", cfg.ID, "Восстановление конфигурации NDMS")

	// === Phase 1: Ensure OpkgTun exists ===
	justCreated := false
	if !o.ndms.OpkgTunExists(ctx, names.NDMSName) {
		if err := o.ndms.CreateOpkgTun(ctx, names.NDMSName, cfg.Name); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("create OpkgTun: %w", err))
		}
		justCreated = true
		o.logInfo("reconcile", cfg.ID, "Created OpkgTun in NDMS")
	}

	// === Phase 2: Recreate kernel interface as amneziawg type ===
	// After reboot, NDMS creates a generic OpkgTun interface from saved config.
	// awg commands require an amneziawg-type interface, so we must recreate it.
	// ip link del triggers transient NDMS state:error — safe under per-tunnel lock.
	o.ipRun(ctx, "/opt/sbin/ip", "link", "del", "dev", names.IfaceName)
	if err := o.backend.Start(ctx, names.IfaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "backend", err)
	}
	if err := o.backend.WaitReady(ctx, names.IfaceName, interfaceReadyTimeout); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "backend", fmt.Errorf("wait ready: %w", err))
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", names.IfaceName,
		"txqueuelen", "1000", "mtu", fmt.Sprintf("%d", mtu)); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "kernel", fmt.Errorf("configure interface: %w", err))
	}
	o.logInfo("reconcile", cfg.ID, "Kernel interface recreated as amneziawg")

	// === Phase 3: Apply WireGuard configuration ===
	if err := o.wg.SetConf(ctx, names.IfaceName, cfg.ConfPath); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "wg", err)
	}
	o.logInfo("reconcile", cfg.ID, "WireGuard config applied")

	// === Phase 3: Configure NDMS interface ===
	// Full config (address + MTU + IPv6) only when OpkgTun was just created.
	// SetAddress on a running kernel-mode interface fails (exit 122).
	// MTU is always re-applied: NDMS may have default (1420) if config was lost,
	// causing oversized encrypted packets that degrade upload throughput.
	if justCreated {
		if err := o.ndms.SetAddress(ctx, names.NDMSName, cfg.Address); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
		}
		if cfg.AddressIPv6 != "" {
			if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set NDMS IPv6 address: "+err.Error())
			}
		}
	}
	if cfg.MTU > 0 {
		if err := o.ndms.SetMTU(ctx, names.NDMSName, cfg.MTU); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to re-apply NDMS MTU: "+err.Error())
		}
	}

	// Ensure ip global is set
	if err := o.ndms.SetIPGlobal(ctx, names.NDMSName); err != nil {
		o.logWarn("reconcile", cfg.ID, "Failed to set ip global: "+err.Error())
	}

	// Re-apply DNS servers (may have been lost after reboot)
	if len(cfg.DNS) > 0 {
		if err := o.ndms.SetDNS(ctx, names.NDMSName, cfg.DNS); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to re-apply DNS: "+err.Error())
		} else {
			o.appliedDNSMu.Lock()
			o.appliedDNS[cfg.ID] = cfg.DNS
			o.appliedDNSMu.Unlock()
		}
	}

	// Assign addresses on kernel interface (we own it after ip link del + add)
	if cfg.Address != "" {
		addr := cfg.Address
		if !strings.Contains(addr, "/") {
			addr += "/32"
		}
		if _, err := o.ipRun(ctx, "/opt/sbin/ip", "address", "add", "dev", names.IfaceName, addr); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to set IPv4 address: "+err.Error())
		}
	}
	if cfg.AddressIPv6 != "" {
		if _, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "add", "dev", names.IfaceName, cfg.AddressIPv6+"/128"); err != nil {
			o.logWarn("reconcile", cfg.ID, "Failed to set IPv6 address: "+err.Error())
			o.appLog.Warn("reconcile", cfg.ID, "IPv6 адрес: "+err.Error())
		}
	}

	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
	}

	// NDMS InterfaceUp: only when OpkgTun was just created.
	if justCreated {
		o.expectHook(names.NDMSName, "running")
		if err := o.ndms.InterfaceUp(ctx, names.NDMSName); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("interface up: %w", err))
		}
	}

	o.logInfo("reconcile", cfg.ID, "Interface configured and up")

	// === Phase 4: Set up routing ===
	// Endpoint route: always set up when endpoint is configured
	endpointRouteOK := false
	if cfg.Endpoint != "" {
		routeEndpoint := endpointWithResolvedIP(cfg.Endpoint, cfg.EndpointIP)
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.KernelDevice, cfg.ISPInterface); err != nil {
			o.logWarn("reconcile", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
			o.appLog.Warn("reconcile", cfg.ID, "Не удалось создать endpoint route: "+err.Error())
		} else {
			endpointRouteOK = true
		}
	} else {
		endpointRouteOK = true
	}

	// Track resolved ISP for dashboard display (NDMS name from service layer).
	if cfg.ISPInterface != "" {
		o.resolvedISPMu.Lock()
		o.resolvedISP[cfg.ID] = cfg.ISPInterface
		o.resolvedISPMu.Unlock()
	}

	// Default route: only when DefaultRoute is enabled.
	if cfg.DefaultRoute {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			_ = o.CleanupEndpointRoute(ctx, cfg.ID)
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set default route: %w", err))
		}
		if cfg.AddressIPv6 != "" {
			if err := o.ndms.SetIPv6DefaultRoute(ctx, names.NDMSName); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set IPv6 default route: "+err.Error())
			}
		}
		o.appLog.Info("reconcile", cfg.ID, "Маршрут по умолчанию добавлен через "+names.IfaceName)
		if !endpointRouteOK {
			o.appLog.Warn("reconcile", cfg.ID, "Default route установлен без endpoint route — возможны проблемы с маршрутизацией")
		}
	}

	o.logInfo("reconcile", cfg.ID, "Routing configured")

	// === Phase 5: Add firewall rules ===
	if err := o.firewall.AddRules(ctx, names.IfaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "firewall", err)
	}
	o.logInfo("reconcile", cfg.ID, "Firewall rules added")
	o.appLog.Info("reconcile", cfg.ID, "Правила файрвола добавлены для "+names.IfaceName)

	// === Phase 6: Save NDMS configuration ===
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("reconcile", cfg.ID, "Failed to save NDMS config: "+err.Error())
	}

	o.logInfo("reconcile", cfg.ID, "Reconciliation complete")
	o.appLog.Info("reconcile", cfg.ID, "Конфигурация NDMS восстановлена")
	return nil
}

// SetDefaultRoute adds a default route through the tunnel interface.
func (o *OperatorOS5Impl) SetDefaultRoute(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
		return err
	}
	return o.ndms.Save(ctx)
}

// RemoveDefaultRoute removes the default route through the tunnel interface.
func (o *OperatorOS5Impl) RemoveDefaultRoute(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	o.ndms.RemoveIPv6DefaultRoute(ctx, names.NDMSName)
	if err := o.ndms.RemoveDefaultRoute(ctx, names.NDMSName); err != nil {
		return err
	}
	return o.ndms.Save(ctx)
}

// Suspend sets link down without removing the interface or changing NDMS conf.
// NDMS sees pending state and handles failover automatically.
// Routes and firewall are NOT touched — NDMS manages failover.
func (o *OperatorOS5Impl) Suspend(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName); err != nil {
		return fmt.Errorf("suspend: ip link set down: %w", err)
	}
	o.logInfo("suspend", tunnelID, "Interface suspended (link down)")
	o.appLog.Info("suspend", tunnelID, "Интерфейс приостановлен")
	return nil
}

// Resume sets link up after Suspend. NDMS restores routing automatically.
func (o *OperatorOS5Impl) Resume(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
		return fmt.Errorf("resume: ip link set up: %w", err)
	}
	o.logInfo("resume", tunnelID, "Interface resumed (link up)")
	o.appLog.Info("resume", tunnelID, "Интерфейс возобновлён")
	return nil
}

// ApplyConfig applies a new WireGuard config to a running tunnel.
func (o *OperatorOS5Impl) ApplyConfig(ctx context.Context, tunnelID, configPath string) error {
	names := tunnel.NewNames(tunnelID)

	if err := o.wg.SetConf(ctx, names.IfaceName, configPath); err != nil {
		return tunnel.NewOpError("apply_config", tunnelID, "wg", err)
	}

	o.logInfo("apply_config", tunnelID, "Config applied")
	return nil
}

// SetMTU sets MTU on a running tunnel interface via NDMS.
func (o *OperatorOS5Impl) SetMTU(ctx context.Context, tunnelID string, mtu int) error {
	names := tunnel.NewNames(tunnelID)
	if err := o.ndms.SetMTU(ctx, names.NDMSName, mtu); err != nil {
		return tunnel.NewOpError("set_mtu", tunnelID, "ndms", err)
	}
	o.logInfo("set_mtu", tunnelID, fmt.Sprintf("MTU set to %d", mtu))
	return nil
}

// SyncDNS updates DNS servers on a running tunnel's NDMS interface.
func (o *OperatorOS5Impl) SyncDNS(ctx context.Context, tunnelID string, dns []string) error {
	names := tunnel.NewNames(tunnelID)
	// Clear previously applied DNS first
	o.appliedDNSMu.RLock()
	oldDNS := o.appliedDNS[tunnelID]
	o.appliedDNSMu.RUnlock()
	if len(oldDNS) > 0 {
		_ = o.ndms.ClearDNS(ctx, names.NDMSName, oldDNS)
	}
	if len(dns) == 0 {
		o.appliedDNSMu.Lock()
		delete(o.appliedDNS, tunnelID)
		o.appliedDNSMu.Unlock()
	} else {
		if err := o.ndms.SetDNS(ctx, names.NDMSName, dns); err != nil {
			return tunnel.NewOpError("sync_dns", tunnelID, "ndms", err)
		}
		o.appliedDNSMu.Lock()
		o.appliedDNS[tunnelID] = dns
		o.appliedDNSMu.Unlock()
	}
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("sync_dns", tunnelID, "Failed to save NDMS config: "+err.Error())
	}
	o.logInfo("sync_dns", tunnelID, fmt.Sprintf("DNS synced: %v", dns))
	return nil
}

// SyncAddress updates IPv4/IPv6 address on a running tunnel's NDMS interface.
func (o *OperatorOS5Impl) SyncAddress(ctx context.Context, tunnelID string, address, ipv6 string) error {
	names := tunnel.NewNames(tunnelID)
	if err := o.ndms.SetAddress(ctx, names.NDMSName, address); err != nil {
		return tunnel.NewOpError("sync_address", tunnelID, "ndms", err)
	}
	if ipv6 != "" {
		if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, ipv6); err != nil {
			o.logWarn("sync_address", tunnelID, "Failed to set IPv6: "+err.Error())
		}
	} else {
		o.ndms.ClearIPv6Address(ctx, names.NDMSName)
	}
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("sync_address", tunnelID, "Failed to save NDMS config: "+err.Error())
	}
	o.logInfo("sync_address", tunnelID, fmt.Sprintf("Address synced: %s, IPv6: %s", address, ipv6))
	return nil
}

// UpdateDescription updates the NDMS interface description for a tunnel.
func (o *OperatorOS5Impl) UpdateDescription(ctx context.Context, tunnelID, description string) error {
	names := tunnel.NewNames(tunnelID)
	if err := o.ndms.SetDescription(ctx, names.NDMSName, description); err != nil {
		return tunnel.NewOpError("update_description", tunnelID, "ndms", err)
	}
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("update_description", tunnelID, "Failed to save NDMS config: "+err.Error())
	}
	o.logInfo("update_description", tunnelID, fmt.Sprintf("Description updated to %q", description))
	return nil
}

// GetDefaultGatewayInterface returns the current default gateway interface name.
func (o *OperatorOS5Impl) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	return o.ndms.GetDefaultGatewayInterface(ctx)
}

// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
func (o *OperatorOS5Impl) GetResolvedISP(tunnelID string) string {
	o.resolvedISPMu.RLock()
	defer o.resolvedISPMu.RUnlock()
	return o.resolvedISP[tunnelID]
}

// rollbackStart cleans up after a failed start operation.
// justCreated indicates whether we created the OpkgTun in this Start attempt.
// When false (OpkgTun already existed), we preserve NDMS conf state (conf: running)
// so the tunnel stays in StateNeedsStart and can be retried.
func (o *OperatorOS5Impl) rollbackStart(ctx context.Context, tunnelID string, names tunnel.Names, justCreated bool) {
	o.logInfo("rollback", tunnelID, "Rolling back failed start")

	o.clearAppliedDNS(ctx, tunnelID, names)
	_ = o.firewall.RemoveRules(ctx, names.IfaceName)
	if justCreated {
		// We created this OpkgTun — clean it up entirely.
		o.expectHook(names.NDMSName, "disabled")
		_ = o.ndms.InterfaceDown(ctx, names.NDMSName)
	}
	// Don't call InterfaceDown for existing OpkgTun — preserve conf: running.
	_ = o.backend.Stop(ctx, names.IfaceName)
}

// logInfo logs an info message.
func (o *OperatorOS5Impl) logInfo(action, target, message string) {
	if o.log != nil {
		o.log.Infof("[%s] %s: %s", action, target, message)
	}
}

// logWarn logs a warning message.
func (o *OperatorOS5Impl) logWarn(action, target, message string) {
	if o.log != nil {
		o.log.Warnf("[%s] %s: %s", action, target, message)
	}
}

// HasWANIPv6 checks if a WAN interface has IPv6 connectivity via NDMS RCI.
func (o *OperatorOS5Impl) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	return o.ndms.HasWANIPv6(ctx, ifaceName)
}

// GetSystemName resolves an NDMS ID to its kernel interface name via NDMS RCI.
func (o *OperatorOS5Impl) GetSystemName(ctx context.Context, ndmsID string) string {
	return o.ndms.GetSystemName(ctx, ndmsID)
}

// SetAppLogger sets the web UI logger.
func (o *OperatorOS5Impl) SetAppLogger(logger logging.AppLogger) {
	o.appLog = logging.NewScopedLogger(logger, logging.GroupTunnel, logging.SubOps)
}

// Ensure OperatorOS5Impl implements Operator interface.
var _ Operator = (*OperatorOS5Impl)(nil)
