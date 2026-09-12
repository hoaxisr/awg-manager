package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// running nativewg tunnel whose boot-quiescence window has long elapsed —
// the state in which a conf=disabled edge is taken as user intent to stop.
func settledTunnel() *Orchestrator {
	o := &Orchestrator{state: newState(), confSettleDelay: 50 * time.Millisecond}
	o.state.tunnels["awg11"] = &tunnelState{
		ID: "awg11", Backend: "nativewg", NWGIndex: 2,
		Enabled: true, Running: true,
		quiescentUntil: time.Now().Add(-time.Hour),
	}
	return o
}

func confHook(level string) Event {
	return Event{Type: EventNDMSHook, NDMSName: "Wireguard2", Layer: "conf", Level: level}
}

// Issue #667: NDMS ping-check restarts the interface by bouncing conf
// disabled→running within a couple of seconds. Taking the disabled edge at
// face value stopped the tunnel and tore down its routes.
func TestConfDisabled_SuppressedWhenNDMSBouncesBackToRunning(t *testing.T) {
	o := settledTunnel()

	go func() {
		time.Sleep(10 * time.Millisecond)
		_ = o.HandleEvent(context.Background(), confHook("running"))
	}()

	if o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("conf=disabled followed by conf=running is an NDMS restart — the stop must be suppressed")
	}
}

// A genuine disable from the router web UI has no conf=running behind it and
// must still stop the tunnel (issue #183 sync intent).
func TestConfDisabled_StopsWhenNoRunningFollows(t *testing.T) {
	o := settledTunnel()

	if !o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("a lone conf=disabled is user intent — the stop must proceed")
	}
}

// A conf=running seen BEFORE the disabled edge (e.g. the tunnel's own start)
// must not cancel a later, unrelated disable.
func TestConfDisabled_StaleRunningDoesNotSuppress(t *testing.T) {
	o := settledTunnel()
	_ = o.HandleEvent(context.Background(), confHook("running"))

	if !o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("a conf=running that predates the disabled edge must not suppress the stop")
	}
}

// Issue #669: the conf=running edge behind an NDMS interface restart can be
// missed entirely — the hook POST is fire-and-forget, and NDMS may take longer
// than the settle window to bring the interface back. Ask NDMS itself before
// tearing the tunnel down.
func TestConfDisabled_SuppressedWhenNDMSStillHoldsInterfaceEnabled(t *testing.T) {
	o := settledTunnel()
	o.confLayerRunning = func(context.Context, string) (bool, error) { return true, nil }

	if o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("NDMS reports conf=running — the disabled edge was a restart, the stop must be suppressed")
	}
}

func TestConfDisabled_StopsWhenNDMSConfirmsDisabled(t *testing.T) {
	o := settledTunnel()
	o.confLayerRunning = func(context.Context, string) (bool, error) { return false, nil }

	if !o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("NDMS confirms the interface is disabled — the stop must proceed")
	}
}

// An unreadable NDMS (busy, transport error) must not turn into a suppressed
// stop: fall back to the edge we were given.
func TestConfDisabled_StopsWhenNDMSUnreadable(t *testing.T) {
	o := settledTunnel()
	o.confLayerRunning = func(context.Context, string) (bool, error) {
		return false, errors.New("rci timeout")
	}

	if !o.settleConfDisabled(context.Background(), confHook("disabled")) {
		t.Fatal("probe error must leave the disabled edge in force")
	}
}

// Issue #669: a conf=running arriving while our own stop is still executing
// used to be swallowed by decide's t.Running guard — and since the stop
// persists Enabled=false, no later event would ever bring the tunnel back.
// The running edge must wait for the in-flight sequence to finish.
func TestConfRunning_WaitsForInFlightStop(t *testing.T) {
	o := settledTunnel()
	if err := o.lockTunnel(context.Background(), "awg11", "test"); err != nil {
		t.Fatalf("lock: %v", err)
	}

	done := make(chan struct{})
	go func() {
		_ = o.HandleEvent(context.Background(), confHook("running"))
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("conf=running must not be decided while the tunnel has an operation in flight")
	case <-time.After(100 * time.Millisecond):
	}

	o.unlockTunnel("awg11")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("conf=running must proceed once the in-flight operation releases the tunnel")
	}
}

// Inside the boot-quiescence window decide() already suppresses; settle must
// not add its own delay there.
func TestConfDisabled_NoWaitInsideQuiescence(t *testing.T) {
	o := settledTunnel()
	o.state.tunnels["awg11"].quiescentUntil = time.Now().Add(time.Minute)
	o.confSettleDelay = 5 * time.Second

	start := time.Now()
	o.settleConfDisabled(context.Background(), confHook("disabled"))

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("settle must not wait inside the quiescence window, waited %s", elapsed)
	}
}

// stoppedTunnel — остановленный kernel-туннель: состояние, в котором внешний
// conf=running поднимает туннель даже при Enabled=false (issue #183).
func stoppedTunnel() *Orchestrator {
	o := &Orchestrator{state: newState()}
	o.state.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: false, Running: false,
	}
	return o
}

func opkgConfHook(level string) Event {
	return Event{Type: EventNDMSHook, NDMSName: "OpkgTun10", Layer: "conf", Level: level}
}

// F233: NDMS переигрывает конфигурацию сам и шлёт conf=running по интерфейсам,
// которых мы не трогали (стенд 5.01: пачка через девять секунд после удаления
// СОСЕДНЕГО OpkgTun). Такую грань опровергает сам NDMS: он держит интерфейс
// выключенным.
func TestConfRunning_SuppressedWhenNDMSHoldsInterfaceDown(t *testing.T) {
	o := stoppedTunnel()
	var asked string
	o.SetConfLayerProbe(func(_ context.Context, name string) (bool, error) {
		asked = name
		return false, nil
	})

	if o.settleConfRunning(context.Background(), opkgConfHook("running")) {
		t.Fatal("NDMS держит интерфейс выключенным — грань не должна поднимать туннель")
	}
	// NDMS знает интерфейс, а не наш id туннеля: спросив про "awg10", мы бы
	// получили «не найден» на каждую грань.
	if asked != "OpkgTun10" {
		t.Errorf("пробу спросили про %q, а NDMS знает имя интерфейса", asked)
	}
}

// Туннель не наш (грань по чужому интерфейсу — в пачке #233 таких большинство):
// решать нечего, и ходить в NDMS незачем.
func TestConfRunning_UnknownTunnelSkipsProbe(t *testing.T) {
	o := &Orchestrator{state: newState()}
	probed := false
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) {
		probed = true
		return false, nil
	})

	if !o.settleConfRunning(context.Background(), opkgConfHook("running")) {
		t.Fatal("грань по неизвестному туннелю гасить нечем — decide её и так пропустит")
	}
	if probed {
		t.Error("лишний синхронный запрос к NDMS на чужую грань")
	}
}

// Проба вернула и ответ, и ошибку — верим ошибке: грань остаётся в силе.
func TestConfRunning_ErrorWinsOverValue(t *testing.T) {
	for _, up := range []bool{false, true} {
		o := stoppedTunnel()
		o.SetConfLayerProbe(func(context.Context, string) (bool, error) {
			return up, errors.New("частичный ответ")
		})

		if !o.settleConfRunning(context.Background(), opkgConfHook("running")) {
			t.Fatalf("при ошибке ответ пробы (up=%v) не считается — грань обязана остаться в силе", up)
		}
	}
}

// Настоящее включение из веб-интерфейса роутера: NDMS держит интерфейс
// поднятым, и туннель обязан подняться даже при Enabled=false (issue #183).
func TestConfRunning_StartsWhenNDMSHoldsInterfaceUp(t *testing.T) {
	o := stoppedTunnel()
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) { return true, nil })

	if !o.settleConfRunning(context.Background(), opkgConfHook("running")) {
		t.Fatal("NDMS держит интерфейс включённым — это внешнее включение, туннель обязан подняться")
	}
}

// Непрочитанный NDMS оставляет грань в силе: лучше лишний старт, чем туннель,
// лежащий до ручного вмешательства (#669).
func TestConfRunning_ProbeFailureLeavesEdgeInForce(t *testing.T) {
	o := stoppedTunnel()
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) {
		return false, errors.New("ndms unreachable")
	})

	if !o.settleConfRunning(context.Background(), opkgConfHook("running")) {
		t.Fatal("отказ пробы не должен глушить грань")
	}
}

// Без пробы (её может не быть) поведение прежнее.
func TestConfRunning_NoProbeLeavesEdgeInForce(t *testing.T) {
	o := stoppedTunnel()

	if !o.settleConfRunning(context.Background(), opkgConfHook("running")) {
		t.Fatal("без пробы грань обязана оставаться в силе")
	}
}

// Работающий туннель decide и так не тронет — к NDMS не ходим.
func TestConfRunning_RunningTunnelSkipsProbe(t *testing.T) {
	o := stoppedTunnel()
	o.state.tunnels["awg10"].Running = true
	probed := false
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) {
		probed = true
		return false, nil
	})

	if !o.settleConfRunning(context.Background(), opkgConfHook("running")) {
		t.Fatal("для работающего туннеля грань не гасим")
	}
	if probed {
		t.Error("лишний запрос к NDMS для туннеля, который и так работает")
	}
}

// Сквозь HandleEvent: грань, которую опровергает сам NDMS, не доходит до
// decide — туннель не стартует. Без этого теста проверка живёт только в
// юнитах settleConfRunning, а её вызов из HandleEvent ничем не закреплён.
func TestHandleEvent_ConfRunning_NDMSHoldsDown_DoesNotStart(t *testing.T) {
	rec := &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: false}
	rec.Peer.Endpoint = "203.0.113.5:51820"
	store := lifecycleStore(t, rec)
	op := &fakeKernelOp{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}
	o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel"}
	o.state.anyWANUpFn = func() bool { return true }
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) { return false, nil })

	if err := o.HandleEvent(context.Background(), opkgConfHook("running")); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if op.coldStarts != 0 {
		t.Fatalf("туннель поднят по грани, которую опровергает NDMS: coldStarts=%d", op.coldStarts)
	}
	// Главный вред #233 — не лишний старт, а порча стора: ActionPersistRunning
	// вернул бы Enabled=true поверх того, что нажал пользователь.
	if rec := mustGet(t, store, "awg10"); rec.Enabled {
		t.Error("стор противоречит пользователю: Enabled=true после подавленной грани")
	}
}

// И обратное: NDMS подтверждает включение — туннель стартует.
func TestHandleEvent_ConfRunning_NDMSHoldsUp_Starts(t *testing.T) {
	rec := &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: false}
	rec.Peer.Endpoint = "203.0.113.5:51820"
	store := lifecycleStore(t, rec)
	op := &fakeKernelOp{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}
	o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel"}
	o.state.anyWANUpFn = func() bool { return true }
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) { return true, nil })

	if err := o.HandleEvent(context.Background(), opkgConfHook("running")); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	if op.coldStarts == 0 {
		t.Fatal("внешнее включение из веб-интерфейса роутера обязано поднимать туннель (issue #183)")
	}
	if got := mustGet(t, store, "awg10"); !got.Enabled {
		t.Error("внешнее включение обязано вернуться в стор: Enabled=false")
	}
}

// Грань, дождавшаяся НАШЕЙ операции, идёт в decide без вопроса к NDMS: он
// отдаст то, что мы сами только что записали (наш Stop ставит conf: disabled),
// и туннель остался бы лежать с Enabled=false — регресс #669.
func TestConfRunning_InFlightStopSkipsProbe(t *testing.T) {
	rec := &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: false}
	rec.Peer.Endpoint = "203.0.113.5:51820"
	store := lifecycleStore(t, rec)
	op := &fakeKernelOp{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}
	o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel"}
	o.state.anyWANUpFn = func() bool { return true }
	probed := false
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) {
		probed = true
		return false, nil // NDMS отдаёт последствие нашей же остановки
	})

	// Занимаем замок туннеля — так выглядит наша операция в полёте.
	if err := o.lockTunnel(context.Background(), "awg10", "test"); err != nil {
		t.Fatalf("lock: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- o.HandleEvent(context.Background(), opkgConfHook("running")) }()
	time.Sleep(30 * time.Millisecond)
	o.unlockTunnel("awg10")

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleEvent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HandleEvent завис")
	}

	if probed {
		t.Error("после ожидания своей операции проба бессмысленна — спрашивать NDMS не надо")
	}
	if op.coldStarts == 0 {
		t.Error("грань должна дойти до decide: иначе туннель остаётся лежать с Enabled=false (#669)")
	}
}

// Вызывающий сдался — исполнять на мёртвом контексте нечего.
func TestConfRunning_CancelledContextStopsHere(t *testing.T) {
	o := stoppedTunnel()
	o.SetConfLayerProbe(func(context.Context, string) (bool, error) { return true, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if o.settleConfRunning(ctx, opkgConfHook("running")) {
		t.Fatal("на отменённом контексте старт начинать нельзя — действия отвалятся посередине")
	}
}
