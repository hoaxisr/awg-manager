package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

type clashTargetEngine struct {
	fakeMihomoEngine
	addr   string
	secret string
}

func (c *clashTargetEngine) ControllerTarget() (string, string) {
	return c.addr, c.secret
}

func TestMihomoClashProxy_DegradedMatrix(t *testing.T) {
	var upstreamRequests int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamRequests, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"ok"}`))
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	engine := &clashTargetEngine{
		addr:   u.Host,
		secret: "secret-bearer-123",
	}

	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(engine)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	mutatingMethods := []string{
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	for _, method := range mutatingMethods {
		t.Run("Mutating_"+method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/mihomo/clash/configs", strings.NewReader(`{"port": 7890}`))
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503 Service Unavailable for %s, got: %d", method, rec.Code)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if resp["code"] != "RECOVERY_REQUIRED" {
				t.Fatalf("expected code RECOVERY_REQUIRED, got: %#v", resp)
			}
		})
	}

	// Assert zero mutating requests reached the upstream server while degraded!
	if atomic.LoadInt32(&upstreamRequests) != 0 {
		t.Fatalf("mutating requests reached upstream controller while degraded! Count: %d", upstreamRequests)
	}

	// Non-mutating observation methods (GET, HEAD) are permitted while degraded
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run("Observation_"+method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/mihomo/clash/version", nil)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 OK for observation method %s while degraded, got: %d", method, rec.Code)
			}
		})
	}

	if atomic.LoadInt32(&upstreamRequests) != 2 {
		t.Fatalf("expected 2 observation requests to reach upstream, got: %d", upstreamRequests)
	}
}

func TestMihomoClashProxy_AuthenticatedForwarding(t *testing.T) {
	var capturedAuth string
	var capturedPath string
	var capturedMethod string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedPath = r.URL.RequestURI()
		capturedMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"1.18.0"}`))
	}))
	defer upstream.Close()

	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	engine := &clashTargetEngine{
		addr:   u.Host,
		secret: "operator-configured-secret",
	}

	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(engine)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: false}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	// Send request with an attacker/client-supplied Authorization header
	req := httptest.NewRequest(http.MethodPut, "/api/mihomo/clash/configs?force=true", strings.NewReader(`{"port":7890}`))
	req.Header.Set("Authorization", "Bearer attacker-fake-token")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got: %d (%s)", rec.Code, rec.Body.String())
	}

	if capturedMethod != http.MethodPut {
		t.Fatalf("expected method PUT, got: %s", capturedMethod)
	}
	if capturedPath != "/configs?force=true" {
		t.Fatalf("expected upstream path /configs?force=true, got: %s", capturedPath)
	}

	// Assert client-supplied token was stripped and replaced by operator secret!
	expectedAuth := "Bearer operator-configured-secret"
	if capturedAuth != expectedAuth {
		t.Fatalf("expected Authorization %q, got: %q", expectedAuth, capturedAuth)
	}
}
