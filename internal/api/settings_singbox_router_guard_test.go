package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Вторая дверь к смене режима: SettingsPatch.SingboxRouter копируется
// ApplyPatch целиком, поэтому POST /settings/update писал бы чужой
// routingMode/enabled прямо в персист — мимо SwitchRoutingMode, без
// нормализации и без transitionMu. Reconcile отсюда не зовётся, но тик
// планировщика (30 с) подхватил бы новый режим и оставил ресурсы прежнего
// жить (см. шапку internal/singbox/router/fakeip_transition.go).
//
// Прочие поля блока обязаны продолжать доезжать до персиста — этот путь
// используется round-trip'ом страницы настроек.
func TestUpdate_SingboxRouterModeAndEnabledIgnored(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	all, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cp := *all
	cp.SingboxRouter.RoutingMode = "tproxy"
	cp.SingboxRouter.Enabled = true
	if err := store.Save(&cp); err != nil {
		t.Fatalf("seed Save: %v", err)
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
	if saved.SingboxRouter.BypassExtraPorts != "8080 TCP" {
		t.Errorf("прочие поля блока не сохранились: BypassExtraPorts = %q", saved.SingboxRouter.BypassExtraPorts)
	}
}
