package orchestrator

import (
	"strconv"
	"time"
)

// EventType identifies the kind of event.
type EventType int

const (
	EventBoot            EventType = iota // Router boot — start all enabled
	EventReconnect                        // Daemon restart — restore state
	EventStart                            // User clicks Start
	EventStop                             // User clicks Stop
	EventRestart                          // User clicks Restart
	EventDelete                           // User deletes tunnel
	EventWANUp                            // WAN interface came up
	EventWANDown                          // WAN interface went down
	EventNDMSHook                         // NDMS iflayerchanged.d hook
	EventPingCheckFailed                  // Connectivity loss detected
	EventQuiesce                          // Stop running tunnels without disabling (backup/restore)

	// eventTypeCount — сентинель для теста полноты String(). Держать
	// последним: новое событие, добавленное после него, останется
	// безымянным в логе держателя замка и тест этого не заметит.
	eventTypeCount
)

// Event is the input to the orchestrator.
type Event struct {
	Type   EventType
	Tunnel string // tunnel ID for tunnel-specific events

	// Now is the decision-time clock, stamped by HandleEvent. Lets the pure
	// decide functions compare against per-tunnel time windows without I/O.
	Now time.Time

	// NDMS hook data
	NDMSName string
	Layer    string
	Level    string

	// WAN event data
	WANIface string
}

// String names the event for logs — notably the per-tunnel lock holder
// (issue #795), where a bare int told nobody which operation was wedged.
func (t EventType) String() string {
	switch t {
	case EventBoot:
		return "boot"
	case EventReconnect:
		return "reconnect"
	case EventStart:
		return "start"
	case EventStop:
		return "stop"
	case EventRestart:
		return "restart"
	case EventDelete:
		return "delete"
	case EventWANUp:
		return "wan-up"
	case EventWANDown:
		return "wan-down"
	case EventNDMSHook:
		return "ndms-hook"
	case EventPingCheckFailed:
		return "pingcheck-failed"
	case EventQuiesce:
		return "quiesce"
	}
	return "event-" + strconv.Itoa(int(t))
}
