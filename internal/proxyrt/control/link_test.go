//go:build linux

package control

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// fakeProcess — дочерний процесс в тестах: настоящий слушатель протокола.
// Обе стороны разговаривают одним кодом, поэтому тест проверяет контракт
// целиком, а не согласие клиента с самим собой.
type fakeProcess struct {
	mu  sync.Mutex
	st  awgmproto.State
	srv *awgmproto.Server
}

func (p *fakeProcess) State() awgmproto.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

func (p *fakeProcess) AttachTun(iface string, f *os.File) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.st.Tun = &awgmproto.TunState{Iface: iface, Attached: true}
	_ = f.Close()
	return nil
}

func (p *fakeProcess) DetachTun() error { return awgmproto.ErrNotSupported }

func startProcess(t *testing.T, path string, st awgmproto.State) *fakeProcess {
	t.Helper()
	return startProcessAs(t, path, "wt-client", "client", "default", st)
}

func startProcessAs(t *testing.T, path, impl, role, instance string, st awgmproto.State) *fakeProcess {
	t.Helper()
	p := &fakeProcess{st: st}
	srv, err := awgmproto.Listen(awgmproto.ServerConfig{
		Path: path, Impl: impl, Role: role, Instance: instance, Handler: p,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.srv = srv
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })
	return p
}

// eventSink — воркер в тестах: копит виды будильников и пояснения к ним.
type eventSink struct {
	mu      sync.Mutex
	kinds   []proxyrt.EventKind
	details []string
}

func (s *eventSink) post(kind proxyrt.EventKind) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kinds = append(s.kinds, kind)
	return true
}

func (s *eventSink) log(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.details = append(s.details, msg)
}

func (s *eventSink) waitFor(t *testing.T, kind proxyrt.EventKind) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, k := range s.kinds {
			if k == kind {
				s.mu.Unlock()
				return
			}
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("событие %s не пришло", kind)
}

// hasDetail — есть ли среди пояснений строка с подстрокой.
func (s *eventSink) hasDetail(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.details {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}

func newLink(t *testing.T, path string, sink *eventSink, alive func(int, string) bool) *Link {
	t.Helper()
	l := NewLink(LinkOpts{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default",
		Binary: "/opt/bin/wt-client",
		Post:   sink.post, Log: sink.log, Alive: alive,
		RetryEvery: 10 * time.Millisecond, ConnectDeadline: 300 * time.Millisecond,
		CallTimeout: time.Second,
	})
	t.Cleanup(l.Close)
	return l
}

func TestLinkStateReadsProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{Role: "client", Mode: "raw", Instance: "default",
		PID: os.Getpid(), ConfigHash: "aa", BinarySHA256: "bb", Address: "10.70.0.5", MTU: 1300})

	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })
	st, err := l.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Address != "10.70.0.5" || st.MTU != 1300 {
		t.Fatalf("состояние не доехало: %+v", st)
	}
	snap, ok := l.Snapshot()
	if !ok || snap.State.Address != "10.70.0.5" {
		t.Fatal("снимок не сохранён")
	}
}

func TestLinkPushWakesWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	p := startProcess(t, path, awgmproto.State{PID: os.Getpid()})
	sink := &eventSink{}
	l := newLink(t, path, sink, func(int, string) bool { return true })
	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}

	p.srv.Push(awgmproto.Event{Event: awgmproto.EventAddress, Address: "10.70.0.5", MTU: 1300})
	sink.waitFor(t, proxyrt.EventProcessState)
	// Сам вид события причины не несёт — она обязана доехать в журнал, иначе
	// «инстанс проснулся» будет нечитаемо.
	if !sink.hasDetail("address") {
		t.Fatal("причина будильника не попала в журнал")
	}
}

// TestLinkDeadProcessRaisesDied — закрытие соединения плюс мёртвый pid даёт
// именно смерть процесса, а не «связь потеряна».
func TestLinkDeadProcessRaisesDied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	p := startProcess(t, path, awgmproto.State{PID: os.Getpid()})
	sink := &eventSink{}
	l := newLink(t, path, sink, func(int, string) bool { return false })
	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}

	_ = p.srv.Close()
	sink.waitFor(t, proxyrt.EventProcessDied)
}

// TestLinkEvictedLatch — вытеснение защёлкивается, переподключений нет, и
// защёлка снимается только снаружи.
func TestLinkEvictedLatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{PID: os.Getpid()})
	sink := &eventSink{}
	l := newLink(t, path, sink, func(int, string) bool { return true })
	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Второй менеджер: подключается и вытесняет нас.
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	deadline := time.Now().Add(5 * time.Second)
	for !l.Evicted() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !l.Evicted() {
		t.Fatal("защёлка вытеснения не встала")
	}
	if _, err := l.State(context.Background()); !errors.Is(err, ErrEvicted) {
		t.Fatalf("после evicted обязан быть отказ с причиной, получили %v", err)
	}

	// Граница прогона: защёлка снимается, связь восстанавливается.
	_ = other.Close()
	l.ClearEvicted()
	if _, err := l.State(context.Background()); err != nil {
		t.Fatalf("после снятия защёлки связь не восстановилась: %v", err)
	}
}

func TestLinkNoSocketFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "нет.sock")
	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })
	start := time.Now()
	if _, err := l.State(context.Background()); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("ожидали «процесс не открыл управляющий сокет», получили %v", err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("окно ретраев не выдержано")
	}
}

// TestLinkRetriesStateOnce — при таймауте ответа менеджер переспрашивает
// state ровно один раз, а не ретраит вслепую в цикле.
func TestLinkRetriesStateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	p := startMuteProcess(t, path)

	l := NewLink(LinkOpts{
		Path: path, Instance: "default",
		Post:        func(proxyrt.EventKind) bool { return true },
		Alive:       func(int, string) bool { return true },
		RetryEvery:  10 * time.Millisecond,
		CallTimeout: 100 * time.Millisecond,
	})
	defer l.Close()

	if _, err := l.State(context.Background()); err == nil {
		t.Fatal("молчащий процесс обязан давать отказ")
	}
	if got := len(p.requests); got != 2 {
		t.Fatalf("запросов state %d, ожидали 2 (первый и один повторный)", got)
	}
}

// TestLinkDropsConnectionAfterDoubleTimeout — после второго таймаута подряд
// соединение считается МЁРТВЫМ (§5.1, §7), а не просто «ответа не было».
//
// Без этого зависший процесс с открытым сокетом навсегда остаётся
// «подключённым»: l.cur жив, переподключаться некуда, died не наступит
// никогда, и инстанс тихо застревает.
func TestLinkDropsConnectionAfterDoubleTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	p := startMuteProcess(t, path)
	sink := &eventSink{}

	l := NewLink(LinkOpts{
		Path: path, Instance: "default",
		Post:        sink.post,
		Alive:       func(int, string) bool { return true },
		RetryEvery:  10 * time.Millisecond,
		CallTimeout: 100 * time.Millisecond,
	})
	defer l.Close()

	if _, err := l.State(context.Background()); err == nil {
		t.Fatal("молчащий процесс обязан давать отказ")
	}
	// Разрыв объявлен наружу…
	sink.waitFor(t, proxyrt.EventProcessState)
	// …и следующее наблюдение подключается заново, а не сидит на трупе.
	_, _ = l.State(context.Background())
	if got := len(p.accepts); got != 2 {
		t.Fatalf("подключений %d, ожидали 2: мёртвое соединение не сброшено", got)
	}
}

// TestLinkRejectsForeignProcess — hello сверяется, а не принимается на веру.
// На сокете клиента отвечает сервер: это постоянный отказ, а не повод ретраить.
func TestLinkRejectsForeignProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcessAs(t, path, "wdtt-server", "server", "default",
		awgmproto.State{Role: "server", PID: os.Getpid()})

	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })
	if _, err := l.State(context.Background()); !errors.Is(err, ErrForeignProcess) {
		t.Fatalf("чужой процесс принят за свой: %v", err)
	}
}

// muteProcess — процесс, который здоровается и молчит в ответ на команды.
type muteProcess struct {
	requests chan awgmproto.Request
	accepts  chan struct{}
}

func startMuteProcess(t *testing.T, path string) *muteProcess {
	t.Helper()
	p := &muteProcess{
		requests: make(chan awgmproto.Request, 8),
		accepts:  make(chan struct{}, 8),
	}
	requests := p.requests
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case p.accepts <- struct{}{}:
			default:
			}
			go func() {
				defer conn.Close()
				hello, _ := awgmproto.EncodeLine(awgmproto.Event{
					V: awgmproto.Version, Event: awgmproto.EventHello,
					Impl: "wt-client", Role: "client", Instance: "default", PID: os.Getpid(),
				})
				if _, err := conn.Write(hello); err != nil {
					return
				}
				fc := awgmproto.NewFrameConn(conn.(*net.UnixConn))
				for {
					line, _, err := fc.ReadFrame()
					if err != nil {
						return
					}
					kind, msg, err := awgmproto.DecodeLine(line)
					if err != nil || kind != awgmproto.KindRequest {
						continue
					}
					select {
					case requests <- msg.(awgmproto.Request):
					default:
					}
				}
			}()
		}
	}()
	return p
}

func TestSocketPathRejectsBadInstance(t *testing.T) {
	cases := []string{"", "с пробелом", "точка.точка", strings.Repeat("a", 33)}
	for _, id := range cases {
		if _, err := SocketPath("/tmp/awgm", "wt-client", "client", id); err == nil {
			t.Fatalf("идентификатор %q принят", id)
		}
	}
	got, err := SocketPath("/tmp/awgm", "wt-client", "client", "default")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/awgm/wt-client-client-default.sock" {
		t.Fatalf("путь сокета: %s", got)
	}
}

func TestSocketPathRejectsOverlongPath(t *testing.T) {
	// Длина sun_path — предел ядра, и обрезание молчаливое: проверяет менеджер.
	dir := "/tmp/" + strings.Repeat("d", 100)
	if _, err := SocketPath(dir, "wt-client", "client", "default"); err == nil {
		t.Fatal("путь длиннее sun_path принят")
	}
}
