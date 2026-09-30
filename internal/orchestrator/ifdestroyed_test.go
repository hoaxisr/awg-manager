package orchestrator

import (
	"context"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// F569, Q1: запись нашего работающего kernel-туннеля снята снаружи — тот же
// набор, что внешний conf=disabled вне окна quiescence: остановка, снятие
// маршрутов, Enabled=false. Автопересоздания нет.
func TestDecide_IfDestroyed_KernelRunning_Stops(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel", Running: true, Enabled: true, Monitoring: true}

	got := decide(Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun10"}, &s)
	want := decideStop(Event{Type: EventStop, Tunnel: "awg10"}, &s)
	if len(want) == 0 || !slices.Equal(got, want) {
		t.Fatalf("actions = %+v, want %+v", got, want)
	}
	for _, a := range []ActionType{ActionStopKernel, ActionRemoveStaticRoutes, ActionRemoveClientRoutes, ActionPersistStopped} {
		if !hasAction(got, a) {
			t.Errorf("нет действия %v", a)
		}
	}
	if hasAction(got, ActionColdStartKernel) {
		t.Error("автопересоздание по ifdestroyed запрещено (Q1)")
	}
}

// Остановленный kernel-туннель — делать нечего.
func TestDecide_IfDestroyed_KernelStopped_NoActions(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel"}
	if got := decide(Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun10"}, &s); len(got) != 0 {
		t.Fatalf("actions = %+v", got)
	}
}

// NativeWG: устройство снимает сам NDMS, conf=disabled уже обработан.
func TestDecide_IfDestroyed_NativeWG_NoActions(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0}
	if got := decide(Event{Type: EventNDMSIfDestroyed, NDMSName: "Wireguard0"}, &s); len(got) != 0 {
		t.Fatalf("actions = %+v", got)
	}
}

func TestDecide_IfDestroyed_UnknownName_NoActions(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel", Running: true}
	if got := decide(Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun11"}, &s); len(got) != 0 {
		t.Fatalf("actions = %+v", got)
	}
}

func runningKernelOrch(t *testing.T) (*Orchestrator, *fakeKernelOp, *storage.AWGTunnelStore) {
	t.Helper()
	rec := &storage.AWGTunnel{ID: "awg10", Name: "g", Enabled: true}
	rec.Peer.Endpoint = "203.0.113.5:51820"
	store := lifecycleStore(t, rec)
	op := &fakeKernelOp{}
	o := &Orchestrator{state: newState(), store: store, kernelOp: op, wanModel: wan.NewModel()}
	o.state.tunnels["awg10"] = &tunnelState{ID: "awg10", Backend: "kernel", Running: true, Enabled: true}
	return o, op, store
}

// Наш собственный `no interface OpkgTun10` (Delete зарегистрировал
// ожидание) — хук поглощён, туннель не трогаем.
func TestHandleEvent_IfDestroyed_Expected_Consumed(t *testing.T) {
	o, op, store := runningKernelOrch(t)
	o.ExpectHook("OpkgTun10", "destroyed")

	if err := o.HandleEvent(context.Background(), Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun10"}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if n := op.stops.Load(); n != 0 {
		t.Fatalf("свой снос принят за внешний: stops=%d", n)
	}
	if !mustGet(t, store, "awg10").Enabled {
		t.Fatal("Enabled снят по своему же сносу")
	}
}

// Внешнее снятие — Stop и Enabled=false в сторе.
func TestHandleEvent_IfDestroyed_External_StopsAndDisables(t *testing.T) {
	o, op, store := runningKernelOrch(t)

	if err := o.HandleEvent(context.Background(), Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun10"}); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if n := op.stops.Load(); n != 1 {
		t.Fatalf("stops=%d, want 1", n)
	}
	if mustGet(t, store, "awg10").Enabled {
		t.Fatal("Enabled не снят: туннель пересоздастся без пользователя (Q1)")
	}
}
