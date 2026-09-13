package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Замок оркестратора существует, чтобы действия по одному туннелю шли по
// очереди. Владельцы, правящие туннель в обход событий (service.Update,
// ReplaceConfig), обязаны брать тот же замок — иначе их работа переплетается
// с WAN-up по тому же туннелю.
func TestWithTunnelLock_BusyLockKeepsCallerOut(t *testing.T) {
	o := &Orchestrator{state: newState()}
	if err := o.lockTunnel(context.Background(), "awg0", "держатель"); err != nil {
		t.Fatalf("подготовка: %v", err)
	}

	// Отменённый контекст — чтобы не ждать tunnelLockTimeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := o.WithTunnelLock(ctx, "awg0", "правка", func() error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("занятый замок обязан отказать")
	}
	if called {
		t.Fatal("работа пошла в обход занятого замка")
	}

	o.unlockTunnel("awg0")
	if !o.tryLockTunnel("awg0", "проверка") {
		t.Fatal("замок не освободился")
	}
	o.unlockTunnel("awg0")
}

func TestWithTunnelLock_ReleasesAfterWork(t *testing.T) {
	o := &Orchestrator{state: newState()}
	if err := o.WithTunnelLock(context.Background(), "awg0", "правка", func() error { return nil }); err != nil {
		t.Fatalf("WithTunnelLock: %v", err)
	}
	if !o.tryLockTunnel("awg0", "проверка") {
		t.Fatal("замок не освободился после работы")
	}
	o.unlockTunnel("awg0")
}

// Адрес target'а обязан попасть в запись той же транзакцией, что и ActiveWAN:
// ActionPersistRunning доезжает лишь через несколько действий, а до тех пор
// сосед с тем же target читает из стора прежний адрес.
func TestExecuteStartNativeWG_PersistsResolvedEndpointIP(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{ID: "awg0", Name: "awg0", Backend: "nativewg"}); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	op := &fakeNWGOp{trackedIP: "203.0.113.5"}
	o := &Orchestrator{state: newState(), store: store, nwgOp: op}

	if err := o.executeStartNativeWG(context.Background(), Action{Type: ActionStartNativeWG, Tunnel: "awg0"}); err != nil {
		t.Fatalf("executeStartNativeWG: %v", err)
	}

	stored, err := store.Get("awg0")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ResolvedEndpointIP != "203.0.113.5" {
		t.Fatalf("адрес не персистнут вместе со стартом: %q", stored.ResolvedEndpointIP)
	}
}
