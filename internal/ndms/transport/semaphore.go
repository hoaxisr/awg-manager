package transport

import (
	"context"
	"time"
)

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

// semAcquireBackstop bounds a deadline-less wait for an RCI slot. Callers
// passing context.Background() (boot paths, background loops) must not queue
// forever behind a wedged request.
//
// Переменная, а не константа, ровно по одной причине: иначе страховку нельзя
// проверить — тест не станет ждать минуту, и её снятие остаётся зелёным. Тот
// же приём, что у путей хуков netfilter. Менять в проде некому: писатель
// один — тест.
var semAcquireBackstop = 60 * time.Second

// acquireWithBackstop is Acquire with semAcquireBackstop applied ONLY when
// ctx carries no deadline of its own — explicitly-set deadlines (shorter or
// longer) are honored as-is. The backstop covers just the slot wait, never
// the request that follows.
func (s *Semaphore) acquireWithBackstop(ctx context.Context) error {
	if _, ok := ctx.Deadline(); ok {
		return s.Acquire(ctx)
	}
	waitCtx, cancel := context.WithTimeout(ctx, semAcquireBackstop)
	defer cancel()
	return s.Acquire(waitCtx)
}

// Release returns a slot. Panics if called without a matching Acquire.
func (s *Semaphore) Release() {
	select {
	case <-s.slots:
	default:
		panic("transport: Semaphore.Release without matching Acquire")
	}
}
