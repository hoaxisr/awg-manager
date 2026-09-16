package connectivity

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

// MatrixRunner triggers a single monitoring-matrix tick. Concrete impl is
// monitoring.Scheduler.RunOnce, but stays decoupled here so connectivity has
// no compile-time dependency on the monitoring package.
type MatrixRunner interface {
	RunOnce(ctx context.Context)
}

// HandshakeChecker reports the set of tunnels that have completed a WireGuard
// handshake.
//
// Пакетный по устройству: одна выборка на раунд опроса, а не выборка на
// туннель. Поштучная форма стоила перечисления ВСЕХ туннелей на каждый
// опрашиваемый, то есть при подъёме N штук — 15N перечислений за 30 секунд.
type HandshakeChecker interface {
	Handshaked(ctx context.Context) map[string]bool
}

// Monitor reacts to "tunnel:state running" events: after the WireGuard
// handshake lands, it asks the monitoring scheduler to run an extra matrix
// tick. The matrix snapshot then drives card latency via the
// monitoring:matrix-update SSE event — no separate per-tunnel probe loop.
type Monitor struct {
	bus       *events.Bus
	matrix    MatrixRunner
	handshake HandshakeChecker
	appLog    *logging.ScopedLogger
	triggerCh chan string
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewMonitor creates a Monitor that pokes the matrix scheduler after
// handshake. Call Start() to begin listening.
func NewMonitor(bus *events.Bus, matrix MatrixRunner, hs HandshakeChecker, appLogger logging.AppLogger) *Monitor {
	return &Monitor{
		bus:       bus,
		matrix:    matrix,
		handshake: hs,
		appLog:    logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubConnectivity),
		triggerCh: make(chan string, 16),
		stopCh:    make(chan struct{}),
	}
}

// Start launches the background event listener.
func (m *Monitor) Start() {
	m.wg.Add(2)
	go m.loop()
	go m.listenStateEvents()
}

// Stop signals all goroutines to stop and waits.
func (m *Monitor) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// listenStateEvents subscribes to the event bus and queues a matrix tick
// when a tunnel transitions to "running".
func (m *Monitor) listenStateEvents() {
	defer m.wg.Done()

	_, ch, unsub := m.bus.Subscribe()
	defer unsub()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "tunnel:state" {
				continue
			}
			stateEv, ok := ev.Data.(events.TunnelStateEvent)
			if !ok {
				continue
			}
			if stateEv.State == "running" {
				select {
				case m.triggerCh <- stateEv.ID:
				default: // channel full, skip
				}
			}
		case <-m.stopCh:
			return
		}
	}
}

const (
	// handshakeWait — сколько всего ждём рукопожатий подъёмной пачки.
	handshakeWait = 30 * time.Second
	// handshakePoll — шаг опроса. Один поход за состоянием на шаг, сколько бы
	// туннелей ни ждали.
	handshakePoll = 2 * time.Second
)

func (m *Monitor) loop() {
	defer m.wg.Done()
	for {
		select {
		case tunnelID := <-m.triggerCh:
			pending := map[string]bool{tunnelID: true}
			m.drainTriggers(pending)
			m.awaitAndRun(pending)
		case <-m.stopCh:
			return
		}
	}
}

// drainTriggers забирает всё, что уже лежит в канале, в текущую пачку.
// Туннели поднимаются пачками (ребут, рестарт панели, применение настроек),
// и обрабатывать их порознь незачем — прогон матрицы всё равно общий.
func (m *Monitor) drainTriggers(pending map[string]bool) {
	for {
		select {
		case id := <-m.triggerCh:
			pending[id] = true
		default:
			return
		}
	}
}

// awaitAndRun ждёт рукопожатий пачки и делает РОВНО ОДИН прогон матрицы.
//
// Прежде на каждое событие поднималась своя горутина, и каждая звала полный
// прогон по ВСЕМ туннелям: подъём N штук давал N² зондов, а для метода "http"
// каждый зонд — TLS-рукопожатие ценой ~190 мс CPU на softfloat MIPS.
//
// Ждём не всю пачку целиком: застрявший туннель не должен задерживать показ
// поднявшихся. Прогон идёт, когда ждать больше некого либо когда очередной
// шаг не принёс никого нового, — то есть через шаг после последнего подъёма.
func (m *Monitor) awaitAndRun(pending map[string]bool) {
	if m.matrix == nil {
		return
	}
	if m.handshake == nil {
		m.runMatrix(len(pending))
		return
	}

	deadline := time.After(handshakeWait)
	poll := time.NewTicker(handshakePoll)
	defer poll.Stop()

	ready := 0
	for {
		select {
		case <-poll.C:
			m.drainTriggers(pending)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			done := m.handshake.Handshaked(ctx)
			cancel()

			fresh := false
			for id := range pending {
				if done[id] {
					delete(pending, id)
					ready++
					fresh = true
				}
			}
			if ready > 0 && (len(pending) == 0 || !fresh) {
				m.runMatrix(ready)
				return
			}
		case <-deadline:
			if ready > 0 {
				m.runMatrix(ready)
				return
			}
			m.appLog.Debug("await-handshake", "", "рукопожатий не дождались (30с) — прогон матрицы пропущен")
			return
		case <-m.stopCh:
			return
		}
	}
}

func (m *Monitor) runMatrix(ready int) {
	m.appLog.Debug("matrix-tick", "", fmt.Sprintf("рукопожатий получено: %d — прогон матрицы", ready))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m.matrix.RunOnce(ctx)
}
