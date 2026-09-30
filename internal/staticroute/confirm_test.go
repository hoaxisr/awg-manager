package staticroute

import (
	"context"
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

// Установка на отсутствующий — ни одной команды, E нет.
func TestStaticRoute_AddOnAbsent_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: false},
	})
	if err := s.SetEnabled(context.Background(), "srl1", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
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
