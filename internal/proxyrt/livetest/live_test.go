package livetest

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/procres"
)

// double — обвязка-двойник: потокобезопасный Handler (контракт awgmproto:
// State зовётся из accept параллельно командам).
type double struct {
	mu sync.Mutex
	st awgmproto.State
}

func (d *double) State() awgmproto.State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.st
}

func (d *double) AttachTun(iface string, f *os.File) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Tun = &awgmproto.TunState{Iface: iface, Attached: true}
	return nil
}

func (d *double) DetachTun() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Tun = &awgmproto.TunState{Iface: "", Attached: false}
	return nil
}

func startDouble(t *testing.T, sock string, st awgmproto.State) (*double, *awgmproto.Server) {
	t.Helper()
	d := &double{st: st}
	srv, err := awgmproto.Listen(awgmproto.ServerConfig{
		Path: sock, Impl: "wt-client", Role: "client", Instance: "it",
		Handler: d,
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { _ = srv.Close() })
	return d, srv
}

func newLink(t *testing.T, sock string, post func(proxyrt.EventKind) bool) *control.Link {
	t.Helper()
	l := control.NewLink(control.LinkOpts{
		Path: sock, Impl: "wt-client", Role: "client", Instance: "it",
		Post: post,
		// Быстрые сроки: тесты не ждут продовые 20 с.
		RetryEvery: 20 * time.Millisecond, ConnectDeadline: 2 * time.Second,
		CallTimeout: time.Second,
	})
	t.Cleanup(l.Close)
	return l
}

func baseState(args []string) awgmproto.State {
	return awgmproto.State{
		Role: "client", Instance: "it", PID: os.Getpid(),
		ConfigHash: awgmproto.ConfigHash(args), BinarySHA256: "sha",
		Tun: &awgmproto.TunState{},
	}
}

func TestLiveObserveSettles(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wt-client-client-it.sock")
	args := []string{"-peer", "vps", "-password", "pw"}
	startDouble(t, sock, baseState(args))
	link := newLink(t, sock, nil)

	p := procres.NewProc(procres.ProcConfig{
		ID: "process", Instance: "it", Impl: "wt-client", Role: "client",
		Binary: "/bin/true", SocketPath: sock, LogPath: sock + ".log",
		Link: link, Runner: deadRunner{}, Gate: passGate{}, Now: time.Now,
	})
	p.SetDesired(true, args, nil)

	obs, err := p.Observe(context.Background())
	if err != nil || !obs.Known || !obs.Exists {
		t.Fatalf("живой двойник: obs=%+v err=%v", obs, err)
	}
	if steps := p.Plan(obs); len(steps) != 0 {
		t.Fatalf("хеш совпал — шагов нет: %v", steps)
	}
}

func TestLiveHashDriftDetected(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wt-client-client-it.sock")
	startDouble(t, sock, baseState([]string{"-peer", "старый"}))
	link := newLink(t, sock, nil)
	p := procres.NewProc(procres.ProcConfig{
		ID: "process", Instance: "it", Impl: "wt-client", Role: "client",
		Binary: "/bin/true", SocketPath: sock, LogPath: sock + ".log",
		Link: link, Runner: deadRunner{}, Gate: passGate{}, Now: time.Now,
	})
	p.SetDesired(true, []string{"-peer", "новый"}, nil)
	obs, _ := p.Observe(context.Background())
	steps := p.Plan(obs)
	if len(steps) != 1 || steps[0].Op != "restart" {
		t.Fatalf("дрейф отпечатка через настоящий протокол: %v", steps)
	}
}

func TestLiveForeignImplRejected(t *testing.T) {
	// Двойник представляется чужим impl — Link обязан отвергнуть по hello.
	sock := filepath.Join(t.TempDir(), "wdtt-server-client-it.sock")
	d := &double{st: baseState(nil)}
	srv, err := awgmproto.Listen(awgmproto.ServerConfig{
		Path: sock, Impl: "wdtt-server", Role: "client", Instance: "it", Handler: d,
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	link := newLink(t, sock, nil)
	_, err = link.State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "impl") {
		t.Fatalf("чужой impl обязан отвергаться: %v", err)
	}
}

func TestLivePushWakesWorker(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wt-client-client-it.sock")
	_, srv := startDouble(t, sock, baseState(nil))

	var mu sync.Mutex
	var kinds []proxyrt.EventKind
	link := newLink(t, sock, func(k proxyrt.EventKind) bool {
		mu.Lock()
		kinds = append(kinds, k)
		mu.Unlock()
		return true
	})
	if _, err := link.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Push(awgmproto.Event{Event: awgmproto.EventAddress, Address: "10.70.0.5", MTU: 1300})

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(kinds)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("push address не разбудил воркер")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestLiveTunHandoffBadFdIsFailure(t *testing.T) {
	// Не-TUN дескриптор: проверка живёт в БИБЛИОТЕКЕ (VerifyTunFD, §5.3) —
	// двойник ответит bad-request, ресурс обязан отдать отказ, не ретрай.
	sock := filepath.Join(t.TempDir(), "wt-client-client-it.sock")
	startDouble(t, sock, baseState(nil))
	link := newLink(t, sock, nil)
	if _, err := link.State(context.Background()); err != nil {
		t.Fatal(err) // прогреть снимок
	}
	h := procres.NewTunHandoff("tun_handoff", link, func(string) (*os.File, error) {
		r, w, err := os.Pipe() // заведомо не TUN
		if err != nil {
			return nil, err
		}
		w.Close()
		return r, nil
	}, time.Now)
	h.SetDesired("opkgtun18")
	err := h.Apply(context.Background(), proxyrt.Step{Resource: "tun_handoff", Op: "attach"})
	if err == nil {
		t.Fatal("библиотечная проверка дескриптора обязана долететь отказом")
	}
}

// TestLiveEvictionIsTerminalForOldManager — вытеснение через ЖИВОЙ сокет и
// библиотечный evict обязано стать терминальным отказом старого менеджера.
//
// Наблюдатель старого менеджера НАМЕРЕННО задержан в будильнике: пока он стоит
// в post, очередь push (глубина 16, control) переполняется, и кадр evicted
// уходит в ветку «очередь полна» — до наблюдателя он не доедет НИКОГДА.
// Проверяется то, что остаётся: защёлка, которую соединение ставит до
// буферизации. Без задержки тест ставил бы систему в единственную
// конфигурацию, где кадр заведомо доезжает (наблюдатель запаркован в select и
// получает событие напрямую), и потерю защёлки поймать не мог.
func TestLiveEvictionIsTerminalForOldManager(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "wt-client-client-it.sock")
	_, srv := startDouble(t, sock, baseState(nil))

	gate := make(chan struct{})
	var holdOnce, freeOnce sync.Once
	// release идемпотентна и отложена: Link.Close ждёт наблюдателя, и
	// незакрытая калитка подвесила бы уборку теста.
	release := func() { freeOnce.Do(func() { close(gate) }) }
	defer release()

	first := newLink(t, sock, func(proxyrt.EventKind) bool {
		holdOnce.Do(func() { <-gate })
		return true
	})
	if _, err := first.State(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Заведомо больше глубины очереди: наблюдатель задержан и разберёт не
	// больше одного события.
	for i := 0; i < 64; i++ {
		srv.Push(awgmproto.Event{Event: awgmproto.EventAddress, Address: "10.70.0.5"})
	}

	second := newLink(t, sock, nil)
	if _, err := second.State(context.Background()); err != nil {
		t.Fatal(err) // accept второго вытесняет первого
	}

	// Ждём, пока старое соединение доразберёт поток. io.EOF приходит из него
	// самого и означает, что кадр evicted УЖЕ разобран: порядок в разборе
	// жёсткий — защёлка, буферизация, следом EOF. Пока наблюдатель задержан,
	// State ходит по тому же мёртвому соединению и переподключений не делает.
	deadline := time.After(5 * time.Second)
	for {
		if _, err := first.State(context.Background()); errors.Is(err, io.EOF) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("вытесненное соединение не закрыто процессом")
		case <-time.After(5 * time.Millisecond):
		}
	}

	release() // отпустить наблюдателя: он разберёт очередь и упрётся в разрыв

	deadline = time.After(2 * time.Second)
	for !first.Evicted() {
		select {
		case <-deadline:
			t.Fatal("первый менеджер не узнал о вытеснении")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := first.State(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "другой менеджер") {
		t.Fatalf("вытесненный обязан отказывать до конца прогона: %v", err)
	}
}

// deadRunner/passGate — двойнику процесс не нужен.
type deadRunner struct{}

func (deadRunner) Start(context.Context, []string) (int, error) { return 0, nil }
func (deadRunner) Stop(context.Context, int) error              { return nil }
func (deadRunner) AlivePID() (int, bool)                        { return 0, false }

type passGate struct{}

func (passGate) Check(context.Context, string, string, string, []string) error { return nil }

// nopRole/nopJournal — инстансу в этом тесте считать нечего: проверяется
// владение связью, а не реконсиляция.
type nopRole struct{}

func (nopRole) Resources(proxyrt.Intent, any, proxyrt.Observations) []proxyrt.Resource { return nil }

type nopJournal struct{}

func (nopJournal) Info(string, string, string) {}
func (nopJournal) Warn(string, string, string) {}

// linkWatchers — сколько горутин control.(*Link).watch сейчас живо. Прямая
// улика утечки: наблюдатель заканчивается только после закрытия соединения,
// то есть отпущенного unix-сокета.
func linkWatchers() int {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "control.(*Link).watch(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

func TestLiveInstanceStopReleasesLink(t *testing.T) {
	// Инстанс кончается на Stop — связь обязана кончиться вместе с ним.
	// Иначе каждый цикл «удалить инстанс» (ручки API плана 5) оставляет
	// горутину watch и открытый сокет.
	sock := filepath.Join(t.TempDir(), "wt-client-client-it.sock")
	startDouble(t, sock, baseState(nil))

	before := linkWatchers()
	link := newLink(t, sock, nil)
	if _, err := link.State(context.Background()); err != nil {
		t.Fatal(err) // соединение поднято, наблюдатель заведён
	}
	if got := linkWatchers(); got != before+1 {
		t.Fatalf("наблюдатель связи не поднялся — тест ничего не доказывает: было %d, стало %d", before, got)
	}

	inst := instance.New(instance.Config{
		ID: "it", Role: nopRole{},
		Cfg:     func() any { return nil },
		Intent:  func() proxyrt.Intent { return proxyrt.IntentEnabled },
		Link:    link,
		States:  proxyrt.NewStateStore(nil, nil),
		Journal: nopJournal{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inst.Start(ctx)
	inst.Stop()

	// Срок обязателен: тест, который «висит», обязан падать сам.
	deadline := time.Now().Add(2 * time.Second)
	for linkWatchers() > before {
		if time.Now().After(deadline) {
			t.Fatal("после Stop наблюдатель связи жив: горутина watch и unix-сокет утекли")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := link.State(context.Background()); !errors.Is(err, control.ErrClosed) {
		t.Fatalf("после Stop связь обязана быть закрыта навсегда: %v", err)
	}
}
