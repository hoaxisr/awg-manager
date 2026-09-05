package orchestrator

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// fakeStateMgr — источник состояния kernel-бэкенда: работающими считаются
// только перечисленные ID.
type fakeStateMgr struct{ running map[string]bool }

func (f *fakeStateMgr) GetState(_ context.Context, id string) tunnel.StateInfo {
	if f.running[id] {
		return tunnel.StateInfo{State: tunnel.StateRunning, ProcessRunning: true}
	}
	return tunnel.StateInfo{State: tunnel.StateStopped}
}

// lifecycleStore — хранилище в TempDir; каталог .conf тоже уводится в TempDir:
// executeColdStartKernel пишет (config.WriteFile), executeDeleteKernel удаляет
// (config.RemoveFile) файл в tunnel.ConfDir — с дефолтом это /opt/etc/awg-manager
// хоста. Интерфейсы хоста не читаем: listInterfaces → пусто.
func lifecycleStore(t *testing.T, recs ...*storage.AWGTunnel) *storage.AWGTunnelStore {
	t.Helper()
	prevDir, prevIfaces := tunnel.ConfDir, listInterfaces
	tunnel.ConfDir = t.TempDir()
	listInterfaces = func() ([]net.Interface, error) { return nil, nil }
	t.Cleanup(func() { tunnel.ConfDir, listInterfaces = prevDir, prevIfaces })

	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, r := range recs {
		if err := store.Create(r); err != nil {
			t.Fatalf("сид записи %s: %v", r.ID, err)
		}
	}
	return store
}

func mustGet(t *testing.T, store *storage.AWGTunnelStore, id string) *storage.AWGTunnel {
	t.Helper()
	rec, err := store.Get(id)
	if err != nil {
		t.Fatalf("перечитать %s: %v", id, err)
	}
	return rec
}

// Пользовательская остановка ПЕРСИСТИТСЯ (Enabled=false) — иначе туннель
// воскресает на ребуте; runtime-поля чистятся. Мутант «Enabled не трогать»
// проходил зелёным.
func TestExecutePersistStopped_ClearsEnabledAndRuntime(t *testing.T) {
	store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: true,
		ActiveWAN: "ISP1", StartedAt: "2026-09-03T10:00:00Z"})
	o := &Orchestrator{state: newState(), store: store, kernelOp: &fakeKernelOp{}, wanModel: wan.NewModel()}

	if err := o.executeOne(context.Background(), Action{Type: ActionPersistStopped, Tunnel: "awg10"}); err != nil {
		t.Fatalf("executeOne(PersistStopped): %v", err)
	}

	got := mustGet(t, store, "awg10")
	if got.Enabled || got.ActiveWAN != "" || got.StartedAt != "" {
		t.Fatalf("после PersistStopped: Enabled=%v ActiveWAN=%q StartedAt=%q", got.Enabled, got.ActiveWAN, got.StartedAt)
	}
}

// Stop как ШАГ (restart = Stop+Start) НЕ трогает Enabled: провал Start между
// ними оставил бы туннель выключенным (класс #669). Runtime-поля чистятся.
func TestExecuteStop_KeepsEnabledClearsRuntime(t *testing.T) {
	t.Run("kernel", func(t *testing.T) {
		store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: true,
			ActiveWAN: "ISP1", StartedAt: "2026-09-03T10:00:00Z"})
		op := &fakeKernelOp{}
		o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}

		if err := o.executeOne(context.Background(), Action{Type: ActionStopKernel, Tunnel: "awg10"}); err != nil {
			t.Fatalf("executeOne(StopKernel): %v", err)
		}

		got := mustGet(t, store, "awg10")
		if !got.Enabled || got.ActiveWAN != "" || got.StartedAt != "" || op.stops != 1 {
			t.Fatalf("kernel Stop: Enabled=%v ActiveWAN=%q StartedAt=%q stops=%d",
				got.Enabled, got.ActiveWAN, got.StartedAt, op.stops)
		}
	})

	t.Run("nativewg", func(t *testing.T) {
		store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg0", Name: "n", Backend: "nativewg", Enabled: true,
			ActiveWAN: "ISP1", StartedAt: "2026-09-03T10:00:00Z"})
		o := &Orchestrator{state: newState(), store: store, nwgOp: &fakeNWGOp{}, wanModel: wan.NewModel()}

		if err := o.executeOne(context.Background(), Action{Type: ActionStopNativeWG, Tunnel: "awg0"}); err != nil {
			t.Fatalf("executeOne(StopNativeWG): %v", err)
		}

		got := mustGet(t, store, "awg0")
		if !got.Enabled || got.ActiveWAN != "" || got.StartedAt != "" {
			t.Fatalf("nwg Stop: Enabled=%v ActiveWAN=%q StartedAt=%q", got.Enabled, got.ActiveWAN, got.StartedAt)
		}
	})
}

// Отказ действия НЕ обновляет кэш состояния: упавший ColdStart, записанный
// как «работает», заставит boot выбрать Reconcile вместо ColdStart, а
// WANDown/Up — вести себя как для живого туннеля.
func TestExecuteActions_FailedActionLeavesStateUntouched(t *testing.T) {
	// Полная запись: до kernelOp.ColdStart идут resolveWAN → config.WriteFile →
	// ResolveEndpointIP(Peer.Endpoint) → checkSystemAddressConflict; с пустым
	// Endpoint путь обрывается ДО ColdStart, и coldStartErr не исполнялся бы —
	// тест краснел бы по чужой причине.
	newFixture := func(t *testing.T) *storage.AWGTunnelStore {
		t.Helper()
		return lifecycleStore(t, &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: true,
			ISPInterface: "eth3",
			Interface:    storage.AWGInterface{Address: "10.9.7.2/26"},
			Peer:         storage.AWGPeer{PublicKey: "pk", Endpoint: "203.0.113.7:51820", AllowedIPs: []string{"0.0.0.0/0"}}})
	}

	t.Run("отказ не двигает кэш", func(t *testing.T) {
		store := newFixture(t)
		op := &fakeKernelOp{coldStartErr: errors.New("wg setconf: boom")}
		o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}
		o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel", Enabled: true, Running: false}

		if err := o.executeActions(context.Background(), []Action{{Type: ActionColdStartKernel, Tunnel: "awg10"}}); err == nil {
			t.Fatal("отказ ColdStart обязан вернуться из executeActions")
		}
		if o.state.tunnels["awg10"].Running {
			t.Fatal("упавший ColdStart записан в кэш как работающий")
		}
	})

	// Улика, что фикстура доходит до ColdStart: тот же вызов без отказа обязан
	// вернуть nil и поставить Running=true — иначе подтест выше зеленел бы на
	// обрыве пути ДО оператора.
	t.Run("успех двигает кэш", func(t *testing.T) {
		store := newFixture(t)
		o := &Orchestrator{state: newState(), store: store, kernelOp: &fakeKernelOp{}, wanModel: wan.NewModel()}
		o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel", Enabled: true, Running: false}

		if err := o.executeActions(context.Background(), []Action{{Type: ActionColdStartKernel, Tunnel: "awg10"}}); err != nil {
			t.Fatalf("успешный ColdStart: %v", err)
		}
		if !o.state.tunnels["awg10"].Running {
			t.Fatal("успешный ColdStart не записан в кэш")
		}
	})
}

// Delete: при отказе ядра запись НЕ исчезает (иначе OpkgTun сиротеет без
// записи); при успехе запись удалена.
func TestExecuteDeleteKernel_KeepsRecordWhenKernelDeleteFails(t *testing.T) {
	store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg10", Name: "g"})
	op := &fakeKernelOp{deleteErr: errors.New("ndms: busy")}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}

	if err := o.executeOne(context.Background(), Action{Type: ActionDeleteKernel, Tunnel: "awg10"}); err == nil {
		t.Fatal("отказ Delete ядра обязан вернуться")
	}
	if !store.Exists("awg10") {
		t.Fatal("запись удалена, хотя ядро отказало — OpkgTun осиротеет")
	}

	op.deleteErr = nil
	if err := o.executeOne(context.Background(), Action{Type: ActionDeleteKernel, Tunnel: "awg10"}); err != nil {
		t.Fatalf("executeOne(DeleteKernel): %v", err)
	}
	if store.Exists("awg10") {
		t.Fatal("запись жива после успешного Delete")
	}
}

// LoadState: Running берётся у источника состояния СВОЕГО бэкенда, Monitoring
// включается только у работающего с включённым pingcheck.
func TestLoadState_RunningFromBackendAndMonitoringGate(t *testing.T) {
	store := lifecycleStore(t,
		&storage.AWGTunnel{ID: "awg10", Name: "k-run", Enabled: true, PingCheck: &storage.TunnelPingCheck{Enabled: true}},
		&storage.AWGTunnel{ID: "awg11", Name: "k-stop", Enabled: true, PingCheck: &storage.TunnelPingCheck{Enabled: true}},
		&storage.AWGTunnel{ID: "awg0", Name: "n-run", Backend: "nativewg", Enabled: true},
	)
	o := &Orchestrator{state: newState(), store: store,
		kernelOp: &fakeKernelOp{},
		nwgOp:    &fakeNWGOp{state: tunnel.StateInfo{State: tunnel.StateRunning}},
		stateMgr: &fakeStateMgr{running: map[string]bool{"awg10": true}},
		wanModel: wan.NewModel()}

	o.LoadState(context.Background())

	if !o.state.tunnels["awg10"].Running || !o.state.tunnels["awg10"].Monitoring {
		t.Errorf("awg10: Running=%v Monitoring=%v, ждали true/true",
			o.state.tunnels["awg10"].Running, o.state.tunnels["awg10"].Monitoring)
	}
	if o.state.tunnels["awg11"].Running || o.state.tunnels["awg11"].Monitoring {
		t.Errorf("awg11: Running=%v Monitoring=%v, ждали false/false",
			o.state.tunnels["awg11"].Running, o.state.tunnels["awg11"].Monitoring)
	}
	if !o.state.tunnels["awg0"].Running || o.state.tunnels["awg0"].Monitoring {
		t.Errorf("awg0: Running=%v Monitoring=%v, ждали true/false",
			o.state.tunnels["awg0"].Running, o.state.tunnels["awg0"].Monitoring)
	}
}

// executeGroup держит лок туннеля: чужой держатель — группа не исполняется.
func TestExecuteGroup_RespectsTunnelLock(t *testing.T) {
	store := lifecycleStore(t, &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: true})
	op := &fakeKernelOp{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}

	if err := o.lockTunnel(context.Background(), "awg10", "hook"); err != nil {
		t.Fatalf("взять лок: %v", err)
	}

	// Свой дедлайн: без него ожидание чужого лока тянется tunnelLockTimeout=15s.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := o.executeGroup(ctx, []Action{{Type: ActionStopKernel, Tunnel: "awg10"}}, "user"); err == nil {
		t.Fatal("группа исполнилась под чужим локом")
	}
	if op.stops != 0 {
		t.Fatalf("Stop исполнен под чужим локом: %d", op.stops)
	}
	o.unlockTunnel("awg10")
}
