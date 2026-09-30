package events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Сценарии Фазы 1 F546 на оракуле: E считает то, что на живом роутере
// становится строкой E в журнале ndm.

// deliverHooks отдаёт накопленные оракулом хуки диспетчеру одной пачкой и
// ждёт конца прохода.
func deliverHooks(t *testing.T, f *query.FakeNDMS, q *query.Queries, log Logger) {
	t.Helper()
	d := NewDispatcher(q, log)
	done := drainBarrier(d)
	for _, h := range f.DrainHooks() {
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID})
	}
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
}

// Чужой поток (как на HEAD) шлёт `interface X down` по отсутствующему X и
// сразу `no interface X`; хуки обоих событий приходят одной пачкой.
func TestScenario_PhantomPairFromOwnCommand(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	lists := f.ListCalls()

	for _, line := range []string{"interface Wireguard5 down", "no interface Wireguard5"} {
		if _, err := f.Post(ctx, map[string]any{"parse": line}); err != nil {
			t.Fatalf("post %q: %v", line, err)
		}
	}
	deliverHooks(t, f, q, NopLogger())

	// Phantoms == 1 здесь ожидаем: команда по отсутствующему пришла снаружи
	// теста, Фаза 1 её не убирает (это Фаза 2). Фаза 1 убирает E по паре
	// created→destroyed — поэтому ассерт только на E и на цену пачки.
	if f.E != 0 {
		t.Fatalf("E=%d, want 0: пара created→destroyed не должна читать по имени", f.E)
	}
	if got := f.ListCalls() - lists; got != 0 {
		t.Fatalf("пара created→destroyed стоила %d списков, want 0", got)
	}
	if got, _ := q.Interfaces.Get(ctx, "Wireguard5"); got != nil {
		t.Fatalf("снятый X остался в кэше: %#v", got)
	}
}

// ifdestroyed потерян: поллер пиров трижды спрашивает X. Первое чтение
// получает «записи нет» и выселяет X, остальные в NDMS не уходят.
func TestScenario_LostDestroyThenPoll(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap: X в кэше
	f.Remove("Wireguard3")
	_ = f.DrainHooks() // хук потерян

	for i := 0; i < 3; i++ {
		peers, err := q.Peers.GetPeers(ctx, "Wireguard3")
		if err != nil || len(peers) != 0 {
			t.Fatalf("poll %d: peers=%v err=%v, want пусто без ошибки", i, peers, err)
		}
		if i == 0 {
			if got, _ := q.Interfaces.Get(ctx, "Wireguard3"); got != nil {
				t.Fatalf("X не выселен после первого чтения: %#v", got)
			}
		}
		q.Peers.Invalidate("Wireguard3") // истёк TTL пиров — следующий опрос идёт в fetch
	}
	if f.E != 1 {
		t.Fatalf("E=%d, want 1: только первое чтение по снятому X", f.E)
	}
}

// Список (InvalidateAll) уже увидел Y, хук ifcreated Y доехал позже: id
// известен — ни одного лишнего списка.
func TestScenario_DelayedHooksVsInvalidateAll(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	f.Add(ndms.Interface{ID: "Wireguard4", Type: "Wireguard", SystemName: "nwg4"})
	q.Interfaces.InvalidateAll()
	if got, _ := q.Interfaces.Get(ctx, "Wireguard4"); got == nil {
		t.Fatal("InvalidateAll не увидел Y")
	}
	lists := f.ListCalls()

	deliverHooks(t, f, q, NopLogger())

	if got := f.ListCalls() - lists; got != 0 || f.E != 0 {
		t.Fatalf("поздний хук известного Y: %d списков, E=%d; want 0 и 0", got, f.E)
	}
	if got, _ := q.Interfaces.Get(ctx, "Wireguard4"); got == nil || got.SystemName != "nwg4" {
		t.Fatalf("Y потерян или испорчен после позднего хука: %#v", got)
	}
}

type recLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recLogger) Warnf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, fmt.Sprintf(format, args...))
}

func (l *recLogger) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// Список не прочитался во время добора: pending сохраняется, следующий проход
// добирает Z одним списком.
func TestScenario_ListErrorDuringPending(t *testing.T) {
	ctx := context.Background()
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(ctx) // bootstrap
	lists := f.ListCalls()

	f.FailList(errors.New("rci timeout"))
	f.Add(ndms.Interface{ID: "Wireguard6", Type: "Wireguard", SystemName: "nwg6"})
	log := &recLogger{}
	d := NewDispatcher(q, log)
	done := drainBarrier(d)
	d.Start()
	defer d.Stop()
	for _, h := range f.DrainHooks() {
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID})
	}
	waitDrain(t, done)

	if !log.has("reconcile pending") {
		t.Fatalf("ошибка ReconcilePending не дошла до журнала: %v", log.msgs)
	}
	if !q.Interfaces.HasPending() {
		t.Fatal("pending потерян на ошибке списка")
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("первый проход: %d списков, want 1 (неудачный)", got)
	}

	// Следующий проход — от любого хука, не связанного с Z.
	f.FailList(nil)
	d.Enqueue(Event{Type: EventIfIPChanged, ID: "Bridge0"})
	waitDrain(t, done)

	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("второй проход: всего %d списков, want 2", got)
	}
	if q.Interfaces.HasPending() || f.E != 0 {
		t.Fatalf("pending=%v E=%d после добора; want false и 0", q.Interfaces.HasPending(), f.E)
	}
	if got, _ := q.Interfaces.Get(ctx, "Wireguard6"); got == nil || got.SystemName != "nwg6" {
		t.Fatalf("Z не добран из списка: %#v", got)
	}
}
