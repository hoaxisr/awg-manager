package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
)

func TestIngressDispatcherHandler_ReportsLiveRunningState(t *testing.T) {
	disp := cdndispatcher.New(cdndispatcher.Config{
		ListenAddr:    "127.0.0.1:0",
		DefaultAction: "404",
	})
	coord := serveringress.New(t.TempDir(), nil, disp, nil)
	h := NewIngressDispatcherHandler(coord, disp, nil)

	type envelope struct {
		Success bool `json:"success"`
		Data    struct {
			Running       bool   `json:"running"`
			DefaultAction string `json:"default_action"`
		} `json:"data"`
	}

	decodeResp := func(rec *httptest.ResponseRecorder) struct {
		Running       bool   `json:"running"`
		DefaultAction string `json:"default_action"`
	} {
		t.Helper()
		var out envelope
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
		}
		if !out.Success {
			t.Fatalf("expected success=true envelope, got false")
		}
		return out.Data
	}

	// 1. Before Start(): running must be false.
	req := httptest.NewRequest(http.MethodGet, "/api/servers/ingress/dispatcher", nil)
	rec := httptest.NewRecorder()
	h.GetDispatcherConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET before start status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeResp(rec); got.Running {
		t.Fatalf("expected running=false before Start(), got true")
	}

	// 2. After Start(): both GET and PUT must report running=true.
	if err := disp.Start(); err != nil {
		t.Fatalf("disp.Start(): %v", err)
	}
	t.Cleanup(func() { _ = disp.Stop() })

	req = httptest.NewRequest(http.MethodGet, "/api/servers/ingress/dispatcher", nil)
	rec = httptest.NewRecorder()
	h.GetDispatcherConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after start status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeResp(rec); !got.Running {
		t.Fatalf("expected running=true after Start(), got false")
	}

	body, _ := json.Marshal(cdndispatcher.Config{
		ListenAddr:    "127.0.0.1:0",
		DefaultAction: "reject",
	})
	req = httptest.NewRequest(http.MethodPut, "/api/servers/ingress/dispatcher", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	h.UpdateDispatcherConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT after start status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeResp(rec); !got.Running || got.DefaultAction != "reject" {
		t.Fatalf("unexpected PUT response: %+v", got)
	}

	// 3. After Stop(): running must return false.
	if err := disp.Stop(); err != nil {
		t.Fatalf("disp.Stop(): %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/servers/ingress/dispatcher", nil)
	rec = httptest.NewRecorder()
	h.GetDispatcherConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after stop status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := decodeResp(rec); got.Running {
		t.Fatalf("expected running=false after Stop(), got true")
	}
}
