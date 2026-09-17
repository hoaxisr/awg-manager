package hydraroute

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

type spyPub struct {
	mu   sync.Mutex
	keys []events.Resource
}

func (p *spyPub) PublishInvalidated(r events.Resource, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, r)
}

func (p *spyPub) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

// Сторож обязан публиковать на СМЕНЕ состояния процесса. Без него смерть
// демона hrneo не замечал никто, и карточка показывала «работает» бессрочно —
// ради этого панель и держала опрос из каждой вкладки (F353/F364).
func TestWatchdog_PublishesOnStateChange(t *testing.T) {
	prev := watchdogIntervalForTest(t, 10*time.Millisecond)
	defer prev()

	s := &Service{appLog: logging.NewScopedLogger(nil, logging.GroupRouting, logging.SubHrNeo)}
	pub := &spyPub{}

	// Подменяем наблюдение: первые тики — «жив», дальше «умер».
	var calls int
	var mu sync.Mutex
	s.probeForTest = func() ProcessState {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls > 3 {
			return StateDead
		}
		return StateRunning
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := s.StartWatchdog(ctx, pub)
	defer func() { stop(); cancel() }()

	deadline := time.After(2 * time.Second)
	for pub.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("сторож не заметил смерть процесса")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pub.keys[0] != events.ResourceRoutingHydrarouteStatus {
		t.Errorf("опубликован ключ %q", pub.keys[0])
	}
}

// Пока состояние не меняется, сторож обязан молчать: иначе он превращается в
// тот же опрос, только на бэкенде.
func TestWatchdog_SilentWhileStateStable(t *testing.T) {
	prev := watchdogIntervalForTest(t, 10*time.Millisecond)
	defer prev()

	s := &Service{appLog: logging.NewScopedLogger(nil, logging.GroupRouting, logging.SubHrNeo)}
	pub := &spyPub{}
	s.probeForTest = func() ProcessState { return StateRunning }

	ctx, cancel := context.WithCancel(context.Background())
	stop := s.StartWatchdog(ctx, pub)
	defer func() { stop(); cancel() }()

	time.Sleep(200 * time.Millisecond)
	if n := pub.count(); n != 0 {
		t.Errorf("сторож опубликовал %d раз при неизменном состоянии", n)
	}
}

// watchdogIntervalForTest ускоряет сторож на время теста и возвращает откат.
func watchdogIntervalForTest(t *testing.T, d time.Duration) func() {
	t.Helper()
	prev := watchdogInterval
	watchdogInterval = d
	return func() { watchdogInterval = prev }
}
