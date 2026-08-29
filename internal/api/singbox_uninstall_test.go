package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func singboxHandlerWithRouter(t *testing.T, mode string, enabled bool) *SingboxHandler {
	t.Helper()
	store := storage.NewSettingsStore(t.TempDir())
	st, err := store.Load()
	if err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	st.SingboxRouter.RoutingMode = mode
	st.SingboxRouter.Enabled = enabled
	if err := store.Update(func(cur *storage.Settings) error { *cur = *st; return nil }); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	h := NewSingboxHandler(nil, nil, nil, nil)
	h.SetSettingsStore(store)
	return h
}

func TestSingboxHandler_Uninstall_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	w := httptest.NewRecorder()
	h.Uninstall(w, httptest.NewRequest(http.MethodGet, "/api/singbox/uninstall", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("код = %d, ожидался 405", w.Code)
	}
}

// Пока маршрутизация включена, удалять движок нельзя: правила iptables,
// OpkgTun и ACL остались бы висеть без процесса, который их обслуживает.
func TestSingboxHandler_Uninstall_RefusesWhileRoutingEnabled(t *testing.T) {
	for _, mode := range []string{"tproxy", "fakeip-tun", "policy-tun"} {
		t.Run(mode, func(t *testing.T) {
			h := singboxHandlerWithRouter(t, mode, true)
			w := httptest.NewRecorder()
			h.Uninstall(w, httptest.NewRequest(http.MethodPost, "/api/singbox/uninstall", nil))
			if w.Code != http.StatusConflict {
				t.Fatalf("код = %d, ожидался 409", w.Code)
			}
			if !strings.Contains(w.Body.String(), "маршрутизаци") {
				t.Errorf("ответ не объясняет причину: %s", w.Body.String())
			}
		})
	}
}

// Выключенная маршрутизация удалению не мешает: гейт пропускает вызов
// дальше (оператор в этом тесте не подключён, поэтому ждём 500, а не 409).
func TestSingboxHandler_Uninstall_PassesGateWhenRoutingOff(t *testing.T) {
	h := singboxHandlerWithRouter(t, "fakeip-tun", false)
	w := httptest.NewRecorder()
	h.Uninstall(w, httptest.NewRequest(http.MethodPost, "/api/singbox/uninstall", nil))
	if w.Code == http.StatusConflict {
		t.Errorf("гейт сработал при выключенной маршрутизации: %s", w.Body.String())
	}
}
