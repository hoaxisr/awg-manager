package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/install"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
)

type fakeInstaller struct {
	stale     []install.Subsystem
	fail      map[string]error
	installed []string
}

func (f *fakeInstaller) Stale([]instancestore.Record) []install.Subsystem { return f.stale }
func (f *fakeInstaller) Install(_ context.Context, name string) error {
	f.installed = append(f.installed, name)
	return f.fail[name]
}

type recProxyJournal struct{ lines []string }

func (j *recProxyJournal) Info(a, t, m string) { j.lines = append(j.lines, "I "+a+" "+t+" "+m) }
func (j *recProxyJournal) Warn(a, t, m string) { j.lines = append(j.lines, "W "+a+" "+t+" "+m) }

func TestProxyEnsureBinaries(t *testing.T) {
	t.Run("нечего качать — nil без вызовов", func(t *testing.T) {
		f := &fakeInstaller{}
		if err := proxyEnsureBinaries(f, &recProxyJournal{})(context.Background(), nil, func(string) {}); err != nil || len(f.installed) != 0 {
			t.Fatalf("err=%v installed=%v", err, f.installed)
		}
	})
	t.Run("успех — обе подсистемы, прогресс до загрузки", func(t *testing.T) {
		f := &fakeInstaller{stale: []install.Subsystem{install.SubsystemWdtt, install.SubsystemFreeTurn}}
		var progress []string
		err := proxyEnsureBinaries(f, &recProxyJournal{})(context.Background(), nil, func(m string) { progress = append(progress, m) })
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(f.installed, ",") != "wdtt,freeturn" {
			t.Fatalf("installed=%v", f.installed)
		}
		if len(progress) != 2 || !strings.Contains(progress[0], "wdtt") || !strings.Contains(progress[0], "загрузка") {
			t.Fatalf("прогресс: %v", progress)
		}
	})
	t.Run("отказ — ErrBinariesPending с причиной, остальные не качаются", func(t *testing.T) {
		f := &fakeInstaller{
			stale: []install.Subsystem{install.SubsystemWdtt, install.SubsystemFreeTurn},
			fail:  map[string]error{"wdtt": errors.New("зеркало недоступно")},
		}
		err := proxyEnsureBinaries(f, &recProxyJournal{})(context.Background(), nil, func(string) {})
		if !errors.Is(err, manager.ErrBinariesPending) {
			t.Fatalf("ждали ErrBinariesPending: %v", err)
		}
		for _, want := range []string{"wdtt", "зеркало недоступно", "прежней версии не тронуты"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("текст без %q: %v", want, err)
			}
		}
		if len(f.installed) != 1 {
			t.Fatalf("после отказа качать дальше незачем: %v", f.installed)
		}
	})
}

// Ревью Б2: цикл вооружается там, где ВИДЕН ErrBinariesPending, один раз на
// процесс; чужие ошибки бута (RCI) цикл не заводят.
func TestArmBinariesRetry(t *testing.T) {
	var once sync.Once
	started := 0
	start := func() { started++ }
	if armBinariesRetry(&once, errors.New("rci down"), start) || started != 0 {
		t.Fatal("отказ посева цикл не вооружает")
	}
	if armBinariesRetry(&once, nil, start) || started != 0 {
		t.Fatal("успех цикл не вооружает")
	}
	pending := fmt.Errorf("%w: wdtt", manager.ErrBinariesPending)
	if !armBinariesRetry(&once, pending, start) || started != 1 {
		t.Fatalf("первый ErrBinariesPending обязан вооружить: %d", started)
	}
	if armBinariesRetry(&once, pending, start) || started != 1 {
		t.Fatalf("повторный ErrBinariesPending — цикл уже идёт: %d", started)
	}
}

// proxyWait — единственное место цикла с настоящими часами: подтесты ниже
// подсовывают фейковый wait, и тело, выродившееся в `return true`, оставило бы
// их зелёными, а в проде превратило бы backoff в busy-loop.
func TestProxyWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if proxyWait(ctx, time.Hour) {
		t.Fatal("отменённый контекст обязан прервать ожидание, не досиживая час")
	}
	if !proxyWait(context.Background(), time.Millisecond) {
		t.Fatal("истёкший интервал — ожидание состоялось")
	}
}

func TestProxyBinariesRetry(t *testing.T) {
	t.Run("backoff по таблице, последний интервал повторяется, стоп по Booted", func(t *testing.T) {
		rt := &fakeProxyRuntime{}
		var waits []time.Duration
		nudges := 0
		wait := func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }
		nudge := func(string) {
			nudges++
			if nudges == 4 {
				rt.booted = true
			}
		}
		proxyBinariesRetry(context.Background(), rt, []time.Duration{time.Second, 2 * time.Second}, wait, nudge)
		want := []time.Duration{time.Second, 2 * time.Second, 2 * time.Second, 2 * time.Second}
		if len(waits) != len(want) {
			t.Fatalf("ожидания %v, ждали %v", waits, want)
		}
		for i := range want {
			if waits[i] != want[i] {
				t.Fatalf("ожидания %v, ждали %v", waits, want)
			}
		}
		if nudges != 4 {
			t.Fatalf("нуджей %d", nudges)
		}
	})
	t.Run("Booted снаружи (WAN-up) — выход без нуджа", func(t *testing.T) {
		rt := &fakeProxyRuntime{booted: true}
		nudges := 0
		proxyBinariesRetry(context.Background(), rt,
			[]time.Duration{time.Second},
			func(context.Context, time.Duration) bool { return true },
			func(string) { nudges++ })
		if nudges != 0 {
			t.Fatalf("рантайм уже поднят, нуджей быть не должно: %d", nudges)
		}
	})
	t.Run("отмена контекста — выход", func(t *testing.T) {
		rt := &fakeProxyRuntime{}
		proxyBinariesRetry(context.Background(), rt,
			[]time.Duration{time.Second},
			func(context.Context, time.Duration) bool { return false },
			func(string) { t.Fatal("после отмены нуджей нет") })
	})
}
