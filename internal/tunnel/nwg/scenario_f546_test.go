package nwg

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/events"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
		d.Enqueue(events.Event{Type: events.EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
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

// K2 F546 (стенд 30.09): наш WireguardN снят снаружи, ifdestroyed ещё не
// доставлен, панель опрашивает состояние. Состояние — из снимка списка: ни
// одного запроса по имени (прежде `show interface name=X` → E «unable to
// find»), ни одного POST; до хука — Running по снимку, после — NotCreated.
func TestScenario_ExternalRemoveNoHook_GetStateNoRCI(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS()
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	o := &OperatorNativeWG{queries: q, appLog: logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		supportsASC: func() bool { return true }, hasProxySlot: func(int) bool { return false }}
	t.Cleanup(o.Close)
	st := &storage.AWGTunnel{Name: "n", NWGIndex: 5}

	if _, err := q.Interfaces.List(ctx); err != nil { // карта тёплая, как в проде
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Wireguard5", Type: "Wireguard"})
	f.SetDetail("Wireguard5", json.RawMessage(`{"link":"up","summary":{"layer":{"conf":"running"}},
		"wireguard":{"status":"up","peer":[{"online":true,"last-handshake":5}]}}`))
	deliverHooks(t, q, f)
	if s := o.GetState(ctx, st); s.State != tunnel.StateRunning {
		t.Fatalf("после создания State = %v, want Running", s.State)
	}

	f.Remove("Wireguard5") // хук в очереди оракула, диспетчеру не доставлен
	lists, posts := f.ListCalls(), len(f.Posts)
	for range 3 {
		s := o.GetState(ctx, st)
		if f.E != 0 {
			t.Fatalf("GetState по снятому без хука: E=%d, want 0", f.E)
		}
		if s.State != tunnel.StateRunning {
			t.Fatalf("до ifdestroyed State = %v, want Running (снимок)", s.State)
		}
	}
	if f.E != 0 || len(f.Posts) != posts || f.ListCalls()-lists > 1 {
		t.Fatalf("GetState по снятому: E=%d POST+%d списков+%d; want 0, 0, ≤1",
			f.E, len(f.Posts)-posts, f.ListCalls()-lists)
	}

	deliverHooks(t, q, f)
	if s := o.GetState(ctx, st); s.State != tunnel.StateNotCreated || f.E != 0 {
		t.Fatalf("после ifdestroyed State = %v E=%d, want NotCreated/0", s.State, f.E)
	}
}

// deliverHooks доставляет очередь хуков оракула диспетчеру и ждёт конца прохода.
func deliverHooks(t *testing.T, q *query.Queries, f *query.FakeNDMS) {
	t.Helper()
	d := events.NewDispatcher(q, events.NopLogger())
	done := make(chan struct{}, 1)
	d.SetRoutingChanged(func() { done <- struct{}{} }) // конец прохода
	d.Start()
	defer d.Stop()
	for _, h := range f.DrainHooks() {
		d.Enqueue(events.Event{Type: events.EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("проход диспетчера не завершился за 2 с")
	}
}

// Батч старта идёт транспортом мимо слоя commands: postIfaceBatch метит карту
// грязной, и снимок сразу после Start показывает состояние ПОСЛЕ батча — одним
// списком, без запросов по имени.
func TestStartStop_BatchMarksDirty_SnapshotAfterBatch(t *testing.T) {
	ctx := context.Background()
	o, _, _, f, _ := newLifecycleOperator(t, true, false)
	st := nwgStored(storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{Jc: 4, H1: "10-20", H2: "2", H3: "3", H4: "4"}})
	_ = o.GetState(ctx, st) // карта и снимок тёплые: состояние до старта — down
	if rec, _ := o.queries.Interfaces.Get(ctx, "Wireguard0"); rec == nil || rec.State == "up" {
		t.Fatalf("до старта: %+v", rec)
	}
	lists := f.ListCalls()
	if err := o.Start(ctx, st); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Logf("списков за Start: %d", f.ListCalls()-lists)
	lists = f.ListCalls()
	snap, err := o.queries.Interfaces.Snapshot(ctx, query.SnapshotRecent)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := snap.Record("Wireguard0"); rec.State != "up" {
		t.Fatalf("снимок после Start — до батча: state=%q", rec.State)
	}
	_ = o.GetState(ctx, st)
	if got := f.ListCalls() - lists; got > 1 {
		t.Fatalf("после Start списков %d, want ≤1", got)
	}

	// Stop: батч `up false` — единственная запись потока (DNS пуст), метку
	// ставит только postIfaceBatch; снимок моложе 2 с иначе отдал бы «up».
	if err := o.Stop(ctx, st); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	lists = f.ListCalls()
	snap, err = o.queries.Interfaces.Snapshot(ctx, query.SnapshotRecent)
	if err != nil {
		t.Fatal(err)
	}
	if rec, _ := snap.Record("Wireguard0"); rec.State != "down" {
		t.Fatalf("снимок после Stop — до батча: state=%q", rec.State)
	}
	if got := f.ListCalls() - lists; got > 1 {
		t.Fatalf("после Stop списков %d, want ≤1", got)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}
