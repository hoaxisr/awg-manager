package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler_ReturnsOK(t *testing.T) {
	h := NewHealthHandler("test-version")
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Success {
		t.Errorf("success = false, want true")
	}
	if !body.Data.OK {
		t.Errorf("ok = false, want true")
	}
	if body.Data.Version != "test-version" {
		t.Errorf("version = %q, want test-version", body.Data.Version)
	}
}

func TestHealthHandler_NonGETRejected(t *testing.T) {
	h := NewHealthHandler("x")
	req := httptest.NewRequest(http.MethodPost, "/api/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: status = %d, want 405", w.Code)
	}
}
