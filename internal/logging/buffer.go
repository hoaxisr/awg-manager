package logging

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/logbuf"
)

const (
	defaultMaxAge = 4 * time.Hour
	maxEntries    = 10000
)

// LogBuffer stores app log entries with automatic cleanup.
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

// GetFiltered returns log entries matching group/subgroup/level, newest first.
// Empty string for any field means "no constraint on that field".
func (lb *LogBuffer) GetFiltered(group, subgroup, level string) []LogEntry {
	return lb.inner.Filter(matcher(group, subgroup, level, time.Time{}))
}

// GetPaginated returns filtered entries with pagination, newest first,
// plus the total count of filtered entries. A non-zero `since` restricts
// the result to entries whose Timestamp is strictly after `since`
// (used for SSE catch-up after a reconnect).
func (lb *LogBuffer) GetPaginated(group, subgroup, level string, since time.Time, limit, offset int) ([]LogEntry, int) {
	return lb.inner.FilterPage(matcher(group, subgroup, level, since), limit, offset)
}

// Clear removes all entries.
func (lb *LogBuffer) Clear() { lb.inner.Clear() }

// SetMaxAge updates the maximum age for log entries (hours).
func (lb *LogBuffer) SetMaxAge(hours int) { lb.inner.SetMaxAge(hours) }

// Stop stops the cleanup goroutine.
func (lb *LogBuffer) Stop() { lb.inner.Stop() }

// Len returns the number of entries in the buffer.
func (lb *LogBuffer) Len() int { return lb.inner.Len() }

// matcher builds the group/subgroup/level/since composite predicate once so
// Filter/FilterPage don't recompute the closure shape per entry. A zero
// `since` disables the timestamp cutoff.
func matcher(group, subgroup, level string, since time.Time) func(LogEntry) bool {
	return func(e LogEntry) bool {
		if !since.IsZero() && !e.Timestamp.After(since) {
			return false
		}
		if group != "" && e.Group != group {
			return false
		}
		if subgroup != "" && e.Subgroup != subgroup {
			return false
		}
		if level != "" && !IsVisible(Level(e.Level), Level(level)) {
			return false
		}
		return true
	}
}
