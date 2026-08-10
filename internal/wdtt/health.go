package wdtt

import (
	"sync"
	"time"
)

const (
	// clientNDMSGrace — короче основного grace: OpkgTun down при живом процессе
	// лечится reconcile/restart, ждать 5 мин незачем.
	clientNDMSGrace = 90 * time.Second
	// clientHealthGrace шире, чем у freeturn: детекта капчи в пакете нет, а
	// VK-авторизация wt-client'а с ручной капчей легко занимает минуты — на
	// коротком окне health-check убивал бы её на середине.
	clientHealthGrace   = 5 * time.Minute
	clientHealthStrikes = 4
	// clientStallStrikes — окно для зомби-реле: 20 тиков супервизора ≈ 10 минут
	// подряд без единого входящего байта. Шире, чем «нет сессий»: на коротком
	// окне живой, но простаивающий туннель неотличим от зомби — у обоих
	// bytes_down стоит на месте, а bytes_up растёт от keepalive'ов.
	clientStallStrikes = 20
)

type healthTracker struct {
	mu      sync.Mutex
	need    int
	strikes map[string]int
}

func newHealthTracker(need int) *healthTracker {
	return &healthTracker{need: need, strikes: make(map[string]int)}
}

func (h *healthTracker) note(id string, unhealthy bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !unhealthy {
		delete(h.strikes, id)
		return false
	}
	h.strikes[id]++
	return h.strikes[id] >= h.need
}

func (h *healthTracker) reset(id string) {
	h.mu.Lock()
	delete(h.strikes, id)
	h.mu.Unlock()
}

// clientRawNDMSUnhealthy: raw-клиент на OpkgTun живёт как процесс, но NDMS-
// интерфейс после stop/start awg-manager остался down (teardown без повторной
// активации). Типично после deploy/restart демона.
func clientRawNDMSUnhealthy(cfg ClientConfig, checker InterfaceChecker, st ProcessStatus, now time.Time) bool {
	if !st.Running || st.StartedAt == nil || cfg.UsesWireGuard() || !cfg.usesNDMSOpkgTun() {
		return false
	}
	if now.Sub(*st.StartedAt) < clientNDMSGrace {
		return false
	}
	if checker == nil {
		return false
	}
	iface := cfg.kernelRawIface()
	if !checker.InterfaceExists(iface) {
		return true
	}
	return !checker.InterfaceOperUp(iface)
}

func clientPeerUnhealthy(st ProcessStatus, now time.Time) bool {
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientHealthGrace {
		return false
	}
	// Только явная телеметрия: отсутствие строк статистики в хвосте лога — это
	// «не знаем», а не «активных ноль» (см. activeTelemetry).
	active, known := activeTelemetry(st.Log)
	return known && active == 0
}

// clientRelayStalled — зомби-реле: воркеры числятся активными, наверх уходят
// keepalive'ы, а снизу не приходит ни байта. Так выглядит клиент после
// рестарта сервера: сессия протухла на той стороне, сам он этого не замечает и
// живым рестартом не лечится. Сигнал слабый (тот же профиль у простаивающего
// туннеля), поэтому решение принимается только по clientStallStrikes подряд.
func clientRelayStalled(st ProcessStatus, now time.Time) bool {
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientHealthGrace {
		return false
	}
	return trafficStalled(statsEvents(st.Log))
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
