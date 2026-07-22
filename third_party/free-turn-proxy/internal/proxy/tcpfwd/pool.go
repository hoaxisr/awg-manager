// Package tcpfwd реализует VLESS-режим: пересылка TCP через пул TURN-туннелированных
// smux-сессий. Каждое принятое TCP-соединение открывается как smux-поток
// (round-robin по сессиям) или, с bond, распределяется по всем активным сессиям.
//
// SessionPool/PooledSession экспортированы, чтобы bond-клиент (internal/proxy/bondclient)
// мог распределять одно TCP-соединение по нескольким сессиям.
package tcpfwd

import (
	"sync"
	"sync/atomic"

	"github.com/xtaci/smux"
)

// PooledSession - одна TURN+DTLS+KCP+smux сессия в пуле с lifetime-счётчиками.
// Поля экспортированы для учёта per-lane трафика в bondclient;
// атомики изменять только через их методы.
type PooledSession struct {
	ID          int
	Sess        *smux.Session
	Active      atomic.Int32
	Opened      atomic.Uint64
	Closed      atomic.Uint64
	ToSession   atomic.Uint64
	FromSession atomic.Uint64
}

// SessionPool - конкурентно-безопасный round-robin пул активных smux-сессий.
type SessionPool struct {
	mu          sync.RWMutex
	sessions    []*PooledSession
	counter     atomic.Uint64
	connCounter atomic.Uint64

	readyOnce sync.Once
	ready     chan struct{}
}

// Ready возвращает канал, закрытый при первом появлении сессии в пуле.
func (p *SessionPool) Ready() <-chan struct{} {
	p.mu.Lock()
	if p.ready == nil {
		p.ready = make(chan struct{})
	}
	ch := p.ready
	p.mu.Unlock()
	return ch
}

// Add регистрирует только что подключённую сессию в пуле.
func (p *SessionPool) Add(id int, s *smux.Session) *PooledSession {
	ps := &PooledSession{ID: id, Sess: s}
	p.mu.Lock()
	p.sessions = append(p.sessions, ps)
	if p.ready == nil {
		p.ready = make(chan struct{})
	}
	ready := p.ready
	p.mu.Unlock()
	p.readyOnce.Do(func() { close(ready) })
	return ps
}

// Remove удаляет ps из пула. No-op если не найден.
func (p *SessionPool) Remove(ps *PooledSession) {
	p.mu.Lock()
	for i, sess := range p.sessions {
		if sess == ps {
			p.sessions = append(p.sessions[:i], p.sessions[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
}

// Pick возвращает следующую сессию в round-robin порядке или nil если пул пуст.
func (p *SessionPool) Pick() *PooledSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.sessions)
	if n == 0 {
		return nil
	}
	idx := (p.counter.Add(1) - 1) % uint64(n)
	return p.sessions[idx]
}

// NextConnID возвращает монотонно возрастающий идентификатор соединения.
func (p *SessionPool) NextConnID() uint64 {
	return p.connCounter.Add(1)
}

// Snapshot возвращает копию всех активных (незакрытых) сессий.
func (p *SessionPool) Snapshot() []*PooledSession {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*PooledSession, 0, len(p.sessions))
	for _, ps := range p.sessions {
		if !ps.Sess.IsClosed() {
			out = append(out, ps)
		}
	}
	return out
}

// Count возвращает число сессий в пуле (включая только что закрытые;
// используй Snapshot для live-only).
func (p *SessionPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.sessions)
}
