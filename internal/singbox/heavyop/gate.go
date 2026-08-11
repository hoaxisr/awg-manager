// Package heavyop serializes memory-heavy sing-box work (config apply/reload).
package heavyop

import (
	"context"
	"sync"
)

// Gate ensures memory-heavy sing-box operations do not run concurrently.
// Channel-backed (not sync.Mutex) so acquisition can be bounded by a context.
type Gate struct {
	once sync.Once
	ch   chan struct{}
}

// Default is the process-wide gate shared by the orchestrator reload and the
// router's direct config apply.
var Default Gate

func (g *Gate) sem() chan struct{} {
	g.once.Do(func() { g.ch = make(chan struct{}, 1) })
	return g.ch
}

// Lock blocks until no other heavy operation is running.
func (g *Gate) Lock() {
	g.sem() <- struct{}{}
}

// LockWithContext blocks until the gate is acquired or ctx ends.
// Returns ctx.Err() when the deadline expires or ctx is cancelled first.
func (g *Gate) LockWithContext(ctx context.Context) error {
	select {
	case g.sem() <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Unlock releases the gate.
func (g *Gate) Unlock() {
	select {
	case <-g.sem():
	default:
		panic("heavyop: Unlock of unlocked Gate")
	}
}

// TryLock reports whether the gate was acquired without blocking.
func (g *Gate) TryLock() bool {
	select {
	case g.sem() <- struct{}{}:
		return true
	default:
		return false
	}
}
