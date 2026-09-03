package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestToggleLock_Lifecycle(t *testing.T) {
	h, store := newTunnelsUpdateHarness(t, &stubTunnelSvc{})

	tunnel := &storage.AWGTunnel{
		ID:           "awg1",
		Name:         "test-tunnel",
		Enabled:      true,
		ToggleLocked: false,
	}
	if err := store.Create(tunnel); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 1. Lock
	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/toggle-lock?id=awg1", nil)
	rec := httptest.NewRecorder()
	h.ToggleLock(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if locked, ok := resp["toggleLocked"].(bool); !ok || !locked {
		t.Fatalf("expected toggleLocked=true, got %v", resp["toggleLocked"])
	}

	stored, err := store.Get("awg1")
	if err != nil || stored == nil || !stored.ToggleLocked {
		t.Fatalf("expected ToggleLocked=true in store, got %+v", stored)
	}

	// 2. Unlock
	req = httptest.NewRequest(http.MethodPost, "/api/tunnels/toggle-lock?id=awg1", nil)
	rec = httptest.NewRecorder()
	h.ToggleLock(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if locked, ok := resp["toggleLocked"].(bool); !ok || locked {
		t.Fatalf("expected toggleLocked=false, got %v", resp["toggleLocked"])
	}

	stored, err = store.Get("awg1")
	if err != nil || stored == nil || stored.ToggleLocked {
		t.Fatalf("expected ToggleLocked=false in store, got %+v", stored)
	}
}

func TestToggleLock_NotFound(t *testing.T) {
	h, _ := newTunnelsUpdateHarness(t, &stubTunnelSvc{})

	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/toggle-lock?id=awg999", nil)
	rec := httptest.NewRecorder()
	h.ToggleLock(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing, got %d: %s", rec.Code, rec.Body.String())
	}
}
