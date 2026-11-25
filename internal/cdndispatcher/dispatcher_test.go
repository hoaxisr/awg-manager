package cdndispatcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

func TestDispatcherRouting(t *testing.T) {
	// Mock Xray server
	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "xray")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("xray-response"))
	}))
	defer xrayServer.Close()

	// Mock Telegram server
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "tg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tg-response"))
	}))
	defer tgServer.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayServer.URL,
		TgTarget:       tgServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		PublicHostname: "custom.domain.test",
	})

	tests := []struct {
		name         string
		path         string
		remoteAddr   string
		expectedCode int
		expectedBack string // "xray", "tg", or ""
	}{
		{
			name:         "Health check from loopback",
			path:         "/.cdndisp/health",
			remoteAddr:   "127.0.0.1:45678",
			expectedCode: http.StatusOK,
			expectedBack: "",
		},
		{
			name:         "Health check from IPv6 loopback",
			path:         "/.cdndisp/health",
			remoteAddr:   "[::1]:45678",
			expectedCode: http.StatusOK,
			expectedBack: "",
		},
		{
			name:         "Health check from external IP rejected with 404",
			path:         "/.cdndisp/health",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Exact match on Xray prefix",
			path:         "/cdn-bridge",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "Subpath on Xray prefix",
			path:         "/cdn-bridge/vless-client-1",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "Prefix boundary collision does not leak to Xray or TG",
			path:         "/cdn-bridge-foreign",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Plural boundary collision does not leak to Xray or TG",
			path:         "/cdn-bridges",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Root path routes to TG",
			path:         "/",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "Query string on root routes to TG",
			path:         "/?bridge=secret123",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "API routes route to TG",
			path:         "/api/v1/status",
			remoteAddr:   "198.51.100.1:45678",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = "custom.domain.test"
			req.RemoteAddr = tc.remoteAddr
			w := httptest.NewRecorder()

			d.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedCode {
				t.Fatalf("expected status %d, got %d for path %s", tc.expectedCode, resp.StatusCode, tc.path)
			}

			if tc.path == "/.cdndisp/health" && tc.expectedCode == http.StatusOK {
				var status map[string]string
				if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
					t.Fatalf("failed to decode health response: %v", err)
				}
				if status["status"] != "ok" {
					t.Fatalf("expected status=ok, got %v", status)
				}
				return
			}

			if tc.expectedBack != "" {
				back := resp.Header.Get("X-Backend")
				if back != tc.expectedBack {
					t.Fatalf("expected backend %q, got %q", tc.expectedBack, back)
				}
			}
		})
	}
}

func TestDispatcherSynchronousStartAndConflict(t *testing.T) {
	addr := testutil.FreeAddr(t)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("failed to occupy %s: %v", addr, err)
	}

	// Try to start dispatcher on the same occupied port
	d := New(Config{
		ListenAddr: addr,
	})

	err = d.Start()
	if err == nil {
		d.Stop()
		ln.Close()
		t.Fatalf("expected bind conflict error, got nil")
	}

	if d.IsRunning() {
		t.Fatalf("dispatcher should NOT be running after bind failure")
	}

	// Close the port holder
	ln.Close()

	// Now starting should succeed
	err = d.Start()
	if err != nil {
		t.Fatalf("expected Start() to succeed, got %v", err)
	}
	if !d.IsRunning() {
		t.Fatalf("expected dispatcher to be running")
	}

	// Stop dispatcher
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}
	if d.IsRunning() {
		t.Fatalf("expected dispatcher to not be running after Stop()")
	}
}

func TestDispatcherReconfigure(t *testing.T) {
	xray1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Xray", "v1")
		w.WriteHeader(http.StatusOK)
	}))
	defer xray1.Close()

	xray2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Xray", "v2")
		w.WriteHeader(http.StatusOK)
	}))
	defer xray2.Close()

	// Find free ports
	addr1 := testutil.FreeAddr(t)

	d := New(Config{
		ListenAddr:     addr1,
		XrayTarget:     xray1.URL,
		XrayPathPrefix: "/cdn-bridge",
	})

	if err := d.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer d.Stop()

	// Check xray1 is served
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/cdn-bridge", addr1))
	if err != nil {
		t.Fatalf("request 1 failed: %v", err)
	}
	if resp.Header.Get("X-Xray") != "v1" {
		t.Fatalf("expected X-Xray: v1, got %s", resp.Header.Get("X-Xray"))
	}
	resp.Body.Close()

	// Phase 1: Reconfigure target without changing address
	err = d.Reconfigure(Config{
		XrayTarget: xray2.URL,
	})
	if err != nil {
		t.Fatalf("Reconfigure target failed: %v", err)
	}

	resp, err = client.Get(fmt.Sprintf("http://%s/cdn-bridge", addr1))
	if err != nil {
		t.Fatalf("request after target reconfigure failed: %v", err)
	}
	if resp.Header.Get("X-Xray") != "v2" {
		t.Fatalf("expected X-Xray: v2, got %s", resp.Header.Get("X-Xray"))
	}
	resp.Body.Close()

	// Phase 2: Reconfigure address
	addr2 := testutil.FreeAddr(t)

	err = d.Reconfigure(Config{
		ListenAddr: addr2,
	})
	if err != nil {
		t.Fatalf("Reconfigure address failed: %v", err)
	}

	// Traffic should now succeed on addr2
	var resp2 *http.Response
	for i := 0; i < 20; i++ {
		resp2, err = client.Get(fmt.Sprintf("http://%s/cdn-bridge", addr2))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request on new addr2 failed: %v", err)
	}
	if resp2.Header.Get("X-Xray") != "v2" {
		t.Fatalf("expected X-Xray: v2 on new addr, got %s", resp2.Header.Get("X-Xray"))
	}
	resp2.Body.Close()

	// Phase 3: Reconfigure with conflicting address should fail and keep addr2 intact
	conflictLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conflictAddr := conflictLn.Addr().String()
	defer conflictLn.Close()

	err = d.Reconfigure(Config{
		ListenAddr: conflictAddr,
	})
	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}

	// Addr2 should still work
	resp3, err := client.Get(fmt.Sprintf("http://%s/cdn-bridge", addr2))
	if err != nil {
		t.Fatalf("request on addr2 after failed reconfigure failed: %v", err)
	}
	resp3.Body.Close()
}

func TestDispatcherFallbackUnavailable(t *testing.T) {
	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     "",
		TgTarget:       "",
		XrayPathPrefix: "/cdn-bridge",
	})

	req := httptest.NewRequest(http.MethodGet, "/cdn-bridge", nil)
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for missing xray target, got %d", w.Code)
	}

	reqTg := httptest.NewRequest(http.MethodGet, "/?bridge=test", nil)
	wTg := httptest.NewRecorder()
	d.ServeHTTP(wTg, reqTg)
	if wTg.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for missing tg target, got %d", wTg.Code)
	}
}

func TestApplyConfig_TwoPhaseListenerSwitchAndHostnameClear(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "ok")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	addr1 := testutil.FreeAddr(t)

	d := New(Config{
		ListenAddr:     addr1,
		XrayTarget:     backend.URL,
		TgTarget:       backend.URL,
		PublicHostname: "initial.domain.test",
	})

	if err := d.Start(); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	defer func() { _ = d.Stop() }()

	client := &http.Client{Timeout: 500 * time.Millisecond}

	// Verify addr1 works
	resp, err := client.Get(fmt.Sprintf("http://%s/cdn-bridge", addr1))
	if err != nil {
		t.Fatalf("request on addr1 failed: %v", err)
	}
	_ = resp.Body.Close()

	// Prepare addr2
	addr2 := testutil.FreeAddr(t)

	// ApplyConfig (full replacement) with addr2 and empty hostname
	err = d.ApplyConfig(Config{
		ListenAddr:     addr2,
		XrayTarget:     backend.URL,
		TgTarget:       backend.URL,
		PublicHostname: "", // explicitly cleared!
	})
	if err != nil {
		t.Fatalf("ApplyConfig failed: %v", err)
	}

	// Bounded polling for addr2 to become active
	deadline := time.Now().Add(3 * time.Second)
	var newOk bool
	for time.Now().Before(deadline) {
		r, err := client.Get(fmt.Sprintf("http://%s/cdn-bridge", addr2))
		if err == nil {
			_ = r.Body.Close()
			newOk = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !newOk {
		t.Fatalf("addr2 did not become active within timeout")
	}

	// Bounded polling for addr1 to close after drain
	var oldClosed bool
	for time.Now().Before(deadline.Add(3 * time.Second)) {
		conn, err := net.DialTimeout("tcp", addr1, 100*time.Millisecond)
		if err != nil {
			oldClosed = true
			break
		}
		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	if !oldClosed {
		t.Fatalf("addr1 was not closed within drain timeout")
	}

	// Verify PublicHostname is cleared in config
	if d.GetConfig().PublicHostname != "" {
		t.Fatalf("expected PublicHostname to be cleared, got %q", d.GetConfig().PublicHostname)
	}
}

func TestDispatcherNormalizeHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"example.com", "example.com"},
		{"EXAMPLE.COM", "example.com"},
		{"example.com:443", "example.com"},
		{"EXAMPLE.COM:8443", "example.com"},
		{"192.168.1.1", "192.168.1.1"},
		{"192.168.1.1:9009", "192.168.1.1"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"[2001:db8::1]:443", "2001:db8::1"},
		{"   sub.domain.org:80   ", "sub.domain.org"},
		{"", ""},
	}

	for _, tc := range tests {
		got := normalizeHost(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeHost(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestDispatcherHostAwareRouting_DistinctHosts(t *testing.T) {
	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "xray")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("xray-ok"))
	}))
	defer xrayServer.Close()

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "tg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tg-ok"))
	}))
	defer tgServer.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayServer.URL,
		TgTarget:       tgServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		XrayPublicHost: "xray.vpn.test",
		TgPublicHost:   "tg.proxy.test",
	})

	tests := []struct {
		name         string
		host         string
		path         string
		expectedCode int
		expectedBack string
	}{
		{
			name:         "Xray path with Xray Host routes to Xray",
			host:         "xray.vpn.test:443",
			path:         "/cdn-bridge",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "TG endpoint with Xray Host returns 404 (Detail 6)",
			host:         "xray.vpn.test",
			path:         "/?bridge=abc",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "TG endpoint with TG Host routes to TG",
			host:         "tg.proxy.test:443",
			path:         "/?bridge=abc",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "TG api with TG Host routes to TG",
			host:         "tg.proxy.test",
			path:         "/api/v1/status",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "Xray path with TG Host returns 404 (Detail 6)",
			host:         "tg.proxy.test",
			path:         "/cdn-bridge",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Unknown host returns 404",
			host:         "unknown.domain.test",
			path:         "/cdn-bridge",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Unknown host returns 404 on root",
			host:         "unknown.domain.test",
			path:         "/",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Unsupported path on Xray host returns 404",
			host:         "xray.vpn.test",
			path:         "/some/random/path",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			w := httptest.NewRecorder()

			d.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedCode {
				t.Fatalf("expected status %d, got %d for host %s path %s", tc.expectedCode, resp.StatusCode, tc.host, tc.path)
			}
			if tc.expectedBack != "" {
				back := resp.Header.Get("X-Backend")
				if back != tc.expectedBack {
					t.Fatalf("expected backend %q, got %q", tc.expectedBack, back)
				}
			}
		})
	}
}

func TestDispatcherHostAwareRouting_SharedHost(t *testing.T) {
	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "xray")
		w.WriteHeader(http.StatusOK)
	}))
	defer xrayServer.Close()

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "tg")
		w.WriteHeader(http.StatusOK)
	}))
	defer tgServer.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayServer.URL,
		TgTarget:       tgServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		PublicHostname: "shared.cdn.test",
	})

	tests := []struct {
		name         string
		host         string
		path         string
		expectedCode int
		expectedBack string
	}{
		{
			name:         "Xray path with shared host routes to Xray (Priority 3)",
			host:         "shared.cdn.test:443",
			path:         "/cdn-bridge",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "TG endpoint with shared host routes to TG (Priority 4)",
			host:         "shared.cdn.test",
			path:         "/?bridge=abc",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "Unsupported path on shared host returns 404 (Priority 5)",
			host:         "shared.cdn.test",
			path:         "/random/unsupported",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
		{
			name:         "Unknown host returns 404",
			host:         "other.domain.test",
			path:         "/cdn-bridge",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			w := httptest.NewRecorder()

			d.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedCode {
				t.Fatalf("expected status %d, got %d", tc.expectedCode, resp.StatusCode)
			}
			if tc.expectedBack != "" && resp.Header.Get("X-Backend") != tc.expectedBack {
				t.Fatalf("expected backend %q, got %q", tc.expectedBack, resp.Header.Get("X-Backend"))
			}
		})
	}
}

func TestDispatcherCandidateTransactions(t *testing.T) {
	tempDir := t.TempDir()

	addr := testutil.FreeAddr(t)

	d := NewWithDataDir(tempDir, Config{
		ListenAddr:     addr,
		PublicHostname: "old.domain.test",
	})

	candidate := Candidate{
		Enabled: true,
		Config: Config{
			ListenAddr:     addr,
			PublicHostname: "cand.domain.test",
			XrayTarget:     "http://127.0.0.1:9008",
			TgTarget:       "http://127.0.0.1:8085",
		},
	}

	txID, err := d.PrepareCandidate("", candidate)
	if err != nil {
		t.Fatalf("PrepareCandidate failed: %v", err)
	}

	// Verify manifest on disk
	manifestPath := filepath.Join(tempDir, "dispatcher", "tx", txID, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	// Commit prepared candidate
	if err := d.CommitPrepared(txID); err != nil {
		t.Fatalf("CommitPrepared failed: %v", err)
	}
	if !d.IsRunning() {
		t.Fatalf("expected dispatcher to be running after commit with Enabled: true")
	}
	if d.GetConfig().PublicHostname != "cand.domain.test" {
		t.Fatalf("expected config updated to cand.domain.test")
	}

	// Finalize prepared candidate
	if err := d.FinalizePrepared(txID); err != nil {
		t.Fatalf("FinalizePrepared failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "dispatcher", "tx", txID)); !os.IsNotExist(err) {
		t.Fatalf("expected tx dir cleaned up after finalize")
	}

	// Stop dispatcher
	_ = d.Stop()
}

func TestDispatcherHostAwareRouting_LoopbackReverseProxy(t *testing.T) {
	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "xray")
		w.WriteHeader(http.StatusOK)
	}))
	defer xrayServer.Close()

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "tg")
		w.WriteHeader(http.StatusOK)
	}))
	defer tgServer.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayServer.URL,
		TgTarget:       tgServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		XrayPublicHost: "xray.domain.test",
		TgPublicHost:   "tg.domain.test",
	})

	tests := []struct {
		name         string
		host         string
		path         string
		expectedCode int
		expectedBack string
	}{
		{
			name:         "Keenetic reverse proxy with Host 127.0.0.1 for Telegram root",
			host:         "127.0.0.1",
			path:         "/",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "Keenetic reverse proxy with Host 127.0.0.1:9009 for Telegram bridge",
			host:         "127.0.0.1:9009",
			path:         "/?bridge=abc",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "Keenetic reverse proxy with Host 127.0.0.1 for Xray path",
			host:         "127.0.0.1",
			path:         "/cdn-bridge/vless",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "Loopback request with unsupported path returns 404",
			host:         "127.0.0.1",
			path:         "/unknown/path",
			expectedCode: http.StatusNotFound,
			expectedBack: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			w := httptest.NewRecorder()

			d.ServeHTTP(w, req)
			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedCode {
				t.Fatalf("expected status %d, got %d for host %s path %s", tc.expectedCode, resp.StatusCode, tc.host, tc.path)
			}
			if tc.expectedBack != "" {
				back := resp.Header.Get("X-Backend")
				if back != tc.expectedBack {
					t.Fatalf("expected backend %q, got %q", tc.expectedBack, back)
				}
			}
		})
	}
}

func TestDispatcherXrayPaddingAndTrailingSlash(t *testing.T) {
	var capturedPath string
	var capturedReferer string

	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedReferer = r.Header.Get("Referer")
		w.WriteHeader(http.StatusOK)
	}))
	defer xrayServer.Close()

	d := New(Config{
		XrayTarget:     xrayServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		XrayPublicHost: "vkcdn.example.com",
	})

	// 1. Request to exact prefix without trailing slash and without padding
	req := httptest.NewRequest(http.MethodGet, "/cdn-bridge", nil)
	req.Host = "vkcdn.example.com"
	w := httptest.NewRecorder()
	d.ServeHTTP(w, req)

	if capturedPath != "/cdn-bridge/" {
		t.Fatalf("expected path to be rewritten to '/cdn-bridge/', got %q", capturedPath)
	}
	if !strings.Contains(capturedReferer, "x_padding=") {
		t.Fatalf("expected injected padding Referer, got %q", capturedReferer)
	}

	// 2. Request with preexisting Referer should preserve it
	req2 := httptest.NewRequest(http.MethodGet, "/cdn-bridge/session1", nil)
	req2.Host = "vkcdn.example.com"
	req2.Header.Set("Referer", "https://custom.com/path")
	w2 := httptest.NewRecorder()
	d.ServeHTTP(w2, req2)

	if capturedPath != "/cdn-bridge/session1" {
		t.Fatalf("expected path '/cdn-bridge/session1', got %q", capturedPath)
	}
	if capturedReferer != "https://custom.com/path" {
		t.Fatalf("expected preserved Referer, got %q", capturedReferer)
	}
}

func TestDispatcherStructuredRoutesAndRejectAction(t *testing.T) {
	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "xray")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("xray-resp"))
	}))
	defer xrayServer.Close()

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "tg")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tg-resp"))
	}))
	defer tgServer.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayServer.URL,
		TgTarget:       tgServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		DefaultAction:  "reject",
		Routes: []CDNRouteRule{
			{
				ID:            "r-xray-only",
				Domain:        "xray.example.com",
				Description:   "Xray only endpoint",
				RouteXray:     true,
				RouteTelegram: false,
				Enabled:       true,
			},
			{
				ID:            "r-tg-only",
				Domain:        "tg.example.com",
				Description:   "TG only endpoint",
				RouteXray:     false,
				RouteTelegram: true,
				Enabled:       true,
			},
			{
				ID:            "r-both",
				Domain:        "both.example.com",
				Description:   "Both services",
				RouteXray:     true,
				RouteTelegram: true,
				Enabled:       true,
			},
			{
				ID:            "r-disabled",
				Domain:        "disabled.example.com",
				Description:   "Disabled",
				RouteXray:     true,
				RouteTelegram: true,
				Enabled:       false,
			},
		},
	})

	tests := []struct {
		name         string
		host         string
		path         string
		expectedCode int
		expectedConn string
		expectedBack string
	}{
		{
			name:         "Xray-only domain: Xray path succeeds",
			host:         "xray.example.com",
			path:         "/cdn-bridge/vless",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "Xray-only domain: Telegram path rejected with 403 Forbidden and Connection: close",
			host:         "xray.example.com",
			path:         "/",
			expectedCode: http.StatusForbidden,
			expectedConn: "close",
		},
		{
			name:         "TG-only domain: Telegram path succeeds",
			host:         "tg.example.com",
			path:         "/api/v1/stream",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "TG-only domain: Xray path rejected with 403 Forbidden",
			host:         "tg.example.com",
			path:         "/cdn-bridge/vless",
			expectedCode: http.StatusForbidden,
			expectedConn: "close",
		},
		{
			name:         "Both domain: Xray path succeeds",
			host:         "both.example.com:443",
			path:         "/cdn-bridge",
			expectedCode: http.StatusOK,
			expectedBack: "xray",
		},
		{
			name:         "Both domain: TG path succeeds",
			host:         "both.example.com",
			path:         "/tg/bridge",
			expectedCode: http.StatusOK,
			expectedBack: "tg",
		},
		{
			name:         "Disabled domain rejected with 403 Forbidden",
			host:         "disabled.example.com",
			path:         "/cdn-bridge",
			expectedCode: http.StatusForbidden,
			expectedConn: "close",
		},
		{
			name:         "Unknown domain rejected with 403 Forbidden",
			host:         "foreign.example.com",
			path:         "/cdn-bridge",
			expectedCode: http.StatusForbidden,
			expectedConn: "close",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			w := httptest.NewRecorder()
			d.ServeHTTP(w, req)

			if w.Code != tc.expectedCode {
				t.Fatalf("expected status %d, got %d", tc.expectedCode, w.Code)
			}
			if tc.expectedConn != "" && w.Header().Get("Connection") != tc.expectedConn {
				t.Fatalf("expected Connection header %q, got %q", tc.expectedConn, w.Header().Get("Connection"))
			}
			if tc.expectedBack != "" && w.Header().Get("X-Backend") != tc.expectedBack {
				t.Fatalf("expected backend %q, got %q", tc.expectedBack, w.Header().Get("X-Backend"))
			}
		})
	}
}

func TestDispatcher_PreservesExactPathForFiniteProto(t *testing.T) {
	var gotPath string
	var gotReferer string
	xrayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotReferer = r.Header.Get("Referer")
		w.WriteHeader(http.StatusOK)
	}))
	defer xrayServer.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayServer.URL,
		XrayPathPrefix: "/cdn-bridge",
		PublicHostname: "cdn.example.com",
	})

	// 1. Standard XHTTP request gets trailing slash appended for SplitHTTP compatibility
	reqXHTTP := httptest.NewRequest(http.MethodPost, "/cdn-bridge", nil)
	reqXHTTP.Host = "cdn.example.com"
	w1 := httptest.NewRecorder()
	d.ServeHTTP(w1, reqXHTTP)
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for standard XHTTP, got %d", w1.Code)
	}
	if gotPath != "/cdn-bridge/" {
		t.Fatalf("expected standard XHTTP path to be rewritten to /cdn-bridge/, got %q", gotPath)
	}
	if gotReferer == "" {
		t.Fatalf("expected dummy Referer header on standard XHTTP request")
	}

	// 2. FiniteProto v2 request preserves exact /cdn-bridge path so HMAC path binding succeeds
	reqFP := httptest.NewRequest(http.MethodPost, "/cdn-bridge", nil)
	reqFP.Host = "cdn.example.com"
	reqFP.Header.Set("X-FP-Version", "fp/1")
	reqFP.Header.Set("X-FP-Auth", "fp_123:1700000000000:0123456789abcdef01234567:deadbeef")
	w2 := httptest.NewRecorder()
	d.ServeHTTP(w2, reqFP)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for FiniteProto request, got %d", w2.Code)
	}
	if gotPath != "/cdn-bridge" {
		t.Fatalf("expected FiniteProto path to remain /cdn-bridge, got %q", gotPath)
	}
	if gotReferer != "" {
		t.Fatalf("expected no synthetic Referer header on FiniteProto request, got %q", gotReferer)
	}
}

func TestDispatcher_HostOnlyTelegramTargetOverride(t *testing.T) {
	globalTg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "global-tg")
		w.WriteHeader(http.StatusOK)
	}))
	defer globalTg.Close()

	harnessBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "external-harness-9099")
		w.Header().Set("X-Seen-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer harnessBackend.Close()

	xrayBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "xray")
		w.WriteHeader(http.StatusOK)
	}))
	defer xrayBackend.Close()

	d := New(Config{
		ListenAddr:     ":0",
		XrayTarget:     xrayBackend.URL,
		TgTarget:       globalTg.URL,
		XrayPathPrefix: "/cdn-bridge",
		DefaultAction:  "404",
		Routes: []CDNRouteRule{
			{
				ID:            "prod-tg",
				Domain:        "prod-tg.example.com",
				RouteTelegram: true,
				Enabled:       true,
			},
			{
				ID:             "timeweb-file-https",
				Domain:         "twcdn.example.com",
				RouteXray:      true,
				RouteTelegram:  true,
				TelegramTarget: harnessBackend.URL,
				Enabled:        true,
			},
		},
	})

	// 1. twcdn.example.com /bridge/v1/echo routes to external-harness-9099
	req1 := httptest.NewRequest(http.MethodGet, "/bridge/v1/echo?d=test", nil)
	req1.Host = "twcdn.example.com"
	w1 := httptest.NewRecorder()
	d.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for twcdn /bridge/v1/echo, got %d", w1.Code)
	}
	if got := w1.Header().Get("X-Backend"); got != "external-harness-9099" {
		t.Fatalf("expected backend external-harness-9099, got %q", got)
	}
	if got := w1.Header().Get("X-Seen-Path"); got != "/bridge/v1/echo" {
		t.Fatalf("expected X-Seen-Path /bridge/v1/echo, got %q", got)
	}

	// 2. prod-tg.example.com /api/v1/stream continues to route to globalTg untouched
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
	req2.Host = "prod-tg.example.com"
	w2 := httptest.NewRecorder()
	d.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for prod-tg /api/v1/stream, got %d", w2.Code)
	}
	if got := w2.Header().Get("X-Backend"); got != "global-tg" {
		t.Fatalf("expected backend global-tg, got %q", got)
	}

	// 3. twcdn.example.com /cdn-bridge continues to route to xray untouched
	req3 := httptest.NewRequest(http.MethodPost, "/cdn-bridge", nil)
	req3.Host = "twcdn.example.com"
	w3 := httptest.NewRecorder()
	d.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for twcdn /cdn-bridge, got %d", w3.Code)
	}
	if got := w3.Header().Get("X-Backend"); got != "xray" {
		t.Fatalf("expected backend xray, got %q", got)
	}
}

func TestDispatcher_FullDuplexXrayStreamOneAndNetcrazeKeenDNS(t *testing.T) {
	lnBack, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnBack.Close()

	go func() {
		for {
			c, err := lnBack.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 && bytes.Contains(buf[:n], []byte("ping-one")) {
						break
					}
					if err != nil {
						return
					}
				}
				_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nX-CDN-Route: stale\r\n\r\n7\r\npong-ok\r\n"))
				time.Sleep(2 * time.Second)
			}(c)
		}
	}()

	d := New(Config{
		XrayTarget:     "http://" + lnBack.Addr().String(),
		XrayPathPrefix: "/xhttp-bridge",
		XrayPublicHost: "connect.example.netcraze.club",
	})
	frontSrv := httptest.NewServer(d)
	defer frontSrv.Close()

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(frontSrv.URL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer conn.Close()

	// Send chunked stream-one POST WITHOUT terminating 0\r\n\r\n chunk.
	_, err = conn.Write([]byte("POST /xhttp-bridge HTTP/1.1\r\nHost: connect.example.netcraze.club\r\nTransfer-Encoding: chunked\r\n\r\n8\r\nping-one\r\n"))
	if err != nil {
		t.Fatalf("write open POST: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	out := make([]byte, 4096)
	var acc bytes.Buffer
	for acc.Len() == 0 || !bytes.Contains(acc.Bytes(), []byte("pong-ok")) {
		n, rerr := conn.Read(out)
		if n > 0 {
			acc.Write(out[:n])
		}
		if rerr != nil && rerr != io.EOF {
			t.Fatalf("expected immediate full-duplex response before POST EOF, got err=%v partial=%q", rerr, acc.String())
		}
		if rerr == io.EOF {
			break
		}
	}
	rawResp := acc.String()
	if !strings.Contains(rawResp, "pong-ok") {
		t.Fatalf("missing stream-one payload in response: %q", rawResp)
	}
	if cnt := strings.Count(strings.ToLower(rawResp), "x-cdn-route:"); cnt != 1 {
		t.Fatalf("expected exactly 1 X-Cdn-Route header, got %d in %q", cnt, rawResp)
	}

	reqKD := httptest.NewRequest(http.MethodGet, "/xhttp-bridge", nil)
	reqKD.RemoteAddr = "127.0.0.1:43210"
	reqKD.Host = "connect.example.netcraze.club"
	if !isKeenDNSLoopbackRequest(reqKD) {
		t.Fatalf("expected *.netcraze.club loopback request to be recognized as KeenDNS")
	}
}
