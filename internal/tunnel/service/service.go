// Package service provides the high-level tunnel service with business logic.
// This is the main entry point for tunnel operations.
package service

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Service is the interface for high-level tunnel operations.
// It orchestrates state checking, operator calls, and storage updates.
type Service interface {
	// CRUD operations

	// Create creates a new tunnel and saves it to storage.
	Create(ctx context.Context, tunnelID, name string, cfg tunnel.Config) error

	// Get returns a tunnel with its current state.
	Get(ctx context.Context, tunnelID string) (*TunnelWithStatus, error)

	// List returns all tunnels with their current states.
	List(ctx context.Context) ([]TunnelWithStatus, error)

	// Update updates a tunnel's configuration.
	Update(ctx context.Context, tunnelID string, cfg tunnel.Config) error

	// Lifecycle operations

	// Start starts a tunnel.
	// Checks current state, recovers if broken, then starts.
	// Safe to call on boot — operator only applies NDMS config when OpkgTun
	// was just created, not on every start.
	Start(ctx context.Context, tunnelID string) error

	// Stop stops a tunnel.
	Stop(ctx context.Context, tunnelID string) error

	// Restart stops and starts a tunnel.
	Restart(ctx context.Context, tunnelID string) error

	// Reconcile re-applies system configuration around an already-running process.
	// Used when the process survived a daemon restart but NDMS state was lost (Broken + ProcessRunning).
	Reconcile(ctx context.Context, tunnelID string) error

	// Delete stops (if running) and deletes a tunnel.
	// Includes retry logic for reliable deletion.
	Delete(ctx context.Context, tunnelID string) error

	// SetEnabled changes the enabled/autostart state of a tunnel.
	SetEnabled(ctx context.Context, tunnelID string, enabled bool) error

	// SetDefaultRoute changes the default route setting.
	// If tunnel is running, immediately applies route changes.
	SetDefaultRoute(ctx context.Context, tunnelID string, enabled bool) error

	// Import parses a WireGuard .conf file and creates a tunnel.
	Import(ctx context.Context, confContent, name string) (*TunnelWithStatus, error)

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

	// Reconcile

	// ReconcileInterface handles an NDMS interface state change event.
	// Called by iflayerchanged.d hook when user toggles interface in router UI.
	ReconcileInterface(ctx context.Context, ndmsName, layer, level string) error

	// WAN event handlers

	// HandleWANUp is called when a WAN interface comes up.
	// Starts tunnels bound to this WAN. Auto-mode tunnels may switch
	// to the new default gateway if it changed.
	HandleWANUp(ctx context.Context, iface string)

	// HandleWANDown is called when a WAN interface goes down.
	// Kills only tunnels bound to this specific WAN. Auto-mode tunnels
	// attempt immediate failover to another available gateway.
	HandleWANDown(ctx context.Context, iface string)

	// WANModel returns the unified WAN state model.
	WANModel() *wan.Model

	// PingCheck integration

	// HandleMonitorDead is called when PingCheck detects a dead tunnel.
	// Persists dead state in storage and kills the tunnel process (KillLink).
	HandleMonitorDead(ctx context.Context, tunnelID string) error

	// HandleMonitorRecovered is called when PingCheck detects tunnel recovery.
	// Attempts a full restart. Returns error if restart fails (pingcheck retries).
	HandleMonitorRecovered(ctx context.Context, tunnelID string) error

	// RestoreEndpointTracking restores endpoint route tracking on daemon restart.
	// For running tunnels, re-populates the in-memory tracking map.
	RestoreEndpointTracking(ctx context.Context) error

	// TeardownForBackendSwitch fully removes OS-side resources (OpkgTun, interface,
	// firewall) for all tunnels while keeping storage intact. Used before daemon restart
	// when switching backend mode (kernel ↔ userspace). After restart, Start() will
	// recreate everything from scratch.
	TeardownForBackendSwitch(ctx context.Context) error

	// MigrateISPInterfaceNone converts legacy "none" ISPInterface values to "" (auto).
	// Called once at startup to migrate tunnels from older versions.
	MigrateISPInterfaceNone()
}

// TunnelWithStatus combines stored tunnel data with live status.
type TunnelWithStatus struct {
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	Config             tunnel.Config    `json:"-"`
	State              tunnel.State     `json:"state"`
	StateInfo          tunnel.StateInfo `json:"stateInfo"`
	Enabled            bool             `json:"enabled"`
	AutoStart          bool             `json:"autoStart,omitempty"`
	PingCheckOn        bool             `json:"pingCheckOn,omitempty"`
	DefaultRoute       bool             `json:"defaultRoute"`
	ISPInterface       string           `json:"ispInterface,omitempty"`
	InterfaceName      string           `json:"interfaceName"`      // Kernel interface name (opkgtun0 on OS5, awg0 on OS4)
	ConfigPreview      string           `json:"configPreview,omitempty"` // Generated .conf content for display
	IsDeadByMonitoring bool             `json:"isDeadByMonitoring"` // True if PingCheck marked this tunnel as dead
}
