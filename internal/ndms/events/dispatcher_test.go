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
	fg.SetJSON("/show/ip/route", `[]`)
	fg.SetRaw("/show/running-config", []byte(`{"message":["!"]}`))
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return q, fg
}

// === Event-sourced InterfaceStore behaviour ===

// IfCreated неизвестного id не читает его точечно (по снятому к этому
// моменту имени NDMS пишет E, F546) — пачка кончается ОДНИМ полным списком.
func TestDispatcher_IfCreated_OneListNoPointRead(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	d.Start()
	defer d.Stop()

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	primeList := fg.Calls(ifaceListPath)
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"},
		"Wireguard1": {"id":"Wireguard1","interface-name":"nwg1","type":"Wireguard","state":"up"}}`)

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})
	waitDrain(t, done)

	if got := fg.Calls(ifaceListPath); got != primeList+1 {
		t.Errorf("want exactly one list after IfCreated, before=%d after=%d", primeList, got)
	}
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard1"); got == nil || got.SystemName != "nwg1" {
		t.Errorf("Wireguard1 must come from the list, got %#v", got)
	}
}

// IfDestroyed известного id карту не меняет — ставит «грязно» (П6′): пачка
// кончается ОДНИМ списком, запись уходит по нему; точечных чтений нет.
func TestDispatcher_IfDestroyed_KnownID_OneListThenAbsent(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	done := drainBarrier(d)
	d.Start()
	defer d.Stop()

	_, _ = q.Interfaces.List(context.Background())
	primeList := fg.Calls(ifaceListPath)
	fg.SetJSON(ifaceListPath, `{}`)

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard0"})
	waitDrain(t, done)

	if got := fg.Calls(ifaceListPath); got != primeList+1 {
		t.Errorf("want exactly one list after IfDestroyed, before=%d after=%d", primeList, got)
	}
	if got, _ := q.Interfaces.Get(context.Background(), "Wireguard0"); got != nil {
		t.Errorf("Wireguard0 must be gone after the list, got %#v", got)
	}
	if got := fg.Calls("/show/interface/Wireguard0"); got != 0 {
		t.Errorf("IfDestroyed must not probe, got %d calls", got)
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

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "conf", Level: "disabled"})

	waitFor(t, 200*time.Millisecond, func() bool {
		d, _ := q.Interfaces.GetDetails(context.Background(), "Wireguard0")
		return d != nil && d.ConfLayer == "disabled"
	})

	if got := fg.Calls(ifaceListPath); got != primeList {
		t.Errorf("list re-fetched on IfLayerChanged, before=%d after=%d", primeList, got)
	}
}

// === Legacy InvalidateAll path for non-Interface stores ===

func TestDispatcher_IfDestroyed_InvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})
	waitDrain(t, drained)
	// Отсчёт после прохода: сам диспетчер тоже может читать список
	// (ifcreated неизвестного id), а проверяется сброс кэша серверов. Метка
	// «грязно» делает пересборку видимой: снимок моложе SnapshotRecent
	// иначе отдаётся из памяти без чтения списка.
	q.Interfaces.Invalidate("Wireguard0")
	primed := fg.Calls(ifaceListPath)
	_, _ = q.WGServers.List(context.Background())

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("WGServer list not re-fetched after IfDestroyed")
	}
}

func TestDispatcher_IfCreated_InvalidatesWGServers(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.List(context.Background())

	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard5"})
	waitDrain(t, drained)
	// Отсчёт после прохода: сам диспетчер тоже может читать список
	// (ifcreated неизвестного id), а проверяется сброс кэша серверов. Метка
	// «грязно» делает пересборку видимой: снимок моложе SnapshotRecent
	// иначе отдаётся из памяти без чтения списка.
	q.Interfaces.Invalidate("Wireguard0")
	primed := fg.Calls(ifaceListPath)
	_, _ = q.WGServers.List(context.Background())

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

const samplePeers = `{"wireguard":{"peer":[{"public-key":"KEY","online":true}]}}`

// Пара «создан → снесён» незнакомого id одним пакетом (события кладутся в
// очередь ДО Start — один проход): ifcreated расходится с картой — пачка стоит
// ОДИН список (дизайн §5), записи по нему нет. Порядок пачки здесь не
// наблюдаем: хуки существования карту не меняют, обратный порядок даёт то же.
func TestDispatcher_BatchUnknownCreatedDestroyed_OneList(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)

	if _, err := q.Interfaces.List(context.Background()); err != nil {
		t.Fatalf("prime: %v", err)
	}
	primeList := fg.Calls(ifaceListPath)
	d.Enqueue(Event{Type: EventIfCreated, ID: "Wireguard1"})
	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard1"})

	d.Start()
	defer d.Stop()
	// Барьер взводится только после непустого прохода — «пакет вовсе не
	// разобран» сюда не доходит.
	waitDrain(t, drained)

	if got := fg.Calls(ifaceListPath); got != primeList+1 {
		t.Fatalf("пачка created→destroyed: списков +%d, want 1", got-primeList)
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
	q, fg := primedQueries(t)
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
	// Wireguard1 появляется в NDMS; в кэш его кладёт список после пачки —
	// слушатель обязан сработать уже после него.
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"},
		"Wireguard1": {"id":"Wireguard1","interface-name":"nwg1","type":"Wireguard","state":"up"}}`)
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
			// Пиры — в записи полного списка (F546).
			fg.SetJSON(ifaceListPath, `{"Wireguard0":{"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up",`+samplePeers[1:]+`}`)
			d := NewDispatcher(q, NopLogger())
			drained := drainBarrier(d)
			d.Start()
			defer d.Stop()

			if _, err := q.Peers.GetPeers(context.Background(), "Wireguard0"); err != nil {
				t.Fatalf("prime peers: %v", err)
			}
			d.Enqueue(tc.ev)
			waitDrain(t, drained)
			if tc.ev.Type == EventIfDestroyed {
				fg.SetJSON(ifaceListPath, `{}`) // снятого нет и в NDMS
			}
			// Пиры читаются из снимка списка (F546): метим его грязным — тогда
			// промах кэша пиров виден как один список, попадание — как ноль.
			q.Interfaces.Invalidate("Wireguard0")
			lists := fg.Calls(ifaceListPath)

			peers, err := q.Peers.GetPeers(context.Background(), "Wireguard0")
			if err != nil {
				t.Fatalf("peers after event: %v", err)
			}
			if tc.ev.Type == EventIfDestroyed {
				// Снятого интерфейса нет в кэше — пиры пусты и без запроса
				// (F546); прежние пиры значили бы несброшенный кэш.
				if len(peers) != 0 {
					t.Errorf("кэш пиров не сброшен: после сноса %d пиров", len(peers))
				}
				return
			}
			if after := fg.Calls(ifaceListPath); after <= lists {
				t.Errorf("кэш пиров не сброшен: списков было %d, стало %d", lists, after)
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

// Смена уровня интерфейса меняет его Status, а по нему отбирается состав для
// поллера метрик. Без сброса поднявшийся или упавший системный туннель не
// попадал бы в опрос до истечения TTL (F364).
func TestDispatcher_IfLayerChanged_InvalidatesSystemTunnelList(t *testing.T) {
	q, fg := primedQueries(t)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)
	d.Start()
	defer d.Stop()

	_, _ = q.WGServers.ListSystemTunnels(context.Background())

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard1", Layer: "link", Level: "running"})
	waitDrain(t, drained)
	// Отсчёт после прохода: сам диспетчер тоже может читать список
	// (ifcreated неизвестного id), а проверяется сброс кэша серверов. Метка
	// «грязно» делает пересборку видимой (см. выше).
	q.Interfaces.Invalidate("Wireguard0")
	primed := fg.Calls(ifaceListPath)
	_, _ = q.WGServers.ListSystemTunnels(context.Background())

	if fg.Calls(ifaceListPath) <= primed {
		t.Errorf("состав системных туннелей не перечитан после iflayerchanged")
	}
}

// Хук слоя конфигурацию не меняет: дерево rc (~90 тиков ndm) на нём не
// перечитывается; создание/снятие интерфейса — перечитывается (F546, Task 44).
func TestDispatcher_LayerHookKeepsRC(t *testing.T) {
	const rcTree = "/show/rc/interface/"
	q, fg := primedQueries(t)
	fg.SetRC("Wireguard0", `{"wireguard":{"peer":[]}}`)
	d := NewDispatcher(q, NopLogger())
	drained := drainBarrier(d)
	d.Start()
	defer d.Stop()

	ctx := context.Background()
	if _, err := q.WGServers.List(ctx); err != nil {
		t.Fatal(err)
	}
	primed := fg.Calls(rcTree)
	if primed != 1 {
		t.Fatalf("чтений дерева rc при первом List = %d, want 1", primed)
	}

	d.Enqueue(Event{Type: EventIfLayerChanged, ID: "Wireguard0", Layer: "link", Level: "running"})
	waitDrain(t, drained)
	if _, err := q.WGServers.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fg.Calls(rcTree); got != primed {
		t.Fatalf("iflayerchanged перечитал дерево rc: %d → %d", primed, got)
	}

	d.Enqueue(Event{Type: EventIfDestroyed, ID: "Wireguard5"})
	waitDrain(t, drained)
	if _, err := q.WGServers.List(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fg.Calls(rcTree); got != primed+1 {
		t.Fatalf("ifdestroyed: чтений дерева rc %d, want %d", got, primed+1)
	}
}
