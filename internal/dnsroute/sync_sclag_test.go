package dnsroute

// F568: `/show/sc/dns-proxy/route` показывает снесённую через RCI строку до
// `system configuration save` (стенд). Сверка обязана
// сохранить отложенные правки до чтения, иначе второй подряд снос списка
// видит уже снесённый маршрут первого и сносит его повторно — E «unable to
// find the DNS route» в журнале ndm.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// scLagRouter — oracleRouter, у которого sc-вид dns-proxy route отстаёт до
// save: снесённая строка видна, добавленная обычной записью не видна (стенд
// 5.02.B.1, 20.10, F692). Сносы строк считаются в deletes,
// повторный снос отсутствующей — ещё и в dupDeletes. saveErr — отказ save.
// Правки по index и перестановки `rule` sc тоже не показывает до save (стенд
// 20.10): с первой такой правки sc отдаёт снимок pinned, к которому
// дописаны строки, поставленные после него обычной записью; rc — живой.
type scLagRouter struct {
	*oracleRouter
	mu         sync.Mutex
	ghosts     []fakeRoute
	deletes    int
	dupDeletes int
	saveErr    error
	pinned     []fakeRoute
	edited     map[string]bool // group|interface строк, полученных правкой по index
	added      map[string]bool // group|interface строк, добавленных обычной записью
}

func routeWire(rt fakeRoute) map[string]any {
	return map[string]any{
		"group": rt.group, "interface": rt.iface, "auto": true,
		"reject": rt.reject, "index": rt.index, "disable": rt.disabled,
	}
}

func (r *scLagRouter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	raw, err := r.oracleRouter.GetRaw(ctx, path)
	if err != nil || path != "/show/sc/dns-proxy/route" {
		return raw, err
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	visible := arr[:0]
	for _, m := range arr {
		if !r.added[fmt.Sprint(m["group"], "|", m["interface"])] {
			visible = append(visible, m)
		}
	}
	arr = visible
	shown := map[string]bool{}
	if r.pinned != nil {
		live := arr
		arr = nil
		for _, rt := range r.pinned {
			arr = append(arr, routeWire(rt))
			shown[rt.index] = true
		}
		for _, m := range live {
			idx, _ := m["index"].(string)
			key := fmt.Sprint(m["group"], "|", m["interface"])
			if !shown[idx] && !r.edited[key] {
				arr = append(arr, m)
				shown[idx] = true
			}
		}
	}
	for _, g := range r.ghosts {
		if !shown[g.index] {
			arr = append(arr, routeWire(g))
		}
	}
	return json.Marshal(arr)
}

func (r *scLagRouter) Get(ctx context.Context, path string, dst any) error {
	if isIfacePath(path) {
		return r.oracleRouter.Get(ctx, path, dst)
	}
	raw, err := r.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func (r *scLagRouter) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p, _ := payload.(map[string]any)
	if sys, ok := p["system"].(map[string]any); ok {
		if cfg, ok := sys["configuration"].(map[string]any); ok && cfg["save"] != nil {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.saveErr != nil {
				return nil, r.saveErr
			}
			r.ghosts = nil
			r.pinned, r.edited, r.added = nil, nil, nil
		}
	}
	if dp, ok := p["dns-proxy"].(map[string]any); ok {
		if entries, ok := dp["route"].([]any); ok {
			r.fakeRouter.mu.Lock()
			live := append([]fakeRoute(nil), r.fakeRouter.routes...)
			r.fakeRouter.mu.Unlock()
			r.mu.Lock()
			present := map[string]bool{}
			for _, rt := range live {
				present[rt.group+"|"+rt.iface] = true
			}
			for _, e := range entries {
				m, _ := e.(map[string]any)
				idx, byIndex := m["index"].(string)
				_, isRule := m["rule"]
				no, _ := m["no"].(bool)
				key := fmt.Sprint(m["group"], "|", m["interface"])
				if !byIndex && !isRule && !no && !present[key] {
					if r.added == nil {
						r.added = map[string]bool{}
					}
					r.added[key] = true
					present[key] = true
				}
				if (byIndex && !no) || isRule {
					if r.pinned == nil {
						r.pinned = append([]fakeRoute{}, live...)
						r.edited = map[string]bool{}
					}
					if !isRule {
						r.edited[fmt.Sprint(m["group"], "|", m["interface"])] = true
					}
				}
				if !no {
					continue
				}
				r.deletes++
				found := false
				for _, rt := range live {
					if (byIndex && rt.index == idx) || (!byIndex && rt.group == m["group"] && rt.iface == m["interface"]) {
						k := rt.group + "|" + rt.iface
						present[k] = false
						if r.added[k] {
							delete(r.added, k) // sc её и не видел
						} else {
							r.ghosts = append(r.ghosts, rt)
						}
						found = true
						break
					}
				}
				if !found {
					r.dupDeletes++
				}
			}
			r.mu.Unlock()
		}
	}
	return r.oracleRouter.Post(ctx, payload)
}

// newSCLagService — сервис со списками list_a и list_b (оба на Wireguard0)
// поверх scLagRouter; маршруты уже поставлены, их save отложен.
func newSCLagService(t *testing.T) (*ServiceImpl, *scLagRouter) {
	t.Helper()
	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	data := StoreData{Lists: []DomainList{
		{ID: "list_a", Name: "a", Enabled: true, Domains: []string{"a.example"}, Routes: routes("Wireguard0")},
		{ID: "list_b", Name: "b", Enabled: true, Domains: []string{"b.example"}, Routes: routes("Wireguard0")},
	}}
	if err := store.Save(&data); err != nil {
		t.Fatal(err)
	}
	r := &scLagRouter{oracleRouter: &oracleRouter{
		fakeRouter: newFakeRouter(),
		f:          query.NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"}),
	}}
	q := query.NewQueries(query.Deps{Getter: r, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	// Отложенный save не срабатывает сам за время теста: сохранить до
	// чтения может только сверка.
	sc := command.NewSaveCoordinator(r, nopPublisher{}, time.Hour, time.Hour, 0, nil)
	sc.SetSaveTimings(command.SaveEventCap, 0, command.SaveAfterRemoval) // без шины событий
	c := command.NewCommands(command.Deps{Poster: r, Save: sc, Queries: q, IsOS5: func() bool { return true }})
	s := &ServiceImpl{store: store, queries: q, commands: c}

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, r
}

func TestReconcile_BackToBackDeletes_NoDuplicateRouteDelete(t *testing.T) {
	ctx := context.Background()
	s, r := newSCLagService(t)
	if err := s.Delete(ctx, "list_a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "list_b"); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	dup := r.dupDeletes
	r.mu.Unlock()
	if dup != 0 {
		t.Fatalf("повторный снос уже снесённого маршрута: %d", dup)
	}
	if got := r.ifaceOrder(""); len(got) != 0 {
		t.Fatalf("на роутере остались маршруты: %v", got)
	}
}

// Save не прошёл — сверка не считает сносы по устаревшему виду: ошибка и ни
// одного сноса.
func TestReconcile_FlushFails_NoDiff(t *testing.T) {
	s, r := newSCLagService(t)
	data := s.store.GetCached()
	data.Lists = data.Lists[1:] // list_a снят
	if err := s.store.Save(data); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.saveErr = errors.New("rci busy")
	r.mu.Unlock()
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("сбой save обязан вернуть ошибку сверки")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.deletes != 0 {
		t.Fatalf("сносов при несохранённой конфигурации: %d", r.deletes)
	}
}

func (r *fakeRouter) toggleCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.toggles
}

// assertGroupDisabled: у группы prefix есть строки, и все они выключены.
func (r *fakeRouter) assertGroupDisabled(t *testing.T, prefix string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rt := range r.routes {
		if strings.HasPrefix(rt.group, prefix) {
			n++
			if !rt.disabled {
				t.Errorf("строка %+v включена, список на паузе", rt)
			}
		}
	}
	if n == 0 {
		t.Errorf("у группы %s* нет строк", prefix)
	}
}

// F692: новая группа у списка, уже стоящего на паузе (например, новый чанк
// _pN; Create паузу не ставит — impl.go форсирует Enabled). sc на 5.02 не
// показывает добавленную строку до save, поэтому выключить её отдельным
// toggle по index после записи нечем — пауза обязана ехать в строке записи.
func TestF692_PausedListNewGroupDisabled(t *testing.T) {
	r := newFakeRouter()
	lag := &scLagRouter{oracleRouter: newOracle4(r)}
	svc := newServiceOver(t, lag, time.Hour, time.Hour)
	data := svc.store.GetCached()
	data.Lists = append(data.Lists, DomainList{
		ID: "list_a", Name: "paused", Enabled: false,
		Domains: []string{"a.example"}, Routes: routes("Wireguard1"),
	})
	if err := svc.store.Save(data); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	r.assertGroupDisabled(t, "paused_p")
	if got := r.toggleCount(); got != 0 {
		t.Errorf("toggle-POST-ов disable: %d, пауза обязана ехать в записи", got)
	}
}

// F692: пауза и смена туннеля на 5.00 (legacyOnly) — откат сносом+записью
// обязан записать строки сразу выключенными.
func TestF692_PausedLegacyFallbackDisabled(t *testing.T) {
	r := newFakeRouter()
	r.legacyOnly = true
	lag := &scLagRouter{oracleRouter: newOracle4(r)}
	svc := newServiceOver(t, lag, time.Hour, time.Hour)
	mustCreate(t, svc, "first", "Wireguard1")

	data := svc.store.GetCached()
	data.Lists[0].Enabled = false
	data.Lists[0].Routes = routes("Wireguard3")
	if err := svc.store.Save(data); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	if r.rejectedCount() == 0 {
		t.Fatal("попытки правки на месте не было — откат не проверен")
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard3")
	r.assertGroupDisabled(t, "first_p")
	if got := r.toggleCount(); got != 0 {
		t.Errorf("toggle-POST-ов disable: %d, пауза обязана ехать в записи", got)
	}
}

// F692 + F691: список на паузе с повтором интерфейса, записанный до проверки
// на записи, — строка одна и выключена, повторная сверка ничего не пишет.
func TestF691_PausedStoredDuplicateCollapses(t *testing.T) {
	r := newFakeRouter()
	lag := &scLagRouter{oracleRouter: newOracle4(r)}
	svc := newServiceOver(t, lag, time.Hour, time.Hour)
	mustCreate(t, svc, "first", "Wireguard1")

	data := svc.store.GetCached()
	data.Lists[0].Enabled = false
	data.Lists[0].Routes = routes("Wireguard2", "Wireguard2")
	if err := svc.store.Save(data); err != nil {
		t.Fatal(err)
	}
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertSeq(t, "first", r.ifaceOrder("first_p"), "Wireguard2")
	r.assertGroupDisabled(t, "first_p")

	waitQuiet(t, r)
	before := r.dnsPostCount()
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.dnsPostCount() - before; got != 0 {
		t.Errorf("повторная сверка сделала %d POST-ов dns-proxy", got)
	}
}
