package metrics

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type fakeRunningProvider struct {
	mu   sync.Mutex
	refs []InterfaceRef
	// onTick — зовётся в начале каждого тика (RunningInterfaces).
	onTick func()
}

func (f *fakeRunningProvider) RunningInterfaces(_ context.Context) []InterfaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onTick != nil {
		f.onTick()
	}
	out := make([]InterfaceRef, len(f.refs))
	copy(out, f.refs)
	return out
}

// newNDMS — оракул с Wireguard этого файла; пиры задаёт тест через SetDetail.
func newNDMS() *query.FakeNDMS {
	return query.NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard10", Type: "Wireguard"})
}

// newPeers — PeerStore поверх оракула f (пиры — из снимка списка, F546).
// Карта интерфейсов возвращается для dirtyEachTick.
func newPeers(f *query.FakeNDMS, ttl time.Duration) (*query.PeerStore, *query.InterfaceStore) {
	ifs := query.NewInterfaceStore(f, query.NopLogger())
	return query.NewPeerStoreWithTTL(query.NopLogger(), ifs, ttl), ifs
}

// dirtyEachTick метит карту грязной в начале каждого тика: тогда тик, который
// спросил пиры хоть у одного ref, читает ровно один список, и ListCalls
// считает тики, дошедшие до NDMS.
func dirtyEachTick(run *fakeRunningProvider, ifs *query.InterfaceStore) {
	run.onTick = func() { ifs.Invalidate("tick") }
}

func (f *fakeRunningProvider) Set(refs []InterfaceRef) {
	f.mu.Lock()
	f.refs = refs
	f.mu.Unlock()
}

type fakeMetricsPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	Type string
	Data any
}

func (p *fakeMetricsPublisher) Publish(eventType string, data any) {
	p.mu.Lock()
	p.events = append(p.events, publishedEvent{eventType, data})
	p.mu.Unlock()
}

func (p *fakeMetricsPublisher) Events() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishedEvent, len(p.events))
	copy(out, p.events)
	return out
}

type fakeSubs struct{ count int }

func (f *fakeSubs) ClientCount() int { return f.count }

type fakeSnapshotPub struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeSnapshotPub) PublishServerSnapshot(_ context.Context) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
}

func (f *fakeSnapshotPub) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeHistory struct {
	mu      sync.Mutex
	entries []fakeHistoryEntry
}

type fakeHistoryEntry struct {
	ID string
	Rx int64
	Tx int64
}

func (f *fakeHistory) Feed(id string, rx, tx int64) {
	f.mu.Lock()
	f.entries = append(f.entries, fakeHistoryEntry{id, rx, tx})
	f.mu.Unlock()
}

func (f *fakeHistory) Entries() []fakeHistoryEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeHistoryEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

func TestMetricsPoller_PollsRunningAndPublishes(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":100,"txbytes":200,"last-handshake":5,"online":true,"enabled":true}]}}`))
	peers, _ := newPeers(f, 1*time.Second)

	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0", IsServer: false}})

	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}
	history := &fakeHistory{}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.SetHistoryFeeder(history)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)

	evs := pub.Events()
	if len(evs) == 0 {
		t.Fatal("no events published")
	}
	if evs[0].Type != "tunnel:traffic" {
		t.Errorf("first event type: %s", evs[0].Type)
	}
	payload := evs[0].Data.(map[string]any)
	if payload["id"] != "Wireguard0" {
		t.Errorf("id: %v", payload["id"])
	}
	if payload["rxBytes"].(int64) != 100 {
		t.Errorf("rx: %v", payload["rxBytes"])
	}
	hs, ok := payload["lastHandshake"].(string)
	if !ok {
		t.Fatalf("lastHandshake: want string, got %T", payload["lastHandshake"])
	}
	if hs == "" {
		t.Errorf("lastHandshake: want RFC3339, got empty (secondsAgo=5 should produce timestamp)")
	}
	if _, err := time.Parse(time.RFC3339, hs); err != nil {
		t.Errorf("lastHandshake %q not RFC3339: %v", hs, err)
	}
	if _, extra := payload["lastHandshakeSecondsAgo"]; extra {
		t.Errorf("lastHandshakeSecondsAgo must not be on the wire anymore")
	}
	if _, extra := payload["peerCount"]; extra {
		t.Errorf("peerCount must not be on the wire anymore")
	}

	ents := history.Entries()
	if len(ents) == 0 {
		t.Fatal("history feed not called")
	}
	if ents[0].ID != "Wireguard0" || ents[0].Rx != 100 || ents[0].Tx != 200 {
		t.Errorf("history entry: %+v", ents[0])
	}
}

func TestMetricsPoller_EmptyPeersCooldown_SkipsSubsequentTicks(t *testing.T) {
	// Server with no peers returns an empty list. After the first
	// tick observes this, the poller must skip that interface for
	// the cooldown period so NDMS isn't hammered every 10s for
	// nothing.
	f := newNDMS()
	f.SetDetail("Wireguard1", json.RawMessage(`{"wireguard":{"peer":[]}}`))
	peers, ifs := newPeers(f, 1*time.Millisecond)

	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard1", IsServer: false}})
	dirtyEachTick(run, ifs)
	lists := f.ListCalls()

	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 10*time.Millisecond)
	p.Start()
	defer p.Stop()

	// Let at least 6 ticks fire. Without the cooldown, that's 6 RCI
	// calls. With it, only the first tick hits NDMS and every
	// following tick is skipped.
	time.Sleep(80 * time.Millisecond)

	calls := f.ListCalls() - lists
	if calls == 0 {
		t.Fatal("expected at least one RCI call to prime the empty state")
	}
	if calls > 2 {
		t.Errorf("cooldown not honoured: want at most 2 RCI calls, got %d", calls)
	}
}

// При закрытой панели поллер РАЗРЕЖАЕТСЯ, а не выключается: он единственный
// кормилец истории трафика не управляемых системных туннелей, и полный гейт
// оставлял в графике дыру во всю длину простоя (F352).
func TestMetricsPoller_IdleStillFeedsHistory(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":10,"txbytes":20,"last-handshake":0,"online":true,"enabled":true}]}}`))
	peers, _ := newPeers(f, 1*time.Millisecond)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0"}})
	history := &fakeHistory{}

	// idleInterval = 12×interval = 60 мс. За 250 мс простоя обязано пройти
	// хотя бы несколько разрежённых тиков.
	p := NewWithInterval(peers, &fakeMetricsPublisher{}, run, &fakeSubs{count: 0}, NopLogger(), 5*time.Millisecond)
	p.SetHistoryFeeder(history)
	p.Start()
	defer p.Stop()

	time.Sleep(250 * time.Millisecond)

	if len(history.Entries()) == 0 {
		t.Fatal("история не пишется при закрытой панели — дыра в графике системных туннелей")
	}
}

// Разрежение обязано быть настоящим: без панели тиков должно быть заметно
// меньше, чем с ней. Иначе экономия, ради которой всё затевалось, исчезает.
func TestMetricsPoller_IdleIsThinned(t *testing.T) {
	// Считаем обращения к роутеру, а не записи истории: история кормится только
	// на смену дайджеста пиров, а он в фейке постоянный.
	poll := func(clients int) int {
		f := newNDMS()
		f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":10,"txbytes":20,"last-handshake":0,"online":true,"enabled":true}]}}`))
		peers, ifs := newPeers(f, 1*time.Millisecond)
		run := &fakeRunningProvider{}
		run.Set([]InterfaceRef{{ID: "Wireguard0"}})
		dirtyEachTick(run, ifs)

		p := NewWithInterval(peers, &fakeMetricsPublisher{}, run, &fakeSubs{count: clients}, NopLogger(), 5*time.Millisecond)
		p.SetHistoryFeeder(&fakeHistory{})
		p.Start()
		defer p.Stop()

		time.Sleep(250 * time.Millisecond)
		return f.ListCalls()
	}

	idle, watched := poll(0), poll(1)
	if idle == 0 {
		t.Fatal("простой не должен глушить поллер полностью")
	}
	// 12-кратное разрежение; берём запас втрое на дрожание планировщика.
	if idle*4 > watched {
		t.Errorf("разрежения нет: в простое %d тиков против %d при открытой панели", idle, watched)
	}
}

func TestMetricsPoller_ServerChange_InvokesSnapshotCallback(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard10", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":1,"txbytes":2,"last-handshake":0,"online":true,"enabled":true}]}}`))
	peers, _ := newPeers(f, 1*time.Second)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard10", IsServer: true}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}
	snap := &fakeSnapshotPub{}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.SetServerSnapshotPublisher(snap)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)

	if snap.Calls() == 0 {
		t.Fatal("server snapshot callback not invoked for changed server interface")
	}
	for _, ev := range pub.Events() {
		if ev.Type == "server:updated" {
			t.Errorf("MetricsPoller must not publish server:updated directly, got %+v", ev)
		}
	}
}

func TestMetricsPoller_ServerNilCallback_NoPublish(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard10", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":1,"txbytes":2,"last-handshake":0,"online":true,"enabled":true}]}}`))
	peers, _ := newPeers(f, 1*time.Second)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard10", IsServer: true}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)
	for _, ev := range pub.Events() {
		if ev.Type == "server:updated" {
			t.Errorf("no snapshot callback wired → no server:updated should be published, got %+v", ev)
		}
	}
}

// F469: туннель публикуется каждый тик и при неизменных данных — событие
// служит часами графика (точка на событие, скорость по соседним).
func TestMetricsPoller_TunnelPublishesEveryTick(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":100,"txbytes":200,"last-handshake":5,"online":true,"enabled":true}]}}`))
	peers, _ := newPeers(f, 1*time.Millisecond)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0"}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 10*time.Millisecond)
	p.Start()
	defer p.Stop()

	time.Sleep(80 * time.Millisecond)

	if evs := pub.Events(); len(evs) < 3 {
		t.Errorf("неизменный туннель: событий %d, ждали на каждом тике (>=3)", len(evs))
	}
}

// F469: сервер сравнивается по абсолютному времени рукопожатия. Растущие
// «секунды назад» при том же рукопожатии — не изменение (раньше подсказка
// уходила каждый тик); новые байты — изменение.
func TestServerDigest_HandshakeAgeIsNotAChange(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	a := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 10}}, t0)
	b := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 15}}, t0.Add(5*time.Second))
	if !a.equal(b) {
		t.Error("то же рукопожатие 5 с спустя сочтено изменением")
	}
	// Тик из кэша PeerStore (TTL 8 с): «секунды назад» те же, что при
	// чтении, а now на тик позже — это то же рукопожатие.
	cached := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 10}}, t0.Add(8*time.Second))
	if !a.equal(cached) {
		t.Error("данные из кэша PeerStore на следующем тике сочтены новым рукопожатием")
	}
	// Целые «секунды назад» пересчитываются в абсолютное время с дрожью в 1 с.
	jitter := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 16}}, t0.Add(5*time.Second))
	if !a.equal(jitter) {
		t.Error("дрожь пересчёта в 1 с сочтена новым рукопожатием")
	}
	// Новое рукопожатие (WireGuard — раз в ~2 мин): 120 с после прежнего.
	c := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 1}}, t0.Add(111*time.Second))
	if a.equal(c) {
		t.Error("новое рукопожатие не сочтено изменением")
	}
	d := digestPeers([]ndms.Peer{{RxBytes: 9, TxBytes: 2, LastHandshakeSecondsAgo: 15}}, t0.Add(5*time.Second))
	if a.equal(d) {
		t.Error("новые байты не сочтены изменением")
	}
	offline := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 15, Online: false}}, t0.Add(5*time.Second))
	onlineA := digestPeers([]ndms.Peer{{RxBytes: 1, TxBytes: 2, LastHandshakeSecondsAgo: 10, Online: true}}, t0)
	if onlineA.equal(offline) {
		t.Error("пир ушёл в offline без новых байт — изменение не замечено")
	}
	never := digestPeers([]ndms.Peer{{LastHandshakeSecondsAgo: math.MaxInt32}}, t0)
	never2 := digestPeers([]ndms.Peer{{LastHandshakeSecondsAgo: math.MaxInt32}}, t0.Add(5*time.Second))
	if !never.equal(never2) {
		t.Error("«рукопожатий не было» на разных тиках сочтено изменением")
	}
}

func TestMetricsPoller_Stop_Idempotent(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[]}}`))
	peers, _ := newPeers(f, 1*time.Second)
	run := &fakeRunningProvider{}
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.Start()
	p.Stop()
	p.Stop() // must not panic
}

func TestMetricsPoller_Stop_WithoutStart(t *testing.T) {
	f := newNDMS()
	peers, _ := newPeers(f, 1*time.Second)
	run := &fakeRunningProvider{}
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 0}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	// Stop without Start — must not block, must not panic.
	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Stop without Start should return immediately")
	}
}

// В простое серверные интерфейсы не опрашиваются: историю трафика они не кормят
// (её ведёт publishTunnel, и только для не серверных ref), а их единственный
// выход — подсказка в шину, где при нуле зрителей никого нет. Туннельные при
// этом опрашиваться ОБЯЗАНЫ — ради них поллер в простое и не выключен.
func TestMetricsPoller_IdleSkipsServersButNotTunnels(t *testing.T) {
	f := newNDMS()
	peerJSON := `{"wireguard":{"peer":[{"public-key":"k","rxbytes":10,"txbytes":20,"last-handshake":0,"online":true,"enabled":true}]}}`
	f.SetDetail("Wireguard0", json.RawMessage(peerJSON))
	f.SetDetail("Wireguard10", json.RawMessage(peerJSON))
	peers, ifs := newPeers(f, 1*time.Millisecond)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0"}, {ID: "Wireguard10", IsServer: true}})
	dirtyEachTick(run, ifs)
	history := &fakeHistory{}
	// Список общий на тик, поэтому опрос сервера виден не по NDMS, а по
	// выходу серверной половины: первое наблюдение сервера — подсказка.
	snap := &fakeSnapshotPub{}

	p := NewWithInterval(peers, &fakeMetricsPublisher{}, run, &fakeSubs{count: 0}, NopLogger(), 5*time.Millisecond)
	p.SetHistoryFeeder(history)
	p.SetServerSnapshotPublisher(snap)
	p.Start()
	defer p.Stop()

	time.Sleep(250 * time.Millisecond)

	if f.ListCalls() == 0 || len(history.Entries()) == 0 {
		t.Error("туннель в простое не опрашивается — история трафика встанет")
	}
	if got := snap.Calls(); got != 0 {
		t.Errorf("сервер в простое опрошен (подсказок %d), потребителя у этого нет", got)
	}
}

// F546: тик спрашивает пиры у всех ref параллельно, и они делят ОДИН список —
// ни одного запроса по имени.
func TestPoller_TickOneListForAllRefs(t *testing.T) {
	f := newNDMS()
	peerJSON := `{"wireguard":{"peer":[{"public-key":"k","rxbytes":10,"txbytes":20,"last-handshake":0,"online":true,"enabled":true}]}}`
	for _, id := range []string{"Wireguard0", "Wireguard1", "Wireguard10"} {
		f.SetDetail(id, json.RawMessage(peerJSON))
	}
	peers, ifs := newPeers(f, time.Millisecond)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0"}, {ID: "Wireguard1"}, {ID: "Wireguard10", IsServer: true}})
	p := NewWithInterval(peers, &fakeMetricsPublisher{}, run, &fakeSubs{count: 1}, NopLogger(), time.Second)

	p.tick() // bootstrap карты
	time.Sleep(2 * time.Millisecond)
	lists := f.ListCalls()
	ifs.Invalidate("Wireguard0") // наша запись: следующий снимок — свежий список
	p.tick()

	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("тик на 3 ref: списков %d, want 1", got)
	}
	if f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("E=%d Posts=%v, want 0/none", f.E, f.Posts)
	}
}

// F469: неизменный сервер даёт ОДНУ подсказку — дальше тишина, хотя
// «секунды назад» у рукопожатия растут.
func TestMetricsPoller_UnchangedServerHintsOnce(t *testing.T) {
	f := newNDMS()
	f.SetDetail("Wireguard10", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"k","rxbytes":1,"txbytes":2,"last-handshake":30,"online":true,"enabled":true}]}}`))
	peers, _ := newPeers(f, 1*time.Millisecond)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard10", IsServer: true}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}
	snap := &fakeSnapshotPub{}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 10*time.Millisecond)
	p.SetServerSnapshotPublisher(snap)
	p.Start()
	defer p.Stop()

	time.Sleep(80 * time.Millisecond)
	if got := snap.Calls(); got != 1 {
		t.Errorf("неизменный сервер: подсказок %d, ждали 1", got)
	}
}

// R36: зрителей нет — тик берёт снимок списка до SnapshotBackground (общий
// с другими фоновыми поллерами); со зрителем — не старше SnapshotRecent.
// Память старше SnapshotRecent (ожидание 2,1 с): без зрителя 0 списков,
// со зрителем 1.
func TestMetricsPoller_IdleTickAcceptsBackgroundSnapshot(t *testing.T) {
	type rig struct {
		f     *query.FakeNDMS
		p     *Poller
		lists int
	}
	mk := func(clients int) *rig {
		f := newNDMS()
		f.SetDetail("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"public-key":"K","rxbytes":1}]}}`))
		peers, ifs := newPeers(f, time.Millisecond)
		if _, err := ifs.List(context.Background()); err != nil {
			t.Fatal(err)
		}
		run := &fakeRunningProvider{refs: []InterfaceRef{{ID: "Wireguard0"}}}
		p := NewWithInterval(peers, &fakeMetricsPublisher{}, run, &fakeSubs{count: clients}, NopLogger(), time.Second)
		return &rig{f: f, p: p, lists: f.ListCalls()}
	}
	idle, viewer := mk(0), mk(1)
	time.Sleep(query.SnapshotRecent + 100*time.Millisecond)
	idle.p.tick()
	viewer.p.tick()
	if n := idle.f.ListCalls() - idle.lists; n != 0 {
		t.Fatalf("без зрителя: списков %d, want 0 (снимок до SnapshotBackground)", n)
	}
	if n := viewer.f.ListCalls() - viewer.lists; n != 1 {
		t.Fatalf("со зрителем: списков %d, want 1 (SnapshotRecent)", n)
	}
}
