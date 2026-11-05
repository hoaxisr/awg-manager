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

// OS5-туннель ядра без записи OpkgTun (R37b): включение и создание —
// успех, запись сохранена, статус «ждёт старта», ни одной команды, E нет.
func TestStaticRoute_AddOnAbsent_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: false},
	})
	ctx := context.Background()
	if err := s.SetEnabled(ctx, "srl1", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if got, _ := s.store.GetRouteList("srl1"); !got.Enabled {
		t.Fatal("включение не сохранено")
	}
	created, err := s.Create(ctx, storage.StaticRouteList{Name: "b", TunnelID: "awg10", Subnets: []string{"10.30.0.0/16"}, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p := s.PendingIDs(); !p["srl1"] || !p[created.ID] {
		t.Fatalf("статус «ждёт старта» не выставлен: %v", p)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Старт создал запись — ждущие списки встают одним подтверждением, статус
// снят, E нет (R37b).
func TestStaticRoute_PendingAppliedOnTunnelStart(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: false},
	})
	ctx := context.Background()
	if err := s.SetEnabled(ctx, "srl1", true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	f.Add(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"}) // старт создал запись
	if err := s.OnTunnelStart(ctx, "awg10", "opkgtun10"); err != nil {
		t.Fatalf("OnTunnelStart: %v", err)
	}
	posts := routePosts(f)
	if len(posts) != 1 || !strings.Contains(posts[0], "10.20.0.0") || !strings.Contains(posts[0], "OpkgTun10") {
		t.Fatalf("маршрут не поставлен при старте: %v", posts)
	}
	if s.PendingIDs()["srl1"] {
		t.Fatal("статус «ждёт старта» не снят")
	}
	oracleClean(t, f)
}

// fakeRC — /show/rc/ip/route для старта: записи или отказ чтения.
type fakeRC struct {
	entries []query.StaticRouteEntry
	err     error
}

func (r fakeRC) Fetch(context.Context) ([]query.StaticRouteEntry, error) { return r.entries, r.err }

// startWithRC — старт OS5-туннеля awg10 с двумя подсетями в списке при
// заданном чтении маршрутов; возвращает команды маршрутов старта.
func startWithRC(t *testing.T, rc fakeRC) ([]string, *query.FakeNDMS) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16", "1.2.3.4/32"}, Enabled: true},
	})
	s.rc = rc
	if err := s.OnTunnelStart(context.Background(), "awg10", "opkgtun10"); err != nil {
		t.Fatalf("OnTunnelStart: %v", err)
	}
	return routePosts(f), f
}

// Старт OS5-туннеля (R37b follow-up 2): маршруты уже стоят — ни одной
// команды, значит и запроса сохранения (перезапуски ping-check не пишут флеш).
func TestStaticRoute_StartAllPresent_NoCommands(t *testing.T) {
	posts, f := startWithRC(t, fakeRC{entries: []query.StaticRouteEntry{
		{Network: "10.20.0.0", Mask: "255.255.0.0", Interface: "OpkgTun10"},
		{Host: "1.2.3.4", Interface: "OpkgTun10"},
	}})
	if len(f.Posts) != 0 {
		t.Fatalf("команды при стоящих маршрутах: %v", posts)
	}
	oracleClean(t, f)
}

// Запись снята снаружи и пересоздана стартом — NDMS снёс маршруты: ставятся
// только недостающие, E нет (R37b). Маршрут на ЧУЖОМ интерфейсе не в счёт.
func TestStaticRoute_ExternalRemoval_ReappliedOnStart(t *testing.T) {
	posts, f := startWithRC(t, fakeRC{entries: []query.StaticRouteEntry{
		{Host: "1.2.3.4", Interface: "OpkgTun10"},
		{Network: "10.20.0.0", Mask: "255.255.0.0", Interface: "OpkgTun11"},
	}})
	if len(posts) != 1 || !strings.Contains(posts[0], "10.20.0.0") || !strings.Contains(posts[0], "OpkgTun10") {
		t.Fatalf("want только недостающий 10.20.0.0: %v", posts)
	}
	oracleClean(t, f)
}

// Маршруты не прочитались — ставим все: лишнее сохранение дешевле
// потерянной связности.
func TestStaticRoute_StartRouteReadError_FullApply(t *testing.T) {
	posts, f := startWithRC(t, fakeRC{err: errors.New("rci down")})
	if all := strings.Join(posts, ""); !strings.Contains(all, "10.20.0.0") || !strings.Contains(all, "1.2.3.4") {
		t.Fatalf("want полную установку обеих подсетей, got %v", posts)
	}
	oracleClean(t, f)
}

// NativeWG: запись живёт и у остановленного — нет её, значит снята: явная
// ошибка, ни записи, ни команд (R37b).
func TestStaticRoute_NativeWGAbsent_Error(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg11", Subnets: []string{"10.20.0.0/16"}, Enabled: false},
	})
	s.catalog = &mockCatalog{ifaces: map[string]string{"awg11": "Wireguard1"}}
	ctx := context.Background()
	if err := s.SetEnabled(ctx, "srl1", true); err == nil || !strings.Contains(err.Error(), "Wireguard1") {
		t.Fatalf("SetEnabled: %v, want ошибку с именем интерфейса", err)
	}
	if got, _ := s.store.GetRouteList("srl1"); got.Enabled {
		t.Fatal("включение сохранено")
	}
	if _, err := s.Create(ctx, storage.StaticRouteList{Name: "b", TunnelID: "awg11", Subnets: []string{"10.30.0.0/16"}, Enabled: true}); err == nil {
		t.Fatal("Create: ждали ошибку")
	}
	if lists, _ := s.store.ListRouteLists(); len(lists) != 1 {
		t.Fatalf("создание сохранено: %+v", lists)
	}
	if len(f.Posts) != 0 || len(s.PendingIDs()) != 0 {
		t.Fatalf("posts=%v pending=%v", f.Posts, s.PendingIDs())
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
	got := strings.Join(routePosts(f), "")
	for _, want := range []string{"10.20.0.0", "10.30.0.0", "1.2.3.4"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Reconcile: нет маршрута %s: %s", want, got)
		}
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

// rejectPoster — оракул, отвергающий установку маршрута на 10.99.0.0. В пакете
// отказ приходит на свой элемент, как у NDMS (стенд 5.02.B.2): соседние
// применяются.
type rejectPoster struct{ f *query.FakeNDMS }

func (p rejectPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if batch, ok := payload.([]any); ok {
		replies := make([]json.RawMessage, len(batch))
		for i, item := range batch {
			r, err := p.Post(ctx, item)
			if err != nil {
				r = json.RawMessage(`{"ip":{"route":{"status":[{"status":"error","message":"injected: route rejected"}]}}}`)
			}
			replies[i] = r
		}
		return json.Marshal(replies)
	}
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

// reconcileWithRC — Reconcile со списком awg10 из двух подсетей при заданном
// чтении маршрутов.
func reconcileWithRC(t *testing.T, rc routeReader) ([]string, *query.FakeNDMS) {
	t.Helper()
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16", "1.2.3.4/32"}, Enabled: true},
	})
	s.rc = rc
	if err := s.Reconcile(context.Background()); err != nil {
		if _, failing := rc.(fakeRC); !failing || rc.(fakeRC).err == nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	return routePosts(f), f
}

// #1157: перезапуск демона при стоящих маршрутах — ни команды, ни сохранения,
// сколько бы маршрутов ни было.
func TestReconcile_AllPresent_NoCommands(t *testing.T) {
	posts, f := reconcileWithRC(t, fakeRC{entries: []query.StaticRouteEntry{
		{Network: "10.20.0.0", Mask: "255.255.0.0", Interface: "OpkgTun10"},
		{Host: "1.2.3.4", Interface: "OpkgTun10"},
	}})
	if len(f.Posts) != 0 {
		t.Fatalf("команды при стоящих маршрутах: %v", posts)
	}
	oracleClean(t, f)
}

// Ставится только недостающее; маршрут на ЧУЖОМ интерфейсе не в счёт.
func TestReconcile_OnlyMissing(t *testing.T) {
	posts, f := reconcileWithRC(t, fakeRC{entries: []query.StaticRouteEntry{
		{Host: "1.2.3.4", Interface: "OpkgTun10"},
		{Network: "10.20.0.0", Mask: "255.255.0.0", Interface: "OpkgTun11"},
	}})
	if len(posts) != 1 || !strings.Contains(posts[0], "10.20.0.0") || !strings.Contains(posts[0], "OpkgTun10") {
		t.Fatalf("want только недостающий 10.20.0.0: %v", posts)
	}
	oracleClean(t, f)
}

// Маршруты не прочитались — ndm занят: ни одной команды. Полная установка
// вернула бы сценарий #1157 именно под нагрузкой.
func TestReconcile_RouteReadError_NoCommands(t *testing.T) {
	_, f := reconcileWithRC(t, fakeRC{err: errors.New("rci down")})
	if len(f.Posts) != 0 {
		t.Fatalf("команды при отказе чтения: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Отказ чтения не снимает раздел R37b: записи OpkgTun нет — список ждёт старта.
func TestReconcile_RouteReadError_KeepsPending(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"1.2.3.4/32"}, Enabled: true},
	})
	s.rc = fakeRC{err: errors.New("rci down")}
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("отказ чтения проглочен: вызывающий считал бы маршруты восстановленными")
	}
	if !s.PendingIDs()["srl1"] {
		t.Fatal("статус «ждёт старта» потерян")
	}
}

// Расхождение reject или comment лечится повторной установкой, иначе после
// восстановления бэкапа список и роутер остались бы разными навсегда.
func TestReconcile_ReappliesChangedAttributes(t *testing.T) {
	for name, entry := range map[string]query.StaticRouteEntry{
		"reject":  {Host: "1.2.3.4", Interface: "OpkgTun10", Comment: "c"},
		"comment": {Host: "1.2.3.4", Interface: "OpkgTun10", Reject: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
			s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
				{ID: "srl1", Name: "a", TunnelID: "awg10", Fallback: "reject", Subnets: []string{"1.2.3.4/32 !c"}, Enabled: true},
			})
			s.rc = fakeRC{entries: []query.StaticRouteEntry{entry}}
			if err := s.Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			posts := routePosts(f)
			if len(posts) != 1 || !strings.Contains(posts[0], `"reject":true`) || !strings.Contains(posts[0], `"comment":"c"`) {
				t.Fatalf("want переустановку с reject и comment: %v", posts)
			}
		})
	}
}

// Совпавшие reject и comment — ни команды.
func TestReconcile_SameAttributes_NoCommands(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Fallback: "reject", Subnets: []string{"1.2.3.4/32 !c"}, Enabled: true},
	})
	s.rc = fakeRC{entries: []query.StaticRouteEntry{{Host: "1.2.3.4", Interface: "OpkgTun10", Reject: true, Comment: "c"}}}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("лишние команды: %v", f.Posts)
	}
}

// countingRC считает чтения: одно на весь Reconcile.
type countingRC struct {
	fakeRC
	reads *int
}

func (r countingRC) Fetch(ctx context.Context) ([]query.StaticRouteEntry, error) {
	*r.reads++
	return r.fakeRC.Fetch(ctx)
}

// Чтение маршрутов одно на проход, сколько бы списков ни было.
func TestReconcile_ReadsRoutesOnce(t *testing.T) {
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"},
		ndms.Interface{ID: "OpkgTun11", Type: "OpkgTun"},
	)
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"1.2.3.4/32"}, Enabled: true},
		{ID: "srl2", Name: "b", TunnelID: "awg11", Subnets: []string{"1.2.3.5/32"}, Enabled: true},
	})
	var reads int
	s.rc = countingRC{fakeRC{entries: []query.StaticRouteEntry{
		{Host: "1.2.3.4", Interface: "OpkgTun10"},
		{Host: "1.2.3.5", Interface: "OpkgTun11"},
	}}, &reads}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if reads != 1 {
		t.Fatalf("чтений маршрутов %d, want 1", reads)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("лишние команды: %v", f.Posts)
	}
}

// Одна подсеть в двух списках с разными comment и fallback: запись в NDMS одна,
// и сверка первого списка не должна перезаписывать её вторым на каждом проходе.
func TestReconcile_DuplicateSubnet_Converges(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	s := newOracleStaticRoutes(t, f, []storage.StaticRouteList{
		{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"1.2.3.4/32 !one"}, Enabled: true},
		{ID: "srl2", Name: "b", TunnelID: "awg10", Fallback: "reject", Subnets: []string{"1.2.3.4/32 !two"}, Enabled: true},
	})
	s.rc = fakeRC{entries: []query.StaticRouteEntry{{Host: "1.2.3.4", Interface: "OpkgTun10", Comment: "one"}}}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("запись перезаписана вторым списком: %v", f.Posts)
	}
}

// outagePoster — оракул, отвечающий транспортной ошибкой на пакеты установки
// или снятия, пока down.
type outagePoster struct {
	f    *query.FakeNDMS
	down *bool
}

func (p outagePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if _, batch := payload.([]any); batch && *p.down {
		return nil, errors.New("timeout")
	}
	return p.f.Post(ctx, payload)
}

func newOutageStaticRoutes(t *testing.T, f *query.FakeNDMS, lists []storage.StaticRouteList, down *bool) *ServiceImpl {
	t.Helper()
	s := newOracleStaticRoutes(t, f, lists)
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	p := outagePoster{f, down}
	s.routes = command.NewRouteCommands(p, command.NewSaveCoordinator(p, nil, time.Hour, time.Hour, 0, nil), q)
	return s
}

// Обрыв пакета снятия: маршруты могли остаться, запись обязана уцелеть для повтора.
func TestStaticRoute_RemoveOutage_KeepsRecord(t *testing.T) {
	ctx := context.Background()
	lists := []storage.StaticRouteList{{ID: "srl1", Name: "a", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}, Enabled: true}}

	t.Run("Delete", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
		down := true
		s := newOutageStaticRoutes(t, f, lists, &down)
		if err := s.Delete(ctx, "srl1"); err == nil {
			t.Fatal("обрыв проглочен")
		}
		if _, err := s.store.GetRouteList("srl1"); err != nil {
			t.Fatalf("запись потеряна: %v", err)
		}
	})
	t.Run("SetEnabled", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
		down := true
		s := newOutageStaticRoutes(t, f, lists, &down)
		if err := s.SetEnabled(ctx, "srl1", false); err == nil {
			t.Fatal("обрыв проглочен")
		}
		if got, _ := s.store.GetRouteList("srl1"); !got.Enabled {
			t.Fatal("список выключен, а маршруты могли остаться")
		}
	})
}

// Ответа на пакет нет — маршруты могли встать, откат обязан снимать и их.
func TestStaticRoute_ApplyOutage_RollbackIncludesUnknown(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	down := true
	s := newOutageStaticRoutes(t, f, nil, &down)
	got, err := s.applyRoutes(context.Background(), storage.StaticRouteList{ID: "x", TunnelID: "awg10", Subnets: []string{"10.20.0.0/16"}},
		map[string]query.Confirmed{"OpkgTun10": mustConfirm(t, s, "awg10")}, nil)
	if err == nil {
		t.Fatal("обрыв проглочен")
	}
	if len(got) != 1 || got[0] != "10.20.0.0/16" {
		t.Fatalf("неизвестный исход не передан откату: %v", got)
	}
}

func mustConfirm(t *testing.T, s *ServiceImpl, id string) query.Confirmed {
	t.Helper()
	confirmed, err := s.confirmIfaces(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return confirmed["OpkgTun10"]
}
