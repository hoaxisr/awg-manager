package events

import (
	"fmt"
	"sync"
	"sync/atomic"
)

const subscriberBufferSize = 64

// Bus distributes events to SSE subscribers.
// Thread-safe. Supports multiple concurrent subscribers.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[string]chan Event
	lastID      atomic.Uint64
	nextSubID   uint64
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[string]chan Event),
	}
}

// Publish sends an event to all subscribers.
// Non-blocking: slow subscribers drop events.
func (b *Bus) Publish(eventType string, data any) {
	id := b.lastID.Add(1)
	event := Event{ID: id, Type: eventType, Data: data}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			// subscriber too slow — drop event
		}
	}
}

// Subscribe creates a new subscription.
// Returns subscriber ID, event channel, and unsubscribe function.
func (b *Bus) Subscribe() (string, <-chan Event, func()) {
	b.mu.Lock()
	b.nextSubID++
	id := fmt.Sprintf("sub-%d", b.nextSubID)
	ch := make(chan Event, subscriberBufferSize)
	b.subscribers[id] = ch
	b.mu.Unlock()

	unsub := sync.OnceFunc(func() {
		b.mu.Lock()
		if _, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(ch)
		}
		b.mu.Unlock()
	})
	return id, ch, unsub
}

// SubscriberCount returns the number of active subscribers.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
