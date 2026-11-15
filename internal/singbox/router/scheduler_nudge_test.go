package router

import (
	"context"
	"testing"
	"time"
)

// Nudge даёт проход раньше обычного тика, а пачка Nudge'ей — один проход.
func TestScheduler_NudgeRunsEarlyAndCoalesces(t *testing.T) {
	ticks := make(chan struct{}, 16)
	s := &Scheduler{
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		nudge:     make(chan struct{}, 1),
		every:     time.Hour, // обычный тик в тесте не наступает
		nudgeWait: 50 * time.Millisecond,
		tick:      func(context.Context) { ticks <- struct{}{} },
	}
	s.Start()
	defer s.Stop()

	recv := func(what string) {
		t.Helper()
		select {
		case <-ticks:
		case <-time.After(2 * time.Second):
			t.Fatalf("прохода нет: %s", what)
		}
	}
	recv("стартовый")

	s.Nudge()
	time.Sleep(10 * time.Millisecond) // цикл забрал первый, идёт пауза
	s.Nudge()
	s.Nudge()
	recv("после Nudge")

	select {
	case <-ticks:
		t.Fatal("пачка Nudge'ей дала больше одного прохода")
	case <-time.After(200 * time.Millisecond):
	}
}
