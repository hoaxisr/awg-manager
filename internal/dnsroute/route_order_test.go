package dnsroute

// Регресс #801: порядок routes в списке — это приоритет выбора туннеля, и он
// обязан доезжать до строк `dns-proxy route`.
//
// Модель роутера снята с живого KeeneticOS 5.01 (стенд, 2026-08-26):
//   - порядок строк в running-config = порядок записи и есть приоритет;
//   - upsert существующей строки её НЕ двигает («updated the DNS route»);
//   - новая строка всегда дописывается в хвост;
//   - снос и запись в ОДНОМ payload применяются поэлементно по порядку,
//     то есть блок можно переписать без промежутка без правил.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type fakeRoute struct {
	group    string
	iface    string
	reject   bool
	disabled bool
	index    string
}

type fakeGroup struct {
	includes []string
	excludes []string
}

// fakeRouter — минимальная модель NDMS: упорядоченная таблица dns-proxy
// route + object-group fqdn.
// fakeRouter под замком: POST прилетает из горутины координатора сохранения
// (SaveCoordinator.fire), а читает состав сам тест — без синхронизации это
// гонка данных, падающая под -race независимо от таймингов.
type fakeRouter struct {
	mu     sync.Mutex
	routes []fakeRoute
	groups map[string]*fakeGroup
	seq    int
	// snapshots — состав таблицы после каждого POST. По ним видно окно: если
	// между POST-ами группа осталась без строк, снос и запись разъехались.
	snapshots [][]fakeRoute
}

// waitQuiet ждёт, пока число POST-ов перестанет расти, и возвращает его.
// Нужен там, где тест меряет «сколько POST-ов сделал следующий шаг»: до него
// в очереди может лежать отложенный save от предыдущего.
func waitQuiet(t *testing.T, r *fakeRouter) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	last := r.snapshotCount()
	stable := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
		if n := r.snapshotCount(); n != last {
			last, stable = n, time.Now()
			continue
		}
		if time.Since(stable) > 60*time.Millisecond {
			return last
		}
	}
	t.Fatalf("POST-ы не утихли: %d", last)
	return last
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{groups: map[string]*fakeGroup{}}
}

func (r *fakeRouter) Get(context.Context, string, any) error { return nil }

func (r *fakeRouter) GetRaw(_ context.Context, path string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch path {
	case "/show/sc/dns-proxy/route":
		arr := make([]map[string]any, 0, len(r.routes))
		for _, rt := range r.routes {
			arr = append(arr, map[string]any{
				"group": rt.group, "interface": rt.iface,
				"auto": true, "reject": rt.reject,
				"index": rt.index, "disable": rt.disabled,
			})
		}
		return json.Marshal(arr)
	case "/show/rc/object-group/fqdn":
		m := map[string]any{}
		for name, g := range r.groups {
			inc := make([]map[string]string, 0, len(g.includes))
			for _, a := range g.includes {
				inc = append(inc, map[string]string{"address": a})
			}
			exc := make([]map[string]string, 0, len(g.excludes))
			for _, a := range g.excludes {
				exc = append(exc, map[string]string{"address": a})
			}
			m[name] = map[string]any{"include": inc, "exclude": exc}
		}
		if len(m) == 0 {
			return []byte("[]"), nil
		}
		return json.Marshal(m)
	}
	return []byte("{}"), nil
}

func (r *fakeRouter) Post(_ context.Context, payload any) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, _ := payload.(map[string]any)
	if dp, ok := p["dns-proxy"].(map[string]any); ok {
		switch route := dp["route"].(type) {
		case []any:
			for _, e := range route {
				m := e.(map[string]any)
				group, _ := m["group"].(string)
				iface, _ := m["interface"].(string)
				if no, _ := m["no"].(bool); no {
					r.deleteRoute(group, iface)
					continue
				}
				reject, _ := m["reject"].(bool)
				r.upsertRoute(group, iface, reject)
			}
		case map[string]any:
			if dis, ok := route["disable"].(map[string]any); ok {
				idx, _ := dis["index"].(string)
				no, _ := dis["no"].(bool)
				for i := range r.routes {
					if r.routes[i].index == idx {
						r.routes[i].disabled = !no
					}
				}
			}
		}
	}
	if og, ok := p["object-group"].(map[string]any); ok {
		if fqdn, ok := og["fqdn"].(map[string]any); ok {
			for name, v := range fqdn {
				m := v.(map[string]any)
				if no, _ := m["no"].(bool); no {
					delete(r.groups, name)
					continue
				}
				g := r.groups[name]
				if g == nil {
					g = &fakeGroup{}
					r.groups[name] = g
				}
				applyEntries(&g.includes, m["include"])
				applyEntries(&g.excludes, m["exclude"])
			}
		}
	}
	r.snapshots = append(r.snapshots, append([]fakeRoute(nil), r.routes...))
	return json.RawMessage(`{}`), nil
}

func applyEntries(dst *[]string, v any) {
	entries, ok := v.([]any)
	if !ok {
		return
	}
	for _, e := range entries {
		m := e.(map[string]any)
		addr, _ := m["address"].(string)
		if no, _ := m["no"].(bool); no {
			for i, a := range *dst {
				if a == addr {
					*dst = append((*dst)[:i], (*dst)[i+1:]...)
					break
				}
			}
			continue
		}
		*dst = append(*dst, addr)
	}
}

func (r *fakeRouter) upsertRoute(group, iface string, reject bool) {
	for i := range r.routes {
		if r.routes[i].group == group && r.routes[i].iface == iface {
			r.routes[i].reject = reject // позиция сохраняется
			return
		}
	}
	r.seq++
	r.routes = append(r.routes, fakeRoute{
		group: group, iface: iface, reject: reject,
		index: fmt.Sprintf("idx%d", r.seq),
	})
}

func (r *fakeRouter) deleteRoute(group, iface string) {
	for i := range r.routes {
		if r.routes[i].group == group && r.routes[i].iface == iface {
			r.routes = append(r.routes[:i], r.routes[i+1:]...)
			return
		}
	}
}

// ifaceOrder возвращает порядок интерфейсов для группы — то, что видно в
// running-config и по чему NDMS выбирает туннель.
func (r *fakeRouter) ifaceOrder(groupPrefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rt := range r.routes {
		if strings.HasPrefix(rt.group, groupPrefix) {
			out = append(out, rt.iface)
		}
	}
	return out
}

// assertNoWindow проверяет, что группа ни в одном промежутке между POST-ами
// не осталась без маршрутов после того, как они там появились.
// snapshotCount — число POST-ов, снятое ПОД локом. Голое `len(r.snapshots)`
// из теста — гонка: POST прилетает из горутины координатора сохранения.
func (r *fakeRouter) snapshotCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.snapshots)
}

func (r *fakeRouter) assertNoWindow(t *testing.T, groupPrefix string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := false
	for i, snap := range r.snapshots {
		n := 0
		for _, rt := range snap {
			if strings.HasPrefix(rt.group, groupPrefix) {
				n++
			}
		}
		if n > 0 {
			seen = true
			continue
		}
		if seen {
			t.Errorf("окно без маршрутов: после POST #%d группа %s* пуста", i, groupPrefix)
			return
		}
	}
}

func newServiceWithRouter(t *testing.T, r *fakeRouter) *ServiceImpl {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q := query.NewQueries(query.Deps{Getter: r, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(r, nopPublisher{}, 10*time.Millisecond, time.Second, 0, nil)
	c := command.NewCommands(command.Deps{Poster: r, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	return &ServiceImpl{store: store, queries: q, commands: c}
}

func routes(ifaces ...string) []RouteTarget {
	out := make([]RouteTarget, 0, len(ifaces))
	for _, i := range ifaces {
		out = append(out, RouteTarget{Interface: i, TunnelID: i})
	}
	return out
}

func TestIssue801_RouteOrderReachesRouter(t *testing.T) {
	ctx := context.Background()

	t.Run("создание сразу с четырьмя туннелями", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		if _, err := svc.Create(ctx, DomainList{
			Name:          "prio",
			ManualDomains: []string{"example.com"},
			Routes:        routes("Wireguard0", "Wireguard1", "Wireguard2", "Wireguard3"),
		}); err != nil {
			t.Fatal(err)
		}
		got := r.ifaceOrder("prio_p")
		want := []string{"Wireguard0", "Wireguard1", "Wireguard2", "Wireguard3"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("порядок на роутере = %v, ожидался %v", got, want)
		}
	})

	t.Run("перестановка приоритета через Update", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		created, err := svc.Create(ctx, DomainList{
			Name:          "prio",
			ManualDomains: []string{"example.com"},
			Routes:        routes("Wireguard0", "Wireguard1", "Wireguard2", "Wireguard3"),
		})
		if err != nil {
			t.Fatal(err)
		}
		upd := *created
		upd.Routes = routes("Wireguard3", "Wireguard2", "Wireguard1", "Wireguard0")
		if _, err := svc.Update(ctx, upd); err != nil {
			t.Fatal(err)
		}
		got := r.ifaceOrder("prio_p")
		want := []string{"Wireguard3", "Wireguard2", "Wireguard1", "Wireguard0"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("порядок на роутере = %v, ожидался %v", got, want)
		}
		r.assertNoWindow(t, "prio_p")
	})

	t.Run("новый приоритетный туннель добавлен первым", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		created, err := svc.Create(ctx, DomainList{
			Name:          "prio",
			ManualDomains: []string{"example.com"},
			Routes:        routes("Wireguard1", "Wireguard2", "Wireguard3"),
		})
		if err != nil {
			t.Fatal(err)
		}
		upd := *created
		upd.Routes = routes("Wireguard0", "Wireguard1", "Wireguard2", "Wireguard3")
		if _, err := svc.Update(ctx, upd); err != nil {
			t.Fatal(err)
		}
		got := r.ifaceOrder("prio_p")
		want := []string{"Wireguard0", "Wireguard1", "Wireguard2", "Wireguard3"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("порядок на роутере = %v, ожидался %v", got, want)
		}
		r.assertNoWindow(t, "prio_p")
	})

	t.Run("повторный reconcile без правок ничего не переписывает", func(t *testing.T) {
		r := newFakeRouter()
		svc := newServiceWithRouter(t, r)
		if _, err := svc.Create(ctx, DomainList{
			Name:          "prio",
			ManualDomains: []string{"example.com"},
			Routes:        routes("Wireguard0", "Wireguard1", "Wireguard2"),
		}); err != nil {
			t.Fatal(err)
		}
		// Отложенный save-POST от Create прилетает по debounce и тоже добавляет
		// снимок. Дожидаемся тишины ДО замера, иначе ассерт краснеет ложно —
		// на чужом POST-е, а не на работе Reconcile.
		before := waitQuiet(t, r)
		if err := svc.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if got := r.snapshotCount(); got != before {
			t.Errorf("холостой reconcile сделал %d POST-ов", got-before)
		}
	})
}
