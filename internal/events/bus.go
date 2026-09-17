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
	// clients — подмножество subscribers: клиентские (SSE) стримы. Живёт
	// отдельно, чтобы ClientCount отвечал «сколько человек смотрит», а не
	// «сколько подписок вообще».
	clients   map[string]struct{}
	lastID    atomic.Uint64
	nextSubID uint64
}

// NewBus creates a new event bus.
func NewBus() *Bus {
	return &Bus{
		subscribers: make(map[string]chan Event),
		clients:     make(map[string]struct{}),
	}
}

// Publish sends an event to all subscribers.
// Non-blocking: slow subscribers drop events.
func (b *Bus) Publish(eventType string, data any) {
	if b == nil {
		return
	}
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

// Subscribe creates a new subscription for an IN-PROCESS consumer.
// Returns subscriber ID, event channel, and unsubscribe function.
//
// Такие подписки живут всё время работы процесса (failover, statecache,
// connectivity, awgoutbounds, deviceproxy) и в ClientCount НЕ попадают:
// иначе вопрос «смотрит ли кто-нибудь» получал бы «да» всегда.
func (b *Bus) Subscribe() (string, <-chan Event, func()) {
	return b.subscribe(false)
}

// SubscribeClient — то же самое для КЛИЕНТСКОГО стрима (SSE). Отличие одно:
// такая подписка считается в ClientCount, по которому фоновые опросчики решают,
// нужна ли их работа кому-нибудь прямо сейчас.
//
// Брать её вправе только `internal/api/events.go`; сторож —
// TestSubscribeClient_OnlySSEHandler. Внутреннему потребителю нужен Subscribe.
func (b *Bus) SubscribeClient() (string, <-chan Event, func()) {
	return b.subscribe(true)
}

func (b *Bus) subscribe(client bool) (string, <-chan Event, func()) {
	b.mu.Lock()
	b.nextSubID++
	id := fmt.Sprintf("sub-%d", b.nextSubID)
	ch := make(chan Event, subscriberBufferSize)
	b.subscribers[id] = ch
	if client {
		b.clients[id] = struct{}{}
	}
	b.mu.Unlock()

	unsub := sync.OnceFunc(func() {
		b.mu.Lock()
		if _, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			delete(b.clients, id)
			close(ch)
		}
		b.mu.Unlock()
	})
	return id, ch, unsub
}

// ClientCount returns the number of active CLIENT (SSE) subscriptions —
// «сколько человек сейчас смотрит». Внутренние подписчики не считаются.
//
// nil-приёмник безопасен, как и у Publish: счётчик спрашивают с горячих путей
// (журнал, фоновые опросчики), и там нулевая шина должна означать «никто не
// смотрит», а не панику.
func (b *Bus) ClientCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// SubscriberCount returns the number of active subscribers.
func (b *Bus) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
