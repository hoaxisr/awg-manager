package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

var confDir = "/opt/etc/awg-manager"

// ReconcileHooks allows the reconcile loop to notify external services
// (e.g. PingCheck) about tunnel state changes detected via NDMS.
type ReconcileHooks interface {
	// OnReconcileStart is called when reconcile auto-starts a tunnel.
	OnReconcileStart(tunnelID, tunnelName string)
	// OnReconcileStop is called when reconcile detects NeedsStop (router UI toggled OFF).
	OnReconcileStop(tunnelID string)
	// OnTunnelDelete is called when a tunnel is deleted (cleanup monitoring).
	OnTunnelDelete(tunnelID string)
}

// DnsRouteHooks allows tunnel lifecycle to notify the DNS route service.
type DnsRouteHooks interface {
	OnTunnelStart(ctx context.Context) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
}

// StaticRouteHooks allows tunnel lifecycle to notify the static route service.
type StaticRouteHooks interface {
	OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
	OnTunnelDelete(ctx context.Context, tunnelID string) error
}

// ServiceImpl is the concrete implementation of Service.
type ServiceImpl struct {
	store          *storage.AWGTunnelStore
	state          state.Manager        // state detection for kernel tunnels only
	nwgOperator    *nwg.OperatorNativeWG // NativeWG backend (nil if unavailable)
	legacyOperator ops.Operator          // Kernel backend (OS5/OS4)
	log            *logger.Logger
	appLog         *logging.ScopedLogger // UI-visible logging

	// tunnelMu provides per-tunnel mutexes for lifecycle operations.
	// Key: tunnelID (string), Value: *sync.Mutex
	tunnelMu sync.Map

	// reconcileHooks notifies external services about reconcile events.
	reconcileHooks ReconcileHooks

	// dnsRouteHooks notifies the DNS route service about tunnel lifecycle events.
	dnsRouteHooks DnsRouteHooks

	// staticRouteHooks notifies the static route service about tunnel lifecycle events.
	staticRouteHooks StaticRouteHooks

	// wan is the unified WAN state model (up/down tracking).
	wan *wan.Model

	// reconcileDeadline suppresses self-triggered NDMS hooks.
	// When our Start/Stop/Restart calls InterfaceUp/InterfaceDown, NDMS fires
	// hooks back to ReconcileInterface. We suppress these for a short window
	// to prevent conflicts with our own operations.
	reconcileDeadline map[string]time.Time
	reconcileMu       sync.Mutex

	// reconcileLoopDetect tracks "disabled" events per tunnel for loop detection.
	// If a tunnel receives too many disabled events in a short window, NDMS is
	// cycling the interface — we block reconcile to stop the loop.
	reconcileDisabledEvents map[string][]time.Time
	reconcileLoopBlocked    map[string]time.Time

	// lifecycleOps tracks tunnels currently undergoing lifecycle operations.
	// Used by GetState to override transient misleading states (e.g. NeedsStop
	// during Start when process is running but InterfaceUp hasn't been called yet).
	lifecycleOps   map[string]tunnel.State
	lifecycleOpsMu sync.RWMutex

	// wanOps tracks per-tunnel cancellation for WAN handler goroutines.
	// When a new WAN event arrives for a tunnel, the previous goroutine is cancelled
	// so it releases the lock promptly instead of running its full timeout.
	wanOps   map[string]context.CancelFunc
	wanOpsMu sync.Mutex
}

// New creates a new TunnelService.
func New(
	store *storage.AWGTunnelStore,
	nwgOp *nwg.OperatorNativeWG,
	legacyOp ops.Operator,
	stateMgr state.Manager,
	log *logger.Logger,
	wanModel *wan.Model,
	appLogger logging.AppLogger,
) *ServiceImpl {
	return &ServiceImpl{
		store:             store,
		state:             stateMgr,
		nwgOperator:       nwgOp,
		legacyOperator:    legacyOp,
		log:               log,
		appLog:            logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
		wan:               wanModel,
		reconcileDeadline:       make(map[string]time.Time),
		reconcileDisabledEvents: make(map[string][]time.Time),
		reconcileLoopBlocked:    make(map[string]time.Time),
		lifecycleOps:            make(map[string]tunnel.State),
		wanOps:                  make(map[string]context.CancelFunc),
	}
}

// WANModel returns the WAN state model for direct access by API handlers.
func (s *ServiceImpl) WANModel() *wan.Model { return s.wan }

// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
func (s *ServiceImpl) GetResolvedISP(tunnelID string) string {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return ""
	}
	return stored.ActiveWAN
}

// SetReconcileHooks sets callbacks for reconcile loop events.
func (s *ServiceImpl) SetReconcileHooks(hooks ReconcileHooks) {
	s.reconcileHooks = hooks
}

// SetDnsRouteHooks sets callbacks for tunnel lifecycle events (DNS routing).
func (s *ServiceImpl) SetDnsRouteHooks(hooks DnsRouteHooks) {
	s.dnsRouteHooks = hooks
}

// SetStaticRouteHooks sets callbacks for tunnel lifecycle events (static IP routing).
func (s *ServiceImpl) SetStaticRouteHooks(hooks StaticRouteHooks) {
	s.staticRouteHooks = hooks
}

// suppressReconcile temporarily suppresses ReconcileInterface for a tunnel.
// Must be called BEFORE lockTunnel() so hooks blocked on the lock see the suppression.
const reconcileSuppressDuration = 15 * time.Second

func (s *ServiceImpl) suppressReconcile(tunnelID string) {
	s.reconcileMu.Lock()
	s.reconcileDeadline[tunnelID] = time.Now().Add(reconcileSuppressDuration)
	s.reconcileMu.Unlock()
}

// isReconcileSuppressed checks if a self-triggered hook should be ignored.
func (s *ServiceImpl) isReconcileSuppressed(tunnelID string) bool {
	s.reconcileMu.Lock()
	deadline, ok := s.reconcileDeadline[tunnelID]
	if !ok {
		s.reconcileMu.Unlock()
		return false
	}
	if time.Now().After(deadline) {
		delete(s.reconcileDeadline, tunnelID)
		s.reconcileMu.Unlock()
		return false
	}
	s.reconcileMu.Unlock()
	return true
}

// reconcileLoop detection constants.
const (
	reconcileLoopWindow   = 3 * time.Minute // track disabled events within this window
	reconcileLoopMaxCount = 4               // max disabled events before blocking
	reconcileLoopBlockDur = 5 * time.Minute // block reconcile for this long
)

// recordDisabledEvent records a "disabled" reconcile event and returns true if loop detected.
func (s *ServiceImpl) recordDisabledEvent(tunnelID string) bool {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	// Check if already blocked
	if blockUntil, ok := s.reconcileLoopBlocked[tunnelID]; ok {
		if time.Now().Before(blockUntil) {
			return true // still blocked
		}
		delete(s.reconcileLoopBlocked, tunnelID)
	}

	now := time.Now()
	cutoff := now.Add(-reconcileLoopWindow)

	// Prune old events
	events := s.reconcileDisabledEvents[tunnelID]
	pruned := events[:0]
	for _, t := range events {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	pruned = append(pruned, now)
	s.reconcileDisabledEvents[tunnelID] = pruned

	if len(pruned) >= reconcileLoopMaxCount {
		s.reconcileLoopBlocked[tunnelID] = now.Add(reconcileLoopBlockDur)
		s.reconcileDisabledEvents[tunnelID] = nil // reset counter
		return true
	}
	return false
}

// isReconcileLoopBlocked checks if reconcile is blocked due to loop detection.
func (s *ServiceImpl) isReconcileLoopBlocked(tunnelID string) bool {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	blockUntil, ok := s.reconcileLoopBlocked[tunnelID]
	if !ok {
		return false
	}
	if time.Now().After(blockUntil) {
		delete(s.reconcileLoopBlocked, tunnelID)
		return false
	}
	return true
}

// clearReconcileLoop resets loop detection for a tunnel (called on manual Start/Stop).
func (s *ServiceImpl) clearReconcileLoop(tunnelID string) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	delete(s.reconcileDisabledEvents, tunnelID)
	delete(s.reconcileLoopBlocked, tunnelID)
}

// newWANOp creates a cancellable context for a WAN handler goroutine.
// Cancels any previous WAN operation for this tunnel.
func (s *ServiceImpl) newWANOp(tunnelID string) context.Context {
	s.wanOpsMu.Lock()
	defer s.wanOpsMu.Unlock()

	if cancel, ok := s.wanOps[tunnelID]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.wanOps[tunnelID] = cancel
	return ctx
}

// clearWANOp removes the WAN operation context for a tunnel.
// Called at the end of each WAN handler goroutine.
func (s *ServiceImpl) clearWANOp(tunnelID string) {
	s.wanOpsMu.Lock()
	defer s.wanOpsMu.Unlock()
	if cancel, ok := s.wanOps[tunnelID]; ok {
		cancel()
	}
	delete(s.wanOps, tunnelID)
}

// lockTunnel acquires the per-tunnel mutex.
func (s *ServiceImpl) lockTunnel(tunnelID string) {
	mu, _ := s.tunnelMu.LoadOrStore(tunnelID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
}

// unlockTunnel releases the per-tunnel mutex.
func (s *ServiceImpl) unlockTunnel(tunnelID string) {
	if mu, ok := s.tunnelMu.Load(tunnelID); ok {
		mu.(*sync.Mutex).Unlock()
	}
}

// cleanupTunnelLock removes the lock entry for a deleted tunnel.
func (s *ServiceImpl) cleanupTunnelLock(tunnelID string) {
	s.tunnelMu.Delete(tunnelID)
}

// === CRUD Operations ===

// Create creates a new tunnel and saves it to storage.
// For NativeWG tunnels, stored must be non-nil with Backend="nativewg";
// Create will call nwgOperator.Create and set stored.NWGIndex before returning.
func (s *ServiceImpl) Create(ctx context.Context, tunnelID, name string, cfg tunnel.Config, stored *storage.AWGTunnel) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	// Check if tunnel already exists in storage
	if s.store.Exists(tunnelID) {
		return tunnel.ErrAlreadyExists
	}

	// NativeWG path
	if stored != nil && s.isNativeWG(stored) {
		if s.nwgOperator == nil {
			return fmt.Errorf("NativeWG backend not available")
		}
		index, err := s.nwgOperator.Create(ctx, stored)
		if err != nil {
			return err
		}
		stored.NWGIndex = index
		s.logInfo("create", tunnelID, "NativeWG tunnel created")
		return nil
	}

	// Kernel path: create in NDMS (for OS5, no-op for OS4)
	if err := s.legacyOperator.Create(ctx, cfg); err != nil {
		return err
	}

	s.logInfo("create", tunnelID, "Tunnel created")
	return nil
}

// Get returns a tunnel with its current state.
func (s *ServiceImpl) Get(ctx context.Context, tunnelID string) (*TunnelWithStatus, error) {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return nil, tunnel.ErrNotFound
	}

	var stateInfo tunnel.StateInfo
	if stored.Backend == "nativewg" && s.nwgOperator != nil {
		stateInfo = s.nwgOperator.GetState(ctx, stored)
	} else {
		stateInfo = s.state.GetState(ctx, tunnelID)
	}

	var ifaceName string
	if stored.Backend == "nativewg" {
		ifaceName = nwg.NewNWGNames(stored.NWGIndex).IfaceName
	} else {
		ifaceName = tunnel.NewNames(tunnelID).IfaceName
	}

	isDeadByMonitoring := stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring

	return &TunnelWithStatus{
		ID:                 stored.ID,
		Name:               stored.Name,
		Config:             s.storedToConfig(stored),
		State:              stateInfo.State,
		StateInfo:          stateInfo,
		Enabled:            stored.Enabled,
		AutoStart:          stored.Enabled, // AutoStart == Enabled in current design
		PingCheckOn:        stored.PingCheck != nil && stored.PingCheck.Enabled,
		DefaultRoute:       stored.DefaultRoute,
		ISPInterface:       stored.ISPInterface,
		InterfaceName:      ifaceName,
		ConfigPreview:      config.Generate(stored),
		IsDeadByMonitoring: isDeadByMonitoring,
		Backend:            s.backendLabel(stored),
	}, nil
}

// List returns all tunnels with their current states.
func (s *ServiceImpl) List(ctx context.Context) ([]TunnelWithStatus, error) {
	stored, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("list tunnels: %w", err)
	}

	result := make([]TunnelWithStatus, 0, len(stored))
	for _, t := range stored {
		var stateInfo tunnel.StateInfo
		if !t.Enabled {
			// Disabled tunnel: skip NDMS/sysfs query — return Disabled directly.
			// This avoids "not found: OpkgTunX" errors in router logs for
			// tunnels that don't have an NDMS interface created.
			stateInfo = tunnel.StateInfo{State: tunnel.StateDisabled}
		} else if t.Backend == "nativewg" && s.nwgOperator != nil {
			stateInfo = s.nwgOperator.GetState(ctx, &t)
		} else {
			stateInfo = s.state.GetState(ctx, t.ID)
		}

		var ifaceName string
		if t.Backend == "nativewg" {
			ifaceName = nwg.NewNWGNames(t.NWGIndex).IfaceName
		} else {
			ifaceName = tunnel.NewNames(t.ID).IfaceName
		}
		isDeadByMonitoring := t.PingCheck != nil && t.PingCheck.IsDeadByMonitoring

		result = append(result, TunnelWithStatus{
			ID:                 t.ID,
			Name:               t.Name,
			Config:             s.storedToConfig(&t),
			State:              stateInfo.State,
			StateInfo:          stateInfo,
			Enabled:            t.Enabled,
			AutoStart:          t.Enabled,
			PingCheckOn:        t.PingCheck != nil && t.PingCheck.Enabled,
			DefaultRoute:       t.DefaultRoute,
			ISPInterface:       t.ISPInterface,
			InterfaceName:      ifaceName,
			IsDeadByMonitoring: isDeadByMonitoring,
			Backend:            s.backendLabel(&t),
		})
	}

	return result, nil
}

// Update updates a tunnel's configuration.
func (s *ServiceImpl) Update(ctx context.Context, tunnelID string, cfg tunnel.Config) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	// Block address changes in kernel mode — NDMS cannot change address on a kernel interface.
	// NativeWG can change addresses via NDMS, so this check only applies to kernel tunnels.
	if !s.isNativeWG(stored) {
		stateInfo := s.state.GetState(ctx, tunnelID)
		if stateInfo.BackendType == "kernel" && cfg.Address != "" && cfg.Address != stored.Interface.Address {
			return fmt.Errorf("address change is not supported in kernel mode")
		}
	}

	// Update NDMS description if name changed
	if cfg.Name != "" && cfg.Name != stored.Name {
		if s.isNativeWG(stored) && s.nwgOperator != nil {
			if err := s.nwgOperator.UpdateDescription(ctx, stored, cfg.Name); err != nil {
				s.logWarn("update", tunnelID, "Failed to update NWG description: "+err.Error())
			}
		} else {
			if err := s.legacyOperator.UpdateDescription(ctx, tunnelID, cfg.Name); err != nil {
				s.logWarn("update", tunnelID, "Failed to update NDMS description: "+err.Error())
			}
		}
		stored.Name = cfg.Name
	}

	// Capture old endpoint before updating (for route refresh)
	oldEndpoint := stored.Peer.Endpoint

	// Update stored config
	stored.Interface.Address = cfg.Address
	stored.Interface.MTU = cfg.MTU
	if cfg.Endpoint != "" {
		stored.Peer.Endpoint = cfg.Endpoint
	}

	// Regenerate config file
	if err := s.writeConfigFile(stored); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Save to storage
	if err := s.store.Save(stored); err != nil {
		return fmt.Errorf("save tunnel: %w", err)
	}

	// If running, apply changes to live tunnel
	if s.isNativeWG(stored) {
		// NativeWG: sync address/MTU via NDMS
		if s.nwgOperator != nil {
			stateInfo := s.nwgOperator.GetState(ctx, stored)
			if stateInfo.State == tunnel.StateRunning {
				if err := s.nwgOperator.SyncAddressMTU(ctx, stored); err != nil {
					s.logWarn("update", tunnelID, "Failed to sync NWG address/MTU: "+err.Error())
				}
			}
		}
	} else {
		// Kernel path: ApplyConfig, SetMTU, endpoint route refresh
		stateInfo := s.state.GetState(ctx, tunnelID)
		if stateInfo.State == tunnel.StateRunning {
			confPath := tunnel.NewNames(tunnelID).ConfPath
			if err := s.legacyOperator.ApplyConfig(ctx, tunnelID, confPath); err != nil {
				s.logWarn("update", tunnelID, "Failed to apply config to running tunnel: "+err.Error())
			}
			// Apply MTU immediately to running interface
			if err := s.legacyOperator.SetMTU(ctx, tunnelID, cfg.MTU); err != nil {
				s.logWarn("update", tunnelID, "Failed to apply MTU: "+err.Error())
			}

			// If endpoint changed, refresh endpoint route via ISP
			if cfg.Endpoint != "" && cfg.Endpoint != oldEndpoint {
				_ = s.legacyOperator.CleanupEndpointRoute(ctx, tunnelID)
				resolvedWAN, resolveErr := s.resolveWAN(ctx, stored.ISPInterface)
				if resolveErr != nil {
					s.logWarn("update", tunnelID, "Failed to resolve WAN: "+resolveErr.Error())
				} else if ip, err := s.legacyOperator.SetupEndpointRoute(ctx, tunnelID, stored.Peer.Endpoint, s.resolveKernelDevice(resolvedWAN), resolvedWAN); err != nil {
					s.logWarn("update", tunnelID, "Failed to setup new endpoint route: "+err.Error())
				} else {
					stored.ResolvedEndpointIP = ip
					if err := s.store.Save(stored); err != nil {
						s.logWarn("save", stored.ID, "Failed to persist state: "+err.Error())
					}
				}
			}
		}
	}

	s.logInfo("update", tunnelID, "Tunnel updated")
	return nil
}

// SetEnabled changes the enabled/autostart state of a tunnel.
func (s *ServiceImpl) SetEnabled(ctx context.Context, tunnelID string, enabled bool) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	stored.Enabled = enabled

	if err := s.store.Save(stored); err != nil {
		return fmt.Errorf("save tunnel: %w", err)
	}

	s.logInfo("set_enabled", tunnelID, fmt.Sprintf("Enabled set to %v", enabled))
	return nil
}

// SetDefaultRoute changes the default route setting.
// If tunnel is running, immediately applies route changes.
func (s *ServiceImpl) SetDefaultRoute(ctx context.Context, tunnelID string, enabled bool) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	oldValue := stored.DefaultRoute
	stored.DefaultRoute = enabled
	stored.DefaultRouteSet = true

	if err := s.store.Save(stored); err != nil {
		return fmt.Errorf("save tunnel: %w", err)
	}

	// If tunnel is running and value changed, apply default route changes.
	// NativeWG: NDMS manages routes natively, no action needed here.
	// Kernel: endpoint route is always present (set up in Start), only default route toggles.
	if !s.isNativeWG(stored) {
		stateInfo := s.state.GetState(ctx, tunnelID)
		if stateInfo.State == tunnel.StateRunning && oldValue != enabled {
			if enabled {
				if err := s.legacyOperator.SetDefaultRoute(ctx, tunnelID); err != nil {
					s.logWarn("set_default_route", tunnelID, "Failed to set default route: "+err.Error())
				}
			} else {
				if err := s.legacyOperator.RemoveDefaultRoute(ctx, tunnelID); err != nil {
					s.logWarn("set_default_route", tunnelID, "Failed to remove default route: "+err.Error())
				}
			}
		}
	}

	s.logInfo("set_default_route", tunnelID, fmt.Sprintf("DefaultRoute set to %v", enabled))
	return nil
}

// Import parses a WireGuard .conf file and creates a tunnel.
func (s *ServiceImpl) Import(ctx context.Context, confContent, name, backend string) (*TunnelWithStatus, error) {
	// Parse config
	parsed, err := config.Parse(confContent)
	if err != nil {
		return nil, fmt.Errorf("parse conf: %w", err)
	}

	// Set name
	if name != "" {
		parsed.Name = name
	}
	if parsed.Name == "" {
		parsed.Name = "Imported Tunnel"
	}

	// Determine backend
	if backend == "" {
		backend = "kernel" // default for backwards compat
	}
	parsed.Backend = backend

	if backend == "nativewg" {
		return s.importNativeWG(ctx, parsed)
	}

	// Kernel path (existing logic)
	tunnelID, err := s.store.NextAvailableID()
	if err != nil {
		return nil, fmt.Errorf("generate ID: %w", err)
	}
	parsed.ID = tunnelID
	parsed.Type = "awg"
	parsed.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	parsed.Status = "stopped"
	parsed.Enabled = false

	if err := s.store.Save(parsed); err != nil {
		return nil, fmt.Errorf("save tunnel: %w", err)
	}
	if err := s.writeConfigFile(parsed); err != nil {
		_ = s.store.Delete(tunnelID)
		return nil, fmt.Errorf("write config: %w", err)
	}

	s.logInfo("import", tunnelID, "Tunnel imported: "+parsed.Name)
	return s.Get(ctx, tunnelID)
}

// importNativeWG creates a tunnel using the NativeWG backend.
func (s *ServiceImpl) importNativeWG(ctx context.Context, parsed *storage.AWGTunnel) (*TunnelWithStatus, error) {
	if s.nwgOperator == nil {
		return nil, fmt.Errorf("NativeWG backend not available")
	}

	// Generate tunnel ID
	tunnelID, err := s.store.NextAvailableID()
	if err != nil {
		return nil, fmt.Errorf("generate ID: %w", err)
	}
	parsed.ID = tunnelID
	parsed.Type = "awg"
	parsed.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	parsed.Status = "stopped"
	parsed.Enabled = false
	parsed.Backend = "nativewg"

	// Create NDMS WireGuard interface via NativeWG operator
	index, err := s.nwgOperator.Create(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("create NativeWG interface: %w", err)
	}
	parsed.NWGIndex = index

	// Save to storage
	if err := s.store.Save(parsed); err != nil {
		_ = s.nwgOperator.Delete(ctx, parsed)
		return nil, fmt.Errorf("save tunnel: %w", err)
	}

	// Write config file (for export/display purposes)
	if err := s.writeConfigFile(parsed); err != nil {
		s.logWarn("import", tunnelID, "Failed to write config file: "+err.Error())
	}

	s.logInfo("import", tunnelID, "NativeWG tunnel imported: "+parsed.Name)
	return s.Get(ctx, tunnelID)
}

// === Validation ===

// CheckAddressConflicts returns warnings if the tunnel's address
// conflicts with any other stored tunnel.
func (s *ServiceImpl) CheckAddressConflicts(_ context.Context, tunnelID string) []string {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return nil
	}
	return checkStoredAddressConflicts(s.store, stored.Interface.Address, tunnelID)
}

// clearDeadFlag clears IsDeadByMonitoring when user manually starts a running tunnel.
// This handles the edge case where PingCheck's HandleForcedRestart started the tunnel
// but re-set the dead flag before the user's Start acquired the lock.
func (s *ServiceImpl) clearDeadFlag(tunnelID string) {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return
	}
	if stored.PingCheck != nil && stored.PingCheck.IsDeadByMonitoring {
		stored.PingCheck.IsDeadByMonitoring = false
		stored.PingCheck.DeadSince = nil
		_ = s.store.Save(stored)
	}
}

// clearActiveWAN clears the persisted ActiveWAN and StartedAt for a tunnel.
// Called after KillLink to ensure HandleWANDown won't match stale WAN.
func (s *ServiceImpl) clearActiveWAN(tunnelID string) {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return
	}
	changed := false
	if stored.ActiveWAN != "" {
		stored.ActiveWAN = ""
		changed = true
	}
	if stored.StartedAt != "" {
		stored.StartedAt = ""
		changed = true
	}
	if changed {
		_ = s.store.Save(stored)
	}
}

// collectManagedIfaceNames returns interface names for all stored tunnels.
// Used to exclude managed interfaces from system address conflict checks.
func (s *ServiceImpl) collectManagedIfaceNames() []string {
	tunnels, err := s.store.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		if t.Backend == "nativewg" {
			names = append(names, nwg.NewNWGNames(t.NWGIndex).IfaceName)
		} else {
			names = append(names, tunnel.NewNames(t.ID).IfaceName)
		}
	}
	return names
}

// === Helper Methods ===

// resolveWAN resolves the tunnel's ISPInterface to a kernel interface name.
// Auto mode (empty): uses WAN model priority or NDMS default gateway.
// Tunnel chaining (tunnel:xxx): resolves to parent tunnel's WAN.
// Explicit: returns as-is (after migration, stores kernel name).
func (s *ServiceImpl) resolveWAN(ctx context.Context, ispInterface string) (string, error) {
	if ispInterface == "" {
		// Auto mode: prefer WAN model (priority-based, returns kernel name)
		if iface, ok := s.wan.PreferredUp(); ok {
			return iface, nil
		}
		// Fallback: wan.Model not yet populated (early boot)
		// GetDefaultGatewayInterface returns NDMS ID → translate to kernel name
		ndmsID, err := s.legacyOperator.GetDefaultGatewayInterface(ctx)
		if err != nil {
			return "", fmt.Errorf("no default gateway available: %w", err)
		}
		// Try model reverse lookup first
		if kernelName := s.wan.NameForID(ndmsID); kernelName != "" {
			return kernelName, nil
		}
		// Model not populated — direct NDMS lookup
		return s.legacyOperator.GetSystemName(ctx, ndmsID), nil
	}

	if tunnel.IsTunnelRoute(ispInterface) {
		// Tunnel chaining: resolve to parent's persisted WAN
		parentID := tunnel.TunnelRouteID(ispInterface)
		parentStored, err := s.store.Get(parentID)
		if err != nil {
			return "", fmt.Errorf("parent tunnel %s not found", parentID)
		}
		if parentStored.ActiveWAN != "" {
			return parentStored.ActiveWAN, nil
		}
		// Fallback: ActiveWAN empty (first start or upgrade from old version)
		parentState := s.state.GetState(ctx, parentID)
		if parentState.State != tunnel.StateRunning {
			return "", fmt.Errorf("parent tunnel %s not running (state: %s)", parentID, parentState.State)
		}
		if tunnel.IsTunnelRoute(parentStored.ISPInterface) {
			return "", fmt.Errorf("parent tunnel %s: nested chain, ActiveWAN not tracked", parentID)
		}
		s.logInfo("resolve_wan", parentID, "ActiveWAN empty, resolving from stored config")
		return s.resolveWAN(ctx, parentStored.ISPInterface)
	}

	// Explicit WAN — after migration this is already a kernel name
	return ispInterface, nil
}

// resolveKernelDevice extracts the kernel device name from a resolved WAN.
// resolveWAN already returns kernel names, so this just handles tunnel chaining.
func (s *ServiceImpl) resolveKernelDevice(resolvedWAN string) string {
	if resolvedWAN == "" {
		return ""
	}
	if tunnel.IsTunnelRoute(resolvedWAN) {
		return tunnel.NewNames(tunnel.TunnelRouteID(resolvedWAN)).IfaceName
	}
	return resolvedWAN // already a kernel name
}

// storedToConfig converts storage.AWGTunnel to tunnel.Config.
func (s *ServiceImpl) storedToConfig(stored *storage.AWGTunnel) tunnel.Config {
	names := tunnel.NewNames(stored.ID)
	ipv4, ipv6 := splitAddresses(stored.Interface.Address)
	// Parse DNS servers from comma-separated string
	var dns []string
	if stored.Interface.DNS != "" {
		for _, part := range strings.Split(stored.Interface.DNS, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				dns = append(dns, part)
			}
		}
	}

	return tunnel.Config{
		ID:           stored.ID,
		Name:         stored.Name,
		Address:      ipv4,
		AddressIPv6:  ipv6,
		MTU:          stored.Interface.MTU,
		DNS:          dns,
		ConfPath:     names.ConfPath,
		ISPInterface: stored.ISPInterface,
	}
}

// splitAddresses splits a WireGuard Address field (which may contain
// comma-separated IPv4 and IPv6 addresses) into separate values.
func splitAddresses(address string) (ipv4, ipv6 string) {
	for _, part := range strings.Split(address, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// Strip CIDR prefix for the config — operators add it themselves
		host := part
		if idx := strings.Index(part, "/"); idx != -1 {
			host = part[:idx]
		}
		if strings.Contains(host, ":") {
			ipv6 = host
		} else {
			ipv4 = host
		}
	}
	return
}

// writeConfigFileForStart generates and writes the WireGuard config file for tunnel start.
// When hasIPv6 is false, ::/0 is filtered from AllowedIPs.
func (s *ServiceImpl) writeConfigFileForStart(stored *storage.AWGTunnel, hasIPv6 bool) error {
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	content := config.GenerateForStart(stored, hasIPv6)
	confPath := filepath.Join(confDir, stored.ID+".conf")
	if err := os.WriteFile(confPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// writeConfigFile generates and writes the WireGuard config file.
func (s *ServiceImpl) writeConfigFile(stored *storage.AWGTunnel) error {
	// Ensure directory exists
	if err := os.MkdirAll(confDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// Generate config content
	content := config.Generate(stored)

	// Write to file
	confPath := filepath.Join(confDir, stored.ID+".conf")
	if err := os.WriteFile(confPath, []byte(content), 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}

	return nil
}

// logInfo logs an info message.
func (s *ServiceImpl) logInfo(action, target, message string) {
	if s.log != nil {
		s.log.Infof("[%s] %s: %s", action, target, message)
	}
}

// logWarn logs a warning message.
func (s *ServiceImpl) logWarn(action, target, message string) {
	if s.log != nil {
		s.log.Warnf("[%s] %s: %s", action, target, message)
	}
}

// MigrateISPInterfaceNone converts legacy "none" ISPInterface values to "" (auto).
func (s *ServiceImpl) MigrateISPInterfaceNone() {
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if t.ISPInterface == "none" {
			t.ISPInterface = ""
			_ = s.store.Save(&t)
			s.logInfo("migrate", t.ID, "Migrated ISPInterface from 'none' to auto")
		}
	}
}

// MigrateEmptyBackend sets Backend="kernel" on all tunnels with empty Backend field.
// Legacy tunnels (created before per-tunnel backend) are kernel-mode by definition.
func (s *ServiceImpl) MigrateEmptyBackend() {
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if t.Backend == "" {
			t.Backend = "kernel"
			_ = s.store.Save(&t)
		}
	}
}

// MigrateISPInterfaceToKernel converts legacy NDMS ID values (e.g., "PPPoE0", "ISP")
// in ISPInterface and ActiveWAN to kernel names (e.g., "ppp0", "eth3").
// Called once at startup after WAN model is populated.
func (s *ServiceImpl) MigrateISPInterfaceToKernel() {
	if !s.wan.IsPopulated() {
		return
	}
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		// NativeWG tunnels store NDMS names — skip kernel migration
		if t.Backend == "nativewg" {
			continue
		}
		changed := false
		// Migrate ISPInterface
		if t.ISPInterface != "" && !tunnel.IsTunnelRoute(t.ISPInterface) {
			if kernelName := s.wan.NameForID(t.ISPInterface); kernelName != "" {
				s.logInfo("migrate", t.ID, fmt.Sprintf("ISPInterface: %s → %s", t.ISPInterface, kernelName))
				t.ISPInterface = kernelName
				changed = true
			}
		}
		// Migrate ActiveWAN
		if t.ActiveWAN != "" && !tunnel.IsTunnelRoute(t.ActiveWAN) {
			if kernelName := s.wan.NameForID(t.ActiveWAN); kernelName != "" {
				s.logInfo("migrate", t.ID, fmt.Sprintf("ActiveWAN: %s → %s", t.ActiveWAN, kernelName))
				t.ActiveWAN = kernelName
				changed = true
			}
		}
		if changed {
			_ = s.store.Save(&t)
		}
	}
}

// isNativeWG returns true if the tunnel uses the NativeWG backend.
func (s *ServiceImpl) isNativeWG(stored *storage.AWGTunnel) bool {
	return stored.Backend == "nativewg"
}

// isNativeWGByID returns true if the tunnel uses the NativeWG backend (by ID lookup).
func (s *ServiceImpl) isNativeWGByID(tunnelID string) bool {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return false
	}
	return s.isNativeWG(stored)
}

// backendLabel returns the backend label for a stored tunnel.
func (s *ServiceImpl) backendLabel(stored *storage.AWGTunnel) string {
	if s.isNativeWG(stored) {
		return "nativewg"
	}
	return "kernel"
}

// Ensure ServiceImpl implements Service interface.
var _ Service = (*ServiceImpl)(nil)
