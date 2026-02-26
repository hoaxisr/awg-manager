package ops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/proc"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
	"github.com/hoaxisr/awg-manager/internal/tunnel/firewall"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

const (
	interfaceReadyTimeout = 10 * time.Second
	socketReadyTimeout    = 5 * time.Second
)

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

	appLogger AppLogger

	// Endpoint route tracking (tunnelID -> endpointIP)
	endpointRoutes   map[string]string
	endpointRoutesMu sync.RWMutex

	// Resolved ISP tracking (tunnelID -> WAN interface name)
	// Tracks the actual WAN used for auto-mode tunnels.
	resolvedISP   map[string]string
	resolvedISPMu sync.RWMutex
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

	// Configure address and MTU before Save so NDMS has the full config.
	// This is the only place we call SetAddress/SetMTU for new tunnels —
	// Start() skips NDMS config when OpkgTun already exists.
	if cfg.Address != "" {
		if err := o.ndms.SetAddress(ctx, names.NDMSName, cfg.Address); err != nil {
			_ = o.ndms.DeleteOpkgTun(ctx, names.NDMSName)
			return tunnel.NewOpError("create", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
		}
	}
	if cfg.AddressIPv6 != "" {
		if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
			_ = o.ndms.DeleteOpkgTun(ctx, names.NDMSName)
			return tunnel.NewOpError("create", cfg.ID, "ndms", fmt.Errorf("set ipv6 address: %w", err))
		}
	}
	if cfg.MTU > 0 {
		if err := o.ndms.SetMTU(ctx, names.NDMSName, cfg.MTU); err != nil {
			_ = o.ndms.DeleteOpkgTun(ctx, names.NDMSName)
			return tunnel.NewOpError("create", cfg.ID, "ndms", fmt.Errorf("set MTU: %w", err))
		}
	}

	// Set NDMS default route if enabled
	if cfg.DefaultRoute {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			o.logWarn("create", cfg.ID, "Failed to set NDMS default route: "+err.Error())
		}
	}

	// Save configuration
	if err := o.ndms.Save(ctx); err != nil {
		// Rollback
		_ = o.ndms.DeleteOpkgTun(ctx, names.NDMSName)
		return tunnel.NewOpError("create", cfg.ID, "ndms", err)
	}

	o.logInfo("create", cfg.ID, "Created OpkgTun in NDMS (address + MTU configured)")
	return nil
}

// Start starts a tunnel.
// Sequence: OpkgTun → [NDMS config if just created] → backend (ip link add) →
// kernel config (MTU/qlen) → WG → ip link up → NDMS up → routes → firewall → save.
// NDMS config (SetAddress/SetMTU) is only applied when OpkgTun was just created
// (import flow). For normal starts and boot, NDMS already has the saved config.
func (o *OperatorOS5Impl) Start(ctx context.Context, cfg tunnel.Config) error {
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

	// === Phase 2: NDMS config (only when OpkgTun was just created) ===
	// For existing OpkgTun, NDMS already has address/MTU from Create or previous Start.
	// Calling SetAddress on a running kernel-mode interface fails (exit 122).
	if justCreated {
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

		o.logInfo("start", cfg.ID, "NDMS config applied (address + MTU)")
	} else {
		o.logInfo("start", cfg.ID, "Skipping NDMS config (OpkgTun already configured)")
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
	o.appLog("start", cfg.ID, fmt.Sprintf("Интерфейс создан (%s)", o.backend.Type()))

	// === Phase 4: Kernel config + WireGuard configuration ===
	if o.backend.Type() == backend.TypeKernel {
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
	}

	if err := o.wg.SetConf(ctx, names.IfaceName, cfg.ConfPath); err != nil {
		o.rollbackStart(ctx, cfg.ID, names, justCreated)
		return tunnel.NewOpError("start", cfg.ID, "wg", err)
	}

	o.logInfo("start", cfg.ID, "WireGuard config applied")

	// === Phase 5: Bring up ===
	if cfg.AddressIPv6 != "" {
		if o.backend.Type() == backend.TypeKernel {
			// Kernel: volatile ip command (lost on reboot, always needed)
			if _, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "add", "dev", names.IfaceName, cfg.AddressIPv6+"/128"); err != nil {
				o.logWarn("start", cfg.ID, "Failed to set IPv6 address: "+err.Error())
				o.appLogWarn("start", cfg.ID, "IPv6 адрес: "+err.Error())
			}
		} else {
			// Userspace: persistent via NDMS (manages the interface fully)
			if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
				o.logWarn("start", cfg.ID, "Failed to set IPv6 address via NDMS: "+err.Error())
				o.appLogWarn("start", cfg.ID, "IPv6 адрес: "+err.Error())
			}
		}
	}

	if o.backend.Type() == backend.TypeKernel {
		// Kernel: ip link set up brings the actual network interface up.
		if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
			o.rollbackStart(ctx, cfg.ID, names, justCreated)
			return tunnel.NewOpError("start", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
		}
	}

	// NDMS InterfaceUp sets conf: running (intent UP).
	// Always needed in Start: after Stop, InterfaceDown set conf: disabled.
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
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.ISPInterface); err != nil {
			o.logWarn("start", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
			o.appLogWarn("start", cfg.ID, "Не удалось создать endpoint route — туннель не может быть первым в политике доступа по умолчанию")
		} else {
			endpointRouteOK = true
		}
	} else {
		endpointRouteOK = true // no endpoint — nothing to route
	}

	// Default route: only when DefaultRoute is enabled.
	// Kernel mode: not supported (default route with metric 0 breaks internet
	// when endpoint route is missing). Userspace: NDMS manages the route.
	if cfg.DefaultRoute && o.backend.Type() != backend.TypeKernel {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			_ = o.CleanupEndpointRoute(ctx, cfg.ID)
			o.rollbackStart(ctx, cfg.ID, names, justCreated)
			return tunnel.NewOpError("start", cfg.ID, "ndms", fmt.Errorf("set default route: %w", err))
		}
		if cfg.AddressIPv6 != "" {
			if err := o.ndms.SetIPv6DefaultRoute(ctx, names.NDMSName); err != nil {
				o.logWarn("start", cfg.ID, "Failed to set IPv6 default route: "+err.Error())
			}
		}
		o.appLog("start", cfg.ID, "Маршрут по умолчанию добавлен через "+names.IfaceName)
		if !endpointRouteOK {
			o.appLogWarn("start", cfg.ID, "Default route установлен без endpoint route — возможны проблемы с маршрутизацией")
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
	o.appLog("start", cfg.ID, "Правила файрвола добавлены для "+names.IfaceName)

	// === Phase 8: Save NDMS configuration ===
	// For kernel: saves interface state (address, MTU, conf: running).
	// Routes are kernel-level volatile — re-created on every Start.
	// For userspace: saves everything including NDMS-managed routes.

	// Ensure NDMS default route exists (only when enabled)
	if cfg.DefaultRoute {
		_ = o.ndms.SetDefaultRoute(ctx, names.NDMSName)
		if cfg.AddressIPv6 != "" {
			_ = o.ndms.SetIPv6DefaultRoute(ctx, names.NDMSName)
		}
	}

	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("start", cfg.ID, "Failed to save NDMS config: "+err.Error())
	}

	o.logInfo("start", cfg.ID, "Tunnel started successfully")
	return nil
}

// Stop stops a tunnel.
//
// Order depends on backend type:
//   - Kernel:    InterfaceDown → firewall → routes → ip link del
//     (InterfaceDown needs the device present to succeed; ip link del removes it)
//   - Userspace: firewall → routes → kill process → InterfaceDown
//     (NDMS can't bring TUN down while process owns it; kill first)
func (o *OperatorOS5Impl) Stop(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)

	if o.backend.Type() == backend.TypeKernel {
		return o.stopKernel(ctx, tunnelID, names)
	}
	return o.stopUserspace(ctx, tunnelID, names)
}

// stopKernel stops a kernel-mode tunnel.
// InterfaceDown FIRST (device is present, NDMS can bring it down),
// then ip link del removes the device.
func (o *OperatorOS5Impl) stopKernel(ctx context.Context, tunnelID string, names tunnel.Names) error {
	// === Phase 1: Bring interface down (sets conf: disabled) ===
	// Device is still present → NDMS can bring it down cleanly.
	o.interfaceDownBestEffort(ctx, tunnelID, names.NDMSName)

	// === Phase 2: Remove firewall rules ===
	_ = o.firewall.RemoveRules(ctx, names.IfaceName)
	o.logInfo("stop", tunnelID, "Firewall rules removed")
	o.appLog("stop", tunnelID, "Правила файрвола удалены")

	// === Phase 3: Remove routes ===
	_ = o.CleanupEndpointRoute(ctx, tunnelID)
	o.ndms.RemoveIPv6DefaultRoute(ctx, names.NDMSName)
	o.logInfo("stop", tunnelID, "Routes removed")

	// === Phase 4: Remove kernel interface (ip link del) ===
	if err := o.backend.Stop(ctx, names.IfaceName); err != nil {
		o.logWarn("stop", tunnelID, "Failed to stop backend: "+err.Error())
	} else {
		o.logInfo("stop", tunnelID, "Backend stopped (kernel interface removed)")
	}

	// Save NDMS config so router UI reflects conf: disabled
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("stop", tunnelID, "Failed to save NDMS config: "+err.Error())
	}

	// Clear resolved ISP tracking
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	o.logInfo("stop", tunnelID, "Tunnel stopped successfully")
	return nil
}

// stopUserspace stops a userspace-mode tunnel.
// Kill process FIRST (NDMS can't bring TUN down while process owns it),
// then InterfaceDown sets conf: disabled.
func (o *OperatorOS5Impl) stopUserspace(ctx context.Context, tunnelID string, names tunnel.Names) error {
	// === Phase 1: Remove firewall rules ===
	_ = o.firewall.RemoveRules(ctx, names.IfaceName)
	o.logInfo("stop", tunnelID, "Firewall rules removed")
	o.appLog("stop", tunnelID, "Правила файрвола удалены")

	// === Phase 2: Remove routes ===
	_ = o.ndms.RemoveDefaultRoute(ctx, names.NDMSName)
	o.ndms.RemoveIPv6DefaultRoute(ctx, names.NDMSName)
	o.appLog("stop", tunnelID, "Маршрут по умолчанию удалён")
	_ = o.CleanupEndpointRoute(ctx, tunnelID)
	o.logInfo("stop", tunnelID, "Routes removed")

	// === Phase 3: Kill process ===
	// MUST be before InterfaceDown — NDMS can't bring TUN down while process owns it.
	if err := o.backend.Stop(ctx, names.IfaceName); err != nil {
		o.logWarn("stop", tunnelID, "Failed to stop backend: "+err.Error())
	} else {
		o.logInfo("stop", tunnelID, "Backend process stopped")
	}

	// === Phase 4: Bring interface down (sets conf: disabled) ===
	// Process is dead, TUN is gone → NDMS cleanly sets conf: disabled.
	o.interfaceDownBestEffort(ctx, tunnelID, names.NDMSName)

	// Save NDMS config so router UI reflects conf: disabled
	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("stop", tunnelID, "Failed to save NDMS config: "+err.Error())
	}

	// Clear resolved ISP tracking
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	o.logInfo("stop", tunnelID, "Tunnel stopped successfully")
	return nil
}

// interfaceDownBestEffort tries to set NDMS conf: disabled.
// Retries up to 3 times for transient failures (NDMS busy/timeout).
// Exit 122 = NDMS permanent rejection (already down) — not an error.
func (o *OperatorOS5Impl) interfaceDownBestEffort(ctx context.Context, tunnelID, ndmsName string) {
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
func (o *OperatorOS5Impl) Delete(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)

	// Stop first (ignores errors if not running)
	_ = o.Stop(ctx, tunnelID)

	// Remove OpkgTun from NDMS
	if err := o.ndms.DeleteOpkgTun(ctx, names.NDMSName); err != nil {
		return tunnel.NewOpError("delete", tunnelID, "ndms", err)
	}

	// Kernel: force-remove interface as safety net.
	// NDMS may lose control of the interface and leave it as a zombie.
	// Userspace: TUN already gone (process killed in Stop), skip.
	if o.backend.Type() == backend.TypeKernel {
		o.ipRun(ctx, "/opt/sbin/ip", "link", "del", "dev", names.IfaceName)
	}

	// Save configuration
	_ = o.ndms.Save(ctx)

	o.logInfo("delete", tunnelID, "Tunnel deleted")
	return nil
}

// Recover attempts to bring a broken tunnel into a consistent state.
// Performs full cross-backend cleanup: kills userspace process, removes kernel
// interface, and brings down NDMS interface. This handles backend mode switches
// where resources from the previous backend may still exist.
func (o *OperatorOS5Impl) Recover(ctx context.Context, tunnelID string, state tunnel.StateInfo) error {
	names := tunnel.NewNames(tunnelID)

	o.logInfo("recover", tunnelID, fmt.Sprintf("Recovering from state: %s (%s)", state.State, state.Details))

	// 1. Stop via current backend (handles the normal case)
	if err := o.backend.Stop(ctx, names.IfaceName); err != nil {
		o.logWarn("recover", tunnelID, "Backend stop: "+err.Error())
	}

	// 2. Kill stale userspace process (handles kernel←userspace switch).
	// If current backend is kernel, it doesn't know about PID files.
	if o.backend.Type() == backend.TypeKernel {
		p := proc.NewProcess(tunnelID, "", nil)
		if p.IsRunning() {
			o.logInfo("recover", tunnelID, "Killing stale userspace process")
			_ = p.Stop()
		}
	}

	// 3. Remove stale kernel interface (handles userspace←kernel switch).
	// If current backend is userspace, it doesn't do ip link del.
	if o.backend.Type() == backend.TypeUserspace {
		if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "del", "dev", names.IfaceName); err == nil {
			o.logInfo("recover", tunnelID, "Removed stale kernel interface")
		}
	}

	// 4. Bring NDMS interface down but NEVER delete OpkgTun.
	// Deleting OpkgTun destroys Policy bindings that the user configured
	// through NDMS — these cannot be recreated automatically.
	// Start will re-configure NDMS via SetAddress + InterfaceUp (phase 4),
	// which re-associates OpkgTun with the newly created device.
	_ = o.ndms.InterfaceDown(ctx, names.NDMSName)

	o.logInfo("recover", tunnelID, "Recovery complete")
	return nil
}

// Reconcile re-applies NDMS/system configuration around an already-running process.
// Assumes: process is running, interface exists. Re-applies WG config, NDMS, routing, firewall.
func (o *OperatorOS5Impl) Reconcile(ctx context.Context, cfg tunnel.Config) error {
	names := tunnel.NewNames(cfg.ID)

	o.logInfo("reconcile", cfg.ID, "Reconciling NDMS state around running process")
	o.appLog("reconcile", cfg.ID, "Восстановление конфигурации NDMS")

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
	if o.backend.Type() == backend.TypeKernel {
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
	}

	// === Phase 3: Apply WireGuard configuration ===
	if err := o.wg.SetConf(ctx, names.IfaceName, cfg.ConfPath); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "wg", err)
	}
	o.logInfo("reconcile", cfg.ID, "WireGuard config applied")

	// === Phase 3: Configure NDMS interface ===
	// Only set address/MTU via NDMS when OpkgTun was just created.
	// For existing OpkgTun, NDMS already has the config. Calling SetAddress
	// on a running kernel-mode interface fails (exit 122).
	if justCreated {
		if err := o.ndms.SetAddress(ctx, names.NDMSName, cfg.Address); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set address: %w", err))
		}
		if err := o.ndms.SetMTU(ctx, names.NDMSName, cfg.MTU); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set MTU: %w", err))
		}
		if cfg.AddressIPv6 != "" {
			if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set NDMS IPv6 address: "+err.Error())
			}
		}
	}

	if cfg.AddressIPv6 != "" {
		if o.backend.Type() == backend.TypeKernel {
			if _, err := o.ipRun(ctx, "/opt/sbin/ip", "-6", "address", "add", "dev", names.IfaceName, cfg.AddressIPv6+"/128"); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set IPv6 address: "+err.Error())
				o.appLogWarn("reconcile", cfg.ID, "IPv6 адрес: "+err.Error())
			}
		} else {
			if err := o.ndms.SetIPv6Address(ctx, names.NDMSName, cfg.AddressIPv6); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set IPv6 address via NDMS: "+err.Error())
				o.appLogWarn("reconcile", cfg.ID, "IPv6 адрес: "+err.Error())
			}
		}
	}

	if o.backend.Type() == backend.TypeKernel {
		if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
			return tunnel.NewOpError("reconcile", cfg.ID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
		}
	}

	// NDMS InterfaceUp: kernel only when OpkgTun was just created, userspace always.
	if justCreated || o.backend.Type() != backend.TypeKernel {
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
		if _, err := o.SetupEndpointRoute(ctx, cfg.ID, routeEndpoint, cfg.ISPInterface); err != nil {
			o.logWarn("reconcile", cfg.ID, "Endpoint route failed (non-fatal): "+err.Error())
			o.appLogWarn("reconcile", cfg.ID, "Не удалось создать endpoint route — туннель не может быть первым в политике доступа по умолчанию")
		} else {
			endpointRouteOK = true
		}
	} else {
		endpointRouteOK = true
	}

	// Default route: only when DefaultRoute is enabled.
	// Kernel mode: not supported (skipped).
	if cfg.DefaultRoute && o.backend.Type() != backend.TypeKernel {
		if err := o.ndms.SetDefaultRoute(ctx, names.NDMSName); err != nil {
			_ = o.CleanupEndpointRoute(ctx, cfg.ID)
			return tunnel.NewOpError("reconcile", cfg.ID, "ndms", fmt.Errorf("set default route: %w", err))
		}
		if cfg.AddressIPv6 != "" {
			if err := o.ndms.SetIPv6DefaultRoute(ctx, names.NDMSName); err != nil {
				o.logWarn("reconcile", cfg.ID, "Failed to set IPv6 default route: "+err.Error())
			}
		}
		o.appLog("reconcile", cfg.ID, "Маршрут по умолчанию добавлен через "+names.IfaceName)
		if !endpointRouteOK {
			o.appLogWarn("reconcile", cfg.ID, "Default route установлен без endpoint route — возможны проблемы с маршрутизацией")
		}
	}

	o.logInfo("reconcile", cfg.ID, "Routing configured")

	// === Phase 5: Add firewall rules ===
	if err := o.firewall.AddRules(ctx, names.IfaceName); err != nil {
		return tunnel.NewOpError("reconcile", cfg.ID, "firewall", err)
	}
	o.logInfo("reconcile", cfg.ID, "Firewall rules added")
	o.appLog("reconcile", cfg.ID, "Правила файрвола добавлены для "+names.IfaceName)

	// === Phase 6: Save NDMS configuration ===
	// Ensure NDMS default route exists (only when enabled)
	if cfg.DefaultRoute {
		_ = o.ndms.SetDefaultRoute(ctx, names.NDMSName)
		if cfg.AddressIPv6 != "" {
			_ = o.ndms.SetIPv6DefaultRoute(ctx, names.NDMSName)
		}
	}

	if err := o.ndms.Save(ctx); err != nil {
		o.logWarn("reconcile", cfg.ID, "Failed to save NDMS config: "+err.Error())
	}

	o.logInfo("reconcile", cfg.ID, "Reconciliation complete")
	o.appLog("reconcile", cfg.ID, "Конфигурация NDMS восстановлена")
	return nil
}

// SetDefaultRoute adds a default route through the tunnel interface.
// Kernel mode: no-op (not supported — default route without endpoint route breaks internet).
func (o *OperatorOS5Impl) SetDefaultRoute(ctx context.Context, tunnelID string) error {
	if o.backend.Type() == backend.TypeKernel {
		return nil
	}
	names := tunnel.NewNames(tunnelID)
	return o.ndms.SetDefaultRoute(ctx, names.NDMSName)
}

// RemoveDefaultRoute removes the default route through the tunnel interface.
// Kernel mode: no-op.
func (o *OperatorOS5Impl) RemoveDefaultRoute(ctx context.Context, tunnelID string) error {
	if o.backend.Type() == backend.TypeKernel {
		return nil
	}
	names := tunnel.NewNames(tunnelID)
	o.ndms.RemoveIPv6DefaultRoute(ctx, names.NDMSName)
	return o.ndms.RemoveDefaultRoute(ctx, names.NDMSName)
}

// KillLink kills the tunnel link without changing NDMS admin intent.
// Cleans up side effects (firewall, routes) but does NOT call
// ndms.InterfaceDown — this preserves conf: running so the tunnel
// auto-starts after reboot or WAN recovery.
//
// Kernel mode: bring link down (ip link set down) but preserve interface.
// WG config stays loaded -> awg show works -> handshake check can detect recovery.
// ip link del would destroy the interface entirely, making recovery impossible.
func (o *OperatorOS5Impl) KillLink(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)

	// Clean up side effects from Start (same as Stop phases 2-3,
	// but WITHOUT InterfaceDown to preserve NDMS intent).
	_ = o.firewall.RemoveRules(ctx, names.IfaceName)
	if o.backend.Type() != backend.TypeKernel {
		_ = o.ndms.RemoveDefaultRoute(ctx, names.NDMSName)
		o.ndms.RemoveIPv6DefaultRoute(ctx, names.NDMSName)
	}
	_ = o.CleanupEndpointRoute(ctx, tunnelID)

	// Clear resolved ISP tracking
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	if o.backend.Type() == backend.TypeKernel {
		// Kernel mode: bring link down but preserve interface.
		// WG config stays loaded → awg show works → handshake check can detect recovery.
		// ip link del would destroy the interface entirely, making recovery impossible.
		if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName); err != nil {
			o.logWarn("kill_link", tunnelID, "ip link set down: "+exec.FormatError(result, err).Error())
		}
	} else {
		// Userspace mode: kill the process (TUN disappears as side effect).
		// Do NOT call InterfaceDown — KillLink must preserve NDMS intent
		// (conf: running) so the tunnel auto-starts after WAN recovery.
		if err := o.backend.Stop(ctx, names.IfaceName); err != nil {
			return tunnel.NewOpError("kill_link", tunnelID, "backend", err)
		}
	}

	o.logInfo("kill_link", tunnelID, fmt.Sprintf("Link killed [%s]", o.backend.Type()))
	return nil
}

// InterfaceUp brings only the interface up (for PingCheck recovery).
// Tunnel already exists — kernel doesn't need NDMS InterfaceUp (intent persisted).
func (o *OperatorOS5Impl) InterfaceUp(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)

	if o.backend.Type() == backend.TypeKernel {
		// Kernel: ip link set up is sufficient — NDMS intent already conf: running.
		if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "up", "dev", names.IfaceName); err != nil {
			return tunnel.NewOpError("interface_up", tunnelID, "link", fmt.Errorf("ip link up: %w", exec.FormatError(result, err)))
		}
	} else {
		// Userspace: NDMS manages the TUN interface directly.
		if err := o.ndms.InterfaceUp(ctx, names.NDMSName); err != nil {
			return tunnel.NewOpError("interface_up", tunnelID, "ndms", err)
		}
	}

	// NOTE: default route is NOT re-established here — caller (service layer)
	// decides based on DefaultRoute setting.

	o.logInfo("interface_up", tunnelID, "Interface brought up")
	return nil
}

// InterfaceDown brings only the interface down (for PingCheck dead detection).
func (o *OperatorOS5Impl) InterfaceDown(ctx context.Context, tunnelID string) error {
	names := tunnel.NewNames(tunnelID)

	// Kernel: bring link down at kernel level before NDMS.
	// Userspace: TUN is owned by process, ip link set down is unnecessary.
	if o.backend.Type() == backend.TypeKernel {
		if result, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "down", "dev", names.IfaceName); err != nil {
			o.logWarn("interface_down", tunnelID, "Failed to ip link down: "+exec.FormatError(result, err).Error())
		}
	}

	if err := o.ndms.InterfaceDown(ctx, names.NDMSName); err != nil {
		return tunnel.NewOpError("interface_down", tunnelID, "ndms", err)
	}

	o.logInfo("interface_down", tunnelID, "Interface brought down")
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

// SetQlen sets txqueuelen on a running tunnel interface (kernel mode only).
func (o *OperatorOS5Impl) SetQlen(ctx context.Context, tunnelID string, qlen int) error {
	if o.backend.Type() != backend.TypeKernel {
		return nil // no-op for userspace
	}
	if qlen == 0 {
		qlen = 1000
	}
	names := tunnel.NewNames(tunnelID)
	if _, err := o.ipRun(ctx, "/opt/sbin/ip", "link", "set", "dev", names.IfaceName,
		"txqueuelen", fmt.Sprintf("%d", qlen)); err != nil {
		return tunnel.NewOpError("set_qlen", tunnelID, "kernel", err)
	}
	o.logInfo("set_qlen", tunnelID, fmt.Sprintf("txqueuelen set to %d", qlen))
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

// SetupPolicyTable creates a routing table with default route through tunnel + LAN route.
func (o *OperatorOS5Impl) SetupPolicyTable(ctx context.Context, tunnelIface string, tableNum int) error {
	table := fmt.Sprintf("%d", tableNum)

	// Default route through tunnel
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "replace", "default",
		"dev", tunnelIface, "table", table); err != nil {
		return fmt.Errorf("policy table %s default route: %w", table, exec.FormatError(result, err))
	}

	// LAN route — detect subnet from br0
	lanSubnet := o.detectLANSubnet(ctx)
	if lanSubnet != "" {
		if result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "replace",
			lanSubnet, "dev", "br0", "table", table); err != nil {
			o.logWarn("policy", tunnelIface, "Failed to add LAN route to table "+table+": "+exec.FormatError(result, err).Error())
		}
	}

	return nil
}

// detectLANSubnet reads br0 address to determine LAN CIDR.
func (o *OperatorOS5Impl) detectLANSubnet(ctx context.Context) string {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "-4", "-o", "addr", "show", "dev", "br0")
	if err != nil || result.Stdout == "" {
		return "192.168.1.0/24" // safe fallback
	}
	for _, field := range strings.Fields(result.Stdout) {
		if strings.Contains(field, "/") && strings.Count(field, ".") == 3 {
			parts := strings.SplitN(field, "/", 2)
			ip := parts[0]
			mask := parts[1]
			octets := strings.Split(ip, ".")
			if len(octets) == 4 {
				octets[3] = "0"
				return strings.Join(octets, ".") + "/" + mask
			}
		}
	}
	return "192.168.1.0/24"
}

// CleanupPolicyTable flushes all routes from a routing table.
func (o *OperatorOS5Impl) CleanupPolicyTable(ctx context.Context, tableNum int) error {
	table := fmt.Sprintf("%d", tableNum)
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "route", "flush", "table", table); err != nil {
		return fmt.Errorf("flush table %s: %w", table, exec.FormatError(result, err))
	}
	return nil
}

// AddClientRule adds an ip rule to route a client's traffic through a routing table.
func (o *OperatorOS5Impl) AddClientRule(ctx context.Context, clientIP string, tableNum int) error {
	table := fmt.Sprintf("%d", tableNum)
	// Remove existing rule first (idempotent)
	o.ipRun(ctx, "/opt/sbin/ip", "rule", "del", "from", clientIP, "lookup", table)
	// Add rule
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "add", "from", clientIP,
		"lookup", table, "priority", table); err != nil {
		return fmt.Errorf("add rule from %s lookup %s: %w", clientIP, table, exec.FormatError(result, err))
	}
	return nil
}

// RemoveClientRule removes an ip rule for a client.
func (o *OperatorOS5Impl) RemoveClientRule(ctx context.Context, clientIP string, tableNum int) error {
	table := fmt.Sprintf("%d", tableNum)
	if result, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "del", "from", clientIP,
		"lookup", table); err != nil {
		return fmt.Errorf("del rule from %s lookup %s: %w", clientIP, table, exec.FormatError(result, err))
	}
	return nil
}

// ListUsedRoutingTables returns routing table numbers currently referenced by ip rules.
func (o *OperatorOS5Impl) ListUsedRoutingTables(ctx context.Context) ([]int, error) {
	result, err := o.ipRun(ctx, "/opt/sbin/ip", "rule", "list")
	if err != nil {
		return nil, fmt.Errorf("ip rule list: %w", exec.FormatError(result, err))
	}
	seen := map[int]bool{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if idx := strings.Index(line, "lookup "); idx >= 0 {
			rest := strings.TrimSpace(line[idx+7:])
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				if num, err := strconv.Atoi(fields[0]); err == nil {
					seen[num] = true
				}
			}
		}
	}
	var tables []int
	for t := range seen {
		tables = append(tables, t)
	}
	return tables, nil
}

// rollbackStart cleans up after a failed start operation.
// justCreated indicates whether we created the OpkgTun in this Start attempt.
// When false (OpkgTun already existed), we preserve NDMS conf state (conf: running)
// so the tunnel stays in StateNeedsStart and can be retried.
func (o *OperatorOS5Impl) rollbackStart(ctx context.Context, tunnelID string, names tunnel.Names, justCreated bool) {
	o.logInfo("rollback", tunnelID, "Rolling back failed start")

	_ = o.firewall.RemoveRules(ctx, names.IfaceName)
	if o.backend.Type() != backend.TypeKernel {
		_ = o.ndms.RemoveDefaultRoute(ctx, names.NDMSName)
	}
	if justCreated {
		// We created this OpkgTun — clean it up entirely.
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

// SetAppLogger sets the web UI logger.
func (o *OperatorOS5Impl) SetAppLogger(logger AppLogger) {
	o.appLogger = logger
}

// appLog logs an info event to the web UI.
func (o *OperatorOS5Impl) appLog(action, target, message string) {
	if o.appLogger != nil {
		o.appLogger.Log("tunnel", action, target, message)
	}
}

// appLogWarn logs a warning event to the web UI.
func (o *OperatorOS5Impl) appLogWarn(action, target, message string) {
	if o.appLogger != nil {
		o.appLogger.LogWarn("tunnel", action, target, message)
	}
}

// Ensure OperatorOS5Impl implements Operator interface.
var _ Operator = (*OperatorOS5Impl)(nil)
