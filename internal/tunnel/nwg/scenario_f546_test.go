package nwg

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/events"
)

// S7 F546: WireguardN снят снаружи, хук ifdestroyed задержан. Карта ещё
// считает интерфейс живым, но Delete подтверждает его свежим списком — 0
// команд. Поздний хук потом снимает уже забытый id — ни одного списка.
func TestScenario_ExternalRemoveDelayedHook_DeleteThenHook(t *testing.T) {
	ctx := context.Background()
	o, _, poster, f, srv := newLifecycleOperator(t, false, false)
	if _, err := o.queries.Interfaces.List(ctx); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	f.Remove("Wireguard0") // хук в очереди оракула, диспетчеру не доставлен

	if err := o.Delete(ctx, nwgStored(awgObfuscatedIface())); err != nil {
		t.Fatal(err)
	}
	if srv.log.posts() != 0 || len(poster.list()) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("Delete по снятому: batches=%d posts=%v E=%d phantoms=%d",
			srv.log.posts(), poster.list(), f.E, f.Phantoms)
	}

	lists := f.ListCalls()
	d := events.NewDispatcher(o.queries, events.NopLogger())
	done := make(chan struct{}, 1)
	d.SetRoutingChanged(func() { done <- struct{}{} }) // конец прохода
	d.Start()
	defer d.Stop()
	for _, h := range f.DrainHooks() {
		d.Enqueue(events.Event{Type: events.EventType(h.Type), ID: h.ID})
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("проход диспетчера не завершился за 2 с")
	}

	if got := f.ListCalls() - lists; got != 0 || f.E != 0 || o.queries.Interfaces.HasPending() {
		t.Fatalf("поздний ifdestroyed: %d списков, E=%d, pending=%v; want 0, 0, false",
			got, f.E, o.queries.Interfaces.HasPending())
	}
	if _, ok, _ := o.queries.Interfaces.Lookup(ctx, "Wireguard0"); ok {
		t.Fatal("снятый Wireguard0 остался в кэше")
	}
}
