package pingcheck

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/logbuf"
)

const (
	defaultMaxAge = 2 * time.Hour
	maxEntries    = 5000
)

// LogBuffer stores ping-check log entries with automatic cleanup.
// Thin wrapper over logbuf.Buffer[LogEntry] — see internal/logbuf for
// the shared ring + TTL + goroutine-safe storage machinery.
type LogBuffer struct {
	inner *logbuf.Buffer[LogEntry]
}

// NewLogBuffer creates a new log buffer with automatic cleanup.
func NewLogBuffer() *LogBuffer {
	return &LogBuffer{
		inner: logbuf.New(logbuf.Options[LogEntry]{
			MaxAge:       defaultMaxAge,
			MaxEntries:   maxEntries,
			TimestampOf:  func(e LogEntry) time.Time { return e.Timestamp },
			SetTimestamp: func(e *LogEntry, t time.Time) { e.Timestamp = t },
		}),
	}
}

// Add adds a new log entry to the buffer.
func (lb *LogBuffer) Add(entry LogEntry) { lb.inner.Add(entry) }

// GetAll returns all log entries, newest first.
func (lb *LogBuffer) GetAll() []LogEntry { return lb.inner.GetAll() }

// GetByTunnel returns log entries for the given tunnel, newest first.
func (lb *LogBuffer) GetByTunnel(tunnelID string) []LogEntry {
	return lb.inner.Filter(func(e LogEntry) bool {
		return e.TunnelID == tunnelID
	})
}

// Clear removes all entries.
func (lb *LogBuffer) Clear() { lb.inner.Clear() }

// Stop stops the cleanup goroutine.
func (lb *LogBuffer) Stop() { lb.inner.Stop() }

// Len returns the number of entries in the buffer.
func (lb *LogBuffer) Len() int { return lb.inner.Len() }
