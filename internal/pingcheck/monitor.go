package pingcheck

import (
	"fmt"
	"net"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// runCmd — шов над exec.Run для команд лечения (`ip link set`, `awg set`); в
// тестах подменяется рекордером, в проде — exec.Run.
var runCmd = exec.Run

// resolveEndpoint — шов над DNS-резолвом endpoint'а перед лечением; тесты
// подменяют, прод зовёт tryResolveEndpoint.
var resolveEndpoint = tryResolveEndpoint

// runMonitorLoop runs the simple health sensor loop for a kernel tunnel.
func (s *Service) runMonitorLoop(m *tunnelMonitor) {
	defer m.wg.Done()

	// Канал снимаем под локом один раз: StopMonitoring закрывает его и
	// обнуляет поле под s.mu. Чтение поля в select после остановки посреди
	// тика давало nil — цикл ждал вечно, а wg.Wait останавливающего висел до
	// отмены s.ctx (F628).
	s.mu.RLock()
	stopCh := m.stopCh
	s.mu.RUnlock()
	if stopCh == nil {
		return
	}

	config := s.getCheckConfig(m.tunnelID)
	if config == nil {
		return
	}

	m.failThreshold = config.FailThreshold

	// Run the first check immediately after monitor start.
	// This avoids waiting up to one full interval after enabling monitoring.
	s.lockedTick(m, config)

	interval := time.Duration(config.Interval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			config = s.getCheckConfig(m.tunnelID)
			if config == nil {
				return
			}
			m.failThreshold = config.FailThreshold
			s.lockedTick(m, config)

		case <-stopCh:
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// lockedTick — sensorTick под tickMu монитора (цикл монитора; check-now
// берёт тот же замок через TryLock).
func (s *Service) lockedTick(m *tunnelMonitor, config *checkConfig) {
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	s.sensorTick(m, config)
}

// sensorTick performs one check cycle.
func (s *Service) sensorTick(m *tunnelMonitor, config *checkConfig) {
	ifaceName := s.resolveIfaceName(m.tunnelID)
	// Опущенный линк не проверяем: чаще всего это Suspend на падении WAN, и
	// политику NDMS с такого интерфейса уводит сама (стенд 27.10, F743).
	// Проверка здесь только набирала порог и снимала permit — с записью во
	// флеш на каждый туннель при каждом флапе WAN (F750).
	if !s.linkUp(m, ifaceName) {
		return
	}
	result := performCheck(s.ctx, ifaceName, config.Method, config.Target, config.CheckURL, config.DNSServers)
	if s.ctx.Err() != nil {
		return
	}
	// Проба идёт до 7 с: линк могли опустить, пока она летела, и её провал —
	// след опускания, а не отказ туннеля (ревью F750).
	if !s.linkUp(m, ifaceName) {
		return
	}

	now := time.Now()
	s.mu.Lock()
	m.lastCheck = now
	m.lastResult = &result
	s.mu.Unlock()

	if result.Success {
		s.mu.Lock()
		m.failCount = 0
		recovered := m.restartCount > 0
		m.restartCount = 0
		s.mu.Unlock()

		// Восстановление — только успешная проверка (F743, F747).
		s.restorePolicies(m.tunnelID)
		stateChange := ""
		if recovered {
			stateChange = "recovered"
			s.logInfo(m.tunnelID, "Connectivity restored")
		}

		// Публикуем на каждом успехе, как NWG-монитор: dnsroute-фейловер
		// снимает туннель с failedSet только по «pass» (#855).
		if s.bus != nil {
			s.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
				TunnelID: m.tunnelID,
				Status:   "pass",
			})
			// И подсказку инвалидации — тоже на КАЖДОЙ проверке. Прежний
			// комментарий «состояние карточки не менялось» был неверен:
			// страница мониторинга показывает время последней проверки,
			// задержку и счётчики, а они меняются каждый раз. Из-за этого
			// странице пришлось вернуть таймер опроса (F354); теперь он не
			// нужен — обновление приходит ровно тогда, когда есть что
			// показать (F364).
			s.bus.PublishInvalidated(events.ResourcePingcheck, "check")
		}

		s.addLogEntry(LogEntry{
			Timestamp:   now,
			TunnelID:    m.tunnelID,
			TunnelName:  m.tunnelName,
			Success:     true,
			Latency:     result.Latency,
			FailCount:   0,
			Threshold:   config.FailThreshold,
			StateChange: stateChange,
			Backend:     "kernel",
		})
		return
	}

	s.mu.Lock()
	m.failCount++
	failCount := m.failCount
	s.mu.Unlock()

	s.addLogEntry(LogEntry{
		Timestamp:  now,
		TunnelID:   m.tunnelID,
		TunnelName: m.tunnelName,
		Success:    false,
		Latency:    result.Latency,
		Error:      result.Error,
		FailCount:  failCount,
		Threshold:  config.FailThreshold,
		Backend:    "kernel",
	})

	// Неудачная проверка меняет ровно те же поля карточки, что и удачная:
	// время последней проверки, задержку и счётчик отказов «2 из 3».
	// Публикуем и здесь — иначе странице мониторинга снова понадобился бы
	// таймер (F364).
	if s.bus != nil {
		s.bus.PublishInvalidated(events.ResourcePingcheck, "check")
	}

	if failCount < config.FailThreshold {
		return
	}

	s.doRepair(m, config, ifaceName)
}

// linkUp — поднят ли линк туннеля. Пока он опущен, проверок нет; переход в
// обе стороны пишется в журнал один раз, иначе монитор молчал бы без следа
// (ручной down, пропавшее устройство). Вызывается под m.tickMu.
func (s *Service) linkUp(m *tunnelMonitor, ifaceName string) bool {
	up := ifaceUp(ifaceName)
	if up != m.linkDown {
		return up
	}
	m.linkDown = !up
	if up {
		s.logInfo(m.tunnelID, "Link is up, checks resumed")
	} else {
		s.logInfo(m.tunnelID, "Link is down, checks paused until it is up")
	}
	return up
}

// doRepair — лечение на пороге: увести политику с туннеля и переприменить
// переехавший endpoint. Интерфейс не кладём: быстрый `ip link down/up` в
// обход NDMS оставлял таблицу политики без маршрута по умолчанию (F746), а
// повтор рукопожатия WireGuard делает сам — `awg set endpoint` на поднятом
// интерфейсе даёт рукопожатие за ~6 с (стенд 27.10). Восстановлением
// считается только успешная проверка, не рукопожатие: оно проходит и тогда,
// когда трафик через туннель не идёт (F747).
func (s *Service) doRepair(m *tunnelMonitor, config *checkConfig, ifaceName string) {
	stored, _ := s.tunnels.Get(m.tunnelID)
	// Зеркальная запись прокси-выхода: её интерфейсом владеет прокси-рантайм
	// со своим циклом реконсиляции. Измеряем, но не лечим.
	proxyOwned := stored != nil && stored.Backend == "wdtt-raw"

	msg := fmt.Sprintf("Connectivity lost (%d/%d fails)", m.failCount, config.FailThreshold)
	if proxyOwned {
		msg += "; интерфейс принадлежит прокси-рантайму — восстановление за ним"
	}
	s.logInfo(m.tunnelID, msg)

	if s.bus != nil {
		s.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
			TunnelID:  m.tunnelID,
			Status:    "fail",
			FailCount: m.failCount,
		})
	}
	s.bus.PublishInvalidated(events.ResourcePingcheck, "state-change")

	// Дальше идёт лечение, и оно не наше: `awg set` по не-wg устройству
	// бессмысленен, а политика чужого tun — забота его рантайма. Публикация
	// выше сохранена: измерение остаётся.
	if proxyOwned {
		return
	}

	s.releasePolicies(m.tunnelID)

	// Только по живому устройству: на модуле старше 3.0.20260731-04 `awg set`
	// по отсутствующему интерфейсу роняет ядро (prebuilt/kmod/README.md).
	if stored != nil && deviceExists(ifaceName) {
		if ep := resolveEndpoint(stored.Peer.Endpoint); ep != "" {
			if _, err := runCmd(s.ctx, "/opt/sbin/awg", "set", ifaceName,
				"peer", stored.Peer.PublicKey, "endpoint", ep); err != nil {
				s.logWarn(m.tunnelID, "awg set endpoint failed: "+err.Error())
			}
		}
	}

	s.mu.Lock()
	m.restartCount++
	m.failCount = 0
	s.mu.Unlock()

	s.addLogEntry(LogEntry{
		Timestamp:  time.Now(),
		TunnelID:   m.tunnelID,
		TunnelName: m.tunnelName,
		FailCount:  0,
		Threshold:  config.FailThreshold,
		// Имя перехода прежнее — его читают MCP и журнал; теперь это попытка
		// лечения без down/up.
		StateChange: "link_toggle",
		Backend:     "kernel",
	})
}

// tryResolveEndpoint resolves a hostname endpoint to IP:port.
// Returns "" if endpoint is already an IP or resolution fails.
func tryResolveEndpoint(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return ""
	}
	if net.ParseIP(host) != nil {
		return "" // already an IP
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return net.JoinHostPort(ips[0], port)
}
