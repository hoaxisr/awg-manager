package dnsroute

// Регресс #801: порядок routes в списке — это приоритет выбора туннеля, и он
// обязан доезжать до строк `dns-proxy route`. Регресс #1023: смена туннеля не
// должна уводить блок группы в хвост — порядок строк ещё и приоритет МЕЖДУ
// группами (первая совпавшая выигрывает).
//
// Модель роутера снята с живого KeeneticOS (стенд 5.01, 2026-08-26; 5.02.B.1
// и 5.01.C.1, 2026-10-20):
//   - порядок строк в running-config = порядок записи и есть приоритет;
//   - upsert существующей строки её НЕ двигает («updated the DNS route»);
//   - новая строка всегда дописывается в хвост;
//   - снос и запись в ОДНОМ payload применяются поэлементно по порядку,
//     то есть блок можно переписать без промежутка без правил;
//   - правка по index сохраняет позицию и disable, поля заменяет (пропущенный
//     reject сбрасывается), index пересчитывается; правка в уже существующее
//     содержимое даёт полный дубль с тем же index;
//   - снос по index и правка при дублях — первая совпавшая строка;
//   - `rule` переставляет строку на new-index (с нуля), index не меняется;
//   - `disable:true` в элементе записи ставит строку выключенной, в том числе
//     upsert существующей (F692); что upsert без disable флаг не трогает —
//     допущение модели, не замер стенда;
//   - неизвестный index — status:"error" (4457098 у правки/сноса, 4457748 у
//     `rule`), new-index за концом — 4457648.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
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
	// dnsPosts — POST-ы с ключом dns-proxy (save и object-group не в счёт).
	dnsPosts int
	// indexed — элементы dns-proxy route с index или rule (путь «на месте»).
	indexed int
	// legacyOnly — ГИПОТЕЗА о 5.00 (на железе не мерено): элементы с index и
	// rule отвергаются status:"error", остальные элементы батча применяются.
	legacyOnly bool
	// failRule — ошибка только на rule: правка на месте прошла, перестановка нет.
	failRule bool
	// rcRouteReads — чтения /show/rc/dns-proxy/route (порядок строк).
	rcRouteReads int
	// rejected — элементы index/rule, отвергнутые в режиме legacyOnly.
	rejected int
	// rcObjectForm — rc отдаёт форму-объект с ключом по группе (одна запись
	// на группу, порядок map): по ней порядок невосстановим.
	rcObjectForm bool
	// toggles — POST-ы `dns-proxy.route.disable` (SetDisabled).
	toggles int
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
	if path == "/show/rc/dns-proxy/route" && r.rcObjectForm {
		m := map[string]any{}
		for _, rt := range r.routes {
			m[rt.group] = map[string]any{
				"interface": rt.iface, "auto": true, "reject": rt.reject,
				"index": rt.index, "disable": rt.disabled,
			}
		}
		return json.Marshal(m)
	}
	if path == "/show/rc/dns-proxy/route" {
		r.rcRouteReads++
	}
	switch path {
	case "/show/sc/dns-proxy/route", "/show/rc/dns-proxy/route":
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
	resp := json.RawMessage(`{}`)
	p, _ := payload.(map[string]any)
	if dp, ok := p["dns-proxy"].(map[string]any); ok {
		r.dnsPosts++
		switch route := dp["route"].(type) {
		case []any:
			// Ответ, как у NDMS: статус на каждый элемент; отказ элемента не
			// отменяет уже применённые.
			statuses := make([]any, 0, len(route))
			failed := false
			for _, e := range route {
				st := r.applyRouteElem(e.(map[string]any))
				if st == nil {
					statuses = append(statuses, map[string]any{})
					continue
				}
				failed = true
				statuses = append(statuses, map[string]any{"status": []any{st}})
			}
			if failed {
				resp, _ = json.Marshal(map[string]any{"dns-proxy": map[string]any{"route": statuses}})
			}
		case map[string]any:
			if dis, ok := route["disable"].(map[string]any); ok {
				r.toggles++
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
	return resp, nil
}

func routeErr(code, msg string) map[string]any {
	return map[string]any{"status": "error", "code": code, "message": msg}
}

// applyRouteElem применяет один элемент списка dns-proxy route; nil — успех,
// иначе статус ошибки.
func (r *fakeRouter) applyRouteElem(m map[string]any) map[string]any {
	idx, byIndex := m["index"].(string)
	rule, isRule := m["rule"].(map[string]any)
	if byIndex || isRule {
		r.indexed++
		if r.legacyOnly {
			r.rejected++
			// Текст и код отказа — тоже гипотеза: 5.00 не мерили.
			return routeErr("", "unknown argument")
		}
	}
	group, _ := m["group"].(string)
	iface, _ := m["interface"].(string)
	switch no, _ := m["no"].(bool); {
	case isRule:
		if r.failRule {
			return routeErr("", "rule failed")
		}
		return r.moveRoute(rule)
	case byIndex && no:
		p := r.findIndex(idx)
		if p < 0 {
			return routeErr("4457098", fmt.Sprintf("unable to find the DNS route rule with index %q", idx))
		}
		r.routes = append(r.routes[:p], r.routes[p+1:]...)
	case byIndex:
		p := r.findIndex(idx)
		if p < 0 {
			return routeErr("4457098", fmt.Sprintf("unable to find the DNS route rule with index %q", idx))
		}
		reject, _ := m["reject"].(bool)
		rt := &r.routes[p]
		rt.group, rt.iface, rt.reject = group, iface, reject // disable и позиция сохраняются
		rt.index = r.contentIndex(p)
	case no:
		r.deleteRoute(group, iface)
	default:
		reject, _ := m["reject"].(bool)
		disable, _ := m["disable"].(bool)
		r.upsertRoute(group, iface, reject, disable)
	}
	return nil
}

func (r *fakeRouter) findIndex(idx string) int {
	for i := range r.routes {
		if r.routes[i].index == idx {
			return i
		}
	}
	return -1
}

// contentIndex — index строки p после правки. Index на роутере — хэш
// содержимого: правка в уже существующее содержимое даёт дубль с тем же index.
func (r *fakeRouter) contentIndex(p int) string {
	rt := r.routes[p]
	for i, o := range r.routes {
		if i != p && o.group == rt.group && o.iface == rt.iface && o.reject == rt.reject {
			return o.index
		}
	}
	r.seq++
	return fmt.Sprintf("idx%d", r.seq)
}

func (r *fakeRouter) moveRoute(rule map[string]any) map[string]any {
	idx, _ := rule["index"].(string)
	var pos int
	switch n := rule["new-index"].(type) {
	case int:
		pos = n
	case float64:
		pos = int(n)
	}
	p := r.findIndex(idx)
	if p < 0 {
		// Код с железа, текст не снимали: tolerate у MoveRoutes нет, он не важен.
		return routeErr("4457748", fmt.Sprintf("unknown rule index %q", idx))
	}
	if pos < 0 || pos >= len(r.routes) {
		return routeErr("4457648", "invalid rule index")
	}
	if pos == p {
		return nil // «left the position … unchanged» — status "message"
	}
	rt := r.routes[p]
	r.routes = append(r.routes[:p], r.routes[p+1:]...)
	r.routes = append(r.routes[:pos], append([]fakeRoute{rt}, r.routes[pos:]...)...)
	return nil
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

func (r *fakeRouter) upsertRoute(group, iface string, reject, disable bool) {
	for i := range r.routes {
		if r.routes[i].group == group && r.routes[i].iface == iface {
			r.routes[i].reject = reject // позиция сохраняется
			r.routes[i].disabled = r.routes[i].disabled || disable
			return
		}
	}
	r.seq++
	r.routes = append(r.routes, fakeRoute{
		group: group, iface: iface, reject: reject, disabled: disable,
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
	return newServiceOver(t, newOracle4(r), 10*time.Millisecond, time.Second)
}

// newOracle4 — fakeRouter с оракулом Wireguard0..3: интерфейсы целей
// reconcile подтверждает списком (F548) — их отдаёт оракул.
func newOracle4(r *fakeRouter) *oracleRouter {
	return &oracleRouter{fakeRouter: r, f: query.NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard2", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard3", Type: "Wireguard"},
	)}
}

func newServiceOver(t *testing.T, o interface {
	query.Getter
	command.Poster
}, debounce, maxWait time.Duration) *ServiceImpl {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	q := query.NewQueries(query.Deps{Getter: o, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	sc := command.NewSaveCoordinator(o, nopPublisher{}, debounce, maxWait, 0, nil)
	sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval) // без шины событий
	c := command.NewCommands(command.Deps{Poster: o, Save: sc, Queries: q, IsOS5: func() bool { return true }})
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
