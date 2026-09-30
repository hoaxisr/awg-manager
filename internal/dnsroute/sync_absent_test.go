package dnsroute

// F548: dns-proxy route на туннель, у которого нет интерфейса в NDMS. Цель
// обязана уйти в fallback, как у упавшего туннеля (решение 3), а не
// перезаливаться на отсутствующий интерфейс при каждом применении правил.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// oracleRouter — модель dns-proxy/object-group (fakeRouter) плюс оракул
// интерфейсов FakeNDMS: список и E по ссылкам на отсутствующий интерфейс
// считает оракул, состав таблицы маршрутов — fakeRouter.
type oracleRouter struct {
	*fakeRouter
	f *query.FakeNDMS
}

func isIfacePath(path string) bool {
	return strings.HasPrefix(path, "/show/interface/") || strings.HasPrefix(path, "/show/rc/interface/")
}

func (o *oracleRouter) Get(ctx context.Context, path string, dst any) error {
	if isIfacePath(path) {
		return o.f.Get(ctx, path, dst)
	}
	return o.fakeRouter.Get(ctx, path, dst)
}

func (o *oracleRouter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if isIfacePath(path) {
		return o.f.GetRaw(ctx, path)
	}
	return o.fakeRouter.GetRaw(ctx, path)
}

func (o *oracleRouter) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	resp, err := o.f.Post(ctx, payload)
	if err != nil {
		return nil, err
	}
	if _, err := o.fakeRouter.Post(ctx, payload); err != nil {
		return nil, err
	}
	return resp, nil
}

func newDNSRouteServiceWithOracle(t *testing.T, f *query.FakeNDMS, data StoreData) (*ServiceImpl, *oracleRouter) {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&data); err != nil {
		t.Fatal(err)
	}
	o := &oracleRouter{fakeRouter: newFakeRouter(), f: f}
	q := query.NewQueries(query.Deps{Getter: o, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(o, nopPublisher{}, 10*time.Millisecond, time.Second, 0, nil)
	c := command.NewCommands(command.Deps{Poster: o, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	// resolver не нужен: Reconcile берёт интерфейсы из сохранённых RouteTarget.
	return &ServiceImpl{store: store, queries: q, commands: c}, o
}

// listWithRoutes — один NDMS-список с доменом и маршрутами по порядку на
// указанные интерфейсы (TunnelID = имя интерфейса).
func listWithRoutes(ifaces ...string) StoreData {
	return StoreData{Lists: []DomainList{{
		ID: "list_1", Name: "absent", Enabled: true,
		Domains: []string{"example.com"},
		Routes:  routes(ifaces...),
	}}}
}

// upsertsTo — POST-ы, в которых есть постановка (не снос) строки dns-proxy
// route на iface. Разбор поэлементный: снос и постановка едут в одном
// payload (ReplaceRoutes), и поиск подстроки `"no":true` по всему POST
// спрятал бы постановку рядом со сносом.
func upsertsTo(f *query.FakeNDMS, iface string) []string {
	var out []string
	for _, p := range f.Posts {
		var m struct {
			DNSProxy struct {
				Route json.RawMessage `json:"route"`
			} `json:"dns-proxy"`
		}
		if json.Unmarshal([]byte(p), &m) != nil || len(m.DNSProxy.Route) == 0 {
			continue
		}
		var entries []struct {
			Interface string `json:"interface"`
			No        bool   `json:"no"`
		}
		if json.Unmarshal(m.DNSProxy.Route, &entries) != nil {
			continue // disable-toggle: объект, а не массив строк
		}
		for _, e := range entries {
			if e.Interface == iface && !e.No {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func TestReconcile_AbsentTargetGoesToFallback(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"}) // Wireguard7 нет
	s, o := newDNSRouteServiceWithOracle(t, f, listWithRoutes("Wireguard0", "Wireguard7"))

	lists := f.ListCalls()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Отложенный save приходит из горутины координатора: читать Posts/E
	// только после того, как POST-ы утихли.
	n := waitQuiet(t, o.fakeRouter)
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("чтений списка за Reconcile = %d, ожидалось ровно 1", got)
	}
	if f.E != 0 {
		t.Fatalf("E=%d: dns-proxy route on an absent interface", f.E)
	}
	if f.Phantoms != 0 {
		t.Fatalf("Phantoms=%d", f.Phantoms)
	}
	if p := upsertsTo(f, "Wireguard7"); len(p) != 0 {
		t.Fatalf("upsert to absent target: %v", p)
	}
	if got := o.ifaceOrder("absent_p"); fmt.Sprint(got) != "[Wireguard0]" {
		t.Fatalf("на роутере %v, ожидался fallback [Wireguard0]", got)
	}

	// Повторный Reconcile без изменений — 0 новых POST (F548: перезаливка
	// на каждое применение) и снова ровно одно чтение списка.
	lists = f.ListCalls()
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := waitQuiet(t, o.fakeRouter); got != n {
		t.Fatalf("idempotent reconcile expected, got %d new posts", got-n)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("чтений списка за повторный Reconcile = %d, ожидалось 1", got)
	}
}

// Отсутствует и запасная цель — как сегодня при «упали все туннели списка»:
// группа есть, строк маршрутов нет, ни одной постановки, E нет.
func TestReconcile_FallbackAlsoAbsent_NoRoutes(t *testing.T) {
	f := query.NewFakeNDMS() // ни Wireguard6, ни Wireguard7
	s, o := newDNSRouteServiceWithOracle(t, f, listWithRoutes("Wireguard6", "Wireguard7"))

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitQuiet(t, o.fakeRouter)
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d Phantoms=%d", f.E, f.Phantoms)
	}
	for _, iface := range []string{"Wireguard6", "Wireguard7"} {
		if p := upsertsTo(f, iface); len(p) != 0 {
			t.Fatalf("upsert to absent %s: %v", iface, p)
		}
	}
	if got := o.ifaceOrder("absent_p"); len(got) != 0 {
		t.Fatalf("строки маршрутов %v, ожидалось пусто", got)
	}
	o.mu.Lock()
	_, groupOK := o.groups["absent_p1"]
	o.mu.Unlock()
	if !groupOK {
		t.Fatal("object-group absent_p1 не создана — расхождение с путём «все туннели упали»")
	}
}

// Список интерфейсов не прочитался — не знаем, куда писать: ошибка наружу,
// ни одного POST (решение 4).
func TestReconcile_ListError_NoPosts(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	f.FailList(errors.New("rci down"))
	s, _ := newDNSRouteServiceWithOracle(t, f, listWithRoutes("Wireguard0"))

	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка при непрочитанном списке")
	}
	if len(f.Posts) != 0 {
		t.Fatalf("POST при непрочитанном списке: %v", f.Posts)
	}
}

// Строка из выдачи роутера на уже отсутствующий интерфейс сносится по
// DNSRouteRef — строкой как есть, без подтверждения. Примет ли NDMS такой
// снос без E в журнале — не проверено (стенд); оракул считает любую ссылку
// на отсутствующий интерфейс за E, поэтому E здесь не ассертится.
func TestReconcile_StaleLineToAbsentDeletedByRef(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s, o := newDNSRouteServiceWithOracle(t, f, listWithRoutes("Wireguard0", "Wireguard7"))
	o.groups["absent_p1"] = &fakeGroup{includes: []string{"example.com"}}
	o.routes = []fakeRoute{
		{group: "absent_p1", iface: "Wireguard0", index: "i0"},
		{group: "absent_p1", iface: "Wireguard7", index: "i7"},
	}

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitQuiet(t, o.fakeRouter)
	var del bool
	for _, p := range f.Posts {
		if strings.Contains(p, `{"group":"absent_p1","interface":"Wireguard7","no":true}`) {
			del = true
		}
	}
	if !del {
		t.Fatalf("снос строки на Wireguard7 по ссылке не ушёл: %v", f.Posts)
	}
	if p := upsertsTo(f, "Wireguard7"); len(p) != 0 {
		t.Fatalf("upsert to absent target: %v", p)
	}
	if got := o.ifaceOrder("absent_p"); fmt.Sprint(got) != "[Wireguard0]" {
		t.Fatalf("на роутере %v, ожидался [Wireguard0]", got)
	}
}

// Отсутствие цели решается по интерфейсу, а не по TunnelID: TunnelID бывает
// пустым (REST его не требует) или общим у строк с разными интерфейсами
// (устаревший интерфейс в одном списке, свежий — в другом). Ключ по TunnelID
// увёл бы в fallback и живую строку второго списка (утечка мимо туннеля).
func TestReconcile_AbsentDecidedPerInterface(t *testing.T) {
	cases := map[string]string{"пустой TunnelID": "", "общий TunnelID": "t1"}
	for name, tunnelID := range cases {
		t.Run(name, func(t *testing.T) {
			f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"}) // Wireguard9 нет
			data := StoreData{Lists: []DomainList{
				{ID: "list_1", Name: "alpha", Enabled: true, Domains: []string{"a.example"},
					Routes: []RouteTarget{{Interface: "Wireguard9", TunnelID: tunnelID}}},
				{ID: "list_2", Name: "beta", Enabled: true, Domains: []string{"b.example"},
					Routes: []RouteTarget{{Interface: "Wireguard0", TunnelID: tunnelID}}},
			}}
			s, o := newDNSRouteServiceWithOracle(t, f, data)

			if err := s.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitQuiet(t, o.fakeRouter)
			if f.E != 0 || f.Phantoms != 0 {
				t.Fatalf("E=%d Phantoms=%d", f.E, f.Phantoms)
			}
			if p := upsertsTo(f, "Wireguard9"); len(p) != 0 {
				t.Fatalf("upsert to absent target: %v", p)
			}
			if got := o.ifaceOrder("beta_p"); fmt.Sprint(got) != "[Wireguard0]" {
				t.Fatalf("живая строка списка beta: %v, ожидалась [Wireguard0]", got)
			}
		})
	}
}
