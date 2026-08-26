package api

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// ── Response DTOs ────────────────────────────────────────────────

const maxBodySize = 1 << 20 // 1 MB

// TunnelService defines the interface for tunnel operations used by API handlers.
type TunnelService interface {
	// CRUD
	List(ctx context.Context) ([]service.TunnelWithStatus, error)
	Get(ctx context.Context, tunnelID string) (*service.TunnelWithStatus, error)
	Create(ctx context.Context, tunnelID, name string, cfg tunnel.Config, stored *storage.AWGTunnel) error
	Update(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error
	Delete(ctx context.Context, tunnelID string) error

	// Lifecycle (delegated to orchestrator)
	Start(ctx context.Context, tunnelID string) error
	Stop(ctx context.Context, tunnelID string) error
	Restart(ctx context.Context, tunnelID string) error

	// Validation
	CheckAddressConflicts(ctx context.Context, tunnelID string) []string

	// State
	GetState(ctx context.Context, tunnelID string) tunnel.StateInfo

	// Settings
	SetEnabled(ctx context.Context, tunnelID string, enabled bool) error
	SetDefaultRoute(ctx context.Context, tunnelID string, enabled bool) error

	// Import
	Import(ctx context.Context, confContent, name, backend string) (*service.TunnelWithStatus, error)

	// ReplaceConfig replaces a tunnel's config from a new .conf file.
	ReplaceConfig(ctx context.Context, tunnelID, confContent, newName string) error

	// WAN state model
	WANModel() *wan.Model

	// Resolved ISP for auto-mode tunnels
	GetResolvedISP(tunnelID string) string

	// SetSelfCreateGate wires the gate used by import/create paths to
	// suppress hook-driven snapshot refreshes while an NDMS interface is
	// being created but our store.Save hasn't run yet.
	SetSelfCreateGate(g tunnel.SelfCreateGater)
}

// TunnelsHandler handles tunnel CRUD operations.
type TunnelsHandler struct {
	svc               TunnelService
	orch              *orchestrator.Orchestrator
	store             *storage.AWGTunnelStore
	settingsStore     *storage.SettingsStore
	pingCheck         PingCheckService
	bus               *events.Bus
	catalog           routing.Catalog
	log               *logging.ScopedLogger
	traffic           *traffic.History
	pingCheckSnapshot func()
	// selfCreateGate (optional) suppresses the hook-driven snapshot
	// refresh while awg-manager is itself in the middle of creating an
	// NDMS interface. See tunnel.SelfCreateGater / api.HookHandler for
	// the contract.
	selfCreateGate tunnel.SelfCreateGater
	// buildTunnelsSnapshot (optional) assembles the composite
	// {tunnels, external, system} payload used by GetAll and by
	// mutation handlers that return fresh state. Injected by server.go
	// so TunnelsHandler doesn't need direct references to External /
	// System tunnel handlers. Falls back to managed-only when nil.
	buildTunnelsSnapshot func(ctx context.Context) map[string]interface{}
}

// NewTunnelsHandler creates a new tunnels handler.
func NewTunnelsHandler(svc TunnelService, store *storage.AWGTunnelStore, appLogger logging.AppLogger) *TunnelsHandler {
	return &TunnelsHandler{
		svc:   svc,
		store: store,
		log:   logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
	}
}

// SetEventBus sets the event bus for SSE publishing.
func (h *TunnelsHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// SetCatalog sets the routing catalog for tunnel list updates.
func (h *TunnelsHandler) SetCatalog(cat routing.Catalog) { h.catalog = cat }

// SetTunnelsSnapshotBuilder wires the composer used by GetAll and
// mutation handlers that return fresh snapshot state. Server.go
// typically injects TunnelsSnapshotBuilder.Build.
func (h *TunnelsHandler) SetTunnelsSnapshotBuilder(fn func(ctx context.Context) map[string]interface{}) {
	h.buildTunnelsSnapshot = fn
}

// SetSelfCreateGate wires the gate used to suppress hook-driven snapshot
// refreshes while the handler itself is creating an NDMS interface
// (manual Create path — import path gates inside ServiceImpl directly).
func (h *TunnelsHandler) SetSelfCreateGate(g tunnel.SelfCreateGater) {
	h.selfCreateGate = g
}

// SetSettingsStore sets the settings store for reading defaults.
func (h *TunnelsHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

// SetPingCheckService sets the ping check service for monitoring control.
func (h *TunnelsHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

// SetTrafficHistory sets the traffic history accumulator.
func (h *TunnelsHandler) SetTrafficHistory(th *traffic.History) {
	h.traffic = th
}

// SetOrchestrator sets the orchestrator for lifecycle operations.
func (h *TunnelsHandler) SetOrchestrator(orch *orchestrator.Orchestrator) {
	h.orch = orch
}

// SetPingCheckSnapshot sets the function that publishes a pingcheck snapshot.
func (h *TunnelsHandler) SetPingCheckSnapshot(fn func()) { h.pingCheckSnapshot = fn }
