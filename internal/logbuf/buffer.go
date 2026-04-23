// Package logbuf provides a generic, time-bounded, size-capped in-memory
// log buffer with a background cleanup goroutine. It's the shared
// machinery behind internal/logging and internal/pingcheck — both need
// the same add-capped / age-out / reverse-read semantics over different
// entry types (app log vs ping-check result).
//
// The generic Buffer[T] stores any entry type; callers provide accessor
// and mutator closures for the timestamp. Callers express domain-specific
// filtering via Filter / FilterPage with a predicate closure.
package logbuf

import (
	"sync"
	"time"
)

// Options configures a Buffer[T]. All fields are required.
type Options[T any] struct {
	// MaxAge — entries older than this get dropped on cleanup ticks.
	MaxAge time.Duration
	// MaxEntries — cap on retained entries; oldest drop first on Add.
	MaxEntries int
	// CleanupInterval — how often the background goroutine runs cleanup.
	// A zero value defaults to 5 minutes.
	CleanupInterval time.Duration
	// TimestampOf extracts the entry's timestamp for age comparison.
	TimestampOf func(T) time.Time
	// SetTimestamp stamps the current time onto entries that arrive
	// with a zero timestamp. Optional — when nil, Add accepts entries
	// as-is and the caller is responsible for stamping.
	SetTimestamp func(*T, time.Time)
}

// Buffer is an in-memory ring-style buffer of entries with automatic
// cleanup. Safe for concurrent use.
type Buffer[T any] struct {
	mu              sync.RWMutex
	entries         []T
	maxAge          time.Duration
	maxEntries      int
	cleanupInterval time.Duration
	timestampOf     func(T) time.Time
	setTimestamp    func(*T, time.Time)
	stopCh          chan struct{}
	stopOnce        sync.Once
}

// New creates a Buffer[T] and starts its background cleanup goroutine.
// Callers must invoke Stop to release the goroutine.
func New[T any](opts Options[T]) *Buffer[T] {
	cleanupInterval := opts.CleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}
	b := &Buffer[T]{
		entries:         make([]T, 0, 256),
		maxAge:          opts.MaxAge,
		maxEntries:      opts.MaxEntries,
		cleanupInterval: cleanupInterval,
		timestampOf:     opts.TimestampOf,
		setTimestamp:    opts.SetTimestamp,
		stopCh:          make(chan struct{}),
	}
	go b.cleanupLoop()
	return b
}

// Add appends an entry. If the entry's timestamp is zero and a
// SetTimestamp hook was provided, stamps it with time.Now(). If the
// buffer is at MaxEntries, the oldest entries are dropped to make room.
func (b *Buffer[T]) Add(entry T) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.setTimestamp != nil && b.timestampOf(entry).IsZero() {
		b.setTimestamp(&entry, time.Now())
	}

	if len(b.entries) >= b.maxEntries {
		b.entries = b.entries[len(b.entries)-b.maxEntries+1:]
	}
	b.entries = append(b.entries, entry)
}

// GetAll returns all entries, newest first.
func (b *Buffer[T]) GetAll() []T {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]T, len(b.entries))
	for i, j := 0, len(b.entries)-1; j >= 0; i, j = i+1, j-1 {
		out[i] = b.entries[j]
	}
	return out
}

// Filter returns every entry matching pred, newest first.
func (b *Buffer[T]) Filter(pred func(T) bool) []T {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var out []T
	for i := len(b.entries) - 1; i >= 0; i-- {
		if pred(b.entries[i]) {
			out = append(out, b.entries[i])
		}
	}
	return out
}

// FilterPage returns entries matching pred paginated newest-first,
// along with the total count of matches.
//
// Single-pass: walks newest-first, increments total for every match,
// appends to page only when total falls in [offset, offset+limit).
// Avoids allocating the full matches slice just to slice off a small
// page — a big-buffer / small-page call that previously allocated
// MaxEntries × sizeof(T) now allocates at most limit × sizeof(T).
func (b *Buffer[T]) FilterPage(pred func(T) bool, limit, offset int) ([]T, int) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var page []T
	if limit > 0 {
		page = make([]T, 0, limit)
	} else {
		page = []T{}
	}
	end := offset + limit
	total := 0
	for i := len(b.entries) - 1; i >= 0; i-- {
		if !pred(b.entries[i]) {
			continue
		}
		if limit > 0 && total >= offset && total < end {
			page = append(page, b.entries[i])
		}
		total++
	}
	return page, total
}

// Clear drops all entries.
func (b *Buffer[T]) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = b.entries[:0]
}

// Len returns the current entry count.
func (b *Buffer[T]) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.entries)
}

// SetMaxAge updates the cutoff used by the cleanup loop. A non-positive
// hours value falls back to 2 hours.
func (b *Buffer[T]) SetMaxAge(hours int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hours <= 0 {
		hours = 2
	}
	b.maxAge = time.Duration(hours) * time.Hour
}

// Stop signals the cleanup goroutine to exit. Idempotent — repeat
// calls are no-ops, matching the shutdown contract of the surrounding
// services (Service.Stop / nwgMonitor.Stop may fire through multiple
// defer chains).
func (b *Buffer[T]) Stop() {
	b.stopOnce.Do(func() { close(b.stopCh) })
}

// cleanupLoop runs cleanup at a fixed cadence until Stop is called.
func (b *Buffer[T]) cleanupLoop() {
	ticker := time.NewTicker(b.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.cleanup()
		case <-b.stopCh:
			return
		}
	}
}

// cleanup removes entries older than maxAge.
//
// Assumes entries are appended in non-decreasing timestamp order — scans
// forward and drops the whole prefix up to the first non-expired entry.
// Both wrappers (logging, pingcheck) satisfy this: logging's Add stamps
// time.Now() under the write lock (serialises monotonically); pingcheck
// stamps before Add but its producers are per-tunnel monotonic.
//
// If a caller inserts out-of-order timestamps, cleanup may leave some
// stale entries alive past their TTL — never drops fresh entries, so
// the failure mode is "too few" deletions, not correctness.
func (b *Buffer[T]) cleanup() {
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := time.Now().Add(-b.maxAge)
	firstValid := 0
	for i, entry := range b.entries {
		if b.timestampOf(entry).After(cutoff) {
			firstValid = i
			break
		}
		firstValid = i + 1
	}
	if firstValid > 0 {
		b.entries = b.entries[firstValid:]
	}
}
