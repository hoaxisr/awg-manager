package tunnel

// HookNotifier allows components to register expected NDMS hooks
// before calling InterfaceUp/InterfaceDown. ReconcileInterface
// consumes expected hooks to filter out self-triggered events.
type HookNotifier interface {
	ExpectHook(ndmsName, level string)
}
