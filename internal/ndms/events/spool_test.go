//go:build linux

package events

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// spoolSink собирает события, отданные читателем.
type spoolSink struct {
	mu  sync.Mutex
	got []Event
}

func (s *spoolSink) put(e Event) {
	s.mu.Lock()
	s.got = append(s.got, e)
	s.mu.Unlock()
}

func (s *spoolSink) events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.got...)
}

func (s *spoolSink) waitFor(t *testing.T, n int) []Event {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := s.events()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("событий: want %d, got %d", n, len(got))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// warnLog считает предупреждения читателя.
type warnLog struct {
	mu    sync.Mutex
	warns []string
}

func (w *warnLog) Warnf(format string, args ...any) {
	w.mu.Lock()
	w.warns = append(w.warns, fmt.Sprintf(format, args...))
	w.mu.Unlock()
}

func (w *warnLog) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.warns)
}

// appendLine пишет как хук-скрипт: `>>` = open(O_APPEND) + один write + close.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func startSpool(t *testing.T, path string, capBytes int64) (*spoolSink, *warnLog) {
	t.Helper()
	sink := &spoolSink{}
	log := &warnLog{}
	r := NewSpoolReader(path, sink.put, log)
	if capBytes > 0 {
		r.cap = capBytes
	}
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(r.Stop)
	return sink, log
}

func hookLine(i int) (string, Event) {
	types := []EventType{EventIfCreated, EventIfLayerChanged, EventIfDestroyed}
	e := Event{Type: types[i%3], ID: fmt.Sprintf("Wireguard%d", i%3), SystemName: fmt.Sprintf("nwg%d", i%3)}
	if e.Type == EventIfLayerChanged {
		e.Layer, e.Level = "conf", fmt.Sprintf("l%d", i)
	}
	return fmt.Sprintf("type=%s&id=%s&system_name=%s&layer=%s&level=%s&address=\n",
		e.Type, e.ID, e.SystemName, e.Layer, e.Level), e
}

func TestSpool_OrderPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "hooks", "ndm-hooks")
	sink, _ := startSpool(t, path, 0)

	var want []Event
	for i := 0; i < 200; i++ {
		line, e := hookLine(i)
		appendLine(t, path, line)
		want = append(want, e)
	}
	got := sink.waitFor(t, len(want))
	if len(got) != len(want) {
		t.Fatalf("событий: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("#%d: got %#v, want %#v", i, got[i], want[i])
		}
	}
}

// Накопленное до старта покрыто бутовым списком — читатель начинает с конца.
func TestSpool_StartSkipsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		line, _ := hookLine(i)
		appendLine(t, path, line)
	}
	sink, _ := startSpool(t, path, 0)

	line, e := hookLine(7)
	appendLine(t, path, line)
	got := sink.waitFor(t, 1)
	time.Sleep(50 * time.Millisecond)
	got = sink.events()
	if len(got) != 1 || got[0] != e {
		t.Fatalf("got %#v, want только %#v", got, e)
	}
}

func TestSpool_PartialLineBuffered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	sink, log := startSpool(t, path, 0)

	appendLine(t, path, "type=ifcre")
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.events()); n != 0 {
		t.Fatalf("хвост без \\n отдан раньше времени: %d", n)
	}
	appendLine(t, path, "ated&id=X\n")
	got := sink.waitFor(t, 1)
	if got[0] != (Event{Type: EventIfCreated, ID: "X"}) {
		t.Fatalf("got %#v", got[0])
	}
	if log.count() != 0 {
		t.Fatalf("предупреждения: %d", log.count())
	}
}

// Ротация rename'ом: писатель, открывший файл до ротации и записавший после,
// попадает в .1 — читатель держит его fd до следующей ротации и дочитывает.
func TestSpool_RotationKeepsOrderAndStraggler(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	sink, _ := startSpool(t, path, 1<<10)

	straggler, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer straggler.Close()

	var want []Event
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("ротации не было")
		}
		line, e := hookLine(i)
		appendLine(t, path, line)
		want = append(want, e)
		sink.waitFor(t, len(want))
		if _, err := os.Stat(path + ".1"); err == nil {
			break
		}
	}

	line, e := hookLine(1000)
	if _, err := straggler.WriteString(line); err != nil {
		t.Fatal(err)
	}
	want = append(want, e)
	got := sink.waitFor(t, len(want))
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("#%d: got %#v, want %#v", i, got[i], want[i])
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("после ротации нет %s: %v", path, err)
	}
	if st.Size() != 0 {
		t.Fatalf("новый spool не пуст: %d байт", st.Size())
	}

	// Новый файл читается с начала.
	line, e = hookLine(1001)
	appendLine(t, path, line)
	want = append(want, e)
	got = sink.waitFor(t, len(want))
	if got[len(got)-1] != e {
		t.Fatalf("после ротации: got %#v, want %#v", got[len(got)-1], e)
	}
}

func TestSpool_BadLineSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	sink, log := startSpool(t, path, 0)

	appendLine(t, path, "type=zzz&id=1\n")
	line, e := hookLine(0)
	appendLine(t, path, line)
	got := sink.waitFor(t, 1)
	if len(got) != 1 || got[0] != e {
		t.Fatalf("got %#v, want %#v", got, e)
	}
	if log.count() != 1 {
		t.Fatalf("предупреждений: want 1, got %d", log.count())
	}
}

// Порванная запись без '\n' длиннее maxPendingLine выбрасывается с одним
// Warn; её хвост до '\n' тоже, следующая строка доставляется.
func TestSpool_OversizedPartialDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	sink, log := startSpool(t, path, 0)

	appendLine(t, path, strings.Repeat("x", maxPendingLine+1000))
	// Хвост без '\n' сброшен ДО прихода остатка строки — остаток ("yyy")
	// обязан быть пропущен до '\n', а не разобран как строка.
	deadline := time.Now().Add(3 * time.Second)
	for log.count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("перебор хвоста не сброшен")
		}
		time.Sleep(2 * time.Millisecond)
	}
	appendLine(t, path, "yyy\n")
	line, e := hookLine(0)
	appendLine(t, path, line)
	got := sink.waitFor(t, 1)
	if len(got) != 1 || got[0] != e {
		t.Fatalf("got %#v, want %#v", got, e)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.warns) != 1 || !strings.Contains(log.warns[0], "dropped partial line") {
		t.Fatalf("предупреждения: want одно про сброс хвоста, got %.200q", log.warns)
	}
}

// Недописанная строка не держит ротацию: файл дорос до cap — .1 появился,
// новые строки идут в новый файл и доставляются.
func TestSpool_RotationNotBlockedByPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	sink, _ := startSpool(t, path, 1<<10)

	appendLine(t, path, "type=ifcreated&id="+strings.Repeat("A", 2<<10))
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path + ".1"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ротации не было: недописанная строка держит её")
		}
		time.Sleep(2 * time.Millisecond)
	}
	line, e := hookLine(0)
	appendLine(t, path, line)
	got := sink.waitFor(t, 1)
	if got[0] != e {
		t.Fatalf("got %#v, want %#v", got[0], e)
	}
}

// Stop сносит каталог spool: хук-скрипт без каталога ничего не пишет.
func TestSpool_StopRemovesDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks", "ndm-hooks")
	r := NewSpoolReader(path, func(Event) {}, NopLogger())
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Start не создал spool: %v", err)
	}
	r.Stop()
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("каталог spool после Stop: %v", err)
	}
}
