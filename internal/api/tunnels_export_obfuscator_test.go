package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExport_AppendsInstanceSection(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedObfTunnel(t, store)
	rec := httptest.NewRecorder()
	h.Export(rec, httptest.NewRequest(http.MethodGet, "/tunnels/export?id=awg20", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "Endpoint = 127.0.0.1:39000") || !strings.Contains(body, "[instance]") || !strings.Contains(body, "target = 1.2.3.4:51824") {
		t.Fatalf("export:\n%s", body)
	}
}

func TestUpdate_RejectsInvalidObfuscator(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})
	seedObfTunnel(t, store)
	rec := httptest.NewRecorder()
	h.Update(rec, httptest.NewRequest(http.MethodPost, "/tunnels/update?id=awg20",
		strings.NewReader(`{"obfuscator":{"target":"1.2.3.4:1","key":"k","masking":"TLS"}}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_OBFUSCATOR") {
		t.Fatalf("expected 400 INVALID_OBFUSCATOR: %d %s", rec.Code, rec.Body.String())
	}
	if saved, _ := store.Get("awg20"); saved.Obfuscator.Masking != "STUN" {
		t.Fatal("invalid update must not persist")
	}
}
