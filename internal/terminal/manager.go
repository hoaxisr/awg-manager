package terminal

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// Manager manages the ttyd terminal process lifecycle.
type Manager interface {
	// IsInstalled checks if ttyd binary is available on the system.
	IsInstalled(ctx context.Context) bool
	// Install runs opkg install ttyd.
	Install(ctx context.Context) error
	// Start launches ttyd on a free port. Returns the port number.
	Start(ctx context.Context) (port int, err error)
	// Stop kills the running ttyd process.
	Stop(ctx context.Context) error
	// Shutdown gracefully stops ttyd on app exit. Register in shutdownHooks.
	Shutdown(ctx context.Context) error
	// IsRunning returns true if ttyd process is alive.
	IsRunning() bool
	// HasActiveSession returns true if a WebSocket proxy session is in progress.
	HasActiveSession() bool
	// SetSessionActive sets the session state (called by WebSocket proxy).
	SetSessionActive(active bool)
	// Port returns the current ttyd port (0 if not running).
	Port() int
}

// New creates a new terminal manager.
func New(log logging.AppLogger) *ManagerImpl {
	return &ManagerImpl{log: log}
}
