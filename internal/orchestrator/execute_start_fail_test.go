package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// routeRecorder пишет, для какого туннеля звали постановку маршрутов.
type routeRecorder struct{ started []string }

func (r *routeRecorder) OnTunnelStart(_ context.Context, id, _ string) error {
	r.started = append(r.started, id)
	return nil
}
func (r *routeRecorder) OnTunnelStop(context.Context, string) error   { return nil }
func (r *routeRecorder) OnTunnelDelete(context.Context, string) error { return nil }
func (r *routeRecorder) Reconcile(context.Context) error              { return nil }

// startFailNWG — Start отказывает для одного туннеля (снятый WireguardN),
// ConfigurePingCheck считается по туннелям.
type startFailNWG struct {
	fakeNWGOp
	failID   string
	pingConf []string
}

func (f *startFailNWG) Start(_ context.Context, st *storage.AWGTunnel) error {
	if st.ID == f.failID {
		return fmt.Errorf("start: Wireguard0: %w", tunnel.ErrInterfaceGone)
	}
	return nil
}

func (f *startFailNWG) ConfigurePingCheck(_ context.Context, st *storage.AWGTunnel, _ ndms.PingCheckConfig) error {
	f.pingConf = append(f.pingConf, st.ID)
	return nil
}

// Start туннеля отказал — его хвост в том же прогоне не исполняется: ни
// маршрутов на отсутствующий интерфейс, ни ping-check, ни PersistRunning.
// Соседний туннель в той же пачке отрабатывает полностью; ошибка наружу.
func TestExecuteActions_FailedStartSkipsRestOfTunnel(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	pc := &storage.TunnelPingCheck{Enabled: true}
	for _, id := range []string{"awg0", "awg1"} {
		if err := store.Create(&storage.AWGTunnel{ID: id, Name: id, Backend: "nativewg", PingCheck: pc}); err != nil {
			t.Fatal(err)
		}
	}
	op := &startFailNWG{failID: "awg0"}
	sr, cr := &routeRecorder{}, &routeRecorder{}
	o := &Orchestrator{state: newState(), store: store, nwgOp: op, staticRoute: sr, clientRoute: cr,
		appLog: logging.NewScopedLogger(&capturingLog{}, logging.GroupTunnel, logging.SubOrchestrator)}

	var actions []Action
	for _, id := range []string{"awg0", "awg1"} {
		ts := &tunnelState{ID: id, Backend: "nativewg", PingCheck: pc}
		actions = append(actions, Action{Type: ActionStartNativeWG, Tunnel: id})
		actions = appendPostStartActions(actions, ts)
	}

	err := o.executeActions(context.Background(), actions)
	if !errors.Is(err, tunnel.ErrInterfaceGone) {
		t.Fatalf("ошибка Start обязана дойти до вызывающего: %v", err)
	}
	if fmt.Sprint(sr.started) != "[awg1]" || fmt.Sprint(cr.started) != "[awg1]" || fmt.Sprint(op.pingConf) != "[awg1]" {
		t.Fatalf("static=%v client=%v pingcheck=%v, ждали только awg1", sr.started, cr.started, op.pingConf)
	}
	for id, want := range map[string]bool{"awg0": false, "awg1": true} {
		st, _ := store.Get(id)
		if st.Enabled != want || (st.StartedAt != "") != want {
			t.Fatalf("%s: Enabled=%v StartedAt=%q, ждали %v", id, st.Enabled, st.StartedAt, want)
		}
	}
}
