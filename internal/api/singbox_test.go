package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox"
)

func TestSingboxHandler_StatusSmoke(t *testing.T) {
	// NewSingboxHandler requires a real *singbox.Operator; we can't easily build one in this unit test
	// without a full NDMS mock. Skip for now — operator behaviour is covered by singbox package tests.
	// This file exists so future CRUD tests have a place to land.
	t.Skip("operator-dependent tests live in singbox package; HTTP surface covered in Task 16+")
}

func TestSingboxHandler_MethodNotAllowed_ListTunnels(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodDelete, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.ListTunnels(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_AddTunnels(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.AddTunnels(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_GetTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels?tag=foo", nil)
	w := httptest.NewRecorder()
	h.GetTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_UpdateTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels?tag=foo", nil)
	w := httptest.NewRecorder()
	h.UpdateTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MethodNotAllowed_DeleteTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels?tag=foo", nil)
	w := httptest.NewRecorder()
	h.DeleteTunnel(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_MissingTag_GetTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.GetTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_MissingTag_UpdateTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodPut, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.UpdateTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_MissingTag_DeleteTunnel(t *testing.T) {
	h := &SingboxHandler{op: nil}
	req := httptest.NewRequest(http.MethodDelete, "/api/singbox/tunnels", nil)
	w := httptest.NewRecorder()
	h.DeleteTunnel(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_DelayCheck_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/delay-check?tag=A", nil)
	w := httptest.NewRecorder()
	h.DelayCheck(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_DelayCheck_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/delay-check", nil)
	w := httptest.NewRecorder()
	h.DelayCheck(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/test/connectivity?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/connectivity", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_OperatorNotWired(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/connectivity?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_MethodNotAllowed(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/test/ip?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/ip", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckIP_OperatorNotWired(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/ip?tag=A", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.Code)
	}
}

func TestSingboxHandler_CheckConnectivity_IfaceOverride_DoesNotRequireOperator(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/connectivity?iface=t2s1", nil)
	w := httptest.NewRecorder()
	h.CheckConnectivity(w, req)

	body := w.Body.String()
	if strings.Contains(body, "operator not wired") {
		t.Fatalf("iface override should bypass operator lookup, got body: %s", body)
	}
	if w.Code == http.StatusInternalServerError {
		t.Fatalf("expected non-500 for iface override connectivity path, got %d body=%s", w.Code, body)
	}
}

func TestSingboxHandler_CheckIP_IfaceOverride_DoesNotRequireOperator(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/test/ip?iface=t2s1", nil)
	w := httptest.NewRecorder()
	h.CheckIP(w, req)

	body := w.Body.String()
	if strings.Contains(body, "operator not wired") {
		t.Fatalf("iface override should bypass operator lookup, got body: %s", body)
	}
	if w.Code == http.StatusInternalServerError {
		t.Fatalf("expected non-500 for iface override IP path, got %d body=%s", w.Code, body)
	}
}

func TestResolveTunnelInterfaceFromList_Found(t *testing.T) {
	iface, err := resolveTunnelInterfaceFromList([]singbox.TunnelInfo{
		{Tag: "A", KernelInterface: "t2s1"},
		{Tag: "B", KernelInterface: "t2s2"},
	}, "B")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if iface != "t2s2" {
		t.Fatalf("expected t2s2, got %q", iface)
	}
}

func TestResolveTunnelInterfaceFromList_NotFound(t *testing.T) {
	_, err := resolveTunnelInterfaceFromList([]singbox.TunnelInfo{
		{Tag: "A", KernelInterface: "t2s1"},
	}, "missing")
	if !errors.Is(err, singbox.ErrTunnelNotFound) {
		t.Fatalf("expected ErrTunnelNotFound, got %v", err)
	}
}

func TestResolveTunnelInterfaceFromList_NoInterface(t *testing.T) {
	_, err := resolveTunnelInterfaceFromList([]singbox.TunnelInfo{
		{Tag: "A", KernelInterface: ""},
	}, "A")
	if !errors.Is(err, errTunnelNoInterface) {
		t.Fatalf("expected errTunnelNoInterface, got %v", err)
	}
}
