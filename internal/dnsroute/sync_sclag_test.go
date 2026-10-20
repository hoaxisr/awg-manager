package dnsroute

// F568: `/show/sc/dns-proxy/route` показывает снесённую через RCI строку до
// `system configuration save` (добавленную — сразу; стенд). Сверка обязана
// сохранить отложенные правки до чтения, иначе второй подряд снос списка
// видит уже снесённый маршрут первого и сносит его повторно — E «unable to
// find the DNS route» в журнале ndm.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// scLagRouter — oracleRouter, у которого sc-вид dns-proxy route отстаёт на
// сносах: снесённая строка видна до save. Сносы строк считаются в deletes,
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
			r.pinned, r.edited = nil, nil
		}
	}
	if dp, ok := p["dns-proxy"].(map[string]any); ok {
		if entries, ok := dp["route"].([]any); ok {
			r.fakeRouter.mu.Lock()
			live := append([]fakeRoute(nil), r.fakeRouter.routes...)
			r.fakeRouter.mu.Unlock()
			r.mu.Lock()
			for _, e := range entries {
				m, _ := e.(map[string]any)
				idx, byIndex := m["index"].(string)
				_, isRule := m["rule"]
				no, _ := m["no"].(bool)
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
						r.ghosts = append(r.ghosts, rt)
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
