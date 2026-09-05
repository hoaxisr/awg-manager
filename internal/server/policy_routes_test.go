package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Политики доступа существуют и на KeeneticOS 4.x: команда `ip policy` есть с
// прошивки 2.12, `ip policy standalone` с 4.02, а `/rci/show/rc/ip/policy` на
// живом 4.x роутере отдаёт конфиг политик. Регистрация ручек по версии
// прошивки прятала вкладку целиком, поэтому маршруты не должны зависеть от
// версии вовсе.
//
// Отдельно это защищает от второй ловушки: osdetect.Get() отдаёт Version4x,
// пока не заполнен ndmsinfo, а маршруты регистрируются один раз при старте.
// Медленный NDMS на загрузке молча выключал бы политики и на 5.x до
// перезапуска демона. В тесте ndmsinfo пуст, то есть проверка идёт ровно в
// том состоянии, в котором ручки пропадали.
//
// Гард здесь подставлен identity: тест про регистрацию секции, а не про
// авторизацию. Гард проверяет route_guard_test.go.
func TestPolicyRoutesRegisteredRegardlessOfFirmware(t *testing.T) {
	mux := http.NewServeMux()
	s := &Server{}
	h := &routeHandlers{guarded: func(f http.HandlerFunc) http.HandlerFunc { return f }}

	s.registerPolicyRoutes(mux, h)

	paths := []string{
		"/api/access-policies",
		"/api/access-policies/create",
		"/api/access-policies/delete",
		"/api/access-policies/description",
		"/api/access-policies/standalone",
		"/api/access-policies/permit",
		"/api/access-policies/assign",
		"/api/access-policies/interfaces",
		"/api/access-policies/interface-up",
		"/api/access-policies/devices",
		"/api/routing/access-policies",
		"/api/routing/policy-interfaces",
		"/api/routing/policy-devices",
	}

	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("%s не зарегистрирован", path)
		}
	}
}
