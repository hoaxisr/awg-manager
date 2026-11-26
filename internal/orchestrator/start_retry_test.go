package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// F625: упавший на системном событии ColdStart повторяется по таймеру.

func newRetryFixture(t *testing.T) (*Orchestrator, *fakeKernelOp, *capturedSchedule, *storage.AWGTunnelStore) {
	t.Helper()
	store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg15", Name: "nl", Backend: "kernel", Enabled: true,
		ISPInterface: "eth3",
		Interface:    storage.AWGInterface{Address: "10.8.4.2/32"},
		Peer:         storage.AWGPeer{PublicKey: "pk", Endpoint: "203.0.113.7:36887", AllowedIPs: []string{"0.0.0.0/0"}}})
	op := &fakeKernelOp{coldStartErr: errors.New("endpoint resolve failed")}
	sched := &capturedSchedule{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel(), schedule: sched.schedule}
	o.state.tunnels["awg15"] = &tunnelState{ID: "awg15", Backend: "kernel", Enabled: true}
	return o, op, sched, store
}

var bootGroup = []Action{{Type: ActionColdStartKernel, Tunnel: "awg15"}}

func TestStartRetry_FailedBootStartRetriesUntilRunning(t *testing.T) {
	o, op, sched, _ := newRetryFixture(t)

	_ = o.executeGroup(context.Background(), bootGroup, "boot")
	// WAN-up следом падает на том же туннеле — вторая цепочка не нужна.
	_ = o.executeGroup(context.Background(), bootGroup, "wan-up")
	if sched.count() != 1 || sched.delays[0] != startRetryFirst {
		t.Fatalf("запланировано %d повторов (задержки %v), want 1 через %s", sched.count(), sched.delays, startRetryFirst)
	}

	// Первый повтор снова падает — следующий с удвоенной задержкой.
	sched.fn(t, 0)()
	if sched.count() != 2 || sched.delays[1] != 2*startRetryFirst {
		t.Fatalf("после отказа повтора: запланировано %d (задержки %v)", sched.count(), sched.delays)
	}

	// DNS ожил — повтор поднимает туннель, цепочка кончается.
	op.coldStartErr = nil
	sched.fn(t, 1)()
	if !o.state.tunnels["awg15"].Running {
		t.Fatal("удачный повтор не поднял туннель")
	}
	if sched.count() != 2 {
		t.Fatalf("после удачного повтора запланировано ещё: %d", sched.count()-2)
	}
	if o.startRetry["awg15"] {
		t.Fatal("цепочка не снята после успеха")
	}
	if n := op.coldStarts.Load(); n != 4 {
		t.Fatalf("ColdStart звали %d раз, want 4 (бут, WAN-up, 2 повтора)", n)
	}
}

func TestStartRetry_UserStopEndsChain(t *testing.T) {
	o, op, sched, store := newRetryFixture(t)
	_ = o.executeGroup(context.Background(), bootGroup, "boot")

	if err := store.Update("awg15", func(t *storage.AWGTunnel) error { t.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	sched.fn(t, 0)()
	if n := op.coldStarts.Load(); n != 1 {
		t.Fatalf("повтор поднял выключенный пользователем туннель (ColdStart %d раз)", n)
	}
	if sched.count() != 1 || o.startRetry["awg15"] {
		t.Fatalf("цепочка не закончилась: запланировано %d", sched.count())
	}
}

func TestStartRetry_GivesUpAfterMax(t *testing.T) {
	o, op, sched, _ := newRetryFixture(t)
	_ = o.executeGroup(context.Background(), bootGroup, "boot")
	for i := range startRetryMax {
		sched.fn(t, i)()
	}
	if sched.count() != startRetryMax {
		t.Fatalf("запланировано %d повторов, want %d", sched.count(), startRetryMax)
	}
	if n := op.coldStarts.Load(); n != 1+startRetryMax {
		t.Fatalf("ColdStart %d раз, want %d", n, 1+startRetryMax)
	}
	if o.startRetry["awg15"] {
		t.Fatal("цепочка не снята после предела")
	}
	if got := sched.delays[startRetryMax-1]; got != startRetryCap {
		t.Fatalf("последняя задержка %s, want потолок %s", got, startRetryCap)
	}
}

// Занятый замок: повтор не ждёт и не исполняет старт мимо держателя.
func TestStartRetry_BusyLockSkipsAttempt(t *testing.T) {
	o, op, sched, _ := newRetryFixture(t)
	_ = o.executeGroup(context.Background(), bootGroup, "boot")

	if !o.tryLockTunnel("awg15", "user-stop") {
		t.Fatal("замок занят до теста")
	}
	sched.fn(t, 0)()
	o.unlockTunnel("awg15")

	if n := op.coldStarts.Load(); n != 1 {
		t.Fatalf("повтор исполнил старт под чужим замком (ColdStart %d раз)", n)
	}
	if sched.count() != 2 {
		t.Fatalf("после занятого замка запланировано %d, want 2", sched.count())
	}
}

// Пользовательский старт (не executeGroup) повтора не заводит.
func TestStartRetry_UserStartNoRetry(t *testing.T) {
	o, _, sched, _ := newRetryFixture(t)
	_ = o.executeActions(context.Background(), bootGroup)
	if sched.count() != 0 {
		t.Fatalf("отказ пользовательского старта завёл повтор: %d", sched.count())
	}
}

// Удалённый туннель заканчивает цепочку без старта.
func TestStartRetry_DeletedTunnelEndsChain(t *testing.T) {
	o, op, sched, store := newRetryFixture(t)
	_ = o.executeGroup(context.Background(), bootGroup, "boot")
	if err := store.Delete("awg15"); err != nil {
		t.Fatal(err)
	}
	sched.fn(t, 0)()
	if n := op.coldStarts.Load(); n != 1 {
		t.Fatalf("повтор стартовал удалённый туннель (ColdStart %d раз)", n)
	}
	if sched.count() != 1 || o.startRetry["awg15"] {
		t.Fatalf("цепочка не закончилась: запланировано %d", sched.count())
	}
}

// Группа бута, отброшенная занятым замком, тоже получает повтор: старт не
// исполнялся вовсе.
func TestStartRetry_GroupLockFailureSchedulesRetry(t *testing.T) {
	o, op, sched, _ := newRetryFixture(t)
	if !o.tryLockTunnel("awg15", "wedged") {
		t.Fatal("замок занят до теста")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // lockTunnel отказывает сразу, без таймаута
	if err := o.executeGroup(ctx, bootGroup, "boot"); err == nil {
		t.Fatal("группа исполнилась под чужим замком")
	}
	o.unlockTunnel("awg15")
	if op.coldStarts.Load() != 0 {
		t.Fatal("ColdStart исполнен без замка")
	}
	if sched.count() != 1 {
		t.Fatalf("отказ замка не завёл повтор: запланировано %d", sched.count())
	}
}

// Модуль amneziawg на этом ядре не загружается (5.2 Beta 5): повтор старта
// бессмыслен и только писал бы .conf — цепочка не заводится.
func TestStartRetry_NoRetryWhenKernelModuleUnusable(t *testing.T) {
	o, _, sched, _ := newRetryFixture(t)
	o.SetKernelModuleUsable(func() bool { return false })
	_ = o.executeGroup(context.Background(), bootGroup, "boot")
	if sched.count() != 0 || o.startRetry["awg15"] {
		t.Fatalf("повтор заведён при непригодном модуле: %d", sched.count())
	}
}
