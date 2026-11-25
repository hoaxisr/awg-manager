package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type foreignFircFixture struct {
	root  string
	mux   *http.ServeMux
	store *storage.SettingsStore
	hits  *atomic.Int32
	// turnOff — переход на sing-box пришёл с меткой firc.WithTurnOff.
	turnOff bool
}

// newForeignFircFixture — роутер с пакетом firc и живым демоном на сокете;
// движок панели — engine.
func newForeignFircFixture(t *testing.T, engine string) *foreignFircFixture {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{
		"opt/bin/fircd":               "fircd",
		"opt/bin/tunvless":            "tunvless",
		"opt/etc/init.d/S99firc":      "#!/bin/sh\necho \"$1\" >> \"$0.ran\"\n",
		"opt/etc/firc/firc.conf":      "app:\n  httpWeb:\n    enabled: true\n    host:\n      port: 666\n",
		"opt/etc/firc/tunnels.yaml":   "tunnels:\n- id: foreign-tunnel\n  device: tunvless0\n  enable: true\n  failback: true\n",
		"opt/etc/firc/groups.yaml":    "groups:\n- id: foreign1\n  name: foreign-group\n  enable: true\n  interface: tunvless0\n  rules: []\n",
		"opt/etc/firc/awgm-ext.json":  `{"proxyGroups":[{"id":"pg","name":"foreign-ext","interfaces":["tunvless0"]}]}`,
		"opt/var/lib/firc/hwid":       "hwid\n",
		"opt/lib/opkg/info/firc.list": "/opt/bin/fircd\n/opt/bin/tunvless\n",
		"opt/lib/opkg/status":         "Package: firc\nVersion: 0.8.5-1\nStatus: install user installed\n\n",
		"sb/config.d/00-base.json":    `{"log":{"level":"info"}}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hits := &atomic.Int32{}
	sockDir, err := os.MkdirTemp("", "firc-f-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true,"tunnels":[{"id":"foreign-tunnel"}],"groups":[{"id":"foreign1","name":"foreign-group"}]}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	store := storage.NewSettingsStore(t.TempDir())
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = engine
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	op := firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(root, "opt/etc/firc"),
		SocketPath:  sock,
		FircdBinary: filepath.Join(root, "opt/bin/fircd"),
		TunvlessBin: filepath.Join(root, "opt/bin/tunvless"),
		InitScript:  filepath.Join(root, "opt/etc/init.d/S99firc"),
		Foreign:     firc.NewForeignDetector(root),
	})
	op.SetVarLibDir(filepath.Join(root, "opt/var/lib/firc"))
	inst := firc.NewOnDemandInstaller("mipsel-3.4", firc.FircBinarySpec{
		Version: "0.8.0-awgm.4", PackageURL: "https://example.invalid/r.ipk", PackageSHA256: strings.Repeat("a", 64), PackageSize: 1,
	})
	inst.SetPackageLayout(firc.DefaultPackageLayout(root))
	inst.SetPackageManager(firc.OpkgRuntimeManager{StatusPath: filepath.Join(root, "opt/lib/opkg/status")})

	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	h.SetInstaller(inst)
	h.SetSingboxConfigDir(filepath.Join(root, "sb/config.d"))
	nop := func(context.Context, string) error { return nil }
	f := &foreignFircFixture{root: root, store: store, hits: hits}
	h.SetOtherEngineLifecycle(func(ctx context.Context, _ string) error {
		f.turnOff = firc.TurnOffRequested(ctx)
		return nil
	}, nop)
	f.mux = http.NewServeMux()
	h.RegisterRoutes(f.mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return f
}

func (f *foreignFircFixture) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func (f *foreignFircFixture) engine() string {
	s, _ := f.store.Get()
	return s.SingboxRouter.RoutingEngine
}

// digest — дерево побайтово, с правами и отметками времени.
func (f *foreignFircFixture) digest(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		var body []byte
		if info.Mode().IsRegular() {
			if body, err = os.ReadFile(p); err != nil {
				return err
			}
		}
		fmt.Fprintf(&out, "%s %v %d %q\n", p, info.Mode(), info.ModTime().UnixNano(), body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func (f *foreignFircFixture) assertUntouched(t *testing.T, what, before string) {
	t.Helper()
	if after := f.digest(t); after != before {
		t.Errorf("%s: дерево изменилось\nбыло:\n%s\nстало:\n%s", what, before, after)
	}
	if n := f.hits.Load(); n != 0 {
		t.Errorf("%s: %d обращений к сокету чужого fircd", what, n)
	}
}

var fircRouteRe = regexp.MustCompile(`mux\.HandleFunc\("(?:(GET|POST|PUT|DELETE|PATCH) )?(/api/firc/[^"]+)"`)

// fircRoutes читает маршруты из исходника: новый маршрут попадает в тест сам.
func fircRoutes(t *testing.T) [][2]string {
	t.Helper()
	src, err := os.ReadFile("firc_handler.go")
	if err != nil {
		t.Fatal(err)
	}
	var out [][2]string
	for _, m := range fircRouteRe.FindAllStringSubmatch(string(src), -1) {
		if m[1] != "" {
			out = append(out, [2]string{m[1], m[2]})
		}
	}
	if len(out) < 40 {
		t.Fatalf("маршрутов найдено %d — разбор исходника сломан", len(out))
	}
	return out
}

func TestFircForeign_EveryMutationRefusedWithoutTrace(t *testing.T) {
	var calls [][3]string
	for _, rt := range fircRoutes(t) {
		if rt[0] != http.MethodGet && !fircEngineExitPaths[rt[1]] {
			calls = append(calls, [3]string{rt[0], rt[1], `{}`})
		}
	}
	for _, sub := range [][2]string{
		{"PUT", "/tunnels"}, {"PUT", "/groups"}, {"PUT", "/system/settings"}, {"POST", "/system/restart"},
		{"POST", "/tunnels/probe"}, {"POST", "/tunnels/preview"}, {"POST", "/system/capture"}, {"DELETE", "/system/capture"},
		{"POST", "/tunnels/foreign-tunnel/refresh"}, {"POST", "/tunnels/foreign-tunnel/restart"}, {"POST", "/groups/foreign1/move-up"},
		{"POST", "/groups/foreign1/list/sync"}, {"PATCH", "/groups/foreign1/list/rules"}, {"POST", "/system/autolist/candidates/example.com/add"},
	} {
		calls = append(calls, [3]string{sub[0], "/api/firc/v1" + sub[1], `{"tunnels":[],"groups":[],"confirmed":true,"groupId":"foreign1"}`})
	}
	// Ручки ухода с движка: всё, кроме самого ухода, закрыто.
	for _, action := range []string{"start", "stop", "reload", "clear_recovery", "reconcile_recovery"} {
		calls = append(calls, [3]string{"POST", "/api/firc/control", `{"action":"` + action + `"}`})
	}
	calls = append(calls,
		[3]string{"POST", "/api/firc/control", `{"action":"select"}`},
		[3]string{"POST", "/api/firc/control", `{"action":"select","engine":"firc"}`},
		[3]string{"POST", "/api/firc/engine/switch", `{"engine":"firc"}`},
		[3]string{"PUT", "/api/firc/settings", `{"routingEngine":"firc"}`},
		[3]string{"PUT", "/api/firc/settings", `{"selected":true}`},
		[3]string{"PUT", "/api/firc/settings", `{"app.dnsProxy.host.port":53}`},
	)
	for _, engine := range []string{"sing-box", "firc"} {
		f := newForeignFircFixture(t, engine)
		before := f.digest(t)
		for _, c := range calls {
			what := engine + ": " + c[0] + " " + c[1] + " " + c[2]
			rec := f.do(c[0], c[1], c[2])
			var env struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			if rec.Code != http.StatusConflict || env.Code != "FIRC_FOREIGN" || !strings.Contains(env.Message, "управляется своим интерфейсом") {
				t.Errorf("%s = %d %s", what, rec.Code, rec.Body.String())
			}
			f.assertUntouched(t, what, before)
			if f.engine() != engine {
				t.Fatalf("%s: движок стал %s", what, f.engine())
			}
		}
	}
}

func TestFircForeign_ReadsShowNothingOfForeign(t *testing.T) {
	paths := []string{
		"/api/firc/v1/tunnels", "/api/firc/v1/tunnels/state", "/api/firc/v1/tunnels/probe", "/api/firc/v1/groups",
		"/api/firc/v1/groups/foreign1/list/rules", "/api/firc/v1/system/settings", "/api/firc/v1/system/events",
		"/api/firc/v1/system/netfilter", "/api/firc/v1/system/dnsstats", "/api/firc/v1/system/autolist", "/api/firc/v1/system/interfaces",
	}
	for _, rt := range fircRoutes(t) {
		if rt[0] == http.MethodGet {
			paths = append(paths, rt[1])
		}
	}
	for _, engine := range []string{"sing-box", "firc"} {
		f := newForeignFircFixture(t, engine)
		before := f.digest(t)
		for _, p := range paths {
			rec := f.do(http.MethodGet, p, "")
			if rec.Code != http.StatusOK {
				t.Errorf("%s: GET %s = %d %s", engine, p, rec.Code, rec.Body.String())
			}
			for _, leak := range []string{"foreign-tunnel", "foreign-group", "foreign-ext", "tunvless0", "0.8.5"} {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("%s: GET %s показывает %q: %s", engine, p, leak, rec.Body.String())
				}
			}
			f.assertUntouched(t, engine+": GET "+p, before)
		}
		for _, p := range []string{"/api/firc/status", "/api/firc/install-status"} {
			body := f.do(http.MethodGet, p, "").Body.String()
			if !strings.Contains(body, `"foreign":{"package":"firc","webPort":666}`) || strings.Contains(body, `"running":true`) {
				t.Errorf("%s: GET %s = %s", engine, p, body)
			}
		}
	}
}

func TestFircForeign_LeavingEngineAllowed(t *testing.T) {
	for _, c := range [][3]string{
		{"POST", "/api/firc/engine/switch", `{"engine":"sing-box"}`},
		{"POST", "/api/firc/control", `{"action":"select","engine":"sing-box"}`},
		{"PUT", "/api/firc/settings", `{"routingEngine":"sing-box"}`},
		{"PUT", "/api/firc/settings", `{"selected":false}`},
	} {
		t.Run(c[1]+c[2], func(t *testing.T) {
			f := newForeignFircFixture(t, "firc")
			before := f.digest(t)
			rec := f.do(c[0], c[1], c[2])
			if rec.Code != http.StatusOK || f.engine() != "sing-box" {
				t.Fatalf("= %d %s, движок %s", rec.Code, rec.Body.String(), f.engine())
			}
			if !f.turnOff {
				t.Error("явный уход без метки WithTurnOff: без sing-box с движка не уйти")
			}
			f.assertUntouched(t, "уход с движка", before)
		})
	}
}

// Настройки роутера sing-box тоже умеют менять движок — отказ там тот же.
func TestFircForeign_SingboxRouterSwitchRefused(t *testing.T) {
	h := NewSingboxRouterHandler(nil, nil)
	rec := httptest.NewRecorder()
	h.handleErr(rec, "engine-switch", fmt.Errorf("switch: %w", firc.ErrForeignFirc))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_FOREIGN") {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.handleErr(rec, "engine-switch", errors.New("другое"))
	if strings.Contains(rec.Body.String(), "FIRC_FOREIGN") {
		t.Fatalf("посторонняя ошибка выдана за сторонний firc: %s", rec.Body.String())
	}
}

// Владельца firc не узнать: свой выбранный рантайм останавливается, остальное
// закрыто понятным текстом.
func TestFircForeignUnknown_StopOwnAllowedRestExplained(t *testing.T) {
	unknown := func(t *testing.T, engine string) *foreignFircFixture {
		f := newForeignFircFixture(t, engine)
		if err := os.Remove(filepath.Join(f.root, "opt/lib/opkg/info/firc.list")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.root, "opt/lib/opkg/info/broken.list"), 0o755); err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := unknown(t, "firc")
	for _, c := range [][3]string{
		{"PUT", "/api/firc/v1/groups", `{}`}, {"POST", "/api/firc/install", `{}`}, {"POST", "/api/firc/control", `{"action":"start"}`},
		{"POST", "/api/firc/engine/switch", `{"engine":"firc"}`},
	} {
		rec := f.do(c[0], c[1], c[2])
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "FIRC_OWNER_UNKNOWN") ||
			!strings.Contains(rec.Body.String(), "/opt/lib/opkg/info") {
			t.Errorf("%s %s = %d %s", c[0], c[1], rec.Code, rec.Body.String())
		}
	}
	ran := filepath.Join(f.root, "opt/etc/init.d/S99firc.ran")
	if rec := f.do("POST", "/api/firc/control", `{"action":"stop"}`); rec.Code != http.StatusOK {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body.String())
	}
	if raw, _ := os.ReadFile(ran); string(raw) != "stop\n" {
		t.Fatalf("init-скрипт: %q", raw)
	}
	// Движок не FIRC: работающий firc может быть чужим — не трогаем.
	f = unknown(t, "sing-box")
	if rec := f.do("POST", "/api/firc/control", `{"action":"stop"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("stop при sing-box = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(f.root, "opt/etc/init.d/S99firc.ran")); !os.IsNotExist(err) {
		t.Fatal("остановлен firc неизвестного владельца при движке sing-box")
	}
}
