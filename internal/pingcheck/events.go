package pingcheck

import "github.com/hoaxisr/awg-manager/internal/events"

// publishInvalidatedBus posts a resource:invalidated hint to the SSE bus.
// Duplicate of internal/api.publishInvalidated — lives here to avoid an
// import cycle between this package and internal/api.
//
// TODO(tech-debt): consolidate publishInvalidatedBus helpers into
// internal/events once the import-cycle with internal/api is resolved.
// Currently duplicated in internal/orchestrator and internal/pingcheck
// because those packages cannot import internal/api.
func publishInvalidatedBus(bus *events.Bus, resource, reason string) {
	if bus == nil {
		return
	}
	bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{
		Resource: resource,
		Reason:   reason,
	})
}
