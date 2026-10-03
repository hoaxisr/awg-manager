package proxyengine

import (
	"context"
	"time"
)

// Engine is the generic interface that abstracts the underlying proxy core
// (Sing-box or Mihomo) from the router and orchestrator services.
type Engine interface {
	Reload() error
	IsRunning() (bool, int)
	Start() error
	Stop() error
	// ClearManualStop clears the sticky master-Stop intent so the
	// orchestrator cold-start is no longer suppressed.
	ClearManualStop() error
	ValidateConfigDir(ctx context.Context) error
	ConfigDir() string
	// Binary returns the absolute path (or PATH-resolvable name) of the
	// executable. May return empty string when the binary is unknown.
	Binary() string
	// LastError returns the last captured fatal/exit reason.
	LastError() string
	// CrashStats returns crash observability (recent crashes, reason, suppression time).
	CrashStats() (recentCrashes int, lastCrashReason string, restartSuppressedUntil time.Time)
}
