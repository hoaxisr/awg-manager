package connectivity

import (
	"context"
	"sort"
	"strings"
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
// поднявшихся. Прогон идёт через шаг после последнего подъёма либо когда ждать
// больше некого; если кто-то подъедет позже — он получит СВОЙ прогон, а не
// окажется брошен. Прогонов на пачку выходит один-два вместо N, а не N, как
// было до коалесцирования.
func (m *Monitor) awaitAndRun(pending map[string]bool) {
	if m.matrix == nil {
		return
	}
	// Контекст отменяется по stopCh: прогон матрицы синхронный и учтён в wg,
	// поэтому без отмены Stop() ждал бы его завершения до 30 секунд, а на
	// роутере это шанс получить SIGKILL от init-скрипта посреди работы.
	ctx, cancel := m.stopContext()
	defer cancel()

	if m.handshake == nil {
		m.runMatrix(ctx, keysOf(pending))
		return
	}

	deadline := time.After(handshakeWait)
	poll := time.NewTicker(handshakePoll)
	defer poll.Stop()

	var settled []string // подъехали и ещё не попали в прогон
	for {
		select {
		case <-poll.C:
			m.drainTriggers(pending)

			qctx, qcancel := context.WithTimeout(ctx, 5*time.Second)
			done := m.handshake.Handshaked(qctx)
			qcancel()

			fresh := false
			for id := range pending {
				if done[id] {
					delete(pending, id)
					settled = append(settled, id)
					fresh = true
				}
			}
			if len(settled) > 0 && (len(pending) == 0 || !fresh) {
				m.runMatrix(ctx, settled)
				settled = nil
				if len(pending) == 0 {
					return
				}
			}
		case <-deadline:
			if len(settled) > 0 {
				m.runMatrix(ctx, settled)
			}
			if len(pending) > 0 {
				m.appLog.Debug("await-handshake", strings.Join(keysOf(pending), ","),
					"рукопожатия не дождались (30с)")
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

// stopContext отдаёт контекст, отменяемый вместе с остановкой монитора.
func (m *Monitor) stopContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-m.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out) // стабильный порядок в журнале
	return out
}

func (m *Monitor) runMatrix(parent context.Context, ids []string) {
	m.appLog.Debug("matrix-tick", strings.Join(ids, ","), "рукопожатие получено — прогон матрицы")
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	m.matrix.RunOnce(ctx)
}
