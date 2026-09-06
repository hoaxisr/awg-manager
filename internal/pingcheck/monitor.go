package pingcheck

import (
	"fmt"
	"net"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	handshakeTimeout = 30 * time.Second
	maxBackoff       = 30 * time.Minute
)

// handshakePollFreq — период опроса wg в waitHandshake; тесты ужимают.
var handshakePollFreq = 2 * time.Second

// runCmd — шов над exec.Run для команд лечения (`ip link set`, `awg set`); в
// тестах подменяется рекордером, в проде — exec.Run.
var runCmd = exec.Run

// resolveEndpoint — шов над DNS-резолвом endpoint'а перед лечением; тесты
// подменяют, прод зовёт tryResolveEndpoint.
var resolveEndpoint = tryResolveEndpoint

// runMonitorLoop runs the simple health sensor loop for a kernel tunnel.
func (s *Service) runMonitorLoop(m *tunnelMonitor) {
	defer m.wg.Done()

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

		case <-m.stopCh:
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
	result := performCheck(s.ctx, ifaceName, config.Method, config.Target, config.CheckURL, config.DNSServers)
	if s.ctx.Err() != nil {
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
		m.restartCount = 0
		s.mu.Unlock()

		// Публикуем на каждом успехе, как NWG-монитор: dnsroute-фейловер
		// снимает туннель с failedSet только по «pass», а toggle отдаёт «pass»
		// лишь при рукопожатии в своём окне. Поднялся позже — иначе списки
		// висят на backup до рестарта демона (#855). Подсказку инвалидации
		// не шлём: состояние карточки не менялось.
		if s.bus != nil {
			s.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
				TunnelID: m.tunnelID,
				Status:   "pass",
			})
		}

		s.addLogEntry(LogEntry{
			Timestamp:  now,
			TunnelID:   m.tunnelID,
			TunnelName: m.tunnelName,
			Success:    true,
			Latency:    result.Latency,
			FailCount:  0,
			Threshold:  config.FailThreshold,
			Backend:    "kernel",
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

	if failCount < config.FailThreshold {
		return
	}

	s.doLinkToggle(m, config, ifaceName)
}

// doLinkToggle performs link down → re-resolve → link up → wait handshake → backoff.
func (s *Service) doLinkToggle(m *tunnelMonitor, config *checkConfig, ifaceName string) {
	// Стор читается здесь и так (шаг 1) — гард лишнего похода не добавляет.
	stored, _ := s.tunnels.Get(m.tunnelID)
	// Зеркальная запись прокси-выхода: её интерфейсом владеет прокси-рантайм
	// со своим циклом реконсиляции. Измеряем, но не лечим.
	proxyOwned := stored != nil && stored.Backend == "wdtt-raw"

	msg := fmt.Sprintf("Connectivity lost (%d/%d fails), toggling link",
		m.failCount, config.FailThreshold)
	if proxyOwned {
		msg = fmt.Sprintf("Connectivity lost (%d/%d fails); интерфейс принадлежит прокси-рантайму — восстановление за ним, link toggle пропущен",
			m.failCount, config.FailThreshold)
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

	// Дальше идёт лечение, и оно не наше: `ip link set down` уронил бы живой
	// tun чужого инстанса, `awg set` по не-wg устройству бессмысленен, а
	// рукопожатия на tun не будет никогда — цикл ушёл бы в вечный backoff по
	// чужому ресурсу. Публикация выше сохранена: измерение остаётся.
	if proxyOwned {
		return
	}

	// 1. Re-resolve DNS endpoint before link down (while DNS may still work)
	var newEndpoint string
	if stored != nil {
		newEndpoint = resolveEndpoint(stored.Peer.Endpoint)
	}

	// Канал остановки снимаем под локом ДО лечения: StopMonitoring закрывает
	// его и обнуляет поле под s.mu, а чтение поля из середины лечения могло
	// застать nil — и ожидание рукопожатия с backoff шли бы до конца.
	s.mu.RLock()
	stopCh := m.stopCh
	s.mu.RUnlock()
	if stopCh == nil {
		return
	}

	// Штамп рукопожатия ДО down: ядро (wireguard и amneziawg, стенд
	// 2026-09-06) сохраняет latest-handshake через down/up, а ключи сессии
	// сбрасывает — трафик пойдёт только после НОВОГО рукопожатия. «Штамп
	// моложе N минут» после up отдавал ложное «восстановлено».
	baseline := s.lastHandshake(ifaceName)

	// 2. Link down — NDMS switches to fallback immediately
	//    conf: running preserved (user intent intact), link: pending
	linkDown := true
	if _, err := runCmd(s.ctx, "/opt/sbin/ip", "link", "set", ifaceName, "down"); err != nil {
		s.logWarn(m.tunnelID, "ip link set down failed: "+err.Error())
		linkDown = false
	}

	// 3. Re-apply endpoint if resolved to new IP.
	//    Only when the link went down: on a module older than 3.0.20260731-04
	//    `awg set` panics the kernel when the interface is gone, and this path
	//    runs exactly when connectivity is lost (prebuilt/kmod/README.md).
	if linkDown && newEndpoint != "" && stored != nil {
		runCmd(s.ctx, "/opt/sbin/awg", "set", ifaceName,
			"peer", stored.Peer.PublicKey,
			"endpoint", newEndpoint)
	}

	// 4. Link up — WireGuard re-initiates handshake
	if _, err := runCmd(s.ctx, "/opt/sbin/ip", "link", "set", ifaceName, "up"); err != nil {
		s.logWarn(m.tunnelID, "ip link set up failed: "+err.Error())
	}

	// 5. Wait for handshake (interruptible by monitor stop signal)
	ok := s.waitHandshake(ifaceName, baseline, stopCh)

	s.mu.Lock()
	m.restartCount++
	m.failCount = 0
	if ok {
		// Иначе статус «recovering» висел бы до следующего тика.
		m.lastResult = &CheckResult{Success: true}
	}
	restartCount := m.restartCount
	s.mu.Unlock()

	stateChange := "link_toggle"
	if ok {
		stateChange = "recovered"
		s.logInfo(m.tunnelID, "Link toggle successful, handshake restored")

		if s.bus != nil {
			s.bus.Publish("pingcheck:state", events.PingCheckStateEvent{
				TunnelID:  m.tunnelID,
				Status:    "pass",
				FailCount: 0,
			})
		}
		s.bus.PublishInvalidated(events.ResourcePingcheck, "state-change")
	} else {
		s.logWarn(m.tunnelID, fmt.Sprintf("Link toggle: no handshake, backoff #%d", restartCount))
	}

	s.addLogEntry(LogEntry{
		Timestamp:   time.Now(),
		TunnelID:    m.tunnelID,
		TunnelName:  m.tunnelName,
		Success:     ok,
		FailCount:   0,
		Threshold:   config.FailThreshold,
		StateChange: stateChange,
		Backend:     "kernel",
	})

	// 6. Backoff if handshake didn't restore
	if !ok {
		backoff := time.Duration(config.Interval) * time.Second * time.Duration(restartCount*restartCount)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		s.logInfo(m.tunnelID, fmt.Sprintf("Backoff %v before next cycle", backoff))
		select {
		case <-time.After(backoff):
		case <-stopCh:
		case <-s.ctx.Done():
		}
	}
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

// lastHandshake отдаёт точный штамп рукопожатия интерфейса (epoch-секунды
// ядра). Если штамп прочитать не удалось, отдаёт текущую секунду: тогда
// восстановлением считается только рукопожатие новее «сейчас» — ложный минус
// безвреден (следующий успешный тик всё равно публикует pass), ложный плюс
// по старому штампу — нет.
func (s *Service) lastHandshake(ifaceName string) time.Time {
	if s.wg != nil {
		if hs, err := s.wg.LatestHandshake(s.ctx, ifaceName); err == nil {
			return hs
		}
	}
	return time.Now().Truncate(time.Second)
}

// waitHandshake polls awg show after link toggle for a handshake NEWER than
// baseline (the stamp taken before link down). stopCh allows early exit when
// StopMonitoring is called during link toggle, preventing the HTTP handler
// from blocking for up to 30 seconds.
func (s *Service) waitHandshake(ifaceName string, baseline time.Time, stopCh <-chan struct{}) bool {
	timeout := s.handshakeTimeout
	if timeout <= 0 {
		timeout = handshakeTimeout
	}
	deadline := time.After(timeout)
	poll := time.NewTicker(handshakePollFreq)
	defer poll.Stop()

	for {
		select {
		case <-poll.C:
			if s.wg == nil {
				continue
			}
			hs, err := s.wg.LatestHandshake(s.ctx, ifaceName)
			if err != nil {
				continue
			}
			if !hs.IsZero() && hs.After(baseline) {
				return true
			}
		case <-deadline:
			return false
		case <-stopCh:
			return false
		case <-s.ctx.Done():
			return false
		}
	}
}
