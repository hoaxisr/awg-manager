package freeturn

import (
	"context"
	"sync"
	"time"
)

const (
	// clientHealthGrace — не проверяем peer сразу после старта: VK-auth и
	// первые DTLS-handshake могут занимать несколько минут.
	clientHealthGrace = 3 * time.Minute
	// clientHealthStrikes — сколько подряд тиков supervisor (30 с) с нулём
	// активных DTLS-сессий нужно, прежде чем перезапустить клиент.
	clientHealthStrikes = 4
)

type healthTracker struct {
	mu      sync.Mutex
	strikes map[string]int
}

func newHealthTracker() *healthTracker {
	return &healthTracker{strikes: make(map[string]int)}
}

func (h *healthTracker) note(id string, unhealthy bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !unhealthy {
		delete(h.strikes, id)
		return false
	}
	h.strikes[id]++
	return h.strikes[id] >= clientHealthStrikes
}

func (h *healthTracker) reset(id string) {
	h.mu.Lock()
	delete(h.strikes, id)
	h.mu.Unlock()
}

// clientPeerUnhealthy reports whether a running client looks stuck with no
// working peer connection (process alive, but zero active DTLS sessions).
func clientPeerUnhealthy(st ProcessStatus, now time.Time) bool {
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientHealthGrace {
		return false
	}
	if logIndicatesCaptchaWaiting(st.Log) {
		return false
	}
	if logRecentHandshakeFailures(st.Log, handshakeFailWindowLines, handshakeFailMinCount) {
		return true
	}
	if clientPeerStaleZombie(st, now) {
		return true
	}
	// Только явная телеметрия: пустой лог-хвост (маркеры стримов вытеснены из
	// ринга) неотличим от «сессий нет», а рестарт живого клиента рвёт трафик.
	active, known := dtlsTelemetry(st.Log)
	return known && active == 0
}

// serverPeerUnhealthy reports a running server stuck retrying handshakes
// (backend WG down, obf mismatch, or client side gone stale after server restart).
func serverPeerUnhealthy(st ProcessStatus, now time.Time) bool {
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientHealthGrace {
		return false
	}
	if logRecentHandshakeFailures(st.Log, handshakeFailWindowLines, handshakeFailMinCount) {
		return true
	}
	if now.Sub(*st.StartedAt) >= logStaleMinUptime && logTailStale(st.Log, now, logStaleMaxAge) {
		return true
	}
	return false
}

// restartClientInstance stops and starts the client without clearing Enabled
// (unlike StopClientInstance, which is a user-initiated stop).
func (s *Service) restartClientInstance(id string) error {
	if _, err := s.clientInstance(id); err != nil {
		return err
	}
	if err := s.clientProcs.get(id).Stop(); err != nil {
		return err
	}
	// Stop блокирующий (до ~3 с): пользователь мог за это время нажать «стоп»,
	// и его решение важнее нашего health-рестарта.
	if inst, err := s.clientInstance(id); err == nil && !inst.Config.Enabled {
		return nil
	}
	return s.StartClientInstance(id)
}

func (s *Service) restartServerInstance(id string) error {
	if _, err := s.serverInstance(id); err != nil {
		return err
	}
	inst, _ := s.serverInstance(id)
	removeServerListenFirewall(context.Background(), inst.Config)
	if err := s.serverProcs.get(id).Stop(); err != nil {
		return err
	}
	if inst, err := s.serverInstance(id); err == nil && !inst.Config.Enabled {
		return nil
	}
	return s.StartServerInstance(id)
}
