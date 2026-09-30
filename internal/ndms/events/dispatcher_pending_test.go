package events

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func oracleQueries(t *testing.T, f *query.FakeNDMS) *query.Queries {
	t.Helper()
	return query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
}

func TestDispatcher_CreatedThenDestroyed_NoRCI(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background()) // bootstrap
	lists := f.ListCalls()
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	// Пачка: фантом создан и тут же снят (наша команда по отсутствующему + no).
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard2"})
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard2"})
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	if q.Interfaces.HasPending() {
		t.Fatal("pending остался после пачки created→destroyed")
	}
	if f.E != 0 || f.ListCalls() != lists {
		t.Fatalf("E=%d lists=%d (want 0 and %d): created→destroyed must cost nothing", f.E, f.ListCalls(), lists)
	}
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard2"); got != nil {
		t.Fatalf("stub inserted: %#v", got)
	}
}

func TestDispatcher_ExternalCreate_OneListPerBatch(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	lists := f.ListCalls()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	f.Add(ndms.Interface{ID: "Wireguard2", Type: "Wireguard", SystemName: "nwg2"})
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	for _, h := range f.DrainHooks() {
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID})
	}
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	a, _ := q.Interfaces.Get(context.Background(), "Wireguard1")
	b, _ := q.Interfaces.Get(context.Background(), "Wireguard2")
	if a == nil || b == nil {
		t.Fatalf("созданные не видны после пачки: %#v %#v", a, b)
	}
	if f.ListCalls() != lists+1 || f.E != 0 {
		t.Fatalf("want exactly one list for the batch, got %d extra, E=%d", f.ListCalls()-lists, f.E)
	}
	if a.SystemName != "nwg1" {
		t.Fatalf("record from the list expected, got %#v", a)
	}
}

func TestDispatcher_LateCreatedForKnownID_IsFree(t *testing.T) {
	// id уже в кэше (мы создали и подтвердили сами) → ifcreated не ведёт ни к чему.
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	lists, posts := f.ListCalls(), len(f.Posts)
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard0"})
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	if f.ListCalls() != lists || len(f.Posts) != posts || q.Interfaces.HasPending() || f.E != 0 {
		t.Fatalf("known id must cost nothing: lists=%d posts=%d pending=%v E=%d",
			f.ListCalls()-lists, len(f.Posts)-posts, q.Interfaces.HasPending(), f.E)
	}
}
