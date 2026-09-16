package traffic

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// Publisher receives SSE events. Implemented by *events.Bus in production.
type Publisher interface {
	Publish(eventType string, data any)
}

// HistoryFeeder is the minimal traffic-history surface used by the poller.
// *History satisfies this.
type HistoryFeeder interface {
	Feed(tunnelID string, rxBytes, txBytes int64)
}

// PollerLogger is the narrow logger used for structural errors
// (malformed counter values, permissions). Missing-interface errors
// are expected and silent.
type PollerLogger interface {
	Warnf(format string, args ...any)
}

// SysfsPoller reads /sys/class/net/<iface>/statistics/{rx,tx}_bytes
// for every running managed tunnel every interval, feeds rate points
// into History, and publishes a "tunnel:traffic" SSE event on every
// successful read.
//
// Alignment note (MIPS): no 64-bit atomic fields; ordering constraints
// therefore do not apply.
type SysfsPoller struct {
	lister   TunnelLister
	history  HistoryFeeder
	pub      Publisher
	log      PollerLogger
	appLog   *logging.ScopedLogger
	root     string
	interval time.Duration

	stopCh    chan struct{}
	doneCh    chan struct{}
	started   atomic.Bool
	stopOnce  sync.Once
	startOnce sync.Once

	// idleInterval — шаг, когда панель не открыта ни у кого.
	idleInterval time.Duration

	mu      sync.RWMutex
	clients ClientCounter
}

// ClientCounter reports the number of open panels (SSE client subscriptions).
type ClientCounter interface {
	ClientCount() int
}

// NewSysfsPoller wires the production poller. 10 s interval, standard sysfs root.
func NewSysfsPoller(lister TunnelLister, history HistoryFeeder, pub Publisher, log PollerLogger, appLogger logging.AppLogger) *SysfsPoller {
	return newSysfsPoller(lister, history, pub, log, appLogger, DefaultSysfsRoot, 10*time.Second)
}

// newSysfsPoller is the test-facing constructor; exposes root and interval.
func newSysfsPoller(lister TunnelLister, history HistoryFeeder, pub Publisher, log PollerLogger, appLogger logging.AppLogger, root string, interval time.Duration) *SysfsPoller {
	if log == nil {
		log = nopPollerLogger{}
	}
	return &SysfsPoller{
		lister:   lister,
		history:  history,
		pub:      pub,
		log:      log,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubTraffic),
		root:     root,
		interval: interval,
		// Шесть шагов простоя на точку графика: история агрегирует час
		// в 60 точек, то есть точку в минуту.
		idleInterval: 6 * interval,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start launches the ticker goroutine. Safe to call multiple times.
func (p *SysfsPoller) Start() {
	p.startOnce.Do(func() {
		p.started.Store(true)
		go p.run()
	})
}

// Stop halts the ticker and waits for the goroutine to exit. Safe to call multiple times.
func (p *SysfsPoller) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	if p.started.Load() {
		<-p.doneCh
	}
}

// SetClientCounter wires the source of "сколько панелей сейчас открыто".
// Optional: без него поллер работает в прежнем темпе всегда.
func (p *SysfsPoller) SetClientCounter(c ClientCounter) {
	p.mu.Lock()
	p.clients = c
	p.mu.Unlock()
}

// nobodyWatching — правда ли, что панель не открыта ни у кого. Считаются только
// клиентские подписки: внутренние живут всё время работы процесса.
func (p *SysfsPoller) nobodyWatching() bool {
	p.mu.RLock()
	c := p.clients
	p.mu.RUnlock()
	return c != nil && c.ClientCount() == 0
}

func (p *SysfsPoller) run() {
	defer close(p.doneCh)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	// Fire once immediately so callers don't wait the full interval on startup.
	p.tick()
	lastRun := time.Now()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			// Тик стоит дорого: RunningTunnels перечисляет туннели с резолвом
			// состояния, а оно читается мимо кэша (кэш 2 с при шаге 10 с —
			// промах всегда), то есть по RCI-запросу на туннель. При закрытой
			// панели публикация уходит в никуда, и единственный уцелевший
			// потребитель — часовая история трафика. Её разрешение — точка в
			// минуту, поэтому в простое держим шаг idleInterval: график
			// остаётся верным, а обращений к роутеру вшестеро меньше.
			if p.nobodyWatching() && time.Since(lastRun) < p.idleInterval {
				continue
			}
			p.tick()
			lastRun = time.Now()
		}
	}
}

func (p *SysfsPoller) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), p.interval)
	defer cancel()

	items := p.lister.RunningTunnels(ctx)
	if len(items) == 0 {
		return
	}

	for _, rt := range items {
		if rt.IfaceName == "" {
			continue
		}
		rx, tx, err := readSysfsCounters(p.root, rt.IfaceName)
		if err != nil {
			// Iface may legitimately disappear during start/stop.
			// Only log non-existence-neutral errors (malformed values, perm).
			if !os.IsNotExist(err) {
				p.log.Warnf("sysfs %s (%s): %v", rt.ID, rt.IfaceName, err)
				p.appLog.Warn("read-counters", rt.ID, fmt.Sprintf("sysfs %s: %v", rt.IfaceName, err))
			}
			continue
		}
		p.history.Feed(rt.ID, rx, tx)
		p.pub.Publish("tunnel:traffic", map[string]any{
			"id":      rt.ID,
			"rxBytes": rx,
			"txBytes": tx,
		})
	}
}

type nopPollerLogger struct{}

func (nopPollerLogger) Warnf(string, ...any) {}
