//go:build linux

package control

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"

	"golang.org/x/sys/unix"
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
	// gate, если задан, задерживает ПЕРВЫЙ будильник: пока watch стоит в
	// post, он не разбирает очередь событий клиента, и её можно переполнить.
	gate     chan struct{}
	holdOnce sync.Once
	freeOnce sync.Once
}

func (s *eventSink) post(kind proxyrt.EventKind) bool {
	s.holdOnce.Do(func() {
		if s.gate != nil {
			<-s.gate
		}
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kinds = append(s.kinds, kind)
	return true
}

// release отпускает задержанный watch. Идемпотентна и обязана быть отложена
// в тесте: Link.Close ждёт watch, и незакрытая калитка подвесила бы уборку.
func (s *eventSink) release() {
	s.freeOnce.Do(func() {
		if s.gate != nil {
			close(s.gate)
		}
	})
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
//
// Очередь событий клиента НАМЕРЕННО переполнена к моменту вытеснения: watch
// задержан в первом post, значит из очереди (глубина eventQueue) уходит не
// больше одного события, и кадр evicted гарантированно попадает в ветку
// «очередь полна» (client.go). До watch он не доедет НИКОГДА, и остаётся
// единственный источник правды — клиентская защёлка, которую read ставит ДО
// буферизации.
//
// Без переполнения тест проверял бы только самый удобный случай: watch
// запаркован в select, событие отдаётся напрямую разбуженному получателю. В
// проде так не бывает — post пишет в журнал, очередь не пуста, и evicted либо
// теряется в ней, либо проигрывает разрыву в select самого watch (сервер шлёт
// кадр и тут же закрывает сокет, оба канала готовы, выбор ветки случаен).
func TestLinkEvictedLatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	p := startProcess(t, path, awgmproto.State{PID: os.Getpid()})
	sink := &eventSink{gate: make(chan struct{})}
	defer sink.release()
	l := newLink(t, path, sink, func(int, string) bool { return true })
	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	c := l.cur
	l.mu.Unlock()

	// Пишем заведомо больше глубины очереди: читатель на той стороне задержан
	// в post и разберёт не больше одного события.
	for i := 0; i < 4*eventQueue; i++ {
		p.srv.Push(awgmproto.Event{Event: awgmproto.EventAddress, Address: "10.70.0.5"})
	}

	// Второй менеджер: подключается и вытесняет нас.
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	// Соединение закрыто сервером — значит кадр evicted УЖЕ разобран: в read
	// порядок жёсткий (защёлка, буферизация, следом EOF).
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("сервер не закрыл вытесненное соединение")
	}
	if !c.Evicted() {
		t.Fatal("клиентская защёлка не встала — тест бьёт мимо")
	}
	if len(c.events) != cap(c.events) {
		t.Fatalf("очередь не переполнена (%d из %d): кадр evicted мог доехать событием, и тест ничего не проверяет",
			len(c.events), cap(c.events))
	}

	sink.release() // отпустить watch: он разберёт очередь и упрётся в разрыв

	deadline := time.Now().Add(5 * time.Second)
	for !l.Evicted() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !l.Evicted() {
		t.Fatal("защёлка вытеснения не встала")
	}
	if !sink.hasDetail("вытеснен") {
		t.Fatalf("вытеснение обязано доехать в журнал: %v", sink.details)
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

// waitUntil ждёт условия до пяти секунд.
func waitUntil(t *testing.T, why string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(why)
}

// newDialCountingLink — связь со счётчиком дозвонов: сам дозвон настоящий,
// подменён только учёт.
func newDialCountingLink(t *testing.T, path string, sink *eventSink,
	dial func(ctx context.Context, path string) (*Client, error)) *Link {
	t.Helper()
	l := NewLink(LinkOpts{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default",
		Binary: "/opt/bin/wt-client",
		Post:   sink.post, Log: sink.log, Alive: func(int, string) bool { return true },
		Dial:       dial,
		RetryEvery: 10 * time.Millisecond, ConnectDeadline: 5 * time.Second,
		CallTimeout: 5 * time.Second,
	})
	t.Cleanup(l.Close)
	return l
}

// TestLinkEvictedShortCircuitsBeforeDial — гард ВЫТЕСНЕНИЯ В connect.
//
// Терминальность вытеснения держат два гарда: короткое замыкание в connect и
// проверка в adopt. Они взаимно избыточны, поэтому страж на причину отказа
// (ErrEvicted) не защищает НИ ОДИН из них: снятие любого одного отдаёт ту же
// ошибку из соседнего места и проходит молча. Отсюда страж на наблюдаемое
// различие — при стоящей защёлке связь не набирает номер вовсе.
func TestLinkEvictedShortCircuitsBeforeDial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{PID: os.Getpid()})
	sink := &eventSink{}
	var dials atomic.Int32
	l := newDialCountingLink(t, path, sink, func(ctx context.Context, p string) (*Client, error) {
		dials.Add(1)
		return Dial(ctx, p)
	})

	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Второй менеджер: защёлка встаёт настоящим путём.
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	waitUntil(t, "защёлка вытеснения не встала", l.Evicted)

	dials.Store(0)
	if _, err := l.State(context.Background()); !errors.Is(err, ErrEvicted) {
		t.Fatalf("после вытеснения обязан быть ErrEvicted, получили %v", err)
	}
	if n := dials.Load(); n != 0 {
		t.Fatalf("при стоящей защёлке номер не набирается, а набран %d раз", n)
	}
}

// TestLinkEvictedRejectsConnectionDialedInFlight — гард ВЫТЕСНЕНИЯ В adopt.
//
// Второй гард ловит то, чего первый поймать не может: защёлка встала, ПОКА мы
// дозванивались, — connect проверял её до набора номера. Случай достижим при
// двух дозвонах разом (докстрока adopt), и тест воспроизводит именно его:
// первый дозвон заперт в калитке, второй успевает поднять соединение, и это
// соединение вытесняет настоящий второй менеджер.
func TestLinkEvictedRejectsConnectionDialedInFlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{PID: os.Getpid()})
	sink := &eventSink{}
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var held atomic.Bool
	l := newDialCountingLink(t, path, sink, func(ctx context.Context, p string) (*Client, error) {
		if held.CompareAndSwap(false, true) {
			started <- struct{}{}
			<-gate
		}
		return Dial(ctx, p)
	})
	openGate := sync.OnceFunc(func() { close(gate) })
	defer openGate() // калитка обязана открыться и на падении теста

	// Дозвон №1 замирает в калитке — свою проверку защёлки connect уже прошёл.
	inflight := make(chan error, 1)
	go func() {
		_, err := l.State(context.Background())
		inflight <- err
	}()
	<-started

	// Дозвон №2 проходит насквозь и ставит соединение на пост.
	go func() { _, _ = l.State(context.Background()) }()
	waitUntil(t, "второе соединение не встало на пост", func() bool {
		_, ok := l.Snapshot()
		return ok
	})

	// Настоящий второй менеджер вытесняет соединение, стоящее на посту.
	other, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	waitUntil(t, "защёлка вытеснения не встала", l.Evicted)

	openGate()
	if err := <-inflight; !errors.Is(err, ErrEvicted) {
		t.Fatalf("соединение, поднятое при вставшей защёлке, обязано быть отвергнуто, получили %v", err)
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
// Это постоянный отказ, а не повод ретраить.
//
// Каждый случай расходится РОВНО по одному полю. Подставной процесс, который
// расходится сразу по двум, ловится соседней сверкой, и исчезновение одной из
// трёх остаётся незамеченным: проверено мутацией — снятие сверки impl при
// расхождении и по impl, и по role тесты переживали.
func TestLinkRejectsForeignProcess(t *testing.T) {
	cases := []struct{ name, impl, role, instance string }{
		{"impl", "wdtt-server", "client", "default"},
		{"role", "wt-client", "server", "default"},
		{"instance", "wt-client", "client", "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.sock")
			startProcessAs(t, path, tc.impl, tc.role, tc.instance,
				awgmproto.State{Role: tc.role, PID: os.Getpid()})

			l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })
			if _, err := l.State(context.Background()); !errors.Is(err, ErrForeignProcess) {
				t.Fatalf("чужой процесс принят за свой: %v", err)
			}
		})
	}
}

// TestLinkSilentProcessFailsNotHangs — процесс принял соединение и молчит.
//
// Ожидание hello обязано быть ограничено сроком ВСЕГДА, иначе наблюдение
// повисает навсегда: воркер инстанса встаёт без единого симптома, и ни stuck,
// ни StopAwaiting движка этого не видят. Срок в самом тесте обязателен — без
// него провал выглядел бы зависанием прогона, а не отказом теста.
func TestLinkSilentProcessFailsNotHangs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var accepted []net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range accepted {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Соединение держим открытым и не пишем ни байта.
			mu.Lock()
			accepted = append(accepted, c)
			mu.Unlock()
		}
	}()

	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })
	done := make(chan error, 1)
	go func() {
		_, err := l.State(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("молчащий процесс обязан давать отказ")
		}
		if !errors.Is(err, ErrNoSocket) {
			t.Fatalf("ожидали отказ подключения, получили %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("State завис: ожидание hello не ограничено сроком")
	}
}

// TestLinkKeepsConnOnCallerDeadline — истёкший срок ВЫЗЫВАЮЩЕГО не повод
// объявлять живое соединение мёртвым.
//
// Иначе наблюдение с дедлайном короче CallTimeout рвало бы исправную связь на
// каждом прогоне и переподключалось на ровном месте.
func TestLinkKeepsConnOnCallerDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{Role: "client", Instance: "default", PID: os.Getpid()})
	sink := &eventSink{}
	l := newLink(t, path, sink, func(int, string) bool { return true })
	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	before := l.cur
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := l.State(ctx); err == nil {
		t.Fatal("истёкший срок вызывающего обязан давать отказ")
	}

	l.mu.Lock()
	after := l.cur
	l.mu.Unlock()
	if after != before {
		t.Fatal("здоровое соединение снято с поста по нетерпению вызывающего")
	}
	if sink.hasDetail("разорвано") {
		t.Fatal("разрыв объявлен на пустом месте")
	}
	if _, err := l.State(context.Background()); err != nil {
		t.Fatalf("связь не пережила нетерпеливый вызов: %v", err)
	}
}

// serveRaw отдаёт первому подключившемуся заранее заготовленные байты.
func serveRaw(t *testing.T, path string, payload []byte) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write(payload)
		time.Sleep(time.Second)
	}()
}

// TestLinkRejectsProtocolVersionWithoutRetries — несовпадение мажора
// терминально (§4, §5.4).
//
// Отказ обязан прийти СРАЗУ и своим классом. Если он падает в общий цикл
// ретраев, наверх уезжает «связь не установлена» после всего окна — то есть
// «ещё не поднялся» там, где процесс говорит на другом языке и не поднимется
// никогда, и план 3 разветвится не туда.
func TestLinkRejectsProtocolVersionWithoutRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	serveRaw(t, path, []byte(
		`{"v":2,"event":"hello","impl":"wt-client","role":"client","instance":"default"}`+"\n"))

	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })
	start := time.Now()
	_, err := l.State(context.Background())
	if !errors.Is(err, ErrProtocolVersion) {
		t.Fatalf("ожидали отказ по версии протокола, получили %v", err)
	}
	if errors.Is(err, ErrNoSocket) {
		t.Fatal("несовпадение мажора уехало как временная неготовность связи")
	}
	// Окно ретраев у теста 300 мс: без терминальной ветки отказ пришёл бы
	// позже него, а не сразу.
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("отказ занял %v: несовпадение мажора ретраилось", el)
	}
}

// TestDialGarbageHelloIsNotProtocolVersion — обратная сторона предыдущего
// теста и цена ошибки в ней выше.
//
// Кадр без поля v — мусор (оборванная строка, чужой писатель в сокете), а не
// «версия ноль». Спутать их нельзя в эту сторону: терминальный отказ снимает
// ретраи, и живой процесс, чей первый кадр не дописался, был бы приговорён
// навсегда вместо переподключения.
func TestDialGarbageHelloIsNotProtocolVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	serveRaw(t, path, []byte(
		`{"event":"hello","impl":"wt-client","role":"client","instance":"default"}`+"\n"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Dial(ctx, path)
	if err == nil {
		t.Fatal("кадр без поля версии принят за hello")
	}
	if errors.Is(err, ErrProtocolVersion) {
		t.Fatalf("мусор в кадре приговорил инстанс как чужая версия: %v", err)
	}
}

// TestLinkTunCommandsTravel — путь передачи дескриптора: единственное место
// протокола с SCM_RIGHTS.
//
// Проверяется не согласие клиента с самим собой, а то, что дескриптор доехал до
// ЧУЖОГО конца: процессу отвечает библиотека, и её отказ по TUNGETIFF возможен
// только если fd действительно пришёл. Отказ «attach-tun без дескриптора»
// означал бы обратное — они различимы по тексту, и в этом весь смысл проверки.
func TestLinkTunCommandsTravel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{Role: "client", Instance: "default", PID: os.Getpid()})
	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	err = l.AttachTun(context.Background(), "opkgtun18", r)
	if err == nil {
		t.Fatal("не-tun дескриптор принят")
	}
	var pe *awgmproto.Error
	if !errors.As(err, &pe) || pe.Code != awgmproto.CodeBadRequest {
		t.Fatalf("ожидали bad-request, получили %v", err)
	}
	if !strings.Contains(err.Error(), "TUNGETIFF") {
		t.Fatalf("дескриптор до процесса не доехал (отказ не про ioctl): %v", err)
	}

	// detach у fakeProcess неприменим — код роли обязан доехать как есть.
	err = l.DetachTun(context.Background())
	if !errors.As(err, &pe) || pe.Code != awgmproto.CodeNotSupported {
		t.Fatalf("ожидали not-supported, получили %v", err)
	}
}

// tunNSChildEnv — маркер дочернего прогона: тот же тестовый бинарь в своих
// user+net namespace, где TUNSETIFF разрешён без прав на машине. Приём и
// помощники ниже — те же, что в awgmproto/fd_test.go; копия нужна потому, что
// это другой модуль и помощники в нём неэкспортируемые.
const tunNSChildEnv = "AWGM_CONTROL_TEST_TUN_NS_CHILD"

// TestLinkAttachTunHandsFDOver — положительный путь: настоящий tun доезжает до
// процесса, и тот докладывает о нём в state.
//
// Без настоящего дескриптора этот путь не исполняется ничем: библиотека
// проверяет fd ioctl'ом до вызова обвязки, поэтому любой суррогат упирается в
// отказ, и слот обвязки остаётся непройденным.
func TestLinkAttachTunHandsFDOver(t *testing.T) {
	if os.Getenv(tunNSChildEnv) == "" && !tunCreatable() {
		rerunInUserNS(t)
		return
	}
	fd, err := openTestTun("awgmctl0", unix.IFF_TUN|unix.IFF_NO_PI)
	if err != nil {
		t.Skipf("tun создать не удалось (%v): положительный путь не проверен", err)
	}
	f := os.NewFile(uintptr(fd), "awgmctl0")
	defer f.Close()

	path := filepath.Join(t.TempDir(), "c.sock")
	startProcess(t, path, awgmproto.State{Role: "client", Instance: "default", PID: os.Getpid()})
	l := newLink(t, path, &eventSink{}, func(int, string) bool { return true })

	if err := l.AttachTun(context.Background(), "awgmctl0", f); err != nil {
		t.Fatalf("законный дескриптор не доехал: %v", err)
	}
	st, err := l.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Tun == nil || !st.Tun.Attached || st.Tun.Iface != "awgmctl0" {
		t.Fatalf("процесс дескриптор не принял: %+v", st.Tun)
	}

	// Имя из сообщения сверяется с именем у ядра, а не берётся на веру.
	if err := l.AttachTun(context.Background(), "awgmctl1", f); err == nil {
		t.Fatal("дескриптор чужого интерфейса принят")
	}
}

// openTestTun создаёт tun с заданными флагами.
func openTestTun(name string, flags uint16) (int, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return -1, err
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	ifr.SetUint16(flags)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func tunCreatable() bool {
	fd, err := openTestTun("awgmctlprobe0", unix.IFF_TUN|unix.IFF_NO_PI)
	if err != nil {
		return false
	}
	_ = unix.Close(fd)
	return true
}

// rerunInUserNS перезапускает этот же тест в своих user+net namespace: там мы
// root, TUNSETIFF разрешён, а интерфейс умирает вместе с namespace и машину не
// пачкает. Нет прав или самого unshare — тест честно пропускается, а не
// притворяется пройденным.
func rerunInUserNS(t *testing.T) {
	t.Helper()
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("TUNSETIFF запрещён и unshare не найден: передача дескриптора не проверена")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(unshare, "-Urn", self, "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), tunNSChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	switch {
	case !bytes.Contains(out, []byte("=== RUN")):
		t.Skipf("подпроцесс в user namespace не стартовал (%v): %s", err, out)
	case bytes.Contains(out, []byte("--- SKIP")):
		t.Skipf("подпроцесс пропустил проверку: %s", out)
	case err != nil:
		t.Fatalf("прогон в user namespace провалился (%v):\n%s", err, out)
	}
	t.Logf("проверено в user namespace:\n%s", out)
}

// TestDialRejectsFirstFrameNotHello — соединение, где первым кадром пришёл не
// hello, менеджер обязан отвергнуть (§5.4). Библиотека шлёт hello первым по
// конструкции, но полагаться на чужую конструкцию здесь нельзя: сокет мог
// открыть кто угодно.
func TestDialRejectsFirstFrameNotHello(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	line, err := awgmproto.EncodeLine(awgmproto.Event{
		V: awgmproto.Version, Event: awgmproto.EventAddress, Address: "10.70.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	serveRaw(t, path, line)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, path); err == nil {
		t.Fatal("соединение без hello принято")
	} else if !strings.Contains(err.Error(), "не hello") {
		t.Fatalf("отказ без внятной причины: %v", err)
	}
}

// TestDialRejectsOverlongFrame — ReadFrame МОЖЕТ отдать кадр длиннее потолка,
// если перевод строки приехал в том же чтении, что и перебор. Ловит это
// DecodeLine, и клиент обязан на таком кадре отказать, а не принять его.
func TestDialRejectsOverlongFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.sock")
	line := []byte(`{"v":1,"event":"hello","impl":"wt-client","role":"client","instance":"` +
		strings.Repeat("x", 70*1024) + `"}` + "\n")
	serveRaw(t, path, line)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Dial(ctx, path); err == nil {
		t.Fatalf("кадр в %d байт принят", len(line))
	} else if !strings.Contains(err.Error(), "потолок") {
		t.Fatalf("отказ не про длину кадра: %v", err)
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
	// "a.b" отдельным случаем: "точка.точка" отбраковывается по кириллице, и
	// разрешение точки тесты переживало — проверено мутацией. Сам запрет точки
	// нужен потому, что §3 задаёт закрытый набор [A-Za-z0-9_-], и держится он
	// не на разборе имени: round-trip через InstanceFromPath точку переживает
	// (проверено), а вот набор — единственное, что не даёт идентификатору
	// притащить в имя файла что попало.
	cases := []string{"", "с пробелом", "точка.точка", "a.b", "a/b", strings.Repeat("a", 33)}
	for _, id := range cases {
		if _, err := SocketPath("/tmp/awgm", "wt-client", "client", id); err == nil {
			t.Fatalf("идентификатор %q принят", id)
		}
		// Журнал живёт по тому же правилу: имя собирает тот же код, и щель в
		// нём означала бы, что негодный идентификатор всё равно доедет до имени
		// файла — просто другого.
		if _, err := LogPath("/tmp/awgm", "wt-client", "client", id); err == nil {
			t.Fatalf("LogPath принял идентификатор %q", id)
		}
	}
	got, err := SocketPath("/tmp/awgm", "wt-client", "client", "default")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/awgm/wt-client-client-default.sock" {
		t.Fatalf("путь сокета: %s", got)
	}
	got, err = LogPath("/tmp/awgm", "wt-client", "client", "default")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/awgm/wt-client-client-default.log" {
		t.Fatalf("путь журнала: %s", got)
	}
	// Предел sun_path — про сокет и только про него. Журналу длина безразлична,
	// и распространить проверку на него значило бы запретить законный путь.
	if _, err := LogPath("/tmp/"+strings.Repeat("d", 100), "wt-client", "client", "default"); err != nil {
		t.Fatalf("длинный путь журнала отвергнут: %v", err)
	}
}

func TestSocketPathRejectsOverlongPath(t *testing.T) {
	// Длина sun_path — предел ядра, и обрезание молчаливое: проверяет менеджер.
	dir := "/tmp/" + strings.Repeat("d", 100)
	if _, err := SocketPath(dir, "wt-client", "client", "default"); err == nil {
		t.Fatal("путь длиннее sun_path принят")
	}

	// Граница — на точном значении, а не «заведомо большим» путём: случай выше
	// бьёт по 135 байтам и мутанта > → >= не заметит, а тот начал бы отвергать
	// законный путь ровно в 107 байт. Предел проверен ядром: bind на 107 байт
	// проходит, на 108 даёт EINVAL.
	//
	// Длина набирается из фактической, а не зашита числом: имя файла инстанса
	// собирает сам пакет, и подгонять его руками значит проверять свою
	// арифметику вместо чужой границы.
	name := "wt-client-client-default.sock"
	cases := []struct {
		total  int
		accept bool
	}{
		{maxSunPath, true},
		{maxSunPath + 1, false},
	}
	for _, tc := range cases {
		// путь = dir + "/" + name, dir = "/" + d…d
		d := "/" + strings.Repeat("d", tc.total-len(name)-2)
		if got := len(d) + 1 + len(name); got != tc.total {
			t.Fatalf("собрали путь в %d байт вместо %d", got, tc.total)
		}
		path, err := SocketPath(d, "wt-client", "client", "default")
		if tc.accept {
			if err != nil {
				t.Fatalf("законный путь в %d байт отвергнут: %v", tc.total, err)
			}
			if len(path) != tc.total {
				t.Fatalf("путь собрался в %d байт вместо %d", len(path), tc.total)
			}
			continue
		}
		if err == nil {
			t.Fatalf("путь в %d байт принят", tc.total)
		}
	}
}

// TestLinkGivesNewIncarnationItsWindow — после смерти процесса связь обязана
// забыть его pid. Иначе короткое замыкание connect («мёртвый pid — сразу
// мёртв») судит НОВОЕ воплощение по номеру предыдущего и отбирает у него окно
// ретраев §7: на mips сокет поднимается 4-5 с, а отказ за микросекунды —
// приговор живому процессу.
func TestLinkGivesNewIncarnationItsWindow(t *testing.T) {
	const oldPID, newPID = 1000001, 1000002
	path := filepath.Join(t.TempDir(), "c.sock")
	old := startProcess(t, path, awgmproto.State{PID: oldPID})
	sink := &eventSink{}
	// Живым считается ТОЛЬКО новое воплощение: старый pid мёртв, как ему и
	// положено после смерти процесса.
	l := NewLink(LinkOpts{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default",
		Binary:          "/opt/bin/wt-client",
		Post:            sink.post,
		Log:             sink.log,
		Alive:           func(pid int, _ string) bool { return pid == newPID },
		RetryEvery:      10 * time.Millisecond,
		ConnectDeadline: 3 * time.Second,
		CallTimeout:     time.Second,
	})
	t.Cleanup(l.Close)

	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Процесс умер. Ждём вердикт «died»: он выносится по pid УМЕРШЕГО
	// воплощения, то есть к следующему наблюдению lost уже отработал.
	_ = old.srv.Close()
	sink.waitFor(t, proxyrt.EventProcessDied)

	// Новое воплощение с ДРУГИМ pid поднимает сокет позже.
	type spawn struct {
		srv *awgmproto.Server
		err error
	}
	spawned := make(chan spawn, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		srv, err := awgmproto.Listen(awgmproto.ServerConfig{
			Path: path, Impl: "wt-client", Role: "client", Instance: "default",
			Handler: &fakeProcess{st: awgmproto.State{PID: newPID}},
		})
		spawned <- spawn{srv: srv, err: err}
		if err == nil {
			srv.Serve()
		}
	}()

	// Срок наблюдения длиннее задержки подъёма сокета, но не бесконечный:
	// зависший тест обязан падать, а не висеть.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := l.State(ctx)

	s := <-spawned
	if s.err != nil {
		t.Fatalf("новое воплощение не подняло сокет: %v", s.err)
	}
	t.Cleanup(func() { _ = s.srv.Close() })

	if err != nil {
		t.Fatalf("новое воплощение лишили окна на подъём сокета: %v", err)
	}
	if st.PID != newPID {
		t.Fatalf("ответило не новое воплощение: pid %d", st.PID)
	}
}

// TestLinkKeepsPIDOfLivingIncarnation — разрыв связи с ЖИВЫМ процессом pid не
// отменяет: воплощение то же. Умри процесс в окне переподключения — вердикт
// обязан прийти сразу, а не через полное окно ретраев §7.
func TestLinkKeepsPIDOfLivingIncarnation(t *testing.T) {
	const pid = 1000003
	path := filepath.Join(t.TempDir(), "c.sock")
	p := startProcess(t, path, awgmproto.State{PID: pid})
	sink := &eventSink{}
	var alive atomic.Bool
	alive.Store(true)
	l := NewLink(LinkOpts{
		Path: path, Impl: "wt-client", Role: "client", Instance: "default",
		Binary:          "/opt/bin/wt-client",
		Post:            sink.post,
		Log:             sink.log,
		Alive:           func(int, string) bool { return alive.Load() },
		RetryEvery:      10 * time.Millisecond,
		ConnectDeadline: 3 * time.Second,
		CallTimeout:     time.Second,
	})
	t.Cleanup(l.Close)

	if _, err := l.State(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Связь потеряна при живом процессе.
	_ = p.srv.Close()
	sink.waitFor(t, proxyrt.EventProcessState)
	if !sink.hasDetail("разорвано") {
		t.Fatal("разрыв при живом процессе обязан читаться как потеря связи")
	}

	// Процесс умер в окне переподключения: короткое замыкание обязано сработать.
	alive.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := l.State(ctx)
	if !errors.Is(err, ErrNoSocket) || !strings.Contains(err.Error(), "мёртв") {
		t.Fatalf("ожидали приговор по мёртвому pid, получили %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("вердикт занял %v: pid забыт, приговор ждал окна ретраев", elapsed)
	}
}
