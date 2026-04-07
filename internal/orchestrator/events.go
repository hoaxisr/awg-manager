package orchestrator

import (
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// EventType identifies the kind of event.
type EventType int

const (
	EventBoot            EventType = iota // Router boot — start all enabled
	EventReconnect                         // Daemon restart — restore state
	EventStart                             // User clicks Start
	EventStop                              // User clicks Stop
	EventRestart                           // User clicks Restart
	EventCreate                            // User creates tunnel
	EventDelete                            // User deletes tunnel
	EventUpdate                            // User updates config
	EventImport                            // User imports .conf
	EventSetEnabled                        // User toggles enabled
	EventSetDefaultRoute                   // User toggles default route
	EventWANUp                             // WAN interface came up
	EventWANDown                           // WAN interface went down
	EventNDMSHook                          // NDMS iflayerchanged.d hook
	EventPingCheckFailed                   // Connectivity loss detected
)

// Event is the input to the orchestrator.
type Event struct {
	Type    EventType
	Tunnel  string // tunnel ID for tunnel-specific events

	// NDMS hook data
	NDMSName string
	Layer    string
	Level    string

	// WAN event data
	WANIface string

	// Create/Update data
	Config *tunnel.Config
	Stored *storage.AWGTunnel

	// Import data
	ConfContent   string
	ImportName    string
	ImportBackend string

	// Toggle data
	Enabled      *bool
	DefaultRoute *bool
}
