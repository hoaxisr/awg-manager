package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Неверные поля DNS-сервера — ошибка ввода: 400 DNS_SERVER_INVALID в обоих
// слотах, а не 500 INTERNAL_ERROR (F671).
func TestAddDNSServer_InvalidFieldsAre400(t *testing.T) {
	router, _ := newTestRouterHandlerReal(t)
	fakeip := newTestFakeIPConfigHandler(t)
	for name, add := range map[string]http.HandlerFunc{
		"router": router.AddDNSServer,
		"fakeip": fakeip.AddDNSServer,
	} {
		for _, body := range []string{
			`{"tag":"x","type":"nope","server":"1.1.1.1"}`,
			`{"tag":"x","type":"udp","server":"1.1.1.1","server_port":70000}`,
		} {
			rr := httptest.NewRecorder()
			add(rr, httptest.NewRequest(http.MethodPost, "/dns/servers/add", strings.NewReader(body)))
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "DNS_SERVER_INVALID") {
				t.Errorf("%s %s: status %d body %s", name, body, rr.Code, rr.Body.String())
			}
		}
	}
}
