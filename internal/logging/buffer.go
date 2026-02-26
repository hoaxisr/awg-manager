package logging

import (
	"sync"
	"time"
)

const (
	defaultMaxAge   = 2 * time.Hour
	cleanupInterval = 5 * time.Minute
	maxEntries      = 5000
)

// LogBuffer stores log entries in memory with automatic cleanup.
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

// GetByCategory returns log entries for a specific category, newest first.
func (lb *LogBuffer) GetByCategory(category string) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	var result []LogEntry
	// Iterate in reverse for newest first
	for i := len(lb.entries) - 1; i >= 0; i-- {
		if lb.entries[i].Category == category {
			result = append(result, lb.entries[i])
		}
	}
	return result
}

// GetByLevel returns log entries for a specific level, newest first.
func (lb *LogBuffer) GetByLevel(level string) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	var result []LogEntry
	// Iterate in reverse for newest first
	for i := len(lb.entries) - 1; i >= 0; i-- {
		if lb.entries[i].Level == level {
			result = append(result, lb.entries[i])
		}
	}
	return result
}

// GetFiltered returns log entries filtered by category and/or level, newest first.
func (lb *LogBuffer) GetFiltered(category, level string) []LogEntry {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	var result []LogEntry
	// Iterate in reverse for newest first
	for i := len(lb.entries) - 1; i >= 0; i-- {
		entry := lb.entries[i]
		if category != "" && entry.Category != category {
			continue
		}
		if level != "" && entry.Level != level {
			continue
		}
		result = append(result, entry)
	}
	return result
}

// Clear removes all entries from the buffer.
func (lb *LogBuffer) Clear() {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.entries = lb.entries[:0]
}

// SetMaxAge updates the maximum age for log entries.
func (lb *LogBuffer) SetMaxAge(hours int) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if hours <= 0 {
		hours = 2 // default
	}
	lb.maxAge = time.Duration(hours) * time.Hour
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
