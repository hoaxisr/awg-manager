package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func oracleQueries(t *testing.T, f *query.FakeNDMS) *query.Queries {
	t.Helper()
	return query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
}

// Пара created→destroyed в одной пачке: ifcreated незнакомого id расходится с
// картой — один список на пачку (дизайн §5), записи нет, E нет.
func TestDispatcher_CreatedThenDestroyed_OneList(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background()) // bootstrap
	lists := f.ListCalls()
	d := NewDispatcher(q, NopLogger())
	listed := listedBarrier(d)
	// Пачка: фантом создан и тут же снят (наша команда по отсутствующему + no).
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard2"})
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard2"})
	d.Start()
	defer d.Stop()
	waitListed(t, listed)
	if f.E != 0 || f.ListCalls() != lists+1 {
		t.Fatalf("E=%d lists=%d (want 0 and %d): created→destroyed — one list per batch", f.E, f.ListCalls(), lists+1)
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
	listed := listedBarrier(d)
	for _, h := range f.DrainHooks() {
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
	}
	d.Start()
	defer d.Stop()
	waitListed(t, listed)
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
	if f.ListCalls() != lists || len(f.Posts) != posts || f.E != 0 {
		t.Fatalf("known id must cost nothing: lists=%d posts=%d E=%d",
			f.ListCalls()-lists, len(f.Posts)-posts, f.E)
	}
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard0"); got == nil || got.Description != "d0" || got.SystemName != "nwg0" {
		t.Fatalf("known id record changed by ifcreated: %#v", got)
	}
}

// Стенд 5.01.C.6: ifcreated приходит ~1 с после iflayerchanged ctrl ×2 —
// в следующей пачке. Пачка из одних layer-хуков по незнакомому id списка не
// стоит; ifcreated следующей пачки — один список, запись по нему.
func TestDispatcher_LayerBeforeCreated_NoListUntilCreated(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	lists := f.ListCalls()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	listed := listedBarrier(d)
	hooks := f.DrainHooks()
	for _, h := range hooks {
		if h.Type == "iflayerchanged" {
			d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
		}
	}
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got != nil {
		t.Fatalf("layer-хук незнакомого id положил запись: %#v", got)
	}
	if f.ListCalls() != lists || f.E != 0 {
		t.Fatalf("layer-хуки: lists=%d E=%d, want 0, 0", f.ListCalls()-lists, f.E)
	}
	for _, h := range hooks {
		if h.Type == "ifcreated" {
			d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID})
		}
	}
	waitListed(t, listed)
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got == nil || got.SystemName != "nwg1" {
		t.Fatalf("record from the list expected after ifcreated, got %#v", got)
	}
	if f.ListCalls() != lists+1 || f.E != 0 {
		t.Fatalf("want one list, E=0: lists=%d E=%d", f.ListCalls()-lists, f.E)
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
	lists := f.ListCalls()
	log := &countLogger{}
	d := NewDispatcher(q, log)
	listed := listedBarrier(d)
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
	// Отброшенное снятие UI узнаёт только публикацией — после списка.
	if p := waitListed(t, listed); !p {
		t.Fatal("переполнение: publish=false, want true")
	}
	if n := f.ListCalls() - lists; n != 1 {
		t.Fatalf("переполнение: списков +%d, want 1", n)
	}
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

// S13: пачка хуков существования (чужое создание A, чужое снятие известного B)
// стоит ОДНОГО списка, и публикация идёт после его ОТВЕТА, один раз, с
// publish=true. Ответ списка удерживается в фейке: пока он не отпущен,
// слушатель молчит. Мутация «go (*p)(publish) до возврата ReconcileDirty» →
// слушатель зовётся при удержанном ответе → красный.
func TestDispatcher_ExistenceBatch_OneListThenPublish(t *testing.T) {
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Bridge0", Type: "Bridge"},
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	lists := f.ListCalls()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	f.Remove("Wireguard0")

	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	f.InList(func() {
		entered <- struct{}{}
		<-gate
	})
	var calls atomic.Int32
	got := make(chan bool, 4)
	d := NewDispatcher(q, NopLogger())
	d.SetExistenceListed(func(publish bool) {
		calls.Add(1)
		got <- publish
	})
	for _, h := range f.DrainHooks() {
		d.Enqueue(Event{Type: EventType(h.Type), ID: h.ID, Layer: h.Layer, Level: h.Level})
	}
	d.Start()
	defer d.Stop()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("список пачки не начат")
	}
	select {
	case <-got:
		t.Fatal("слушатель вызван до ответа списка")
	case <-time.After(100 * time.Millisecond):
	}
	release()

	var publish bool
	select {
	case publish = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("слушатель существования не вызван")
	}
	if !publish {
		t.Fatal("publish=false, want true: чужие создание и снятие")
	}
	if n := f.ListCalls() - lists; n != 1 || f.E != 0 {
		t.Fatalf("списков +%d E=%d, want 1/0: один список на пачку", n, f.E)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("слушатель вызван %d раз, want 1", n)
	}
}

// S10: флуд layer-хуков незнакомых id одной пачкой — ни списка, ни публикации.
func TestDispatcher_LayerHooksUnknownIDs_NoList(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	lists := f.ListCalls()
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	var calls atomic.Int32
	d.SetExistenceListed(func(bool) { calls.Add(1) })
	for i := range 1000 {
		d.Enqueue(Event{Type: EventIfLayerChanged, ID: fmt.Sprintf("Wireguard%d", i+1), Layer: "ctrl", Level: "running"})
	}
	d.Start()
	defer d.Stop()
	waitDrain(t, done)
	// Слушатель зовётся из горутины, запущенной в проходе до барьера: даём ей
	// время, чтобы лишний вызов успел проявиться.
	time.Sleep(100 * time.Millisecond)
	if n := f.ListCalls() - lists; n != 0 {
		t.Fatalf("layer-хуки незнакомых id: списков %d, want 0", n)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("слушатель существования вызван %d раз, want 0", n)
	}
}

// ownOracle — оракул с Bridge0 и Wireguard0, тёплый стор и диспетчер со
// слушателем существования; хуки самого оракула не доставляются.
func ownOracle(t *testing.T) (*query.FakeNDMS, *query.Queries, *Dispatcher, <-chan bool) {
	t.Helper()
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Bridge0", Type: "Bridge"},
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	d := NewDispatcher(q, NopLogger())
	return f, q, d, listedBarrier(d)
}

// noReaderList — карта не грязная и для следующего читателя: Get списка не читает.
func noReaderList(t *testing.T, f *query.FakeNDMS, q *query.Queries, lists int) {
	t.Helper()
	_, _ = q.Interfaces.Get(context.Background(), "Bridge0")
	if n := f.ListCalls() - lists; n != 0 {
		t.Fatalf("списков +%d, want 0", n)
	}
}

// П22: свой ifcreated (вердикт точки входа, Own) стор не трогает — ни метки,
// ни «грязно», ни списка, ни публикации (её делает создатель после записи
// туннеля в стор). Мутация «игнорировать Own» → OnCreated незнакомого id →
// список и публикация, красный.
func TestDispatcher_OwnCreated_NoListNoPublish(t *testing.T) {
	f, q, d, listed := ownOracle(t)
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	lists := f.ListCalls()
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1", Own: true})
	d.Start()
	defer d.Stop()
	if p := waitListed(t, listed); p {
		t.Fatal("своё создание опубликовано")
	}
	noReaderList(t, f, q, lists)
}

// П22: X создан (k), снят нами, создан снова (k+1) и показан списком; свой
// поздний ifdestroyed воплощения k — ни списка, ни публикации, X в карте.
// «Метку сняло новое воплощение» (leak10) кредит не знает: решает порядок.
// Мутация «звать OnDestroyed при Own» → X известен → «грязно» → список, красный.
func TestDispatcher_OwnDestroyed_NewIncarnationListed_NoList(t *testing.T) {
	f, q, d, listed := ownOracle(t)
	ctx := context.Background()
	q.Interfaces.SetCreatedBackoff()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	if _, err := q.Interfaces.ConfirmCreated(ctx, "Wireguard1", true); err != nil { // k
		t.Fatal(err)
	}
	f.Remove("Wireguard1")
	q.Interfaces.ExpectRemoval("Wireguard1").Removed()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	if _, err := q.Interfaces.ConfirmCreated(ctx, "Wireguard1", true); err != nil { // k+1
		t.Fatal(err)
	}
	if rec, _ := q.Interfaces.Get(ctx, "Wireguard1"); rec == nil {
		t.Fatal("k+1 не в карте")
	}
	lists := f.ListCalls()
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1", Own: true})
	d.Start()
	defer d.Stop()
	if p := waitListed(t, listed); p {
		t.Fatal("свой поздний ifdestroyed опубликован")
	}
	noReaderList(t, f, q, lists)
	if rec, _ := q.Interfaces.Get(ctx, "Wireguard1"); rec == nil {
		t.Fatal("свой поздний ifdestroyed снял новое воплощение из карты")
	}
}

// M6′/П22: свой ifcreated пришёл, пока первый список ConfirmCreated в полёте,
// а имени в карте ещё нет — Own: «грязно» не ставится, подтверждает первый
// список, ReconcileDirty списка не читает. Мутация «игнорировать Own» →
// OnCreated незнакомого id → «грязно» новее списка → ReconcileDirty +1, красный.
func TestDispatcher_OwnCreatedInFirstList_NoDispatcherList(t *testing.T) {
	f, q, d, _ := ownOracle(t)
	ctx := context.Background()
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	f.InList(func() {
		f.InList(nil)
		d.apply(Event{Type: EventIfCreated, ID: "Wireguard1", Own: true})
	})
	lists := f.ListCalls()
	if c, err := q.Interfaces.ConfirmCreated(ctx, "Wireguard1", true); err != nil || c.Name() != "Wireguard1" {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if err := q.Interfaces.ReconcileDirty(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("%d списков, want 1 (только список ConfirmCreated)", got)
	}
	noReaderList(t, f, q, f.ListCalls())
}

// Свою грань conf карта знает от ExpectConf; свой поздний хук прежней грани
// слой не перетирает, чужой — перетирает. Мутация «OnLayerChanged и при Own»
// → слой disabled, красный.
func TestDispatcher_OwnConf_KeepsOurLayer(t *testing.T) {
	_, q, d, _ := ownOracle(t)
	ctx := context.Background()
	layer := func() string {
		rec, _ := q.Interfaces.Get(ctx, "Wireguard0")
		return rec.ConfLayer
	}
	q.Interfaces.ExpectConf("Wireguard0", false) // карта: disabled
	q.Interfaces.ExpectConf("Wireguard0", true)  // карта: running
	d.apply(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "disabled", Own: true})
	if got := layer(); got != "running" {
		t.Fatalf("свой поздний conf=disabled перетёр слой: %q", got)
	}
	d.apply(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "disabled"})
	if got := layer(); got != "disabled" {
		t.Fatalf("чужой conf=disabled не применён: %q", got)
	}
}

// Чужое создание публикуется всегда — и разошедшееся с картой (+1 список), и
// уже показанное списком панели до хука (П14: «публиковать только при
// расхождении» отвергнуто). Мутация «publish только при dirty» → второй
// случай publish=false, красный.
func TestDispatcher_ForeignCreated_ListedPublished(t *testing.T) {
	for _, tc := range []struct {
		name      string
		shown     bool
		wantLists int
	}{
		{"карта не знает", false, 1},
		{"уже показано списком", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, q, d, listed := ownOracle(t)
			f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
			if tc.shown {
				if err := q.Interfaces.Refresh(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			lists := f.ListCalls()
			d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})
			d.Start()
			defer d.Stop()
			if p := waitListed(t, listed); !p {
				t.Fatal("чужое создание не опубликовано")
			}
			if n := f.ListCalls() - lists; n != tc.wantLists {
				t.Fatalf("списков +%d, want %d", n, tc.wantLists)
			}
		})
	}
}

// Имя, снятое нами, занял чужой: его ifcreated и ifdestroyed — чужие (Own
// false — кредитов нет), публикуются.
func TestDispatcher_ForeignDestroyed_RecreatedName_Published(t *testing.T) {
	f, q, d, listed := ownOracle(t)
	f.Remove("Wireguard0")
	q.Interfaces.ExpectRemoval("Wireguard0").Removed()
	f.Add(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"})
	d.Start()
	defer d.Stop()
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard0"})
	if p := waitListed(t, listed); !p {
		t.Fatal("чужое создание не опубликовано")
	}
	f.Remove("Wireguard0")
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard0"})
	if p := waitListed(t, listed); !p {
		t.Fatal("ifdestroyed чужого воплощения не опубликован")
	}
}

// Своё создание и чужое снятие в одной пачке — один список, публикация (OR
// по пачке), в любом порядке хуков. Мутации: «publish только если все чужие»
// → красный; «решает последнее событие» → «чужое → своё» publish=false, красный.
func TestDispatcher_MixedBatch_PublishIfAnyForeign(t *testing.T) {
	for _, tc := range []struct {
		name         string
		foreignFirst bool
	}{
		{"своё → чужое", false},
		{"чужое → своё", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, d, listed := ownOracle(t)
			f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
			f.Remove("Wireguard0")
			own := Event{Type: EventIfCreated, ID: "Wireguard1", Own: true}
			foreign := Event{Type: EventIfDestroyed, ID: "Wireguard0"}
			if tc.foreignFirst {
				own, foreign = foreign, own
			}
			lists := f.ListCalls()
			d.Enqueue(own)
			d.Enqueue(foreign)
			d.Start()
			defer d.Stop()
			if p := waitListed(t, listed); !p {
				t.Fatal("чужое снятие в пачке со своим созданием не опубликовано")
			}
			if n := f.ListCalls() - lists; n != 1 {
				t.Fatalf("списков +%d, want 1", n)
			}
		})
	}
}

// Два ifdestroyed известных id одной пачкой — один список. Wireguard1 жив
// (устаревший хук по переиспользованному имени): список после первого
// снятия оставил бы его в карте, и второй хук стоил бы ещё одного списка.
func TestDispatcher_TwoDestroyedInBatch_OneList(t *testing.T) {
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard", SystemName: "nwg0"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard", SystemName: "nwg1"})
	q := oracleQueries(t, f)
	_, _ = q.Interfaces.List(context.Background())
	f.Remove("Wireguard0")
	lists := f.ListCalls()
	d := NewDispatcher(q, NopLogger())
	listed := listedBarrier(d)
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard0"})
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})
	d.Start()
	defer d.Stop()
	if p := waitListed(t, listed); !p {
		t.Fatal("publish=false, want true")
	}
	if n := f.ListCalls() - lists; n != 1 || f.E != 0 {
		t.Fatalf("два ifdestroyed: списков +%d E=%d, want 1/0", n, f.E)
	}
}
