package pingcheck

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wg"
)

// slowWGClient simulates a WG client that never returns a recent handshake.
type slowWGClient struct{}

func (c *slowWGClient) Show(ctx context.Context, iface string) (*wg.ShowResult, error) {
	return &wg.ShowResult{HasPeer: true}, nil // no recent handshake
}

// TestWaitHandshake_InterruptedByStopCh verifies that waitHandshake exits
// immediately when stopCh is closed, rather than blocking for the full
// 30-second handshake timeout. This prevents HTTP handlers from hanging
// when StopMonitoring is called during an active link toggle.
func TestWaitHandshake_InterruptedByStopCh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := &Service{
		wg:  &slowWGClient{},
		ctx: ctx,
	}

	stopCh := make(chan struct{})

	// Close stopCh after a short delay to simulate StopMonitoring.
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(stopCh)
	}()

	start := time.Now()
	result := s.waitHandshake("fake0", stopCh)
	elapsed := time.Since(start)

	if result {
		t.Error("waitHandshake should return false when interrupted by stopCh")
	}
	if elapsed > 2*time.Second {
		t.Errorf("waitHandshake took %v — should have exited quickly via stopCh, not waited for 30s deadline", elapsed)
	}
}

// TestWaitHandshake_DeadlineWithoutStop verifies that waitHandshake respects
// the configured handshake deadline when stopCh is NOT closed (normal path).
func TestWaitHandshake_DeadlineWithoutStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const fastTimeout = 40 * time.Millisecond
	s := &Service{
		wg:               &slowWGClient{},
		ctx:              ctx,
		handshakeTimeout: fastTimeout,
	}

	stopCh := make(chan struct{}) // never closed

	start := time.Now()
	result := s.waitHandshake("fake0", stopCh)
	elapsed := time.Since(start)

	if result {
		t.Error("waitHandshake should return false when deadline expires")
	}
	if elapsed < fastTimeout/2 {
		t.Errorf("waitHandshake returned too quickly (%v) — deadline should be about %v", elapsed, fastTimeout)
	}
	if elapsed > time.Second {
		t.Errorf("waitHandshake took too long (%v) for configured timeout %v", elapsed, fastTimeout)
	}
}

// spyWGClient считает обращения к wg. waitHandshake — последний шаг лечения:
// добраться до него можно только через `ip link set down/up`, поэтому ноль
// обращений и есть наблюдаемый признак «команд управления не было».
type spyWGClient struct {
	calls int
}

func (c *spyWGClient) Show(_ context.Context, _ string) (*wg.ShowResult, error) {
	c.calls++
	return &wg.ShowResult{HasPeer: true}, nil
}

// alwaysFailDoer роняет HTTP-проверку мгновенно и без сети.
type alwaysFailDoer struct{}

func (alwaysFailDoer) Do(_ context.Context, _ httpclient.CallConfig) (*httpclient.Result, error) {
	return nil, errors.New("no route")
}

// Зеркальную запись прокси-выхода мы измеряем, но не лечим: три провала
// подряд не должны выливаться ни в одну команду управления интерфейсом —
// tun принадлежит прокси-рантайму, у него свой цикл реконсиляции.
func TestSensorTick_WdttRawNeverTouchesInterface(t *testing.T) {
	orig := httpprobe.Client
	defer func() { httpprobe.Client = orig }()
	httpprobe.Client = alwaysFailDoer{}

	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        "wdtt-raw",
		RawKernelIface: "opkgtun18",
		PingCheck: &storage.TunnelPingCheck{
			Enabled: true, Method: "http", Interval: 1, FailThreshold: 3,
		},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lb := NewLogBuffer()
	defer lb.Stop()
	spy := &spyWGClient{}
	rec := stubRunCmd(t)
	// Порог рукопожатия выше частоты опроса (handshakePollFreq): если гард
	// пропадёт, лечение реально дойдёт до wg.Show, и spy это увидит.
	s := &Service{tunnels: store, wg: spy, logBuffer: lb, ctx: ctx, handshakeTimeout: 3 * time.Second}

	config := s.getCheckConfig("wdttraw-de")
	if config == nil || config.FailThreshold != 3 {
		t.Fatalf("config = %+v, ожидали порог 3", config)
	}
	m := &tunnelMonitor{tunnelID: "wdttraw-de", tunnelName: "Германия"}
	for i := 0; i < 3; i++ {
		s.sensorTick(m, config)
	}

	// Улика «три провала действительно случились» берётся из журнала, а не из
	// failCount: лечение обнуляет счётчик, и проверка по нему путала бы
	// «проверки не падали» с «лечение отработало и сбросило счёт».
	fails, transitions := 0, ""
	for _, e := range lb.GetAll() {
		if e.StateChange != "" {
			transitions = e.StateChange
			continue // запись самого лечения, а не сенсора
		}
		if !e.Success {
			fails++
		}
	}
	if fails != config.FailThreshold {
		t.Fatalf("провалов в журнале = %d, ожидали %d: точка лечения не достигнута", fails, config.FailThreshold)
	}
	if spy.calls != 0 {
		t.Fatalf("wg.Show вызван %d раз — значит link toggle отработал", spy.calls)
	}
	if m.restartCount != 0 {
		t.Fatalf("restartCount = %d, ожидали 0: восстановление не наше", m.restartCount)
	}
	if transitions != "" {
		t.Fatalf("в журнале есть переход %q — восстановление отработало", transitions)
	}
	// Прямой пин: при мутации гарда proxyOwned лечение дошло бы до ip link.
	if len(rec.calls) != 0 {
		t.Fatalf("runCmd вызван %d раз(а) — гард proxyOwned пропущен: %v", len(rec.calls), rec.calls)
	}
}

// Дефолт шва — exec.Run: иначе лечение в проде молча становится no-op.
func TestRunCmdDefault_IsExecRun(t *testing.T) {
	if reflect.ValueOf(runCmd).Pointer() != reflect.ValueOf(exec.Run).Pointer() {
		t.Fatal("runCmd по умолчанию обязан быть exec.Run")
	}
}

type cmdRecorder struct{ calls [][]string }

func (r *cmdRecorder) run(_ context.Context, name string, args ...string) (*exec.Result, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return &exec.Result{}, nil
}

func stubRunCmd(t *testing.T) *cmdRecorder {
	t.Helper()
	rec := &cmdRecorder{}
	old := runCmd
	runCmd = rec.run
	t.Cleanup(func() { runCmd = old })
	return rec
}

// okDoer — успешная HTTP-проба без сети.
type okDoer struct{}

func (okDoer) Do(_ context.Context, _ httpclient.CallConfig) (*httpclient.Result, error) {
	return &httpclient.Result{Metrics: httpclient.Metrics{HTTPCode: 204}}, nil
}

func newKernelSensorService(t *testing.T) (*Service, *tunnelMonitor, *checkConfig, *cmdRecorder) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID:   "awg7",
		Name: "Нидерланды",
		// Endpoint — IP-литерал: tryResolveEndpoint не ходит в DNS.
		Peer: storage.AWGPeer{PublicKey: "pk-awg7", Endpoint: "198.51.100.7:51820"},
		PingCheck: &storage.TunnelPingCheck{
			Enabled: true, Method: "http", Interval: 1, FailThreshold: 3,
		},
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lb := NewLogBuffer()
	t.Cleanup(lb.Stop)
	// handshakeTimeout меньше handshakePollFreq: waitHandshake отдаёт false до
	// первого опроса wg — тест не зависит от wg.Show. Закрытый stopCh мгновенно
	// выводит и waitHandshake, и backoff-select после лечения (иначе +1 с на прогон).
	s := &Service{tunnels: store, wg: &spyWGClient{}, logBuffer: lb, ctx: ctx, handshakeTimeout: time.Millisecond}
	config := s.getCheckConfig("awg7")
	if config == nil || config.FailThreshold != 3 {
		t.Fatalf("config = %+v, ожидали порог 3", config)
	}
	stopCh := make(chan struct{})
	close(stopCh)
	m := &tunnelMonitor{tunnelID: "awg7", tunnelName: "Нидерланды", stopCh: stopCh}
	return s, m, config, stubRunCmd(t)
}

// Kernel-туннель: ровно на третьем провале — link down/up, счётчик обнуляется,
// четвёртый провал лечение не повторяет.
func TestSensorTick_KernelTogglesLinkExactlyAtThreshold(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, rec := newKernelSensorService(t)

	s.sensorTick(m, config)
	s.sensorTick(m, config)
	if len(rec.calls) != 0 {
		t.Fatalf("до порога команд быть не должно: %v", rec.calls)
	}
	s.sensorTick(m, config)
	want := [][]string{
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "down"},
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "up"},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("лечение на 3-м провале:\n got %v\nwant %v", rec.calls, want)
	}
	s.sensorTick(m, config)
	if len(rec.calls) != 2 {
		t.Fatalf("лечение обнуляет счётчик — 4-й провал не должен лечить снова: %v", rec.calls)
	}
	s.sensorTick(m, config)
	if len(rec.calls) != 2 {
		t.Fatalf("5-й провал (второй после лечения) ещё не должен лечить: %v", rec.calls)
	}
	s.sensorTick(m, config)
	wantAgain := [][]string{
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "down"},
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "up"},
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "down"},
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "up"},
	}
	if !reflect.DeepEqual(rec.calls, wantAgain) {
		t.Fatalf("лечение обязано повториться на 6-м провале — счётчик после лечения обнулён ПОЛНОСТЬЮ, а не до 1:\n got %v\nwant %v", rec.calls, wantAgain)
	}
}

// Успех между провалами обнуляет счётчик: 2 провала + успех + 2 провала = без лечения.
func TestSensorTick_SuccessResetsFailCount(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	s, m, config, rec := newKernelSensorService(t)

	httpprobe.Client = alwaysFailDoer{}
	s.sensorTick(m, config)
	s.sensorTick(m, config)
	httpprobe.Client = okDoer{}
	s.sensorTick(m, config)
	httpprobe.Client = alwaysFailDoer{}
	s.sensorTick(m, config)
	s.sensorTick(m, config)
	if len(rec.calls) != 0 {
		t.Fatalf("успех обязан обнулить счётчик, лечения не ждём: %v", rec.calls)
	}
}

// Дефолт шва — настоящий резолв: иначе лечение перестало бы переносить
// endpoint на новый адрес, а тесты этого не заметили бы.
func TestResolveEndpointDefault_IsTryResolveEndpoint(t *testing.T) {
	if reflect.ValueOf(resolveEndpoint).Pointer() != reflect.ValueOf(tryResolveEndpoint).Pointer() {
		t.Fatal("resolveEndpoint по умолчанию обязан быть tryResolveEndpoint")
	}
}

func stubResolveEndpoint(t *testing.T, fn func(string) string) {
	t.Helper()
	old := resolveEndpoint
	resolveEndpoint = fn
	t.Cleanup(func() { resolveEndpoint = old })
}

// Переехавший endpoint переприменяется РОВНО между down и up: раньше — модуль
// паникует на живом интерфейсе, позже — рукопожатие уйдёт на старый адрес.
func TestSensorTick_ReappliesEndpointBetweenDownAndUp(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, rec := newKernelSensorService(t)
	stubResolveEndpoint(t, func(ep string) string {
		if ep != "198.51.100.7:51820" {
			t.Fatalf("резолву отдали %q — не endpoint из записи туннеля", ep)
		}
		return "203.0.113.9:51820"
	})

	s.sensorTick(m, config)
	s.sensorTick(m, config)
	s.sensorTick(m, config)

	want := [][]string{
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "down"},
		{"/opt/sbin/awg", "set", "opkgtun7", "peer", "pk-awg7", "endpoint", "203.0.113.9:51820"},
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "up"},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("лечение с переехавшим endpoint:\n got %v\nwant %v", rec.calls, want)
	}
}

// `awg set` по интерфейсу, который не удалось опустить, роняет ядро на модулях
// старше 3.0.20260731-04 — при отказе down переприменения быть не должно,
// а поднять интерфейс всё равно обязаны.
func TestSensorTick_SkipsEndpointReapplyWhenLinkDownFailed(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, _ := newKernelSensorService(t)
	stubResolveEndpoint(t, func(string) string { return "203.0.113.9:51820" })

	var calls [][]string
	old := runCmd
	runCmd = func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 3 && args[3] == "down" {
			return nil, errors.New("injected: link down failed")
		}
		return &exec.Result{}, nil
	}
	t.Cleanup(func() { runCmd = old })

	s.sensorTick(m, config)
	s.sensorTick(m, config)
	s.sensorTick(m, config)

	want := [][]string{
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "down"},
		{"/opt/sbin/ip", "link", "set", "opkgtun7", "up"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("после отказа down:\n got %v\nwant %v", calls, want)
	}
}
