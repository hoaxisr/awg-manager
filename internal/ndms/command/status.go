package command

import "time"

// SaveState enumerates the visible states of the debounced Save pipeline.
type SaveState int

const (
	// SaveStateIdle — no requests pending, no error.
	SaveStateIdle SaveState = iota
	// SaveStatePending — Request()s queued, debounce timer running.
	SaveStatePending
	// SaveStateSaving — Save POST in flight.
	SaveStateSaving
	// SaveStateError — last attempt failed; retries scheduled.
	SaveStateError
	// SaveStateFailed — retries exhausted; manual Flush needed.
	SaveStateFailed
)

// String returns the wire-level name of s (matches SSE payload).
func (s SaveState) String() string {
	switch s {
	case SaveStateIdle:
		return "idle"
	case SaveStatePending:
		return "pending"
	case SaveStateSaving:
		return "saving"
	case SaveStateError:
		return "error"
	case SaveStateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// SaveStatus is the SSE payload emitted by SaveCoordinator on every state
// transition.
type SaveStatus struct {
	State        SaveState
	LastError    string
	LastSaveAt   time.Time
	PendingCount int
}

// StatusPublisher is the subset of *events.Bus SaveCoordinator requires.
// Isolated so tests can inject a fake without pulling in the full bus.
type StatusPublisher interface {
	Publish(eventType string, data any)
}
