//go:build linux

package events

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// busRecorder — что клиент отдал наружу, в порядке вызовов.
type busRecorder struct {
	mu     sync.Mutex
	events []BusEvent
	states []bool
}

func (r *busRecorder) onEvent(ev BusEvent) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *busRecorder) onState(up bool) {
	r.mu.Lock()
	r.states = append(r.states, up)
	r.mu.Unlock()
}

func (r *busRecorder) snapshot() ([]BusEvent, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]BusEvent(nil), r.events...), append([]bool(nil), r.states...)
}

func (r *busRecorder) waitEvents(t *testing.T, n int) []BusEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ev, _ := r.snapshot(); len(ev) >= n {
			return ev
		}
		time.Sleep(time.Millisecond)
	}
	ev, st := r.snapshot()
	t.Fatalf("событий %d, ждали %d (состояния %v)", len(ev), n, st)
	return nil
}

// busSocketPath — короткий каталог: путь unix-сокета ограничен 108 байтами,
// а t.TempDir() с именем теста его превышает.
func busSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// serveBus — фейковый ndm: i-му подключению пишет conns[i]; все, кроме
// последнего, сразу рвёт, последнее держит до конца теста. Уборка дожидается
// горутины (goleak пакета).
func serveBus(t *testing.T, path string, conns ...[]byte) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i, data := range conns {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write(data)
			if i < len(conns)-1 {
				c.Close()
				continue
			}
			<-stop
			c.Close()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		ln.Close()
		<-done
	})
}

func busDocs(raises ...int) []byte {
	var b bytes.Buffer
	for _, r := range raises {
		busSavedDoc(&b, fmt.Sprint(r))
	}
	return b.Bytes()
}

func newTestBusReader(path string, rec *busRecorder) *BusReader {
	r := NewBusReader(path, rec.onEvent, rec.onState, nil)
	r.backoffMin, r.backoffMax = time.Millisecond, time.Millisecond
	return r
}

// Документы потока — по одному, в порядке потока, из одной горутины;
// onState(false) — синхронно в Start, onState(true) — на подключении.
// Мутация: `go r.onEvent(ev)` → порядок 200 событий нарушен, красный.
func TestNDMBus_FakeSocket_DeliversInOrder(t *testing.T) {
	path := busSocketPath(t)
	const n = 200
	raises := make([]int, n)
	for i := range raises {
		raises[i] = i + 1
	}
	serveBus(t, path, busDocs(raises...))
	rec := &busRecorder{}
	r := newTestBusReader(path, rec)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	evs := rec.waitEvents(t, n)
	for i, ev := range evs {
		if ev.RaiseTime != float64(i+1) {
			t.Fatalf("событие %d: raise %v — порядок потока нарушен", i, ev.RaiseTime)
		}
	}
	if _, st := rec.snapshot(); len(st) != 1 || !st[0] {
		t.Fatalf("состояния %v, ждали [true]", st)
	}
}

// Обрыв — onState(false), переподключение — onState(true) и новые события.
// Мутация: убрать onState(false) после обрыва → красный.
func TestNDMBus_Reconnect_NotifiesAndResumes(t *testing.T) {
	path := busSocketPath(t)
	serveBus(t, path, busDocs(1), busDocs(2))
	rec := &busRecorder{}
	r := newTestBusReader(path, rec)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	evs := rec.waitEvents(t, 2)
	if evs[0].RaiseTime != 1 || evs[1].RaiseTime != 2 {
		t.Fatalf("события %+v", evs)
	}
	want := []bool{true, false, true}
	if _, st := rec.snapshot(); fmt.Sprint(st) != fmt.Sprint(want) {
		t.Fatalf("состояния %v, ждали %v", st, want)
	}
}

// Сокета нет на старте (ndm грузится) — не ошибка: клиент дождётся его.
// Мутация: выход из цикла на первом отказе Dial → не подключился, красный.
func TestNDMBus_NoSocketAtStart_RetriesWithoutError(t *testing.T) {
	path := busSocketPath(t)
	rec := &busRecorder{}
	r := newTestBusReader(path, rec)
	if err := r.Start(); err != nil {
		t.Fatalf("Start без сокета: %v", err)
	}
	defer r.Stop()
	time.Sleep(20 * time.Millisecond)
	serveBus(t, path, busDocs(3))
	rec.waitEvents(t, 1)
}

// Stop закрывает соединение и дожидается горутины: после возврата
// обработчики не зовутся (goleak пакета ловит недождавшуюся горутину), а
// своя остановка — не обрыв: onState(false) нет, Warn «шина отключена» на
// каждой остановке демона не пишется.
// Мутация: onState(false) и на Stop → состояния [true false], красный.
func TestNDMBus_Stop_JoinsGoroutine(t *testing.T) {
	path := busSocketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	rec := &busRecorder{}
	r := newTestBusReader(path, rec)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	c := <-accepted
	defer c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for _, st := rec.snapshot(); len(st) == 0 && time.Now().Before(deadline); _, st = rec.snapshot() {
		time.Sleep(time.Millisecond)
	}
	r.Stop()
	_, st := rec.snapshot()
	if len(st) != 1 || !st[0] {
		t.Fatalf("состояния после Stop %v, ждали [true]", st)
	}
	var b bytes.Buffer
	busSavedDoc(&b, "1")
	c.Write(b.Bytes())
	time.Sleep(20 * time.Millisecond)
	if ev, st2 := rec.snapshot(); len(ev) != 0 || len(st2) != len(st) {
		t.Fatalf("после Stop: события %v, состояния %v → %v", ev, st, st2)
	}
}
