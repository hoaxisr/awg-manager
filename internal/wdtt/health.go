package wdtt

import (
	"sync"
	"time"
)

const (
	// clientHealthGrace шире, чем у freeturn: детекта капчи в пакете нет, а
	// VK-авторизация wt-client'а с ручной капчей легко занимает минуты — на
	// коротком окне health-check убивал бы её на середине.
	clientHealthGrace   = 5 * time.Minute
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

func clientPeerUnhealthy(st ProcessStatus, now time.Time) bool {
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientHealthGrace {
		return false
	}
	active, known := activeTelemetry(st.Log)
	if !known {
		return false
	}
	if active == 0 {
		return true
	}
	return ClientTrafficStalled(st.Log)
}

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
