package dnsroute

// Регресс #1023: смена туннеля в карточке списка не должна уводить строки
// `dns-proxy route` группы в хвост. Порядок строк — приоритет и между
// группами: первая совпавшая выигрывает, строка в хвосте проигрывает
// пересечения всем группам выше. Модель роутера — fakeRouter
// (route_order_test.go).

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// groupOrder — порядок групп в таблице, подряд идущие строки одной группы
// схлопнуты.
func (r *fakeRouter) groupOrder() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rt := range r.routes {
		if len(out) == 0 || out[len(out)-1] != rt.group {
			out = append(out, rt.group)
		}
	}
	return out
}

func (r *fakeRouter) dnsPostCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dnsPosts
}

func (r *fakeRouter) indexedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.indexed
}

// insertForeign ставит чужую (не нашу) строку на позицию at, мимо POST.
func (r *fakeRouter) insertForeign(at int, rt fakeRoute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes[:at], append([]fakeRoute{rt}, r.routes[at:]...)...)
}

// assertForeignIntact: во всех снимках, начиная с from, чужая строка на месте
// с тем же index — её не сносили и не правили.
func (r *fakeRouter) assertForeignIntact(t *testing.T, from int, want fakeRoute) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := from; i < len(r.snapshots); i++ {
		found := false
		for _, rt := range r.snapshots[i] {
			if rt == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("после POST #%d чужой строки %+v нет в таблице", i, want)
			return
		}
	}
}

func mustCreate(t *testing.T, svc *ServiceImpl, name string, ifaces ...string) *DomainList {
	t.Helper()
	l, err := svc.Create(context.Background(), DomainList{
		Name:          name,
		ManualDomains: []string{name + ".example"},
		Routes:        routes(ifaces...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func mustUpdateRoutes(t *testing.T, svc *ServiceImpl, l *DomainList, ifaces ...string) {
	t.Helper()
	upd := *l
	upd.Routes = routes(ifaces...)
	if _, err := svc.Update(context.Background(), upd); err != nil {
		t.Fatal(err)
	}
}

func assertSeq(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s = %v, ожидался %v", what, got, want)
	}
}

var manualRoute = fakeRoute{group: "manual", iface: "Wireguard0", index: "idx-manual"}

func TestIssue1023_TunnelSwapKeepsGlobalPosition(t *testing.T) {
	r := newFakeRouter()
	r.routes = []fakeRoute{manualRoute}
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1")
	mustCreate(t, svc, "second", "Wireguard2")

	mustUpdateRoutes(t, svc, first, "Wireguard3")

	assertSeq(t, "порядок групп", r.groupOrder(), "manual", "first_p1", "second_p1")
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3")
	r.assertForeignIntact(t, 0, manualRoute)
	r.assertNoWindow(t, "first_p")
}

func TestIssue1023_ReorderKeepsGlobalPosition(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1", "Wireguard2")
	mustCreate(t, svc, "second", "Wireguard3")

	mustUpdateRoutes(t, svc, first, "Wireguard2", "Wireguard1")

	assertSeq(t, "порядок групп", r.groupOrder(), "first_p1", "second_p1")
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard2", "Wireguard1")
	r.assertNoWindow(t, "first_p")
}

func TestIssue1023_ForeignLineInsideGroup(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1", "Wireguard2")
	from := waitQuiet(t, r)
	r.insertForeign(1, manualRoute) // [first/W1, manual/W0, first/W2]

	mustUpdateRoutes(t, svc, first, "Wireguard3", "Wireguard2")

	assertSeq(t, "порядок групп", r.groupOrder(), "first_p1", "manual")
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3", "Wireguard2")
	r.assertForeignIntact(t, from, manualRoute)
}

func TestIssue1023_PausedSwap(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	mustCreate(t, svc, "first", "Wireguard1")
	mustCreate(t, svc, "second", "Wireguard2")

	// Пауза и смена туннеля приходят в одну сверку.
	data := svc.store.GetCached()
	data.Lists[0].Enabled = false
	data.Lists[0].Routes = routes("Wireguard3")
	if err := svc.store.Save(data); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertSeq(t, "порядок групп", r.groupOrder(), "first_p1", "second_p1")
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rt := range r.routes {
		if rt.group == "first_p1" && (rt.iface != "Wireguard3" || !rt.disabled) {
			t.Errorf("строка first: %+v, ожидалась Wireguard3 с disabled=true", rt)
		}
	}
}

// 5.00 правку по index и rule не умеет (гипотеза, см. legacyOnly): правка
// списка обязана пройти откатом на снос+запись, позиция — хвост, как до #1023.
func TestIssue1023_LegacyFallback(t *testing.T) {
	r := newFakeRouter()
	r.legacyOnly = true
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1")
	mustCreate(t, svc, "second", "Wireguard2")

	mustUpdateRoutes(t, svc, first, "Wireguard3")

	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3")
	assertSeq(t, "порядок групп", r.groupOrder(), "second_p1", "first_p1")
}

// Правка на месте прошла, перестановка нет: откат обязан дописать группу
// ровно в цель, а следующая сверка — ничего не трогать.
func TestIssue1023_PartialFallback(t *testing.T) {
	r := newFakeRouter()
	r.failRule = true
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1", "Wireguard2")

	mustUpdateRoutes(t, svc, first, "Wireguard2", "Wireguard3")

	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard2", "Wireguard3")
	before := waitQuiet(t, r)
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.snapshotCount(); got != before {
		t.Errorf("сверка после отката сделала %d POST-ов", got-before)
	}
}

// [A]→[B]: правка на месте уже ставит строку куда надо, второй POST (rule)
// не нужен.
func TestIssue1023_SwapSinglePost(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1")
	waitQuiet(t, r)
	before := r.dnsPostCount()

	mustUpdateRoutes(t, svc, first, "Wireguard2")

	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard2")
	if got := r.dnsPostCount() - before; got != 1 {
		t.Errorf("POST-ов dns-proxy = %d, ожидался 1", got)
	}
}

// sc не видит правок по index до save: после правки на месте сверка обязана
// сохранить до чтения, иначе по устаревшему sc перепишет блок заново.
func TestIssue1023_NoRebuildAfterInPlace(t *testing.T) {
	r := newFakeRouter()
	lag := &scLagRouter{oracleRouter: newOracle4(r)}
	// Отложенный save сам не срабатывает: сохранить до чтения может только
	// сверка.
	svc := newServiceOver(t, lag, time.Hour, time.Hour)
	first := mustCreate(t, svc, "first", "Wireguard1")

	mustUpdateRoutes(t, svc, first, "Wireguard3")
	if r.indexedCount() == 0 {
		t.Fatal("правка прошла не на месте — тест холостой")
	}

	waitQuiet(t, r)
	before := r.dnsPostCount()
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.dnsPostCount() - before; got != 0 {
		t.Errorf("сверка после правки на месте сделала %d POST-ов dns-proxy", got)
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3")
}

// F691: в списке маршрутов один интерфейс дважды. Планировщик на месте
// запланировал бы дубль строки на роутере, поэтому группа идёт прежним
// сносом+записью.
func TestIssue1023_DuplicateIfaceTakesLegacyPath(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1")
	before := r.indexedCount()

	mustUpdateRoutes(t, svc, first, "Wireguard2", "Wireguard2")

	if got := r.indexedCount(); got != before {
		t.Errorf("правка по index/rule при дубле интерфейса: %d элементов", got-before)
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard2")
}
