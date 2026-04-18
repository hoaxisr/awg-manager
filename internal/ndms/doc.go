// Package ndms is the NDMS RCI integration layer for awg-manager.
//
// The package follows CQRS: reads go through query/*Store (not in this plan),
// writes through command/*Commands (not in this plan). This doc.go only marks
// the root package; the primitives live in sub-packages:
//
//   - cache:     generic TTL + single-flight primitives
//   - transport: low-level HTTP client with a concurrency semaphore
//   - command:   write-side coordinators (SaveCoordinator only in Plan 1)
//
// Later plans add query/, events/, and the per-resource Store / Command groups.
package ndms
