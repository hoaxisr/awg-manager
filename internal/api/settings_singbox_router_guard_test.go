package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Блок singboxRouter через POST /settings/update не пишется вовсе (F684): у
// роутера свои ручки (/singbox/router/settings, /mode, /enable, /disable), а
// здесь частичный блок затёр бы пресеты обхода, пулы FakeIP и QoS, и чужой
// routingMode/enabled ушёл бы в персист мимо SwitchRoutingMode (см. шапку
// internal/singbox/router/fakeip_transition.go). Фронт этот блок не шлёт:
// ответа /settings/get он не содержит.
func TestUpdate_SingboxRouterBlockIgnored(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cp := *all
	cp.SingboxRouter.RoutingMode = "tproxy"
	cp.SingboxRouter.Enabled = true
	cp.SingboxRouter.BypassExtraPorts = "53 UDP"
	if err := store.Update(func(cur *storage.Settings) error { *cur = cp; return nil }); err != nil {
		t.Fatalf("seed Update: %v", err)
	}

	body := []byte(`{"singboxRouter":{"routingMode":"fakeip-tun","enabled":false,"bypassExtraPorts":"8080 TCP"}}`)
	req := httptest.NewRequest(http.MethodPost, "/settings/update", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}

	saved, err := store.Load()
	if err != nil {
		t.Fatalf("Load after update: %v", err)
	}
	if saved.SingboxRouter.RoutingMode != "tproxy" {
		t.Errorf("RoutingMode сменился через /settings/update: %q", saved.SingboxRouter.RoutingMode)
	}
	if !saved.SingboxRouter.Enabled {
		t.Error("Enabled сброшен через /settings/update")
	}
	if saved.SingboxRouter.BypassExtraPorts != "53 UDP" {
		t.Errorf("блок роутера записан через /settings/update: BypassExtraPorts = %q", saved.SingboxRouter.BypassExtraPorts)
	}
}
