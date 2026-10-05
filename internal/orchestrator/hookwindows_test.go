package orchestrator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Стенд 01.10, KN-1810 5.01.C.6: 16 замеров опоздания хуков под churn —
// 14–30 с, максимум ≈30 с. Окно = максимум × 1,5. Вернуть 15/20 с — хук,
// пришедший на 20-й секунде, снова примут за внешнюю грань.
func TestHookWindows_CoverMeasuredLag(t *testing.T) {
	if expectedHookTTL != 45*time.Second {
		t.Errorf("expectedHookTTL = %s, want 45s", expectedHookTTL)
	}
	if bootQuiescenceWindow != 45*time.Second {
		t.Errorf("bootQuiescenceWindow = %s, want 45s", bootQuiescenceWindow)
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
	r.o.noteConfRunning("OpkgTun10")
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

func TestConfDisabled_NoAbsorbedEdge_NoSchedule(t *testing.T) {
	r := newHookWindowRig(t)
	r.expire()
	if n := r.sched.count(); n != 0 {
		t.Fatalf("без поглощённой грани перепроверка не планируется: schedule=%d", n)
	}
	if n := r.probe.calls.Load(); n != 0 {
		t.Fatalf("calls=%d", n)
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

// Окно продлено (повторный подъём/reconcile) к моменту вызова — перенос на
// остаток, решает второй вызов.
func TestConfDisabled_RecheckWindowExtended_Reschedules(t *testing.T) {
	r := newHookWindowRig(t)
	r.disabledEdge(t, 10*time.Second)
	r.o.mu.Lock()
	r.o.state.tunnels["awg10"].quiescentUntil = r.base.Add(60 * time.Second)
	r.o.mu.Unlock()

	r.expire() // base+45 с, окно теперь до base+60 с
	r.sched.fn(t, 0)()
	if n := r.probe.calls.Load(); n != 0 {
		t.Fatalf("внутри продлённого окна проба не нужна: calls=%d", n)
	}
	if n := r.sched.count(); n != 2 {
		t.Fatalf("schedule=%d, want 2 (перенос)", n)
	}
	if r.sched.delays[1] != 15*time.Second {
		t.Fatalf("перенос на %s, want 15s", r.sched.delays[1])
	}

	r.clk.Set(r.base.Add(60 * time.Second))
	r.sched.fn(t, 1)()
	if n := r.probe.calls.Load(); n != 1 {
		t.Fatalf("calls=%d, want 1", n)
	}
	if n := r.op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
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
