package pingcheck

import (
	"sync"
	"time"
)

const (
	defaultMaxAge   = 2 * time.Hour
	cleanupInterval = 5 * time.Minute
	maxEntries      = 5000
)

// LogBuffer stores ping check log entries in memory with automatic cleanup.
type LogBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	maxAge  time.Duration
	stopCh  chan struct{}
}

// NewLogBuffer creates a new log buffer with automatic cleanup.
func NewLogBuffer() *LogBuffer {
	lb := &LogBuffer{
		entries: make([]LogEntry, 0, 256),
		maxAge:  defaultMaxAge,
		stopCh:  make(chan struct{}),
	}
	go lb.cleanupLoop()
	return lb
}

// Add adds a new log entry to the buffer.
func (lb *LogBuffer) Add(entry LogEntry) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	if len(lb.entries) >= maxEntries {
		lb.entries = lb.entries[len(lb.entries)-maxEntries+1:]
	}

	lb.entries = append(lb.entries, entry)
}

// GetAll returns all log entries, newest first.
func (lb *LogBuffer) GetAll() []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	// Return copy in reverse order (newest first)
	result := make([]LogEntry, len(lb.entries))
	for i, j := 0, len(lb.entries)-1; j >= 0; i, j = i+1, j-1 {
		result[i] = lb.entries[j]
	}
	return result
}

// GetByTunnel returns log entries for a specific tunnel, newest first.
func (lb *LogBuffer) GetByTunnel(tunnelID string) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	var result []LogEntry
	// Iterate in reverse for newest first
	for i := len(lb.entries) - 1; i >= 0; i-- {
		if lb.entries[i].TunnelID == tunnelID {
			result = append(result, lb.entries[i])
		}
	}
	return result
}

// Clear removes all entries from the buffer.
func (lb *LogBuffer) Clear() {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.entries = lb.entries[:0]
}

// Stop stops the cleanup goroutine.
func (lb *LogBuffer) Stop() {
	close(lb.stopCh)
}

// cleanupLoop periodically removes old entries.
func (lb *LogBuffer) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			lb.cleanup()
		case <-lb.stopCh:
			return
		}
	}
}

// cleanup removes entries older than maxAge.
func (lb *LogBuffer) cleanup() {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	cutoff := time.Now().Add(-lb.maxAge)

	// Find first entry that's not too old
	firstValid := 0
	for i, entry := range lb.entries {
		if entry.Timestamp.After(cutoff) {
			firstValid = i
			break
		}
		firstValid = i + 1
	}

	if firstValid > 0 {
		// Remove old entries by slicing
		lb.entries = lb.entries[firstValid:]
	}
}

// Len returns the number of entries in the buffer.
func (lb *LogBuffer) Len() int {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return len(lb.entries)
}
