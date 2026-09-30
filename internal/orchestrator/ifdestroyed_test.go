package orchestrator

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

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

// R31: хук ifdestroyed опоздал, а запись уже пересоздана (Restart в окне
// опоздания) — свежий список её видит: хук устарел, туннель не трогаем.
// Записи нет — остановка; список не прочитан — не останавливаем.
func TestHandleEvent_IfDestroyed_FreshListDecides(t *testing.T) {
	cases := []struct {
		name        string
		present     bool
		err         error
		wantStops   int64
		wantEnabled bool
	}{
		{"запись пересоздана — хук устарел", true, nil, 0, true},
		{"записи нет — остановка", false, nil, 1, false},
		{"список не прочитан — не останавливаем", false, errors.New("injected: rci"), 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, op, store := runningKernelOrch(t)
			var asked []string
			o.SetRecordPresenceProbe(func(_ context.Context, name string) (bool, error) {
				asked = append(asked, name)
				return tc.present, tc.err
			})
			if err := o.HandleEvent(context.Background(), Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun10"}); err != nil {
				t.Fatalf("HandleEvent: %v", err)
			}
			if !slices.Equal(asked, []string{"OpkgTun10"}) {
				t.Fatalf("проба: %v", asked)
			}
			if n := op.stops.Load(); n != tc.wantStops {
				t.Fatalf("stops=%d, want %d", n, tc.wantStops)
			}
			if got := mustGet(t, store, "awg10").Enabled; got != tc.wantEnabled {
				t.Fatalf("Enabled=%v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

// Чужое имя — список не читаем (стоимость: только для записи нашего туннеля).
func TestHandleEvent_IfDestroyed_ForeignName_NoProbe(t *testing.T) {
	o, op, _ := runningKernelOrch(t)
	o.SetRecordPresenceProbe(func(context.Context, string) (bool, error) {
		t.Fatal("список прочитан для чужой записи")
		return false, nil
	})
	if err := o.HandleEvent(context.Background(), Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun11"}); err != nil {
		t.Fatal(err)
	}
	if op.stops.Load() != 0 {
		t.Fatal("чужая запись остановила туннель")
	}
}

// R31, гонка check-then-act: список ответил «записи нет», и до Stop успевает
// Restart, пересоздающий запись. Проверка и остановка идут под per-tunnel
// замком, поэтому Restart ждёт: сначала законная остановка по снятой записи,
// потом Restart поднимает туннель и возвращает Enabled. Проба вне замка дала
// бы обратный порядок — устаревший хук остановил бы пересозданный туннель.
func TestHandleEvent_IfDestroyed_ProbeAndStopAtomicWithRestart(t *testing.T) {
	o, op, store := runningKernelOrch(t)
	o.state.anyWANUpFn = func() bool { return true }
	probeEntered, probeRelease := make(chan struct{}), make(chan struct{})
	o.SetRecordPresenceProbe(func(context.Context, string) (bool, error) {
		present := op.coldStarts.Load() > 0 // запись есть, если её пересоздал ColdStart
		close(probeEntered)
		<-probeRelease
		return present, nil
	})

	destroyedDone := make(chan error, 1)
	go func() {
		destroyedDone <- o.HandleEvent(context.Background(), Event{Type: EventNDMSIfDestroyed, NDMSName: "OpkgTun10"})
	}()
	<-probeEntered

	restartDone := make(chan error, 1)
	go func() {
		restartDone <- o.HandleEvent(context.Background(), Event{Type: EventRestart, Tunnel: "awg10"})
	}()
	// Замок держит ifdestroyed — Restart обязан ждать. Если он прошёл,
	// значит проба вне замка; тогда даём ему закончить до ответа пробы.
	select {
	case err := <-restartDone:
		restartDone <- err
	case <-time.After(time.Second):
	}
	close(probeRelease)
	if err := <-destroyedDone; err != nil {
		t.Fatalf("ifdestroyed: %v", err)
	}
	if err := <-restartDone; err != nil {
		t.Fatalf("restart: %v", err)
	}

	if !mustGet(t, store, "awg10").Enabled {
		t.Fatal("пересозданный туннель остановлен устаревшим ifdestroyed: Enabled=false")
	}
	o.mu.Lock()
	running := o.state.tunnels["awg10"].Running
	o.mu.Unlock()
	if !running || op.coldStarts.Load() != 1 {
		t.Fatalf("running=%v coldStarts=%d, want true/1", running, op.coldStarts.Load())
	}
}
