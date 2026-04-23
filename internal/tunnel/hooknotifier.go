package tunnel

// HookNotifier allows components to register expected NDMS hooks
// before calling InterfaceUp/InterfaceDown. ReconcileInterface
// consumes expected hooks to filter out self-triggered events.
type HookNotifier interface {
	ExpectHook(ndmsName, level string)
}

// SelfCreateGater is the contract between the tunnel service and the
// API hook handler for suppressing ifcreated-driven snapshot refreshes
// during awg-manager-initiated NDMS interface creation. While the gate
// is open, the hook handler skips its automatic snapshot rebroadcast —
// the creator is expected to publish a fresh snapshot itself after
// persisting its new tunnel to the store.
type SelfCreateGater interface {
	EnterSelfCreate()
	ExitSelfCreate()
}
