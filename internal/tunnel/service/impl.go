package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

var confDir = "/opt/etc/awg-manager"

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

	// wan is the unified WAN state model (up/down tracking).
	wan *wan.Model

	// orch is the orchestrator for lifecycle operations (Start/Stop/Restart/Delete).
	orch *orchestrator.Orchestrator

	// bus is the event bus for SSE publishing.
	bus *events.Bus

	// selfCreateGate (optional) suppresses the hook-driven snapshot refresh
	// during awg-manager-initiated NDMS interface creations. Without it,
	// the ifcreated hook fires (and rebroadcasts system tunnels) before
	// our own store.Save completes — producing a transient ghost entry in
	// the system tunnels list.
	selfCreateGate tunnel.SelfCreateGater
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
		store:          store,
		state:          stateMgr,
		nwgOperator:    nwgOp,
		legacyOperator: legacyOp,
		log:            log,
		appLog:         logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
		wan:            wanModel,
	}
}

// WANModel returns the WAN state model for direct access by API handlers.
func (s *ServiceImpl) WANModel() *wan.Model { return s.wan }

// SetSelfCreateGate wires the self-create gate used to suppress hook-driven
// snapshot refreshes during Create/Import. Optional; nil is safe (code paths
// degrade to the old behavior).
func (s *ServiceImpl) SetSelfCreateGate(g tunnel.SelfCreateGater) { s.selfCreateGate = g }

// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
func (s *ServiceImpl) GetResolvedISP(tunnelID string) string {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return ""
	}
	return stored.ActiveWAN
}

// SetOrchestrator sets the orchestrator for lifecycle delegation.
func (s *ServiceImpl) SetOrchestrator(orch *orchestrator.Orchestrator) {
	s.orch = orch
}

// SetEventBus sets the event bus for SSE publishing.
func (s *ServiceImpl) SetEventBus(bus *events.Bus) { s.bus = bus }

// RunningTunnels returns the list of currently running tunnels for the traffic collector.
func (s *ServiceImpl) RunningTunnels(ctx context.Context) []traffic.RunningTunnel {
	stored, err := s.store.List()
	if err != nil {
		return nil
	}
	var result []traffic.RunningTunnel
	for _, t := range stored {
		if !t.Enabled {
			continue
		}
		var si tunnel.StateInfo
		if t.Backend == "nativewg" && s.nwgOperator != nil {
			si = s.nwgOperator.GetState(ctx, &t)
		} else {
			si = s.state.GetState(ctx, t.ID)
		}
		if si.State != tunnel.StateRunning {
			continue
		}
		var ifaceName, ndmsName string
		if t.Backend == "nativewg" {
			names := nwg.NewNWGNames(t.NWGIndex)
			ifaceName = names.IfaceName
			ndmsName = names.NDMSName
		} else {
			names := tunnel.NewNames(t.ID)
			ifaceName = names.IfaceName
			ndmsName = names.NDMSName
		}
		result = append(result, traffic.RunningTunnel{
			ID:            t.ID,
			BackendType:   s.backendLabel(&t),
			IfaceName:     ifaceName,
			NDMSName:      ndmsName,
			RxBytes:       si.RxBytes,
			TxBytes:       si.TxBytes,
			LastHandshake: si.LastHandshake,
			ConnectedAt:   si.ConnectedAt,
		})
	}
	return result
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
		// NOTE: the caller (tunnels API handler) calls store.Save AFTER we
		// return, so the self-create gate can't be scoped to this function
		// alone — it would exit too early and let the ifcreated hook see an
		// empty managed list. For now, the gate only protects Import (which
		// saves internally). Manual Create racing with ifcreated is a known
		// edge case; if it surfaces, move the gate up to the handler layer.
		index, err := s.nwgOperator.Create(ctx, stored)
		if err != nil {
			return err
		}
		stored.NWGIndex = index
		s.logInfo("create", tunnelID, "NativeWG tunnel created")
		if s.bus != nil {
			s.bus.Publish("tunnel:created", events.TunnelCreatedEvent{
				ID: stored.ID, Name: stored.Name, Backend: s.backendLabel(stored),
			})
		}
		return nil
	}

	// Kernel path: create in NDMS (for OS5, no-op for OS4)
	if err := s.legacyOperator.Create(ctx, cfg); err != nil {
		return err
	}

	s.logInfo("create", tunnelID, "Tunnel created")
	if s.bus != nil && stored != nil {
		s.bus.Publish("tunnel:created", events.TunnelCreatedEvent{
			ID: stored.ID, Name: stored.Name, Backend: s.backendLabel(stored),
		})
	}
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

	var ifaceName, ndmsName string
	if stored.Backend == "nativewg" {
		names := nwg.NewNWGNames(stored.NWGIndex)
		ifaceName = names.IfaceName
		ndmsName = names.NDMSName
	} else {
		ifaceName = tunnel.NewNames(tunnelID).IfaceName
	}

	return &TunnelWithStatus{
		ID:            stored.ID,
		Name:          stored.Name,
		Config:        orchestrator.StoredToConfig(stored),
		State:         stateInfo.State,
		StateInfo:     stateInfo,
		Enabled:       stored.Enabled,
		AutoStart:     stored.Enabled, // AutoStart == Enabled in current design
		PingCheckOn:   stored.PingCheck != nil && stored.PingCheck.Enabled,
		DefaultRoute:  stored.DefaultRoute,
		ISPInterface:  stored.ISPInterface,
		InterfaceName: ifaceName,
		NDMSName:      ndmsName,
		ConfigPreview: config.Generate(stored),
		Backend:       s.backendLabel(stored),
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

		var ifaceName, ndmsName string
		if t.Backend == "nativewg" {
			names := nwg.NewNWGNames(t.NWGIndex)
			ifaceName = names.IfaceName
			ndmsName = names.NDMSName
		} else {
			ifaceName = tunnel.NewNames(t.ID).IfaceName
		}
		result = append(result, TunnelWithStatus{
			ID:            t.ID,
			Name:          t.Name,
			Config:        orchestrator.StoredToConfig(&t),
			State:         stateInfo.State,
			StateInfo:     stateInfo,
			Enabled:       t.Enabled,
			AutoStart:     t.Enabled,
			PingCheckOn:   t.PingCheck != nil && t.PingCheck.Enabled,
			DefaultRoute:  t.DefaultRoute,
			ISPInterface:  t.ISPInterface,
			InterfaceName: ifaceName,
			NDMSName:      ndmsName,
			Backend:       s.backendLabel(&t),
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

	// Capture old values before updating (for conditional sync)
	oldEndpoint := stored.Peer.Endpoint
	oldDNS := stored.Interface.DNS
	oldAddress := stored.Interface.Address

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

			// Sync DNS servers to NDMS (only if changed)
			if stored.Interface.DNS != oldDNS {
				var dnsServers []string
				if stored.Interface.DNS != "" {
					for _, part := range strings.Split(stored.Interface.DNS, ",") {
						if d := strings.TrimSpace(part); d != "" {
							dnsServers = append(dnsServers, d)
						}
					}
				}
				if err := s.legacyOperator.SyncDNS(ctx, tunnelID, dnsServers); err != nil {
					s.logWarn("update", tunnelID, "Failed to sync DNS: "+err.Error())
				}
			}

			// Sync address (IPv4 + IPv6) to NDMS (only if changed)
			if stored.Interface.Address != oldAddress {
				ipv4, ipv6 := orchestrator.SplitAddresses(stored.Interface.Address)
				if err := s.legacyOperator.SyncAddress(ctx, tunnelID, ipv4, ipv6); err != nil {
					s.logWarn("update", tunnelID, "Failed to sync address: "+err.Error())
				}
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
	if s.bus != nil {
		s.bus.Publish("tunnel:updated", events.TunnelUpdatedEvent{
			ID: tunnelID, Name: stored.Name,
		})
	}
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
	parsed.Enabled = false

	if err := s.store.Save(parsed); err != nil {
		return nil, fmt.Errorf("save tunnel: %w", err)
	}
	if err := s.writeConfigFile(parsed); err != nil {
		_ = s.store.Delete(tunnelID)
		return nil, fmt.Errorf("write config: %w", err)
	}

	s.logInfo("import", tunnelID, "Tunnel imported: "+parsed.Name)
	if s.bus != nil {
		s.bus.Publish("tunnel:created", events.TunnelCreatedEvent{
			ID: parsed.ID, Name: parsed.Name, Backend: s.backendLabel(parsed),
		})
	}
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
	parsed.Enabled = false
	parsed.Backend = "nativewg"

	// Guard: the ifcreated hook fires from NDMS AS SOON AS the interface
	// is created. Without the gate, the hook handler rebroadcasts a
	// snapshot that sees the new NDMS interface but does NOT see this
	// tunnel in our managed store yet (Save hasn't run), so the interface
	// is misclassified as a "system tunnel" — a ghost duplicate vanishing
	// only on next refresh. Gate spans both Create and Save; the caller
	// (import handler) publishes the final snapshot after us.
	if s.selfCreateGate != nil {
		s.selfCreateGate.EnterSelfCreate()
		defer s.selfCreateGate.ExitSelfCreate()
	}

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
	if s.bus != nil {
		s.bus.Publish("tunnel:created", events.TunnelCreatedEvent{
			ID: parsed.ID, Name: parsed.Name, Backend: s.backendLabel(parsed),
		})
	}
	return s.Get(ctx, tunnelID)
}

// ReplaceConfig replaces a tunnel's Interface and Peer from a parsed .conf,
// preserving identity, routing, monitoring, and all other metadata.
func (s *ServiceImpl) ReplaceConfig(ctx context.Context, tunnelID, confContent, newName string) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	parsed, err := config.Parse(confContent)
	if err != nil {
		return fmt.Errorf("parse conf: %w", err)
	}

	// Replace Interface + Peer entirely
	stored.Interface = parsed.Interface
	stored.Peer = parsed.Peer

	// Optionally update name
	if newName != "" {
		stored.Name = newName
	}

	// Clear runtime state (will be re-populated on next start)
	stored.ResolvedEndpointIP = ""
	stored.ActiveWAN = ""
	stored.StartedAt = ""

	// Save to storage
	if err := s.store.Save(stored); err != nil {
		return fmt.Errorf("save tunnel: %w", err)
	}

	// Overwrite .conf file
	if err := s.writeConfigFile(stored); err != nil {
		s.logWarn("replace-config", tunnelID, "Failed to write config file: "+err.Error())
	}

	// NativeWG: sync NDMS config (address, MTU). Peer sync happens on restart.
	if s.nwgOperator != nil && s.isNativeWG(stored) {
		if err := s.nwgOperator.SyncAddressMTU(ctx, stored); err != nil {
			s.logWarn("replace-config", tunnelID, "SyncAddressMTU failed: "+err.Error())
		}
		// Update description if name changed
		if newName != "" {
			if err := s.nwgOperator.UpdateDescription(ctx, stored, newName); err != nil {
				s.logWarn("replace-config", tunnelID, "UpdateDescription failed: "+err.Error())
			}
		}
	}

	s.logInfo("replace-config", tunnelID, "Configuration replaced: "+stored.Name)
	if s.bus != nil {
		s.bus.Publish("tunnel:updated", events.TunnelUpdatedEvent{
			ID: tunnelID, Name: stored.Name,
		})
	}

	return nil
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



// GetState returns the current state of a tunnel.
func (s *ServiceImpl) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	// NativeWG: use nwgOperator.GetState directly
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.StateInfo{State: tunnel.StateUnknown}
	}
	if s.nwgOperator != nil && s.isNativeWG(stored) {
		return s.nwgOperator.GetState(ctx, stored)
	}

	// === Kernel path ===
	info := s.state.GetState(ctx, tunnelID)

	// After our Stop: state matrix sees Intent=DOWN + Process=true → NeedsStop.
	// But if we disabled the tunnel (Enabled=false), it's Disabled, not NeedsStop.
	if info.State == tunnel.StateNeedsStop {
		if !stored.Enabled {
			info.State = tunnel.StateDisabled
		}
	}

	return info
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

// backendLabel returns the backend label for a stored tunnel.
func (s *ServiceImpl) backendLabel(stored *storage.AWGTunnel) string {
	if s.isNativeWG(stored) {
		return "nativewg"
	}
	return "kernel"
}

// Ensure ServiceImpl implements Service interface.
var _ Service = (*ServiceImpl)(nil)
