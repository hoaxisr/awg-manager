// Package proxysup содержит примитивы, общие для супервизоров FreeTurn и WDTT.
package proxysup

import (
	"sync"
	"time"
)

// Backoff растягивает интервал повторных попыток старта. Пока причина отказа не
// устранена (нет бинаря, занят порт, не поднимается интерфейс), повтор на каждом
// тике супервизора бессмысленно нагружает роутер: путь старта WDTT-сервера тянет
// NDMS/RCI-вызовы и ожидание интерфейса до 8 с.
//
// Задержка — base·2^(n-1), но не больше max. Успешный старт сбрасывает счётчик.
type Backoff struct {
	base time.Duration
	max  time.Duration

	mu    sync.Mutex
	state map[string]attempt
}

type attempt struct {
	fails int
	next  time.Time
}

func NewBackoff(base, max time.Duration) *Backoff {
	return &Backoff{base: base, max: max, state: make(map[string]attempt)}
}

// Allow reports whether a start attempt for id may run at now.
func (b *Backoff) Allow(id string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.state[id]
	return !ok || !now.Before(st.next)
}

// Fail records a failed attempt and pushes the next allowed one further away.
func (b *Backoff) Fail(id string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.state[id]
	st.fails++
	delay := b.base
	for i := 1; i < st.fails && delay < b.max; i++ {
		delay *= 2
	}
	if delay > b.max {
		delay = b.max
	}
	st.next = now.Add(delay)
	b.state[id] = st
}

// Success clears the failure streak for id.
func (b *Backoff) Success(id string) { b.Forget(id) }

// Forget drops all state for id (instance disabled or deleted).
func (b *Backoff) Forget(id string) {
	b.mu.Lock()
	delete(b.state, id)
	b.mu.Unlock()
}
