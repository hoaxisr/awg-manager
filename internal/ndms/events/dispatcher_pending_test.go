package events

import (
	"context"
	"errors"
	"sync/atomic"
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
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
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
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: "d0", SystemName: "nwg0"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	// Запись в NDMS правится без хука: перечитай стор её по ifcreated — кэш бы изменился.
	f.Add(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: "d1", SystemName: "nwg9"})
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
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard0"); got == nil || got.Description != "d0" || got.SystemName != "nwg0" {
		t.Fatalf("known id record changed by ifcreated: %#v", got)
	}
}

// Стенд 5.01.C.6: ifcreated приходит ~1 с после iflayerchanged ctrl ×2 —
// в следующей пачке. Пачка из одних layer-хуков по незнакомому id уже
// добирает запись одним списком.
func TestDispatcher_LayerBeforeCreated_OneList(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	lists := f.ListCalls()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	for _, h := range f.DrainHooks() {
		if h.Type == "iflayerchanged" {
			d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
		}
	}
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got == nil || got.SystemName != "nwg1" {
		t.Fatalf("record from the list expected after layer hooks, got %#v", got)
	}
	if f.ListCalls() != lists+1 || f.E != 0 || q.Interfaces.HasPending() {
		t.Fatalf("want one list, E=0, no pending: lists=%d E=%d pending=%v", f.ListCalls()-lists, f.E, q.Interfaces.HasPending())
	}
}

// countLogger считает предупреждения.
type countLogger struct{ n atomic.Int32 }

func (l *countLogger) Warnf(string, ...any) { l.n.Add(1) }

// До Start (spool уже пишет, NDMS ещё не готов) очередь ограничена: старые
// события отбрасываются с одним предупреждением, а первый проход читает
// полный список — он покрывает потерянное (F572).
func TestDispatcher_QueueBoundBeforeStart_OverflowRefreshes(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background()) // bootstrap: Bridge0 в карте
	f.Remove("Bridge0")
	log := &countLogger{}
	d := NewDispatcher(q, log)
	done := drainBarrier(d)
	// Самое старое — снятие Bridge0; переполнение его отбросит.
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Bridge0"})
	for range maxQueuedEvents + 10 {
		d.Enqueue(Event{Type: EventType("noop")})
	}
	d.mu.Lock()
	queued := len(d.queue)
	d.mu.Unlock()
	if queued > maxQueuedEvents {
		t.Fatalf("очередь %d > предела %d", queued, maxQueuedEvents)
	}
	if got := log.n.Load(); got != 1 {
		t.Fatalf("предупреждений %d, want 1", got)
	}
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	if got, _ := q.Interfaces.Get(context.Background(), "Bridge0"); got != nil {
		t.Fatalf("потерянное снятие не покрыто списком: %#v", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// Обновление после переполнения не удалось (список не прочитан) — метка
// остаётся, и следующий проход повторяет его (F572, раунд 1).
func TestDispatcher_OverflowRefreshFails_RetriedNextPass(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	f.Remove("Bridge0")
	d := NewDispatcher(q, &countLogger{})
	done := drainBarrier(d)
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Bridge0"})
	for range maxQueuedEvents {
		d.Enqueue(Event{Type: EventType("noop")})
	}
	f.FailList(errors.New("rci down"))
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	f.FailList(nil)
	d.Enqueue(Event{Type: EventType("noop")})
	waitDrain(t, done)
	if got, _ := q.Interfaces.Get(context.Background(), "Bridge0"); got != nil {
		t.Fatalf("неудачное обновление не повторено: %#v", got)
	}
}
