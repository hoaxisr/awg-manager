package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

func TestComputeFircInterfaceProbeLatencyMs(t *testing.T) {
	t.Run("uses total minus dns instead of local tun connect", func(t *testing.T) {
		m := httpclient.Metrics{
			HTTPCode:       http.StatusNoContent,
			TimeNameLookup: 0.015,
			TimeConnect:    0.016, // local tun SYN-ACK (1ms after DNS)
			TimeTotal:      0.185, // actual end-to-end RTT through proxy
		}
		got := computeFircInterfaceProbeLatencyMs(m)
		if got != 170 {
			t.Fatalf("computeFircInterfaceProbeLatencyMs() = %d, want 170", got)
		}
	})

	t.Run("zero dns uses total duration", func(t *testing.T) {
		m := httpclient.Metrics{
			HTTPCode:    http.StatusOK,
			TimeConnect: 0.001,
			TimeTotal:   0.245,
		}
		got := computeFircInterfaceProbeLatencyMs(m)
		if got != 245 {
			t.Fatalf("computeFircInterfaceProbeLatencyMs() = %d, want 245", got)
		}
	})

	t.Run("floors fast success response to 1ms", func(t *testing.T) {
		m := httpclient.Metrics{
			HTTPCode:  http.StatusNoContent,
			TimeTotal: 0.0002,
		}
		got := computeFircInterfaceProbeLatencyMs(m)
		if got != 1 {
			t.Fatalf("computeFircInterfaceProbeLatencyMs() = %d, want 1", got)
		}
	})
}

func TestHandleProbeInterfaces_SuccessAndDedupe(t *testing.T) {
	tmpDir := t.TempDir()
	settingsStore := storage.NewSettingsStore(tmpDir)
	_, _ = settingsStore.Load()
	if err := settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		s.ConnectivityCheckURL = "https://probe.example.test/generate_204"
		return nil
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	op := firc.NewOperator(firc.Options{ConfigDir: tmpDir})
	h := NewFircHandler(op)
	h.SetSettingsStore(settingsStore)

	var mu sync.Mutex
	calls := map[string]int{}
	var seenURL string
	h.probeInterfaceFn = func(_ context.Context, ifaceName, checkURL string) fircInterfaceProbeResult {
		mu.Lock()
		calls[ifaceName]++
		seenURL = checkURL
		mu.Unlock()
		switch ifaceName {
		case "tunvless0":
			return fircInterfaceProbeResult{Interface: "tunvless0", Ok: true, LatencyMs: 118, HTTPCode: http.StatusNoContent}
		case "tunvless2":
			return fircInterfaceProbeResult{Interface: "tunvless2", Ok: false, LatencyMs: 0, Error: "request timed out"}
		case "eth3":
			return fircInterfaceProbeResult{Interface: "eth3", Ok: true, LatencyMs: 32, HTTPCode: http.StatusNoContent}
		default:
			return fircInterfaceProbeResult{Interface: ifaceName, Ok: false, Error: "unexpected"}
		}
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	body := `{"interfaces":["tunvless0"," tunvless2 ","tunvless0","eth3"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var env struct {
		Success bool                        `json:"success"`
		Data    fircInterfacesProbeResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !env.Success {
		t.Fatalf("expected success=true, got false")
	}
	if seenURL != "https://probe.example.test/generate_204" || env.Data.URL != seenURL {
		t.Fatalf("url = %q (seen=%q), want settings URL", env.Data.URL, seenURL)
	}
	if env.Data.CheckedAt <= 0 {
		t.Fatalf("expected positive checkedAt, got %d", env.Data.CheckedAt)
	}
	if len(calls) != 3 || calls["tunvless0"] != 1 || calls["tunvless2"] != 1 || calls["eth3"] != 1 {
		t.Fatalf("unexpected probe calls map: %+v", calls)
	}
	if len(env.Data.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(env.Data.Results))
	}
	if r := env.Data.Results["tunvless0"]; !r.Ok || r.LatencyMs != 118 || r.HTTPCode != http.StatusNoContent {
		t.Fatalf("unexpected tunvless0 result: %+v", r)
	}
	if r := env.Data.Results["tunvless2"]; r.Ok || r.LatencyMs != 0 || r.Error != "request timed out" {
		t.Fatalf("unexpected tunvless2 result: %+v", r)
	}
	if r := env.Data.Results["eth3"]; !r.Ok || r.LatencyMs != 32 {
		t.Fatalf("unexpected eth3 result: %+v", r)
	}
}

func TestHandleProbeInterfaces_BoundedConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	op := firc.NewOperator(firc.Options{ConfigDir: tmpDir})
	h := NewFircHandler(op)

	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	h.probeInterfaceFn = func(_ context.Context, ifaceName, _ string) fircInterfaceProbeResult {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		defer inFlight.Add(-1)
		return fircInterfaceProbeResult{Interface: ifaceName, Ok: true, LatencyMs: 42, HTTPCode: http.StatusNoContent}
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	ifaces := make([]string, 8)
	for i := range ifaces {
		ifaces[i] = fmt.Sprintf("tunvless%d", i)
	}
	payload, _ := json.Marshal(fircInterfacesProbeRequest{
		Interfaces: ifaces,
		URL:        "https://cp.cloudflare.com/generate_204",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := maxInFlight.Load(); got > 4 {
		t.Fatalf("max concurrent probes = %d, want <= 4", got)
	}
}

func TestHandleProbeInterfaces_Validation(t *testing.T) {
	tmpDir := t.TempDir()
	settingsStore := storage.NewSettingsStore(tmpDir)
	_, _ = settingsStore.Load()
	_ = settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	})

	op := firc.NewOperator(firc.Options{ConfigDir: tmpDir})
	h := NewFircHandler(op)
	h.SetSettingsStore(settingsStore)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	cases := []struct {
		name string
		body string
	}{
		{name: "empty list", body: `{"interfaces":[]}`},
		{name: "whitespace only", body: `{"interfaces":["   "]}`},
		{name: "blackhole", body: `{"interfaces":["blackhole"]}`},
		{name: "block", body: `{"interfaces":["block"]}`},
		{name: "invalid chars", body: `{"interfaces":["tun0;id"]}`},
		{name: "invalid url scheme", body: `{"interfaces":["tunvless0"],"url":"ftp://example.com/file"}`},
		{name: "url with credentials", body: `{"interfaces":["tunvless0"],"url":"https://user:pass@example.com/204"}`},
		{name: "unknown field", body: `{"interfaces":["tunvless0"],"extra":true}`},
		{name: "trailing json", body: `{"interfaces":["tunvless0"]}{}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want 400 (body=%s)", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	t.Run("more than 16 interfaces", func(t *testing.T) {
		ifaces := make([]string, 17)
		for i := range ifaces {
			ifaces[i] = fmt.Sprintf("tunvless%d", i)
		}
		payload, _ := json.Marshal(fircInterfacesProbeRequest{Interfaces: ifaces})
		req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", bytes.NewReader(payload))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("unselected engine returns 409", func(t *testing.T) {
		_ = settingsStore.Update(func(s *storage.Settings) error {
			s.SingboxRouter.RoutingEngine = "sing-box"
			return nil
		})
		req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", strings.NewReader(`{"interfaces":["tunvless0"]}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("nil operator returns 503", func(t *testing.T) {
		hNil := NewFircHandler(nil)
		muxNil := http.NewServeMux()
		hNil.RegisterRoutes(muxNil, func(next http.HandlerFunc) http.HandlerFunc { return next })
		req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", strings.NewReader(`{"interfaces":["tunvless0"]}`))
		rec := httptest.NewRecorder()
		muxNil.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
}

func TestHandleProbeInterfaces_PrefersRunningFircAppCheckURLOverDefaultSettings(t *testing.T) {
	tmpDir := t.TempDir()
	settingsStore := storage.NewSettingsStore(tmpDir)
	_, _ = settingsStore.Load()
	if err := settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		s.ConnectivityCheckURL = storage.DefaultConnectivityCheckURL
		return nil
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	fircMux := http.NewServeMux()
	fircMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"dirty": false})
	})
	fircMux.HandleFunc("/api/v1/system/settings", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"boot":           "ok",
			"restarting":     false,
			"pendingRestart": []string{},
			"settings": map[string]any{
				"app.check.url": "http://cp.cloudflare.com/generate_204",
			},
			"classes": map[string]string{},
		})
	})
	mockFirc := httptest.NewServer(fircMux)
	defer mockFirc.Close()

	op := firc.NewOperator(firc.Options{ConfigDir: tmpDir})
	op.SetClient(firc.NewHTTPClient(mockFirc.URL, mockFirc.Client()))
	h := NewFircHandler(op)
	h.SetSettingsStore(settingsStore)

	var seenURL string
	h.probeInterfaceFn = func(_ context.Context, ifaceName, checkURL string) fircInterfaceProbeResult {
		seenURL = checkURL
		return fircInterfaceProbeResult{Interface: ifaceName, Ok: true, LatencyMs: 45, HTTPCode: http.StatusNoContent}
	}

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	// 1. Default ConnectivityCheckURL -> uses running fircd app.check.url.
	req := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", strings.NewReader(`{"interfaces":["qmi_br0"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seenURL != "http://cp.cloudflare.com/generate_204" {
		t.Fatalf("status=%d seenURL=%q, want http://cp.cloudflare.com/generate_204", rec.Code, seenURL)
	}

	// 2. Explicitly customized ConnectivityCheckURL -> honors settings over app.check.url.
	if err := settingsStore.Update(func(s *storage.Settings) error {
		s.ConnectivityCheckURL = "https://custom.probe.test/204"
		return nil
	}); err != nil {
		t.Fatalf("update custom settings URL: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/firc/interfaces/probe", strings.NewReader(`{"interfaces":["qmi_br0"]}`))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || seenURL != "https://custom.probe.test/204" {
		t.Fatalf("custom settings URL: status=%d seenURL=%q, want https://custom.probe.test/204", rec2.Code, seenURL)
	}
}
