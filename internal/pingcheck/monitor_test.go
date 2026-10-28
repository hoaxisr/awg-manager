package pingcheck

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

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
	rec := stubRunCmd(t)
	stubDeviceExists(t, true)
	stubIfaceUp(t, true)
	// Endpoint из записи пустой, поэтому команда лечения — только если резолв
	// что-то вернёт; отдаём адрес, чтобы пропуск гарда был виден в rec.
	stubResolveEndpoint(t, func(string) string { return "203.0.113.9:51820" })
	s := &Service{tunnels: store, logBuffer: lb, ctx: ctx}

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
	if m.restartCount != 0 {
		t.Fatalf("restartCount = %d, ожидали 0: восстановление не наше", m.restartCount)
	}
	if transitions != "" {
		t.Fatalf("в журнале есть переход %q — восстановление отработало", transitions)
	}
	// Прямой пин: при мутации гарда proxyOwned лечение дошло бы до awg set.
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
	s := &Service{tunnels: store, logBuffer: lb, ctx: ctx}
	config := s.getCheckConfig("awg7")
	if config == nil || config.FailThreshold != 3 {
		t.Fatalf("config = %+v, ожидали порог 3", config)
	}
	stopCh := make(chan struct{})
	close(stopCh)
	m := &tunnelMonitor{tunnelID: "awg7", tunnelName: "Нидерланды", stopCh: stopCh}
	stubIfaceUp(t, true)
	return s, m, config, stubRunCmd(t)
}

// repairs — сколько раз монитор лечил: запись перехода «link_toggle».
func repairs(s *Service) int {
	n := 0
	for _, e := range s.logBuffer.GetAll() {
		if e.StateChange == "link_toggle" {
			n++
		}
	}
	return n
}

// Kernel-туннель: ровно на третьем провале — лечение, счётчик обнуляется,
// четвёртый и пятый провал лечение не повторяют, шестой — повторяет.
// Интерфейс при этом не трогается вовсе (F746).
func TestSensorTick_KernelRepairsExactlyAtThreshold(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, rec := newKernelSensorService(t)

	s.sensorTick(m, config)
	s.sensorTick(m, config)
	if n := repairs(s); n != 0 {
		t.Fatalf("до порога лечения быть не должно: %d", n)
	}
	s.sensorTick(m, config)
	if n := repairs(s); n != 1 || m.restartCount != 1 {
		t.Fatalf("лечение на 3-м провале: repairs=%d restartCount=%d", n, m.restartCount)
	}
	s.sensorTick(m, config)
	s.sensorTick(m, config)
	if n := repairs(s); n != 1 {
		t.Fatalf("4-й и 5-й провал не должны лечить снова: %d", n)
	}
	s.sensorTick(m, config)
	if n := repairs(s); n != 2 {
		t.Fatalf("лечение обязано повториться на 6-м провале — счётчик обнулён ПОЛНОСТЬЮ: %d", n)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("endpoint — IP, команд быть не должно, тем более ip link: %v", rec.calls)
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

// Переехавший endpoint переприменяется на поднятом интерфейсе — без
// `ip link down/up` (стенд 27.10: рукопожатие за ~6 с).
func TestSensorTick_ReappliesEndpointWithoutLinkToggle(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, rec := newKernelSensorService(t)
	stubDeviceExists(t, true)
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
		{"/opt/sbin/awg", "set", "opkgtun7", "peer", "pk-awg7", "endpoint", "203.0.113.9:51820"},
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Fatalf("лечение с переехавшим endpoint:\n got %v\nwant %v", rec.calls, want)
	}
}

// `awg set` по отсутствующему интерфейсу роняет ядро на модулях старше
// 3.0.20260731-04 — без устройства переприменения быть не должно.
func TestSensorTick_SkipsEndpointReapplyWhenDeviceGone(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, rec := newKernelSensorService(t)
	stubDeviceExists(t, false)
	stubResolveEndpoint(t, func(string) string { return "203.0.113.9:51820" })

	s.sensorTick(m, config)
	s.sensorTick(m, config)
	s.sensorTick(m, config)

	if len(rec.calls) != 0 {
		t.Fatalf("устройства нет — команд быть не должно: %v", rec.calls)
	}
	if repairs(s) != 1 {
		t.Fatal("лечение (политики, счётчик) идёт и без устройства")
	}
}

func stubDeviceExists(t *testing.T, v bool) {
	t.Helper()
	old := deviceExists
	deviceExists = func(string) bool { return v }
	t.Cleanup(func() { deviceExists = old })
}

func stubIfaceUp(t *testing.T, v bool) {
	t.Helper()
	old := ifaceUp
	ifaceUp = func(string) bool { return v }
	t.Cleanup(func() { ifaceUp = old })
}

// #855: провал до порога → лечение → проверка снова успешна. Туннель
// обязан выйти из failedSet dnsroute-фейловера.
func TestSensorTick_SuccessAfterRepairRecoversFailover(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	s, m, config, _ := newKernelSensorService(t)
	bus := events.NewBus()
	s.bus = bus
	fm := dnsroute.NewFailoverManager(nil)
	fm.StartListener(bus)
	t.Cleanup(fm.StopListener)

	httpprobe.Client = alwaysFailDoer{}
	for i := 0; i < 3; i++ {
		s.sensorTick(m, config)
	}
	waitFor(t, func() bool { return fm.IsFailed("awg7") }, "после порога туннель должен попасть в failedSet")

	httpprobe.Client = okDoer{}
	s.sensorTick(m, config)
	waitFor(t, func() bool { return !fm.IsFailed("awg7") }, "успешная проверка после лечения обязана вывести туннель из failedSet")
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	// Окно — потолок, а не ожидание: условие проверяется каждые 5 мс и
	// возвращает сразу. 500 мс не хватало горутине взять лок под -race на
	// нагруженной машине (F262).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// check-now не блокирует вызывающего и не запускает второй тик поверх идущего
// (лечение/backoff держат tickMu): занятый монитор пропускается.
func TestCheckAllNow_SkipsBusyMonitorAndReturnsAtOnce(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = okDoer{}
	s, m, _, _ := newKernelSensorService(t)
	m.stopCh = make(chan struct{})
	s.monitors = map[string]*tunnelMonitor{"awg7": m}
	s.running = true

	m.tickMu.Lock() // тик «в процессе»
	done := make(chan struct{})
	go func() { s.CheckAllNow(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("CheckAllNow обязан вернуться сразу, а не ждать занятый монитор")
	}
	time.Sleep(50 * time.Millisecond)
	if !m.lastCheck.IsZero() {
		t.Fatal("занятый монитор не должен получить второй тик")
	}
	m.tickMu.Unlock()

	s.CheckAllNow()
	waitFor(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return !m.lastCheck.IsZero() },
		"свободный монитор обязан выполнить проверку по check-now")
}

// blockingDoer держит HTTP-проверку, пока тест не отпустит release.
type blockingDoer struct{ release chan struct{} }

func (d blockingDoer) Do(_ context.Context, _ httpclient.CallConfig) (*httpclient.Result, error) {
	<-d.release
	return &httpclient.Result{Metrics: httpclient.Metrics{HTTPCode: 204}}, nil
}

// StopMonitoring ждёт горутину check-now (она учтена в m.wg) — после
// возврата никакой тик по этому монитору уже не идёт.
func TestStopMonitoring_WaitsForCheckNow(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	release := make(chan struct{})
	httpprobe.Client = blockingDoer{release: release}
	s, m, _, _ := newKernelSensorService(t)
	m.stopCh = make(chan struct{})
	s.monitors = map[string]*tunnelMonitor{"awg7": m}
	s.running = true

	s.CheckAllNow()
	waitFor(t, func() bool {
		if m.tickMu.TryLock() {
			m.tickMu.Unlock()
			return false
		}
		return true
	}, "check-now обязан взять tickMu")

	stopped := make(chan struct{})
	go func() { s.StopMonitoring("awg7"); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("StopMonitoring вернулся, пока check-now ещё в проверке")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("StopMonitoring не дождался check-now")
	}
	if m.tickMu.TryLock() {
		m.tickMu.Unlock()
	} else {
		t.Fatal("после StopMonitoring tickMu всё ещё занят")
	}
}

// Лечение само по себе не восстановление: до успешной проверки статус
// «recovering» и ни одного pass (F747 — прежде pass давало рукопожатие при
// мёртвом трафике). Первая успешная проверка — «alive» и переход «recovered».
func TestSensorTick_RecoveringUntilSuccessfulCheck(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, _ := newKernelSensorService(t)
	s.monitors = map[string]*tunnelMonitor{"awg7": m}
	bus := events.NewBus()
	s.bus = bus
	_, ch, unsub := bus.Subscribe()
	t.Cleanup(unsub)

	for i := 0; i < 3; i++ {
		s.sensorTick(m, config)
	}
	if got := s.GetTunnelPingStatus("awg7").Status; got != "recovering" {
		t.Fatalf("после лечения без успешной проверки статус %q, want recovering", got)
	}
	for drained := false; !drained; {
		select {
		case ev := <-ch:
			if ev.Type == "pingcheck:state" && ev.Data.(events.PingCheckStateEvent).Status == "pass" {
				t.Fatal("pass без успешной проверки")
			}
		default:
			drained = true
		}
	}

	httpprobe.Client = okDoer{}
	s.sensorTick(m, config)
	if got := s.GetTunnelPingStatus("awg7").Status; got != "alive" {
		t.Fatalf("после успешной проверки статус %q, want alive", got)
	}
	if last := s.logBuffer.GetAll()[0]; last.StateChange != "recovered" || !last.Success { // от новых к старым
		t.Fatalf("последняя запись %+v, want recovered", last)
	}
}

// Страница мониторинга показывает время последней проверки, задержку и счётчики
// — они меняются на КАЖДОЙ проверке, а не только при смене состояния. Пока
// публиковалась одна смена состояния, странице приходилось держать таймер
// опроса (F354); теперь таймера нет, и единственный источник свежести —
// эта публикация (F364).
func TestSensorTick_PublishesInvalidationOnEveryCheck(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = alwaysFailDoer{}
	s, m, config, _ := newKernelSensorService(t)

	bus := events.NewBus()
	s.bus = bus
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	// Две неудачные проверки — до порога лечения, то есть смены состояния НЕТ.
	s.sensorTick(m, config)
	s.sensorTick(m, config)

	got := 0
	deadline := time.After(2 * time.Second)
	for got < 2 {
		select {
		case e := <-ch:
			if e.Type == "resource:invalidated" {
				got++
			}
		case <-deadline:
			t.Fatalf("подсказок инвалидации пришло %d из 2: страница мониторинга замрёт", got)
		}
	}
}

// appLogRecorder — журнал приложения в памяти.
type appLogRecorder struct{ msgs []string }

func (r *appLogRecorder) AppLog(_ logging.Level, _, _, _, _, message string) {
	r.msgs = append(r.msgs, message)
}

// Опущенный линк монитор не проверяет, но и не молчит: переход вниз и обратно
// пишется в журнал ровно по разу, а не на каждом тике (ревью F750).
func TestSensorTick_LinkTransitionsLoggedOnce(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	httpprobe.Client = okDoer{}
	s, m, config, _ := newKernelSensorService(t)
	rec := &appLogRecorder{}
	s.appLog = logging.NewScopedLogger(rec, "test", "pingcheck")

	stubIfaceUp(t, false)
	for range 3 {
		s.sensorTick(m, config)
	}
	stubIfaceUp(t, true)
	for range 2 {
		s.sensorTick(m, config)
	}
	want := []string{"Link is down, checks paused until it is up", "Link is up, checks resumed"}
	if !reflect.DeepEqual(rec.msgs, want) {
		t.Fatalf("журнал = %q, want %q", rec.msgs, want)
	}
}

// «Проверить сейчас» на опущенном линке отвечает строкой в журнале, а не
// тишиной, и проверку не запускает.
func TestCheckNow_LinkDownLogged(t *testing.T) {
	s, m, config, _ := newKernelSensorService(t)
	rec := &appLogRecorder{}
	s.appLog = logging.NewScopedLogger(rec, "test", "pingcheck")
	m.stopCh = make(chan struct{})
	stubIfaceUp(t, false)

	s.checkNowAsync(m, config)
	m.wg.Wait()
	if !reflect.DeepEqual(rec.msgs, []string{"Check skipped: link is down"}) {
		t.Fatalf("журнал = %q", rec.msgs)
	}
	if len(s.logBuffer.GetAll()) != 0 {
		t.Fatal("проверка опущенного линка попала в журнал мониторинга")
	}
}

// Время записи в SSE — той же строкой, что в снимке GET /pingcheck/logs
// (time.Time в JSON): по ней фронт склеивает одну запись из двух источников и
// различает записи одной секунды (F751).
func TestAddLogEntry_SSETimestampMatchesSnapshot(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	bus := events.NewBus()
	s.bus = bus
	_, ch, unsub := bus.Subscribe()
	t.Cleanup(unsub)

	ts := time.Date(2026, 10, 28, 19, 14, 49, 123456789, time.FixedZone("MSK", 3*3600))
	s.addLogEntry(LogEntry{Timestamp: ts, TunnelID: "awg7"})

	snap, err := json.Marshal(s.logBuffer.GetAll()[0].Timestamp)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		got := ev.Data.(events.PingCheckLogEvent).Timestamp
		if `"`+got+`"` != string(snap) {
			t.Fatalf("SSE %q ≠ снимок %s", got, snap)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pingcheck:log не опубликован")
	}
}
