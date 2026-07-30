package orchestrator

import (
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
