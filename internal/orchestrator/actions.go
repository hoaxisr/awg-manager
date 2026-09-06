package orchestrator

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
	ActionReconcileKernel
	ActionSuspendKernel
	ActionResumeKernel
	ActionReconcileNativeWG

	// Monitoring
	ActionStartMonitoring
	ActionStopMonitoring
	ActionConfigurePingCheck
	ActionRemovePingCheck

	// Routing
	ActionApplyDNSRoutes
	ActionApplyStaticRoutes
	ActionRemoveStaticRoutes
	ActionApplyClientRoutes
	ActionRemoveClientRoutes
	ActionReconcileStaticRoutes
	ActionReconcileDNSRoutes
	ActionDeleteDNSRoutes
	ActionDeleteStaticRoutes
	ActionDeleteClientRoutes

	// Persistence
	ActionPersistRunning
	ActionPersistStopped

	// CRUD
	ActionDeleteKernel
	ActionDeleteNativeWG
)

// Action is one step in the execution plan.
type Action struct {
	Type   ActionType
	Tunnel string
	Iface  string // kernel interface name
}
