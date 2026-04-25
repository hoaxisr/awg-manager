// Package service provides the high-level tunnel service with business logic.
// This is the main entry point for tunnel operations.
package service

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Service is the interface for high-level tunnel operations.
// It orchestrates state checking, operator calls, and storage updates.
type Service interface {
	// CRUD operations

	// Create creates a new tunnel and saves it to storage.
	// For NativeWG tunnels, pass stored with Backend="nativewg"; Create will
	// call nwgOperator and set stored.NWGIndex before returning.
	Create(ctx context.Context, tunnelID, name string, cfg tunnel.Config, stored *storage.AWGTunnel) error

	// Get returns a tunnel with its current state.
	Get(ctx context.Context, tunnelID string) (*TunnelWithStatus, error)

	// List returns all tunnels with their current states.
	List(ctx context.Context) ([]TunnelWithStatus, error)

	// Update applies a tunnel configuration diff. The handler is the only
	// writer of storage; this method performs runtime RCI commands based on
	// the difference between oldStored (current persisted state) and newStored
	// (the state about to be persisted). It does NOT save to storage itself.
	//
	// Mutation contract: Update MAY mutate runtime fields on newStored
	// (currently ResolvedEndpointIP and ActiveWAN, populated when the
	// endpoint route is re-set up). Callers must observe the mutation
	// through the same pointer and persist newStored AFTER Update returns.
	//
	// Failure semantics: Update returns an error when an RCI command fails.
	// The handler is responsible for translating this into a 4xx/5xx
	// response and skipping the storage save (fail-closed) so on-disk
	// state never diverges from the running interface.
	Update(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error

	// Lifecycle operations — thin delegators to orchestrator.

	// Start starts a tunnel.
	Start(ctx context.Context, tunnelID string) error

	// Stop stops a tunnel.
	Stop(ctx context.Context, tunnelID string) error

	// Restart stops and starts a tunnel.
	Restart(ctx context.Context, tunnelID string) error

	// Delete stops (if running) and deletes a tunnel.
	Delete(ctx context.Context, tunnelID string) error

	// SetEnabled changes the enabled/autostart state of a tunnel.
	SetEnabled(ctx context.Context, tunnelID string, enabled bool) error

	// SetDefaultRoute changes the default route setting.
	// If tunnel is running, immediately applies route changes.
	SetDefaultRoute(ctx context.Context, tunnelID string, enabled bool) error

	// Import parses a WireGuard .conf file and creates a tunnel.
	// backend selects the tunnel backend: "nativewg" or "kernel" (default).
	Import(ctx context.Context, confContent, name, backend string) (*TunnelWithStatus, error)

	// ReplaceConfig replaces a tunnel's Interface and Peer from a new .conf,
	// preserving all metadata (ID, Backend, NWGIndex, routing, PingCheck, etc.).
	// Does NOT handle stop/start — caller is responsible for lifecycle.
	ReplaceConfig(ctx context.Context, tunnelID, confContent, newName string) error

	// Validation

	// CheckAddressConflicts returns warnings if the tunnel's address
	// conflicts with any other stored tunnel.
	CheckAddressConflicts(ctx context.Context, tunnelID string) []string

	// State operations

	// GetState returns the current state of a tunnel.
	GetState(ctx context.Context, tunnelID string) tunnel.StateInfo

	// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
	// For auto-mode tunnels, returns the WAN picked during endpoint route setup.
	GetResolvedISP(tunnelID string) string

	// WANModel returns the unified WAN state model.
	WANModel() *wan.Model

	// MigrateISPInterfaceNone converts legacy "none" ISPInterface values to "" (auto).
	// Called once at startup to migrate tunnels from older versions.
	MigrateISPInterfaceNone()

	// MigrateISPInterfaceToKernel converts legacy NDMS ID values in ISPInterface
	// and ActiveWAN to kernel names. Called once at startup after WAN model is populated.
	MigrateISPInterfaceToKernel()

	// MigrateEmptyBackend sets Backend="kernel" on all tunnels with empty Backend field.
	MigrateEmptyBackend()
}

// TunnelWithStatus combines stored tunnel data with live status.
type TunnelWithStatus struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Config        tunnel.Config    `json:"-"`
	State         tunnel.State     `json:"state"`
	StateInfo     tunnel.StateInfo `json:"stateInfo"`
	Enabled       bool             `json:"enabled"`
	AutoStart     bool             `json:"autoStart,omitempty"`
	PingCheckOn   bool             `json:"pingCheckOn,omitempty"`
	DefaultRoute  bool             `json:"defaultRoute"`
	ISPInterface  string           `json:"ispInterface,omitempty"`
	InterfaceName string           `json:"interfaceName"`           // Kernel interface name (opkgtun0 on OS5, awg0 on OS4, nwgN for NativeWG)
	NDMSName      string           `json:"ndmsName,omitempty"`      // NDMS interface name (WireguardN), NativeWG only — how SSE events key per tunnel
	ConfigPreview string           `json:"configPreview,omitempty"` // Generated .conf content for display
	Backend       string           `json:"backend"`                 // "nativewg" | "kernel"
}
