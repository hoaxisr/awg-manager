package orchestrator

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// ctxRecorder запоминает контекст, под которым его позвали.
type ctxRecorder struct {
	calls int
	got   context.Context
}

func (r *ctxRecorder) OnTunnelStart(context.Context, string, string) error { return nil }
func (r *ctxRecorder) OnTunnelStop(context.Context, string) error          { return nil }
func (r *ctxRecorder) OnTunnelDelete(context.Context, string) error        { return nil }
func (r *ctxRecorder) Reconcile(ctx context.Context) error {
	r.calls++
	r.got = ctx
	return nil
}

// Проводка отложенного бута целиком: HandleEvent обязан пройти через
// decideLocked (иначе решение принимает не тот, кто владеет состоянием) и
// исполнить бут под контекстом ЖИЗНИ ДЕМОНА, а не под контекстом вызывающего.
// Вызывающий здесь — горутина NDMS-хука с 60-секундным дедлайном
// (internal/api/hook.go); бут на нескольких туннелях с медленным NDMS в него
// не укладывается и обрывался бы посередине, без повтора.
func TestHandleEvent_DeferredBootRunsUnderDaemonContext(t *testing.T) {
	o := &Orchestrator{
		state:  newState(),
		appLog: logging.NewScopedLogger(&capturingLog{}, logging.GroupTunnel, logging.SubOrchestrator),
	}
	o.state.bootPending = true
	o.state.anyWANUpFn = func() bool { return true }

	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	o.SetBaseContext(base)

	rec := &ctxRecorder{}
	o.SetStaticRoute(rec)

	// Контекст вызывающего уже отменён — так выглядит хук, чей дедлайн истёк.
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()

	if err := o.HandleEvent(callerCtx, Event{Type: EventWANUp, WANIface: "ppp0"}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if rec.calls == 0 {
		t.Fatal("действия отложенного бута не доехали до исполнения")
	}
	if rec.got == nil || rec.got.Err() != nil {
		t.Error("бут исполнен под контекстом вызывающего: отменённый хук оборвал бы его посередине")
	}
	if o.state.bootPending {
		t.Error("пометка обязана сниматься состоявшимся бутом")
	}
}

// Обычное событие идёт под контекстом вызывающего: подмена его на baseCtx
// увела бы одиночные операции из-под дедлайна HTTP-запроса.
func TestHandleEvent_OrdinaryEventKeepsCallerContext(t *testing.T) {
	o := &Orchestrator{
		state:  newState(),
		appLog: logging.NewScopedLogger(&capturingLog{}, logging.GroupTunnel, logging.SubOrchestrator),
	}
	o.state.anyWANUpFn = func() bool { return true }
	o.SetBaseContext(context.Background())

	rec := &ctxRecorder{}
	o.SetStaticRoute(rec)

	type ключ struct{}
	callerCtx := context.WithValue(context.Background(), ключ{}, "вызывающий")

	if err := o.HandleEvent(callerCtx, Event{Type: EventBoot, WANUp: true}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if rec.calls == 0 {
		t.Fatal("бут не дошёл до исполнения")
	}
	if rec.got.Value(ключ{}) != "вызывающий" {
		t.Error("обычное событие исполнено не под контекстом вызывающего")
	}
}
