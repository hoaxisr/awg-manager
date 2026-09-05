package events

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

const ifaceListPath = "/show/interface/"

const sampleList = `{"Wireguard0": {"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"}}`

func primedQueries(_ *testing.T) (*query.Queries, *query.FakeGetter) {
	fg := query.NewFakeGetter()
	fg.SetJSON(ifaceListPath, sampleList)
	// Per-interface fetches go through POST — fixture body must include
	// the {"show":{"interface":…}} envelope NDMS returns over the wire.
	fg.SetPostInterface("Wireguard0", `{"show":{"interface":{"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"}}}`)
	fg.SetPostInterface("Wireguard1", `{"show":{"interface":{"id":"Wireguard1","interface-name":"nwg1","type":"Wireguard","state":"up"}}}`)
	fg.SetJSON("/show/ip/route", `[]`)
	fg.SetRaw("/show/running-config", []byte(`{"message":["!"]}`))
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return q, fg
}

// === Event-sourced InterfaceStore behaviour ===

// IfCreated must apply via OnCreated which fetches ONLY the new id —
// it must NOT re-fetch the full list.
func TestDispatcher_IfCreated_FetchesOnlyNewID(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	primeList := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})

	waitFor(t, 200*time.Millisecond, func() bool {
		return fg.PostInterfaceCalls("Wireguard1") > 0
	})

	if got := fg.PostInterfaceCalls("Wireguard1"); got != 1 {
		t.Errorf("after IfCreated: want 1 fetch of new id, got %d", got)
	}
	// Critical: list endpoint must NOT have been re-fetched.
	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list must NOT be re-fetched after IfCreated, before=%d after=%d", primeList, got)
	}
	// And the new entry must now be visible from Get without further HTTP.
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got == nil {
		t.Errorf("Wireguard1 must be queryable after OnCreated")
	}
}

// IfDestroyed must be a pure in-memory delete — no HTTP, no list refetch.
func TestDispatcher_IfDestroyed_NoHTTP(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.Interfaces.List(context.Background())
	primeList := fg.Calls(ifaceListPath)
	primeItem := fg.PostInterfaceCalls("Wireguard0")

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard0"})

	// Wait for the entry to disappear from cache (via OnDestroyed).
	waitFor(t, 200*time.Millisecond, func() bool {
		got, _ := q.Interfaces.Get(context.Background(), "Wireguard0")
		return got == nil
	})

	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard0"); got != nil {
		t.Errorf("Wireguard0 must be removed from cache, got %#v", got)
	}
	// No HTTP for the destroy path on InterfaceStore.
	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list must NOT be re-fetched on IfDestroyed, before=%d after=%d", primeList, got)
	}
	if got := fg.PostInterfaceCalls("Wireguard0"); got != primeItem {
		t.Errorf("item must NOT be re-fetched on IfDestroyed, before=%d after=%d", primeItem, got)
	}
}

// IfLayerChanged must patch in place — no HTTP for the InterfaceStore.
func TestDispatcher_IfLayerChanged_NoHTTPOnInterfaces(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.Interfaces.List(context.Background())
	primeList := fg.Calls(ifaceListPath)
	primeItem := fg.PostInterfaceCalls("Wireguard0")

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "disabled"})

	waitFor(t, 200*time.Millisecond, func() bool {
		d, _ := q.Interfaces.GetDetails(context.Background(), "Wireguard0")
		return d != nil && d.ConfLayer == "disabled"
	})

	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list re-fetched on IfLayerChanged, before=%d after=%d", primeList, got)
	}
	if got := fg.PostInterfaceCalls("Wireguard0"); got != primeItem {
		t.Errorf("item re-fetched on IfLayerChanged, before=%d after=%d", primeItem, got)
	}
}

// === Legacy InvalidateAll path for non-Interface stores ===

func TestDispatcher_IfDestroyed_InvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())
	primed := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})
	waitFor(t, 200*time.Millisecond, func() bool {
		_, _ = q.WGServers.List(context.Background())
		return fg.Calls(ifaceListPath) > primed
	})

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("WGServer list not re-fetched after IfDestroyed")
	}
}

func TestDispatcher_IfCreated_InvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())
	primed := fg.Calls(ifaceListPath)

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard5"})
	waitFor(t, 200*time.Millisecond, func() bool {
		_, _ = q.WGServers.List(context.Background())
		return fg.Calls(ifaceListPath) > primed
	})

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("WGServer list not re-fetched after IfCreated")
	}
}

func TestDispatcher_IfLayerChangedConf_InvalidatesRunningConfig(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	defer d.Stop()

	_, _ = q.RunningConfig.Lines(context.Background())
	primed := fg.Calls("/show/running-config")

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "running"})
	waitFor(t, 200*time.Millisecond, func() bool {
		_, _ = q.RunningConfig.Lines(context.Background())
		return fg.Calls("/show/running-config") > primed
	})

	if fg.Calls("/show/running-config") <= primed {
		t.Errorf("running-config not re-fetched after conf layer hook")
	}
}

// === Worker lifecycle ===

func TestDispatcher_Stop_Idempotent(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	d.Start()
	d.Stop()
	d.Stop()
}

func TestDispatcher_Stop_WithoutStart_ReturnsImmediately(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	done := make(chan struct{})
	go func() { d.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Stop without Start should return immediately")
	}
}

// === Порядок пакета, слушатель маршрутизации, соседние кэши ===

const peersPath = ifaceListPath + "Wireguard0"

const samplePeers = `{"wireguard":{"peer":[{"public-key":"KEY","online":true}]}}`

// Пакет применяется В ПОРЯДКЕ ПРИХОДА — то самое, что обещает докстрока
// Dispatcher («ifcreated → conf=running → link=running»). Все прежние тесты
// слали ОДНО событие, поэтому итерация пакета задом наперёд проходила
// зелёной. Здесь пара «создан → снесён» приходит одним пакетом: события
// кладутся в очередь ДО Start, поэтому воркер разгребает их одним проходом.
// В обратном порядке снос применился бы к ещё отсутствующей записи, и
// интерфейс остался бы в кэше живым.
func TestDispatcher_BatchAppliesInArrivalOrder(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})

	d.Start()
	defer d.Stop()
	waitDrain(t, drained)

	// Создание действительно применилось: за новым id сходили в NDMS. Без
	// этой проверки тест был бы зелёным и на «пакет вовсе не разобран».
	if got := fg.PostInterfaceCalls("Wireguard1"); got != 1 {
		t.Fatalf("создание не применилось: запросов за Wireguard1 %d, ждали 1", got)
	}
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got != nil {
		t.Errorf("после пары «создан → снесён» записи быть не должно, получили %#v", got)
	}
}

// RoutingChangedListener — единственный способ, которым SSE-снимок
// «Маршрутизации» узнаёт о хуке; ни один тест его не проверял, снос вызова
// (dispatcher.go:146-148) проходил зелёным. Слушатель взводится ПОСЛЕ разбора
// пакета, поэтому к моменту вызова состояние уже применено — это и проверяем.
func TestDispatcher_RoutingListenerFiresAfterDrain(t *testing.T) {
	q, _ := primedQueries(t)
	d := NewDispatcher(q, NopLogger())

	seen := make(chan bool, 4)
	d.SetRoutingChanged(func() {
		got, _ := q.Interfaces.Get(context.Background(), "Wireguard1")
		seen <- got != nil
	})
	d.Start()
	defer d.Stop()

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})

	select {
	case applied := <-seen:
		if !applied {
			t.Errorf("слушатель вызван до применения события: Wireguard1 ещё не в кэше")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("слушатель маршрутизации не вызван после прохода с событием")
	}
}

// Обе ветки EventIfIPChanged удалялись целиком зелёными. Смена адреса — это
// сразу два протухших кэша: адрес в кэше интерфейсов и таблица маршрутов.
func TestDispatcher_IfIPChanged_PatchesAddressAndDropsRoutes(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)
	d.Start()
	defer d.Stop()

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime interfaces: %v", err)
	}
	if _, err := q.Routes.List(context.Background()); err != nil {
		t.Fatalf("prime routes: %v", err)
	}
	primedRoutes := fg.Calls("/show/ip/route")

	d.Enqueue(Event{Type: EventIfIPChanged, ID: "Wireguard0", Address: "10.77.0.5"})
	waitDrain(t, drained)

	got, err := q.Interfaces.Get(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("get interface: %v", err)
	}
	if got == nil || got.Address != "10.77.0.5" {
		t.Errorf("адрес в кэше интерфейсов не обновлён: %#v", got)
	}
	if _, err := q.Routes.List(context.Background()); err != nil {
		t.Fatalf("routes after event: %v", err)
	}
	if after := fg.Calls("/show/ip/route"); after <= primedRoutes {
		t.Errorf("кэш маршрутов не сброшен: запросов было %d, стало %d", primedRoutes, after)
	}
}

// Peers.Invalidate в ветках destroy и layer-change удалялся зелёным. Пиры
// живут в отдельном кэше с TTL 8 с: без сброса выдача переживает и снос
// интерфейса, и смену уровня.
func TestDispatcher_InvalidatesPeersOnDestroyAndLayerChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   Event
	}{
		{"снос интерфейса", Event{Type: EventIfDestroyed, ID: "Wireguard0"}},
		{"смена уровня", Event{Type: EventIfLayerChanged, ID: "Wireguard0",
			Layer: "link", Level: "running"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, fg := primedQueries(t)
			fg.SetJSON(peersPath, samplePeers)
			d := NewDispatcher(q, NopLogger())
			drained := drainBarrier(d)
			d.Start()
			defer d.Stop()

			if _, err := q.Peers.GetPeers(context.Background(), "Wireguard0"); err != nil {
				t.Fatalf("prime peers: %v", err)
			}
			primed := fg.Calls(peersPath)

			d.Enqueue(tc.ev)
			waitDrain(t, drained)

			if _, err := q.Peers.GetPeers(context.Background(), "Wireguard0"); err != nil {
				t.Fatalf("peers after event: %v", err)
			}
			if after := fg.Calls(peersPath); after <= primed {
				t.Errorf("кэш пиров не сброшен: запросов было %d, стало %d", primed, after)
			}
		})
	}
}

// === Helpers ===

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// drainBarrier вешает слушателя маршрутизации как барьер конца прохода: он
// взводится ПОСЛЕ применения всего пакета, значит по нему можно ждать
// детерминированно, не опрашивая состояние в цикле.
func drainBarrier(d *Dispatcher) <-chan struct{} {
	ch := make(chan struct{}, 8)
	d.SetRoutingChanged(func() { ch <- struct{}{} })
	return ch
}

func waitDrain(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("проход диспетчера не завершился за 2 с")
	}
}
