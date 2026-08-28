package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Поверхность старого движка снята целиком: её место занял /api/proxyrt/*.
// Оставленная регистрация — это не «лишняя ручка», а живой вход в пакеты
// internal/wdtt и internal/freeturn, которые сносит следующая задача: фронт на
// неё уже не ходит, а конфиги старого мира после посева — производные, и
// запись через них разошлась бы с proxy-instances.json.
func TestLegacyProxyRoutesAreGone(t *testing.T) {
	mux := http.NewServeMux()
	s := &Server{}
	h := &routeHandlers{guarded: func(f http.HandlerFunc) http.HandlerFunc { return f }}

	s.registerSettingsRoutes(mux, h)

	gone := []string{
		"/api/freeturn/config",
		"/api/freeturn/client/config",
		"/api/freeturn/server/config",
		"/api/freeturn/status",
		"/api/freeturn/captcha/status",
		"/api/freeturn/client/start",
		"/api/freeturn/client/stop",
		"/api/freeturn/server/start",
		"/api/freeturn/server/stop",
		"/api/freeturn/server/link",
		"/api/freeturn/link/decode",
		"/api/freeturn/install",
		"/api/freeturn/clients",
		"/api/freeturn/clients/default",
		"/api/freeturn/servers",
		"/api/freeturn/servers/default",
		"/api/wdtt/config",
		"/api/wdtt/client/config",
		"/api/wdtt/status",
		"/api/wdtt/client/start",
		"/api/wdtt/client/stop",
		"/api/wdtt/link/decode",
		"/api/wdtt/link/import",
		"/api/wdtt/install",
		"/api/wdtt/server/config",
		"/api/wdtt/server/start",
		"/api/wdtt/server/stop",
		"/api/wdtt/clients",
		"/api/wdtt/clients/default",
		"/api/wdtt/clients/default/ensure-raw-tunnel",
		"/api/wdtt/servers",
		"/api/wdtt/servers/default",
	}
	for _, path := range gone {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		hh, pattern := mux.Handler(req)
		if pattern != "" {
			t.Errorf("%s всё ещё зарегистрирован по шаблону %q", path, pattern)
			continue
		}
		rec := httptest.NewRecorder()
		hh.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s отвечает %d, ожидали 404", path, rec.Code)
		}
	}

	// Ручки поиска и снятия процесса на локальном порту сохранены: они
	// пересажены на хранилище инстансов, а не сняты вместе со старым движком.
	for _, path := range []string{"/api/proxy/listener", "/api/proxy/kill-listener"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s не зарегистрирован", path)
		}
	}
}

// Новая поверхность регистрируется целиком — иначе снятие старой оставило бы
// фронт без входа вовсе.
func TestProxyRtSurfaceRegisteredWhole(t *testing.T) {
	mux := http.NewServeMux()
	stub := func(http.ResponseWriter, *http.Request) {}
	s := &Server{proxyRt: ProxyRtSurface{
		Instances:          stub,
		WdttLinkDecode:     stub,
		WdttLinkImport:     stub,
		FreeTurnLinkDecode: stub,
		CaptchaStatus:      stub,
		InstallStatus:      stub,
		Install:            stub,
	}}
	h := &routeHandlers{guarded: func(f http.HandlerFunc) http.HandlerFunc { return f }}

	s.registerProxyRtRoutes(mux, h)

	for _, path := range []string{
		"/api/proxyrt/instances",
		"/api/proxyrt/instances/wdtt-client:nl/users",
		"/api/proxyrt/wdtt/link/decode",
		"/api/proxyrt/wdtt/link/import",
		"/api/proxyrt/freeturn/link/decode",
		"/api/proxyrt/freeturn/captcha/status",
		"/api/proxyrt/install/status",
		"/api/proxyrt/install",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s не зарегистрирован", path)
		}
	}
}
