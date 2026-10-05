package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Стенд 01.10, KN-1810 5.01.C.6: 16 замеров опоздания хуков под churn —
// 14–30 с, максимум ≈30 с. Окно = максимум × 1,5. Вернуть 15/20 с — хук,
// пришедший на 20-й секунде, снова примут за внешнюю грань.
// Поведение, а не только значения: свой хук, опоздавший на 44 с, поглощается
// ожиданием; чужая грань на 44-й секунде после подъёма — окном. Мутации
// «TTL ожидания не из константы (15 с)» и «окно в updateState 15 с» → красный.
func TestHookWindows_CoverMeasuredLag(t *testing.T) {
	if expectedHookTTL != 45*time.Second {
		t.Errorf("expectedHookTTL = %s, want 45s", expectedHookTTL)
	}
	if bootQuiescenceWindow != 45*time.Second {
		t.Errorf("bootQuiescenceWindow = %s, want 45s", bootQuiescenceWindow)
	}
	const lag = 44 * time.Second

	// Ожидание: свой disabled зарегистрирован в момент подъёма, хук пришёл
	// через lag — поглощён до окна: ни метки, ни перепроверки.
	r := newHookWindowRig(t)
	r.o.ExpectHook("OpkgTun10", "disabled")
	r.disabledEdge(t, lag)
	if !r.absorbedAt().IsZero() || r.sched.count() != 0 {
		t.Fatalf("свой хук с опозданием %s не поглощён ожиданием: метка=%v schedule=%d",
			lag, r.absorbedAt(), r.sched.count())
	}

	// Окно: без ожидания та же грань на 44-й секунде поглощается окном.
	r = newHookWindowRig(t)
	r.disabledEdge(t, lag)
	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("грань на %s после подъёма остановила туннель: stops=%d", lag, n)
	}
	if !r.absorbedAt().Equal(r.base.Add(lag)) {
		t.Fatalf("грань на %s не поглощена окном: метка=%v", lag, r.absorbedAt())
	}
}

// M2: грань в последние секунды окна. Перепроверка на конце окна без
// выдержки settle пробовала бы NDMS посреди рестарта интерфейса (#667:
// disabled→running за ~2 с) и остановила бы туннель. Срок — не раньше
// confSettleDelay от грани; running на 46-й секунде успевает.
// Мутация «recheckDue = quiescentUntil» → таймер на 45-й, проба, stop → красный.
func TestConfDisabled_EdgeAtWindowEnd_WaitsSettleBeforeProbe(t *testing.T) {
	r := newHookWindowRig(t)
	edgeAt := bootQuiescenceWindow - time.Second
	runningAt := bootQuiescenceWindow + time.Second
	r.disabledEdge(t, edgeAt)
	fn := r.sched.fn(t, 0)
	if r.sched.delays[0] != confSettleDelay {
		t.Errorf("перепроверка через %s, want %s (выдержка settle от грани)", r.sched.delays[0], confSettleDelay)
	}

	// События в порядке времени: таймер раньше running — сначала таймер.
	fire := r.base.Add(edgeAt + r.sched.delays[0])
	sendRunning := func() {
		r.clk.Set(r.base.Add(runningAt))
		if err := r.o.HandleEvent(context.Background(), confHookName("OpkgTun10", "running")); err != nil {
			t.Fatalf("HandleEvent running: %v", err)
		}
	}
	if fire.Before(r.base.Add(runningAt)) {
		r.clk.Set(fire)
		fn()
		sendRunning()
	} else {
		sendRunning()
		r.clk.Set(fire)
		fn()
	}

	if n := r.probe.calls.Load(); n != 0 {
		t.Fatalf("проба посреди рестарта интерфейса: calls=%d", n)
	}
	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("ложная остановка у края окна: stops=%d", n)
	}
	if !mustGet(t, r.store, "awg10").Enabled {
		t.Fatal("Enabled снят")
	}
}

// deleteThenReborn — M1: прежнее воплощение awg10 (OpkgTun10) удалено,
// оставив ожидания (их регистрирует ops.Delete/отказавший Start; болванка
// оператора этого не делает — регистрируем здесь же), затем через 5 с имя
// занимает новый туннель. Возвращает риг на моменте base+5 с без туннеля в
// кэше.
func deleteThenReborn(t *testing.T, levels ...string) *hookWindowRig {
	t.Helper()
	r := newHookWindowRig(t)
	r.o.mu.Lock()
	r.o.state.tunnels["awg10"].Running = false
	r.o.mu.Unlock()
	for _, l := range levels {
		r.o.ExpectHook("OpkgTun10", l)
	}
	r.o.updateState(Action{Type: ActionDeleteKernel, Tunnel: "awg10"})
	r.clk.Set(r.base.Add(5 * time.Second))
	return r
}

// M1: Delete выключенной записи — NDMS может не прислать conf=disabled, токен
// живёт 45 с. Новый туннель на том же OpkgTun10 запущен; внешнее выключение
// в эти 45 с обязано дойти до окна и П13 (остановка, Enabled=false по Q1), а
// не исчезнуть в чужом токене. Мутация «не сравнивать at с bornAt» → грань
// поглощена токеном → перепроверки нет → красный; так же «bornAt не ставить
// в ensureTunnel» и «не переносить bornAt в RefreshTunnelState».
func TestExpectedHook_PreviousIncarnation_DoesNotSwallowDisabled(t *testing.T) {
	r := deleteThenReborn(t, "disabled", "destroyed")
	if err := r.o.HandleEvent(context.Background(), Event{Type: EventStart, Tunnel: "awg10"}); err != nil {
		t.Fatalf("Start нового туннеля: %v", err)
	}
	if !r.tunnel().Running {
		t.Fatal("новый туннель не поднят")
	}
	r.o.RefreshTunnelState("awg10") // правка записи не обнуляет bornAt

	r.disabledEdge(t, 15*time.Second) // токен прежнего ещё жив (до base+45 с)
	if r.absorbedAt().IsZero() {
		t.Fatal("внешняя грань нового туннеля поглощена токеном прежнего воплощения")
	}
	r.clk.Set(r.base.Add(5*time.Second + bootQuiescenceWindow))
	r.sched.fn(t, 0)()
	if n := r.probe.calls.Load(); n != 1 {
		t.Fatalf("проба вызвана %d раз, want 1", n)
	}
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
	if mustGet(t, r.store, "awg10").Enabled {
		t.Fatal("Enabled не снят: внешний стоп обязан персиститься (Q1)")
	}
}

// M1, отказавший Start: прежнее воплощение оставило токен running. Новый
// туннель на том же имени в кэше (правка записи → RefreshTunnelState), не
// запущен; внешнее включение в роутере обязано его поднять (#183), а не
// исчезнуть в чужом токене. Мутации «не сравнивать at с bornAt» и «bornAt не
// ставить для новой записи в RefreshTunnelState» → coldStarts не растёт → красный.
func TestExpectedHook_PreviousIncarnationFailedStart_DoesNotSwallowRunning(t *testing.T) {
	r := newHookWindowRig(t)
	r.o.mu.Lock()
	r.o.state.tunnels["awg10"].Running = false
	r.o.mu.Unlock()
	r.o.state.anyWANUpFn = func() bool { return true }
	r.op.coldStartErr = errors.New("ndms refused")
	r.o.ExpectHook("OpkgTun10", "running") // InterfaceUp отказавшего Start
	if err := r.o.HandleEvent(context.Background(), Event{Type: EventStart, Tunnel: "awg10"}); err == nil {
		t.Fatal("Start прежнего воплощения должен был отказать")
	}
	r.o.ExpectHook("OpkgTun10", "disabled")
	r.o.ExpectHook("OpkgTun10", "destroyed")
	r.o.updateState(Action{Type: ActionDeleteKernel, Tunnel: "awg10"})
	r.op.coldStartErr = nil
	starts := r.op.coldStarts.Load()

	r.clk.Set(r.base.Add(5 * time.Second))
	r.o.RefreshTunnelState("awg10") // новый туннель на OpkgTun10
	r.probe.up = true               // NDMS действительно держит интерфейс включённым
	r.clk.Set(r.base.Add(10 * time.Second))
	if err := r.o.HandleEvent(context.Background(), confHookName("OpkgTun10", "running")); err != nil {
		t.Fatalf("HandleEvent running: %v", err)
	}
	if n := r.op.coldStarts.Load() - starts; n != 1 {
		t.Fatalf("внешнее включение нового туннеля поглощено токеном прежнего: coldStarts+%d", n)
	}
	if !r.tunnel().Running {
		t.Fatal("туннель не поднят")
	}
}

// Своё ожидание нового воплощения (зарегистрировано после появления в кэше)
// поглощается как прежде: граница bornAt не задевает свой Start.
func TestExpectedHook_OwnIncarnation_StillConsumed(t *testing.T) {
	r := deleteThenReborn(t)
	r.o.RefreshTunnelState("awg10")
	r.o.updateState(Action{Type: ActionColdStartKernel, Tunnel: "awg10"})
	r.o.ExpectHook("OpkgTun10", "disabled") // свой Stop нового туннеля
	r.disabledEdge(t, 15*time.Second)
	if !r.absorbedAt().IsZero() || r.sched.count() != 0 {
		t.Fatalf("своё ожидание не поглотило грань: метка=%v schedule=%d", r.absorbedAt(), r.sched.count())
	}
}

// fakeClock — часы теста; читаются и из горутины перепроверки.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

// capturedSchedule подменяет time.AfterFunc: запоминает задержку и функцию,
// вызывает их тест.
type capturedSchedule struct {
	mu     sync.Mutex
	delays []time.Duration
	fns    []func()
}

func (s *capturedSchedule) schedule(d time.Duration, fn func()) {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.fns = append(s.fns, fn)
	s.mu.Unlock()
}

func (s *capturedSchedule) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.fns)
}

func (s *capturedSchedule) fn(t *testing.T, i int) func() {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.fns) {
		t.Fatalf("перепроверка #%d не запланирована (запланировано %d)", i, len(s.fns))
	}
	return s.fns[i]
}

type confProbe struct {
	calls atomic.Int64
	up    bool
	err   error
}

func (p *confProbe) probe(context.Context, string) (bool, error) {
	p.calls.Add(1)
	return p.up, p.err
}

type hookWindowRig struct {
	o     *Orchestrator
	op    *fakeKernelOp
	store *storage.AWGTunnelStore
	clk   *fakeClock
	sched *capturedSchedule
	probe *confProbe
	base  time.Time
}

// newHookWindowRig — kernel-туннель awg10 (OpkgTun10), только что поднятый
// в момент base: окно quiescence ставит сам updateState, как в проде.
func newHookWindowRig(t *testing.T) *hookWindowRig {
	t.Helper()
	rec := &storage.AWGTunnel{ID: "awg10", Name: "g", Backend: "kernel", Enabled: true}
	rec.Peer.Endpoint = "203.0.113.5:51820"
	store := lifecycleStore(t, rec)
	op := &fakeKernelOp{}
	base := time.Unix(100000, 0)
	clk := &fakeClock{now: base}
	sched := &capturedSchedule{}
	pr := &confProbe{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel(),
		clock: clk.Now, schedule: sched.schedule}
	o.SetConfLayerProbe(pr.probe)
	o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel", Enabled: true}
	o.updateState(Action{Type: ActionColdStartKernel, Tunnel: "awg10"})
	return &hookWindowRig{o: o, op: op, store: store, clk: clk, sched: sched, probe: pr, base: base}
}

func (r *hookWindowRig) disabledEdge(t *testing.T, at time.Duration) {
	t.Helper()
	r.clk.Set(r.base.Add(at))
	if err := r.o.HandleEvent(context.Background(), confHookName("OpkgTun10", "disabled")); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
}

func (r *hookWindowRig) tunnel() *tunnelState {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	return r.o.state.tunnels["awg10"]
}

func (r *hookWindowRig) absorbedAt() time.Time {
	r.o.mu.Lock()
	defer r.o.mu.Unlock()
	return r.o.state.tunnels["awg10"].absorbedDisabledAt
}

func (r *hookWindowRig) expire() {
	r.clk.Set(r.base.Add(bootQuiescenceWindow))
}

func confHookName(name, level string) Event {
	return Event{Type: EventNDMSHook, NDMSName: name, Layer: "conf", Level: level}
}

// П13: внешняя грань conf=disabled внутри окна не теряется — при истечении
// окна NDMS спрашивают один раз, down → остановка как при внешнем стопе (Q1).
// Мутация «не планировать» → fn не захвачен → красный.
func TestConfDisabled_AbsorbedInWindow_RecheckedAtExpiry_Stops(t *testing.T) {
	r := newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)

	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("грань в окне остановила туннель: stops=%d", n)
	}
	if !r.absorbedAt().Equal(r.base.Add(10 * time.Second)) {
		t.Fatalf("метка поглощённой грани = %v", r.absorbedAt())
	}
	fn := r.sched.fn(t, 0)
	if want := bootQuiescenceWindow - 10*time.Second; r.sched.delays[0] != want {
		t.Fatalf("перепроверка через %s, want %s (остаток окна)", r.sched.delays[0], want)
	}

	r.expire()
	fn()

	if n := r.probe.calls.Load(); n != 1 {
		t.Fatalf("проба вызвана %d раз, want 1", n)
	}
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
	if mustGet(t, r.store, "awg10").Enabled {
		t.Fatal("Enabled не снят: внешний стоп обязан персиститься (Q1)")
	}
	if !r.absorbedAt().IsZero() {
		t.Fatal("метка не снята после остановки")
	}
}

// M1′: обновление записи из стора между гранью и истечением окна не теряет
// ни метку, ни флаг запланированной перепроверки.
func TestConfDisabled_RecheckSurvivesRefreshTunnelState(t *testing.T) {
	// Метка: грань → обновление записи → истечение окна → остановка.
	r := newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)
	r.o.RefreshTunnelState("awg10")
	r.expire()
	r.sched.fn(t, 0)()
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1: метка потеряна в RefreshTunnelState", n)
	}

	// Флаг: новая грань после обновления записи второй таймер не ставит.
	r = newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)
	r.o.RefreshTunnelState("awg10")
	r.disabledEdge(t, 12*time.Second)
	if n := r.sched.count(); n != 1 {
		t.Fatalf("schedule вызван %d раз, want 1: флаг потерян в RefreshTunnelState", n)
	}
}

func TestConfDisabled_RecheckProbeUp_NoStop(t *testing.T) {
	r := newHookWindowRig(t)
	r.probe.up = true
	r.disabledEdge(t, 10*time.Second)
	r.expire()
	r.sched.fn(t, 0)()

	if n := r.probe.calls.Load(); n != 1 {
		t.Fatalf("проба вызвана %d раз, want 1", n)
	}
	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("NDMS держит up, а туннель остановлен: stops=%d", n)
	}
	if !r.absorbedAt().IsZero() {
		t.Fatal("метка не снята")
	}
	if !mustGet(t, r.store, "awg10").Enabled {
		t.Fatal("Enabled снят")
	}
}

// conf=running после грани — NDMS сам вернул интерфейс: 0 RCI, остановки нет.
// Мутация «игнорировать lastConfRunningAt» → проба вызвана → красный.
func TestConfDisabled_RecheckAfterBounce_NoProbe(t *testing.T) {
	r := newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)
	r.clk.Set(r.base.Add(12 * time.Second))
	if err := r.o.HandleEvent(context.Background(), confHookName("OpkgTun10", "running")); err != nil {
		t.Fatalf("HandleEvent running: %v", err)
	}
	r.expire()
	r.sched.fn(t, 0)()

	if n := r.probe.calls.Load(); n != 0 {
		t.Fatalf("после conf=running проба не нужна: calls=%d", n)
	}
	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("stops=%d", n)
	}
	if !r.absorbedAt().IsZero() {
		t.Fatal("метка не снята")
	}
}

// Грань, отличная от conf=disabled, перепроверку не планирует.
// Мутация «не смотреть на Level в ветке подавления» → conf=running работающего
// туннеля в окне планирует перепроверку → красный.
func TestConfDisabled_NoAbsorbedEdge_NoSchedule(t *testing.T) {
	r := newHookWindowRig(t)
	r.clk.Set(r.base.Add(10 * time.Second))
	if err := r.o.HandleEvent(context.Background(), confHookName("OpkgTun10", "running")); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if n := r.sched.count(); n != 0 {
		t.Fatalf("без поглощённой грани перепроверка не планируется: schedule=%d", n)
	}
	if !r.absorbedAt().IsZero() {
		t.Fatal("метка поставлена без грани conf=disabled")
	}
}

// R31: NDMS не прочитан — на неопределённости не останавливаем.
func TestConfDisabled_RecheckProbeError_LeavesTunnel(t *testing.T) {
	r := newHookWindowRig(t)
	r.probe.err = errors.New("rci timeout")
	r.disabledEdge(t, 10*time.Second)
	r.expire()
	r.sched.fn(t, 0)()

	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("stops=%d на ошибке пробы", n)
	}
	if !r.tunnel().Running {
		t.Fatal("туннель снят с Running")
	}
	if !mustGet(t, r.store, "awg10").Enabled {
		t.Fatal("Enabled снят на ошибке пробы")
	}
}

// Restart внутри окна (через HandleEvent, как из API) продлевает окно и
// сбрасывает метку; новая грань ставит второй таймер. Первый на старом сроке
// переносит себя, на конце нового окна — одна проба и одна остановка.
// Мутация «не переносить при now < quiescentUntil» → первый таймер пробует и
// останавливает внутри нового окна → красный.
func TestConfDisabled_RecheckWindowExtended_Reschedules(t *testing.T) {
	r := newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)

	r.clk.Set(r.base.Add(20 * time.Second))
	if err := r.o.HandleEvent(context.Background(), Event{Type: EventRestart, Tunnel: "awg10"}); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if n := r.op.coldStarts.Load(); n != 1 {
		t.Fatalf("Restart не дошёл до ColdStart: coldStarts=%d", n)
	}
	r.op.stops.Store(0) // Stop самого Restart не в счёт
	if !r.absorbedAt().IsZero() {
		t.Fatal("подъём не сбросил метку прошлого окна")
	}
	r.disabledEdge(t, 25*time.Second) // новое окно до base+65 с
	if n := r.sched.count(); n != 2 {
		t.Fatalf("schedule=%d, want 2 (флаг сброшен подъёмом)", n)
	}

	r.expire() // base+45 с — срок первого таймера
	r.sched.fn(t, 0)()
	if n := r.probe.calls.Load(); n != 0 {
		t.Fatalf("внутри продлённого окна проба не нужна: calls=%d", n)
	}
	if n := r.op.stops.Load(); n != 0 {
		t.Fatalf("остановка внутри продлённого окна: stops=%d", n)
	}
	if n := r.sched.count(); n != 3 || r.sched.delays[2] != 20*time.Second {
		t.Fatalf("перенос: schedule=%d delays=%v, want 3 и 20s", n, r.sched.delays)
	}

	r.clk.Set(r.base.Add(65 * time.Second))
	r.sched.fn(t, 1)()
	r.sched.fn(t, 2)()
	if n := r.probe.calls.Load(); n != 1 {
		t.Fatalf("calls=%d, want 1 на два таймера", n)
	}
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
}

// Гонка штампов: conf=running пришёл раньше disabled, но застрял в
// awaitTunnelIdle (замок держит наша операция) и штамповался бы ПОСЛЕ
// поглощения disabled — грань сошла бы за «bounce», туннель остался бы
// Running при выключенном интерфейсе. Штамп — момент прихода.
// Мутация «штамп после ожиданий (nowFn)» → проба не вызвана → красный.
func TestConfDisabled_RunningQueuedBeforeEdge_NotBounce(t *testing.T) {
	r := newHookWindowRig(t)
	if err := r.o.lockTunnel(context.Background(), "awg10", "test"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	running := confHookName("OpkgTun10", "running")
	running.Now = r.base.Add(10 * time.Second)
	done := make(chan struct{})
	go func() {
		_ = r.o.HandleEvent(context.Background(), running)
		close(done)
	}()
	r.disabledEdge(t, 12*time.Second)
	r.clk.Set(r.base.Add(14 * time.Second))
	r.o.unlockTunnel("awg10")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("conf=running не завершился")
	}

	r.expire()
	r.sched.fn(t, 0)()
	if n := r.probe.calls.Load(); n != 1 {
		t.Fatalf("running пришёл раньше disabled — не bounce, нужна проба: calls=%d", n)
	}
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
}

// Сбой остановки: метка снимается (как ошибка пробы), туннель не числится
// остановленным.
// Мутация «Info об остановке без проверки ошибки» → красный.
func TestConfDisabled_RecheckStopFails_MarkCleared(t *testing.T) {
	r := newHookWindowRig(t)
	lg := &levelLog{}
	r.o.appLog = logging.NewScopedLogger(lg, logging.GroupTunnel, logging.SubOrchestrator)
	r.op.stopErr = errors.New("rci down")
	r.disabledEdge(t, 10*time.Second)
	r.expire()
	r.sched.fn(t, 0)()

	if !r.absorbedAt().IsZero() {
		t.Fatal("метка не снята после сбоя остановки")
	}
	if !r.tunnel().Running {
		t.Fatal("сбойная остановка учтена как успешная")
	}
	if lg.has(logging.LevelInfo, "подтверждена NDMS — остановка") {
		t.Fatal("журнал заявляет остановку, которой не было")
	}
	if !lg.has(logging.LevelWarn, "остановка не удалась") {
		t.Fatalf("нет Warn о сбое остановки: %v", lg.lines)
	}
}

// levelLog — журнал теста с уровнями.
type levelLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *levelLog) AppLog(level logging.Level, _, _, action, target, message string) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf("%v|%s/%s: %s", level, action, target, message))
	l.mu.Unlock()
}

func (l *levelLog) has(level logging.Level, sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.HasPrefix(s, fmt.Sprintf("%v|", level)) && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// L1′(b): перепроверка исполняет действия под уже взятым замком туннеля.
// Мутация executeActionsGrouped → повторный захват того же семафора →
// ждёт tunnelLockTimeout и падает ErrOperationInProgress → красный.
func TestConfDisabled_RecheckUnderTunnelLock_NoGroupedRelock(t *testing.T) {
	r := newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)
	r.expire()
	fn := r.sched.fn(t, 0)

	if err := r.o.lockTunnel(context.Background(), "awg10", "test"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	r.o.unlockTunnel("awg10")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("перепроверка не завершилась после освобождения замка")
	}
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
}
