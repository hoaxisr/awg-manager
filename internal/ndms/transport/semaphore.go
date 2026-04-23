package transport

import "context"

// Semaphore is a channel-backed bounded concurrency gate. Acquire blocks
// until a slot is free or ctx is cancelled; Release returns a slot.
type Semaphore struct {
	slots chan struct{}
}

// NewSemaphore returns a semaphore with the given capacity.
func NewSemaphore(capacity int) *Semaphore {
	if capacity < 1 {
		capacity = 1
	}
	return &Semaphore{slots: make(chan struct{}, capacity)}
}

// Acquire blocks until a slot is available or ctx is cancelled.
// Returns ctx.Err() on cancellation.
func (s *Semaphore) Acquire(ctx context.Context) error {
	select {
	case s.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release returns a slot. Panics if called without a matching Acquire.
func (s *Semaphore) Release() {
	select {
	case <-s.slots:
	default:
		panic("transport: Semaphore.Release without matching Acquire")
	}
}
