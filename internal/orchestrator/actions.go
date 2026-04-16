package orchestrator

import (
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// ActionType identifies what the executor should do.
type ActionType int

const (
	// Tunnel lifecycle
	ActionColdStartKernel ActionType = iota
	ActionStartNativeWG
	ActionStopKernel
	ActionStopNativeWG
	ActionSuspendProxy
	ActionRestoreKmod
	ActionRestoreEndpointTracking
	ActionLinkToggle
	ActionReconcileKernel
	ActionSuspendKernel
	ActionResumeKernel

	// Live config
	ActionApplyConfig
	ActionSetMTU
	ActionSetDefaultRoute
	ActionRemoveDefaultRoute

	// Monitoring
	ActionStartMonitoring
	ActionStopMonitoring
	ActionConfigurePingCheck
	ActionRemovePingCheck
	ActionExternalRestart // soft-stop + restart for externally-disabled tunnels

	// Routing
	ActionApplyDNSRoutes
	ActionApplyStaticRoutes
	ActionRemoveStaticRoutes
	ActionApplyClientRoutes
	ActionRemoveClientRoutes
	ActionApplySystemClientRoutes
	ActionRemoveSystemClientRoutes
	ActionReconcileStaticRoutes
	ActionReconcileDNSRoutes
	ActionDeleteDNSRoutes
	ActionDeleteStaticRoutes
	ActionDeleteClientRoutes

	// Persistence
	ActionPersistRunning
	ActionPersistStopped
	ActionPersistEnabled

	// CRUD
	ActionCreateKernel
	ActionCreateNativeWG
	ActionDeleteKernel
	ActionDeleteNativeWG
)

// Action is one step in the execution plan.
type Action struct {
	Type    ActionType
	Tunnel  string
	Config  *tunnel.Config
	WAN     string // resolved WAN interface
	Iface   string // kernel interface name
	Enabled *bool
}
