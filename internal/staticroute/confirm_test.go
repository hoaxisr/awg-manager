package staticroute

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newOracleStaticRoutes — сервис поверх оракула FakeNDMS: список, чтения и
// команды идут в f (F546).
func newOracleStaticRoutes(t *testing.T, f *query.FakeNDMS, lists []storage.StaticRouteList) *ServiceImpl {
	t.Helper()
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return &ServiceImpl{
		store:   newTestStore(t, lists),
		routes:  command.NewRouteCommands(f, command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil), q),
		ifaces:  q.Interfaces,
		catalog: &mockCatalog{ifaces: map[string]string{"awg10": "OpkgTun10", "awg11": "OpkgTun11"}},
	}
}

func routePosts(f *query.FakeNDMS) []string {
	var out []string
	for _, p := range f.Posts {
		if strings.Contains(p, `"route"`) {
			out = append(out, p)
		}
	}
	return out
}

func oracleClean(t *testing.T, f *query.FakeNDMS) {
	t.Helper()
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d, want 0/0; posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// Интерфейса нет в NDMS — маршруты ушли вместе с ним: снятие без команды (ссылка
// ip route на отсутствующий пишет E), запись удалена.
func TestStaticRoute_RemoveOnAbsent_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16", "1.2.3.4/32"}, Enabled: true},
	})
	if err := s.Delete(context.Background(), "srl1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	if _, err := s.store.GetRouteList("srl1"); err == nil {
		t.Fatal("список обязан быть удалён из хранилища")
	}
	oracleClean(t, f)
}

// Интерфейса NDMS-туннеля нет (R37): включение и создание — явная ошибка,
// ни записи, ни команд, E нет.
func TestStaticRoute_AddOnAbsent_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: false},
	})
	ctx := context.Background()
	if err := s.SetEnabled(ctx, "srl1", true); err == nil || !strings.Contains(err.Error(), "OpkgTun10") {
		t.Fatalf("SetEnabled: %v, want ошибку с именем интерфейса", err)
	}
	if got, _ := s.store.GetRouteList("srl1"); got.Enabled {
		t.Fatal("включение сохранено")
	}
	if _, err := s.Create(ctx, storage.StaticRouteList{Name: "b", TunnelID: "awg10", Subnets: []string{"10.30.0.0/16"}, Enabled: true}); err == nil {
		t.Fatal("Create: ждали ошибку")
	}
	if lists, _ := s.store.ListRouteLists(); len(lists) != 1 {
		t.Fatalf("создание сохранено: %+v", lists)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// OS4-туннель не поднят (R37): установка откладывается до старта — успех,
// запись сохранена, команд нет.
func TestStaticRoute_OS4NotUp_Deferred(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awgm0", Subnets: []string{"10.20.0.0/16"}, Enabled: false},
	})
	s.ifaceExists = func(string) bool { return false }
	if err := s.SetEnabled(context.Background(), "srl1", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if got, _ := s.store.GetRouteList("srl1"); !got.Enabled {
		t.Fatal("включение не сохранено")
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды: %v", f.Posts)
	}
}

// Reconcile двух списков на двух туннелях и Update со сменой туннеля — ОДИН
// список на вызов; маршруты на присутствующих уходят, E нет.
func TestStaticRoute_OneListPerCall(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"}, ndms.Interface{ID: "OpkgTun11", Type: "OpkgTun"})
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16", "10.30.0.0/16"}, Enabled: true},
		{ID: "srl2", Name: "b", TunnelID: "awg11", Subnets: []string{"1.2.3.4/32"}, Enabled: true},
	})
	ctx := context.Background()
	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if n := f.ListCalls(); n != 1 {
		t.Fatalf("Reconcile: чтений списка = %d, want 1", n)
	}
	if got := routePosts(f); len(got) != 3 {
		t.Fatalf("Reconcile: маршрутов %d, want 3: %v", len(got), got)
	}

	if _, err := s.Update(ctx, storage.StaticRouteList{ID: "srl1", Name: "a", TunnelID: "awg11", Subnets: []string{"10.20.0.0/16"}, Enabled: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if n := f.ListCalls(); n != 2 {
		t.Fatalf("Update: чтений списка = %d, want 1", n-1)
	}
	oracleClean(t, f)
}

// Список не прочитан (решение 4) — ни установки, ни снятия.
func TestStaticRoute_ListError_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	f.FailList(errors.New("rci down"))
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: true},
	})
	ctx := context.Background()
	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Снять маршруты нельзя — запись остаётся для повтора, иначе маршруты в
	// NDMS остались бы сиротами.
	if err := s.Delete(ctx, "srl1"); err == nil {
		t.Fatal("Delete при ошибке списка: ждали отказ")
	}
	if _, err := s.store.GetRouteList("srl1"); err != nil {
		t.Fatalf("запись обязана остаться: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды при ошибке списка: %v", f.Posts)
	}
}

// Список не прочитан — маршруты не применить: Update и SetEnabled отвечают
// ошибкой и НЕ сохраняют запись, иначе хранилище и роутер расходятся (F565).
func TestStaticRoute_ListError_UpdateAndSetEnabledKeepRecord(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	f.FailList(errors.New("rci down"))
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: true},
	})
	ctx := context.Background()
	if _, err := s.Update(ctx, storage.StaticRouteList{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.30.0.0/16"}, Enabled: true}); err == nil {
		t.Fatal("Update при ошибке списка: ждали отказ")
	}
	if err := s.SetEnabled(ctx, "srl1", false); err == nil {
		t.Fatal("SetEnabled при ошибке списка: ждали отказ")
	}
	got, err := s.store.GetRouteList("srl1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || len(got.Subnets) != 1 || got.Subnets[0] != "10.20.0.0/16" {
		t.Fatalf("запись изменена: %+v", got)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды при ошибке списка: %v", f.Posts)
	}
}

// rejectPoster — оракул, отвергающий установку маршрута на 10.99.0.0.
type rejectPoster struct{ f *query.FakeNDMS }

func (p rejectPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	js, _ := json.Marshal(payload)
	if strings.Contains(string(js), `10.99.0.0`) && !strings.Contains(string(js), `"no":true`) {
		return nil, errors.New("injected: route rejected")
	}
	return p.f.Post(ctx, payload)
}

func newRejectingStaticRoutes(t *testing.T, f *query.FakeNDMS, lists []storage.StaticRouteList) *ServiceImpl {
	t.Helper()
	s := newOracleStaticRoutes(t, f, lists)
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	p := rejectPoster{f}
	s.routes = command.NewRouteCommands(p, command.NewSaveCoordinator(p, nil, time.Hour, time.Hour, 0, nil), q)
	return s
}

// removedRoute — снят ли маршрут на network (форма `no` в payload).
func removedRoute(f *query.FakeNDMS, network string) bool {
	for _, p := range routePosts(f) {
		if strings.Contains(p, network) && strings.Contains(p, `"no":true`) {
			return true
		}
	}
	return false
}

// Отказ одной подсети: Create/SetEnabled/Update отвечают ошибкой, поставленное
// снято, запись не сохранена (F565, раунд 1).
func TestStaticRoute_RouteRejected_ErrorRollbackNoSave(t *testing.T) {
	bad := []string{"10.20.0.0/16", "10.99.0.0/16"}
	ctx := context.Background()

	t.Run("Create", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
		s := newRejectingStaticRoutes(t, f, nil)
		if _, err := s.Create(ctx, storage.StaticRouteList{Name: "a", TunnelID: "awg10", Subnets: bad, Enabled: true}); err == nil {
			t.Fatal("отказ маршрута проглочен")
		}
		if lists, _ := s.store.ListRouteLists(); len(lists) != 0 {
			t.Fatalf("список сохранён: %+v", lists)
		}
		if !removedRoute(f, "10.20.0.0") {
			t.Fatalf("поставленная подсеть не снята: %v", routePosts(f))
		}
		oracleClean(t, f)
	})

	t.Run("SetEnabled", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
		s := newRejectingStaticRoutes(t, f, []storage.StaticRouteList{
			{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: bad, Enabled: false},
		})
		if err := s.SetEnabled(ctx, "srl1", true); err == nil {
			t.Fatal("отказ маршрута проглочен")
		}
		if got, _ := s.store.GetRouteList("srl1"); got.Enabled {
			t.Fatal("включение сохранено при отказе")
		}
		if !removedRoute(f, "10.20.0.0") {
			t.Fatalf("поставленная подсеть не снята: %v", routePosts(f))
		}
		oracleClean(t, f)
	})

	t.Run("Update", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
		s := newRejectingStaticRoutes(t, f, []storage.StaticRouteList{
			{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.30.0.0/16"}, Enabled: true},
		})
		if _, err := s.Update(ctx, storage.StaticRouteList{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: bad, Enabled: true}); err == nil {
			t.Fatal("отказ маршрута проглочен")
		}
		got, _ := s.store.GetRouteList("srl1")
		if len(got.Subnets) != 1 || got.Subnets[0] != "10.30.0.0/16" {
			t.Fatalf("правка сохранена при отказе: %+v", got)
		}
		posts := routePosts(f)
		if !removedRoute(f, "10.20.0.0") || !strings.Contains(posts[len(posts)-1], "10.30.0.0") || strings.Contains(posts[len(posts)-1], `"no":true`) {
			t.Fatalf("прежний список не возвращён: %v", posts)
		}
		oracleClean(t, f)
	})
}
