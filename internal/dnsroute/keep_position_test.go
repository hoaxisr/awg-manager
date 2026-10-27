package dnsroute

// Регресс #1023: смена туннеля в карточке списка не должна уводить строки
// `dns-proxy route` группы в хвост. Порядок строк — приоритет и между
// группами: первая совпавшая выигрывает, строка в хвосте проигрывает
// пересечения всем группам выше. Модель роутера — fakeRouter
// (route_order_test.go).

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

func (r *fakeRouter) rejectedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rejected
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

	if r.rejectedCount() == 0 {
		t.Fatal("попытки правки на месте не было — откат не проверен")
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3")
	assertSeq(t, "порядок групп", r.groupOrder(), "second_p1", "first_p1")
}

// rc в форме-объекте: порядок невосстановим, FetchOrdered — ошибка, группа
// идёт откатом без единого элемента по index; состав — цель. Без отказа
// свёрнутая выдача (first = [W2] вместо [W1,W2]) дала бы правку W2→W1 —
// дубль уже стоящей строки W1.
func TestIssue1023_RCObjectFormFallsBack(t *testing.T) {
	r := newFakeRouter()
	r.rcObjectForm = true
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1", "Wireguard2")
	mustCreate(t, svc, "second", "Wireguard3")

	mustUpdateRoutes(t, svc, first, "Wireguard1", "Wireguard0")

	if got := r.indexedCount(); got != 0 {
		t.Errorf("элементов по index/rule: %d, ожидался откат", got)
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard1", "Wireguard0")
	assertSeq(t, "порядок групп", r.groupOrder(), "second_p1", "first_p1")
}

// Строка группы без index: правка на месте не пробуется (`"index":""` не
// уходит), откат дописывает цель.
func TestIssue1023_EmptyIndexFallsBack(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	first := mustCreate(t, svc, "first", "Wireguard1")
	r.mu.Lock()
	r.routes[0].index = ""
	r.mu.Unlock()

	mustUpdateRoutes(t, svc, first, "Wireguard3")

	if got := r.indexedCount(); got != 0 {
		t.Errorf("элементов по index/rule: %d, ожидался откат", got)
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3")
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

	if r.indexedCount() == 0 {
		t.Fatal("правка прошла не на месте — тест холостой")
	}
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

// F691: список с повтором интерфейса, записанный до проверки на записи.
// NDMS держит одну строку на (группа, интерфейс), поэтому цель схлопывается
// до первого вхождения и сверка сходится.
func TestF691_StoredDuplicateCollapses(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	mustCreate(t, svc, "first", "Wireguard1")

	data := svc.store.GetCached()
	data.Lists[0].Routes = routes("Wireguard2", "Wireguard2")
	if err := svc.store.Save(data); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard2")

	waitQuiet(t, r)
	before := r.dnsPostCount()
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.dnsPostCount() - before; got != 0 {
		t.Errorf("повторная сверка сделала %d POST-ов dns-proxy", got)
	}
}

// F691: повтор интерфейса в маршрутах отвергается на записи — без сохранения
// и без POST-ов на роутер.
func TestF691_RepeatedInterfaceRejectedOnWrite(t *testing.T) {
	ctx := context.Background()
	assertRejected := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "Wireguard1") {
			t.Fatalf("ошибка = %v, ожидалась с именем интерфейса Wireguard1", err)
		}
	}
	dup := DomainList{
		Name:          "dup",
		ManualDomains: []string{"dup.example"},
		Routes:        routes("Wireguard1", "Wireguard1"),
	}

	t.Run("Create", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		_, err := svc.Create(ctx, dup)
		assertRejected(t, err)
		if n := len(svc.store.GetCached().Lists); n != 0 {
			t.Errorf("сохранено списков: %d", n)
		}
		if n := r.snapshotCount(); n != 0 {
			t.Errorf("POST-ов: %d", n)
		}
	})

	t.Run("CreateBatch", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		_, err := svc.CreateBatch(ctx, []DomainList{dup})
		assertRejected(t, err)
		if n := len(svc.store.GetCached().Lists); n != 0 {
			t.Errorf("сохранено списков: %d", n)
		}
		if n := r.snapshotCount(); n != 0 {
			t.Errorf("POST-ов: %d", n)
		}
	})

	t.Run("Update", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		l := mustCreate(t, svc, "first", "Wireguard2")
		before := waitQuiet(t, r)
		upd := *l
		upd.Routes = routes("Wireguard1", "Wireguard1")
		_, err := svc.Update(ctx, upd)
		assertRejected(t, err)
		if got := svc.store.GetCached().Lists[0].Routes; fmt.Sprint(got) != fmt.Sprint(routes("Wireguard2")) {
			t.Errorf("сохранены маршруты %v", got)
		}
		if n := r.snapshotCount() - before; n != 0 {
			t.Errorf("POST-ов: %d", n)
		}
	})
}

// F693: ошибка на позднем списке батча не оставляет ранние в кэше — иначе
// следующая несвязанная запись сохранила бы их, а сверка довезла до роутера.
func TestCreateBatch_ErrorLeavesCacheUntouched(t *testing.T) {
	ctx := context.Background()
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)

	_, err := svc.CreateBatch(ctx, []DomainList{
		{Name: "batch-a", ManualDomains: []string{"a.example"}, Routes: routes("Wireguard2")},
		{Name: "batch-b", ManualDomains: []string{"b.example"}, Routes: routes("Wireguard1", "Wireguard1")},
	})
	if err == nil || !strings.Contains(err.Error(), "Wireguard1") {
		t.Fatalf("ошибка = %v, ожидалась с именем интерфейса Wireguard1", err)
	}
	if n := len(svc.store.GetCached().Lists); n != 0 {
		t.Fatalf("в кэше списков: %d", n)
	}
	if n := r.snapshotCount(); n != 0 {
		t.Errorf("POST-ов: %d", n)
	}

	mustCreate(t, svc, "after-c", "Wireguard3")
	waitQuiet(t, r)
	for _, l := range svc.store.GetCached().Lists {
		if l.Name != "after-c" {
			t.Errorf("в кэше чужой список %q", l.Name)
		}
	}
	reloaded := NewStore(filepath.Dir(svc.store.path))
	data, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Lists) != 1 || data.Lists[0].Name != "after-c" {
		t.Errorf("на диске списки %+v, ожидался только after-c", data.Lists)
	}
	for _, g := range r.groupOrder() {
		if strings.HasPrefix(g, "batch") {
			t.Errorf("на роутер доехала группа %s", g)
		}
	}
}

// fillDomains — n уникальных доменов с префиксом.
func fillDomains(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d.example", prefix, i)
	}
	return out
}

func mustUpdateDomains(t *testing.T, svc *ServiceImpl, l *DomainList, domains []string) {
	t.Helper()
	upd := *l
	upd.ManualDomains = domains
	text := strings.Join(domains, "\n")
	upd.ManualText = &text
	if _, err := svc.Update(context.Background(), upd); err != nil {
		t.Fatal(err)
	}
}

// #1107: список вырос, и у него появился новый пакет — пакет встаёт сразу за
// своими, а не в хвост за чужими правилами (первая совпавшая группа выигрывает).
func TestIssue1107_NewPackFollowsItsSiblings(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	big := mustCreate(t, svc, "big", "Wireguard1")
	mustUpdateDomains(t, svc, big, fillDomains("a", MaxDomainsPerGroup))
	mustCreate(t, svc, "other", "Wireguard2")
	assertSeq(t, "до роста", r.groupOrder(), "big_p1", "other_p1")

	mustUpdateDomains(t, svc, big, fillDomains("a", 2*MaxDomainsPerGroup+5))

	assertSeq(t, "после роста", r.groupOrder(), "big_p1", "big_p2", "big_p3", "other_p1")
}

// Новое правило (своих пакетов нет) — по-прежнему в хвост.
func TestIssue1107_NewListStaysAtTail(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	mustCreate(t, svc, "first", "Wireguard1")
	mustCreate(t, svc, "second", "Wireguard2")
	assertSeq(t, "порядок групп", r.groupOrder(), "first_p1", "second_p1")
}

// Перестановка отвергнута (OS4, сбой RCI) — пакет остаётся в хвосте, как до
// правки, обновление списка не падает.
func TestIssue1107_MoveFailureKeepsTailNoError(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	big := mustCreate(t, svc, "big", "Wireguard1")
	mustUpdateDomains(t, svc, big, fillDomains("a", MaxDomainsPerGroup))
	mustCreate(t, svc, "other", "Wireguard2")
	r.mu.Lock()
	r.failRule = true
	r.mu.Unlock()

	mustUpdateDomains(t, svc, big, fillDomains("a", MaxDomainsPerGroup+5))

	assertSeq(t, "после роста", r.groupOrder(), "big_p1", "other_p1", "big_p2")
}

// Пакет с несколькими туннелями встаёт целым блоком в прежнем порядке строк.
func TestIssue1107_MultiTunnelPackMovesAsBlock(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	big := mustCreate(t, svc, "big", "Wireguard1", "Wireguard3")
	mustUpdateDomains(t, svc, big, fillDomains("a", MaxDomainsPerGroup))
	mustCreate(t, svc, "other", "Wireguard2")

	mustUpdateDomains(t, svc, big, fillDomains("a", MaxDomainsPerGroup+5))

	assertSeq(t, "после роста", r.groupOrder(), "big_p1", "big_p2", "other_p1")
	assertSeq(t, "big_p2", r.ifaceOrder("big_p2"), "Wireguard1", "Wireguard3")
}

// Новый список из многих пакетов ложится по номеру (p2 раньше p10) без единой
// перестановки и без чтения порядка строк — перестановка только для пакета
// списка, у которого на роутере уже есть строки (#1107, ревью: цена импорта).
func TestIssue1107_NewBigListNumericOrderNoMoves(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	reads0 := r.routeReads()
	mustCreate(t, svc, "first", "Wireguard2")
	smallReads := r.routeReads() - reads0
	before, reads1 := r.indexedCount(), r.routeReads()
	if _, err := svc.Create(context.Background(), DomainList{
		Name: "big", ManualDomains: fillDomains("a", 11*MaxDomainsPerGroup+1), Routes: routes("Wireguard1"),
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"first_p1"}
	for i := 1; i <= 12; i++ {
		want = append(want, fmt.Sprintf("big_p%d", i))
	}
	assertSeq(t, "порядок групп", r.groupOrder(), want...)
	if got := r.indexedCount() - before; got != 0 {
		t.Errorf("перестановок/правок по index при создании нового списка: %d, ожидалось 0", got)
	}
	if got := r.routeReads() - reads1; got != smallReads {
		t.Errorf("чтений порядка строк при создании списка из 12 пакетов: %d, у списка из одного — %d", got, smallReads)
	}
}

func (r *fakeRouter) routeReads() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rcRouteReads
}

// Рост через десятый пакет: p10 и p11 встают за p9, а не лексически за p1.
func TestIssue1107_GrowthPastNinePacks(t *testing.T) {
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	big := mustCreate(t, svc, "big", "Wireguard1")
	mustUpdateDomains(t, svc, big, fillDomains("a", 9*MaxDomainsPerGroup))
	mustCreate(t, svc, "other", "Wireguard2")

	mustUpdateDomains(t, svc, big, fillDomains("a", 10*MaxDomainsPerGroup+5))

	var want []string
	for i := 1; i <= 11; i++ {
		want = append(want, fmt.Sprintf("big_p%d", i))
	}
	assertSeq(t, "порядок групп", r.groupOrder(), append(want, "other_p1")...)
}
