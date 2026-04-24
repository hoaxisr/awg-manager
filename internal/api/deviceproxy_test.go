package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
)

// newTestDeviceProxyHandler builds a DeviceProxyHandler backed by an
// in-memory store in a temp dir. No real sing-box or NDMS needed.
func newTestDeviceProxyHandler(t *testing.T) *DeviceProxyHandler {
	t.Helper()
	store := deviceproxy.NewStore(filepath.Join(t.TempDir(), "d.json"))
	svc := deviceproxy.NewService(deviceproxy.Deps{Store: store})
	// nil appLogger is safe — NewScopedLogger handles nil gracefully.
	return NewDeviceProxyHandler(svc, nil)
}

func TestDeviceProxyHandler_GetConfig_Default(t *testing.T) {
	h := newTestDeviceProxyHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/config", nil)
	rr := httptest.NewRecorder()
	h.GetConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rr.Code, rr.Body)
	}
}

func TestDeviceProxyHandler_GetConfig_MethodNotAllowed(t *testing.T) {
	h := newTestDeviceProxyHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/proxy/config", nil)
	rr := httptest.NewRecorder()
	h.GetConfig(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestDeviceProxyHandler_SaveConfig_MethodNotAllowed(t *testing.T) {
	h := newTestDeviceProxyHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/proxy/config", nil)
	rr := httptest.NewRecorder()
	h.SaveConfig(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestDeviceProxyHandler_SelectOutbound_MethodNotAllowed(t *testing.T) {
	h := newTestDeviceProxyHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/select", nil)
	rr := httptest.NewRecorder()
	h.SelectOutbound(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestDeviceProxyHandler_ListOutbounds_Default(t *testing.T) {
	h := newTestDeviceProxyHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/outbounds", nil)
	rr := httptest.NewRecorder()
	h.ListOutbounds(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rr.Code, rr.Body)
	}
}

func TestDeviceProxyHandler_ListOutbounds_MethodNotAllowed(t *testing.T) {
	h := newTestDeviceProxyHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/proxy/outbounds", nil)
	rr := httptest.NewRecorder()
	h.ListOutbounds(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}
