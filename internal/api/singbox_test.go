package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	h := NewSingboxHandler(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/tunnels/delay-check?tag=A", nil)
	w := httptest.NewRecorder()
	h.DelayCheck(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestSingboxHandler_DelayCheck_MissingTag(t *testing.T) {
	h := NewSingboxHandler(nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/tunnels/delay-check", nil)
	w := httptest.NewRecorder()
	h.DelayCheck(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
