package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/dnscheck"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// routeReg — одна регистрация маршрута в server_routes.go: путь-литерал и
// гард, в который обёрнут handler на месте регистрации.
type routeReg struct {
	path  string
	guard string // "auth" | "expert" | "none"
}

// routeRegistrations читает таблицу маршрутов ИЗ ИСХОДНИКА: все вызовы
// mux.HandleFunc / mux.Handle в server_routes.go с путём-литералом. Ручной
// список путей здесь был бы ровно той дырой, что закрываем: 21 путь из 359
// в старых тестах.
func routeRegistrations(t *testing.T) []routeReg {
	t.Helper()
	src := filepath.Join(".", "server_routes.go")
	srcBytes, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, srcBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Часть handler'ов регистрируется не выражением, а локальной переменной
	// (`openAPIHandler := h.guarded(...)`, дальше два mux.HandleFunc с ней).
	// Без разбора присваиваний такая регистрация читалась бы как «без гарда»
	// — ложная тревога вместо находки, поэтому гард переменной запоминаем.
	//
	// Пишем БЕЗУСЛОВНО, а не только гарды: имена здесь не разведены по
	// областям видимости (все секции register* — один файл), и присваивание
	// голого хендлера обязано ДЕМОТИРОВАТЬ имя до "none". Иначе `x :=
	// h.guarded(f)` в одной секции делал бы зелёной регистрацию одноимённой
	// голой переменной в другой — ровно та дыра, что закрываем. Побеждает
	// последнее присваивание, то есть разбор fail-closed.
	identGuards := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		if name, ok := as.Lhs[0].(*ast.Ident); ok {
			identGuards[name.Name] = guardOfExpr(as.Rhs[0], nil)
		}
		return true
	})

	var regs []routeReg
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		// Имя приёмника не проверяем намеренно: привязка к `mux`
		// молча унесла бы из инвентаря целую секцию, стоило кому-то
		// переименовать параметр. Полноту сторожит сверка ниже.
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Fatalf("%s: путь регистрации не литерал — инвентарь не полон", fset.Position(call.Pos()))
		}
		path, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatal(err)
		}
		regs = append(regs, routeReg{path: path, guard: guardOfExpr(call.Args[1], identGuards)})
		return true
	})
	// Порог «больше N» пропустил бы потерю десятка ручек. Сверяем точно: в
	// файле ровно столько строк с вызовом .Handle/.HandleFunc, сколько
	// регистраций собрал разбор. Расхождение = инвентарь неполон (форма
	// записи, которую AST-ветка выше не узнала) и все выводы обоих тестов
	// недействительны.
	want := 0
	for _, line := range strings.Split(string(srcBytes), "\n") {
		if strings.Contains(line, ".HandleFunc(") || strings.Contains(line, ".Handle(") {
			want++
		}
	}
	if len(regs) != want {
		t.Fatalf("инвентарь неполон: разбор нашёл %d регистраций, в исходнике %d строк с .Handle/.HandleFunc", len(regs), want)
	}
	return regs
}

// guardOfExpr — каким гардом обёрнут handler в этом выражении: прямым
// вызовом h.guarded/h.expertGuarded либо переменной, которой такой вызов
// присвоили (identGuards; nil при разборе самих присваиваний).
func guardOfExpr(e ast.Expr, identGuards map[string]string) string {
	switch v := e.(type) {
	case *ast.CallExpr:
		if gs, ok := v.Fun.(*ast.SelectorExpr); ok {
			switch gs.Sel.Name {
			case "guarded":
				return "auth"
			case "expertGuarded":
				return "expert"
			}
		}
	case *ast.Ident:
		if g, ok := identGuards[v.Name]; ok {
			return g
		}
	}
	return "none"
}

// noAuthRoutes — маршруты БЕЗ гарда, каждый с причиной. Подтверждено
// владельцем 2026-09-02. Любой третий незащищённый маршрут — красный тест.
var noAuthRoutes = map[string]string{
	"/api/auth/login":                      "вход — сессии ещё нет",
	"/api/auth/logout":                     "выход по куке, гард не нужен",
	"/api/auth/status":                     "фронт спрашивает, включена ли авторизация, до входа",
	"/api/health":                          "liveness для внешних проверок",
	"/api/hook/ndms":                       "форма из shell-хуков роутера — сессии быть не может",
	"/api/server/listen/confirm":           "одноразовый токен подтверждения смены адреса (server_listen.go)",
	"/api/boot-status":                     "экран загрузки до входа",
	"/api/dns-check/probe":                 "статический {ok:true} с CORS *, зовётся с origin awgm-dnscheck.test — кука не поедет",
	"/api/singbox/router/rulesets/dat-srs": "отдача .srs для sing-box по URL без сессии",
}

// expertPrefixes — раздел «Система»: root-доступ к файлам, процессам и opkg.
var expertPrefixes = []string{
	"/api/system/files/", "/api/system/opkg/", "/api/system/ports/",
	"/api/system/proc/", "/api/system/services/",
}

func isExpertPath(p string) bool {
	for _, pre := range expertPrefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// Каждая регистрация /api/* обёрнута в гард на месте регистрации: auth —
// везде, кроме allow-списка; expert — ровно на разделе «Система». Читается из
// исходника, поэтому новая ручка без h.guarded(...) роняет тест сама.
func TestAPIRoutes_EveryRegistrationGuarded(t *testing.T) {
	for _, r := range routeRegistrations(t) {
		if !strings.HasPrefix(r.path, "/api/") {
			continue
		}
		reason, allowed := noAuthRoutes[r.path]
		switch {
		case allowed && r.guard != "none":
			t.Errorf("%s в allow-списке (%s), но обёрнут в гард %q — список устарел", r.path, reason, r.guard)
		case !allowed && r.guard == "none":
			t.Errorf("%s зарегистрирован БЕЗ гарда", r.path)
		case isExpertPath(r.path) && r.guard != "expert":
			t.Errorf("%s — раздел «Система», ждали expertGuarded, получили %q", r.path, r.guard)
		case !isExpertPath(r.path) && r.guard == "expert":
			t.Errorf("%s обёрнут в expertGuarded вне раздела «Система»", r.path)
		}
	}
}

type testAuthChecker struct{}

func (testAuthChecker) IsAuthEnabled() bool { return true }
func (testAuthChecker) GetApiKey() string   { return "" }

func stub(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }

// newGuardServer — минимальный Server, на котором registerRoutes регистрирует
// ВСЕ маршруты. Гард срабатывает до вызова хендлера, поэтому хендлерам
// хватает нулевых значений; секции с гейтом `s.x != nil` получают нулевые
// указатели, иначе их маршруты пропали бы молча (класс «ошибка 200»).
func newGuardServer(t *testing.T) (*Server, *auth.SessionStore) {
	t.Helper()
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if _, err := settings.Load(); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessionStore(nil)
	t.Cleanup(sessions.Stop)
	// logging.NewService поднимает cleanupLoop на каждый буфер
	// (internal/logbuf/buffer.go:64) — без Stop горутины переживают тест.
	logSvc := logging.NewService(settings)
	t.Cleanup(logSvc.Stop)
	s := &Server{
		config:                     Config{Version: "test"},
		instanceID:                 "test",
		settings:                   settings,
		tunnels:                    storage.NewAWGTunnelStore(filepath.Join(dir, "tunnels")),
		sessions:                   sessions,
		authMiddleware:             auth.NewMiddleware(sessions, testAuthChecker{}, nil),
		bus:                        events.NewBus(),
		loggingService:             logSvc,
		singboxHandler:             &api.SingboxHandler{},
		singboxConnsHandler:        &api.SingboxConnectionsHandler{},
		singboxRouterHandler:       &api.SingboxRouterHandler{},
		singboxFakeIPConfigHandler: &api.SingboxFakeIPConfigHandler{},
		singboxConfigHandler:       &api.SingboxConfigHandler{},
		singboxConfigEditorHandler: &api.SingboxConfigEditorHandler{},
		singboxInboundsHandler:     &api.SingboxInboundsHandler{},
		singboxProxiesHandler:      &api.SingboxProxiesHandler{},
		bypassSetHandler:           &api.BypassSetHandler{},
		awgOutboundsHandler:        &api.AWGOutboundsHandler{},
		subscriptionHandler:        &api.SubscriptionHandler{},
		dnsRewritesHandler:         &api.DNSRewritesHandler{},
		awg3Handler:                &api.Awg3Handler{},
		clashProxy:                 &api.ClashProxy{},
		// Ниже — зависимости, за nil-гейтами которых прячутся целые секции
		// маршрутов. Без них пути не регистрируются вовсе, и гард на них
		// проверить нечем.
		hydraService:       &hydraroute.Service{},
		dnsCheckService:    &dnscheck.Service{},
		managedServiceImpl: &managed.Service{},
		presetCatalog:      &presets.Catalog{},
		ndmsQueries:        &ndmsquery.Queries{DNSProxyStatus: &ndmsquery.DNSProxyStatusStore{}},
		proxyRt: ProxyRtSurface{
			Instances: stub, ListenMoves: stub, WdttLinkDecode: stub, WdttLinkImport: stub,
			FreeTurnLinkDecode: stub, CaptchaStatus: stub, InstallStatus: stub, Install: stub, Uninstall: stub,
		},
	}
	return s, sessions
}

// Без сессии каждый /api/* отвечает 401 AUTH_REQUIRED (кроме allow-списка);
// с сессией уровня basic раздел «Система» отвечает 403. Это живой
// auth.Middleware на живой таблице маршрутов — мутант «guarded = identity в
// buildRouteHandlers» статический тест не видит, этот роняет.
//
// Свойство отказа под таким мутантом: запрос доходит до настоящего хендлера с
// нулевыми зависимостями. Обычно это паника (её и даёт мутант сегодня —
// /api/access-policies идёт по алфавиту раньше прочих), но в таблице есть
// потоковые ручки — /api/events, /api/diagnostics/stream, /api/terminal/ws,
// /api/test/speed/stream, /api/singbox/router/inspect/stream, — которые в
// хендлере ВИСНУТ: контекст httptest.NewRequest — Background, а Recorder
// реализует Flusher, так что выйти им не по чему. Красный получается быстрым
// только потому, что панический путь идёт раньше зависшего. Отсюда же
// отклонённая правка: recover() вокруг ServeHTTP делает сообщение внятнее, но
// проносит прогон мимо паники прямо в зависание — красный тогда приходит
// только по таймауту go test. Проверено, откачено.
func TestAPIRoutes_RejectWithoutSessionAndGateExpert(t *testing.T) {
	s, sessions := newGuardServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	paths := map[string]bool{}
	for _, r := range routeRegistrations(t) {
		if strings.HasPrefix(r.path, "/api/") {
			paths[r.path] = true
		}
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	for _, p := range sorted {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		// Непустой pattern означает «нашёлся обработчик», не обязательно
		// точное совпадение: путь под зарегистрированным поддеревом
		// (/api/servers/, /api/managed-servers/, /api/singbox/clash/,
		// /api/awg3-endpoints/, /api/proxyrt/instances/) отвечает
		// поддеревом. Сегодня ни один путь инвентаря так не маскируется.
		// Поэтому же фикстура намеренно НЕ задаёт config.FrontendFS: с ним
		// mux.Handle("/") (server_routes.go:1040) ловил бы вообще всё и
		// проверка регистрации стала бы зелёной тавтологией.
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s не зарегистрирован — фикстура не полна или секция за nil-гейтом", p)
			continue
		}
		if _, allowed := noAuthRoutes[p]; allowed {
			continue
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"AUTH_REQUIRED"`) {
			t.Errorf("%s без сессии: код %d, тело %s — гард не стоит", p, rec.Code, rec.Body.String())
		}
	}

	token, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sorted {
		if !isExpertPath(p) {
			continue
		}
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Код без кода ошибки не различает «гейт отказал» и «хендлер сам
		// вернул 403 по своей причине»: ждём ровно ответ ExpertOnly
		// (internal/api/system_tools.go:81).
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"FORBIDDEN"`) {
			t.Errorf("%s с сессией basic: код %d, тело %s — ждали 403 FORBIDDEN, expert-гейт не стоит", p, rec.Code, rec.Body.String())
		}
	}
}
