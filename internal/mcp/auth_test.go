package mcp_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

type fakeThrottle struct {
	mu       sync.Mutex
	fails    int
	blocked  bool
	inFlight int
}

// setBlocked flips the blocked flag under the lock — Begin reads it under
// the same lock, so tests must not write the field directly.
func (f *fakeThrottle) setBlocked(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked = v
}

func (f *fakeThrottle) Begin(string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked {
		return 30 * time.Second, true
	}
	f.inFlight++
	return 0, false
}
func (f *fakeThrottle) Done(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
}
func (f *fakeThrottle) Success(string) {}
func (f *fakeThrottle) Fail(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails++
	return false
}

func (f *fakeThrottle) inFlightNow() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inFlight
}

func newAuthed(enabled bool, thr *fakeThrottle) (http.Handler, *int) {
	hits := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if k, ok := mcpsrv.KeyFromContext(r.Context()); !ok || k.Name != "laptop" {
			http.Error(w, "no key in context", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	cfg := mcpsrv.AuthConfig{
		Enabled: func() bool { return enabled },
		Verify: func(tok string) (mcpsrv.KeyInfo, bool) {
			if tok == "awgm_good" {
				return mcpsrv.KeyInfo{ID: "k1", Name: "laptop"}, true
			}
			return mcpsrv.KeyInfo{}, false
		},
	}
	if thr != nil {
		cfg.Throttle = thr
	}
	return mcpsrv.KeyMiddleware(cfg, next), &hits
}

func do(h http.Handler, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "http://router.local/mcp", strings.NewReader("{}"))
	req.RemoteAddr = "192.168.1.5:5555"
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestKeyMiddleware_Matrix(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		auth    string
		want    int
	}{
		{"disabled/no key", false, "", 404},
		{"disabled/valid key", false, "Bearer awgm_good", 404},
		{"enabled/no header", true, "", 401},
		{"enabled/wrong scheme", true, "Basic abc", 401},
		{"enabled/bad key", true, "Bearer awgm_bad", 401},
		{"enabled/valid key", true, "Bearer awgm_good", 200},
		{"enabled/empty token", true, "Bearer ", 401},
		{"enabled/whitespace only token", true, "Bearer    ", 401},
		{"enabled/lowercase scheme valid key", true, "bearer awgm_good", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, hits := newAuthed(tc.enabled, nil)
			rec := do(h, tc.auth)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 200 && *hits != 1 {
				t.Fatal("next not called")
			}
			if tc.want != 200 && *hits != 0 {
				t.Fatal("next called on rejected request")
			}
			if tc.want == 401 {
				wa := rec.Header().Get("WWW-Authenticate")
				if !strings.HasPrefix(wa, `Bearer resource_metadata="http://router.local/.well-known/oauth-protected-resource"`) {
					t.Fatalf("WWW-Authenticate = %q", wa)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Fatalf("Content-Type = %q", ct)
				}
			}
			if tc.want == 404 && rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("disabled endpoint must not advertise auth")
			}
			if tc.want == 200 && rec.Header().Get("WWW-Authenticate") != "" {
				t.Fatal("successful response must not carry WWW-Authenticate")
			}
		})
	}
}

func TestKeyMiddleware_EnabledNil(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next must not run when Enabled is nil")
	})
	cfg := mcpsrv.AuthConfig{
		Verify: func(string) (mcpsrv.KeyInfo, bool) { return mcpsrv.KeyInfo{ID: "k1", Name: "laptop"}, true },
	}
	h := mcpsrv.KeyMiddleware(cfg, next)
	rec := do(h, "Bearer awgm_good")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("nil Enabled must not advertise auth")
	}
}

func TestKeyMiddleware_VerifyNil(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next must not run when Verify is nil")
	})
	cfg := mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
	}
	h := mcpsrv.KeyMiddleware(cfg, next)
	rec := do(h, "Bearer whatever")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestKeyMiddleware_DuplicateAuthorizationHeaders(t *testing.T) {
	h, hits := newAuthed(true, nil)
	req := httptest.NewRequest(http.MethodPost, "http://router.local/mcp", strings.NewReader("{}"))
	req.RemoteAddr = "192.168.1.5:5555"
	req.Header.Add("Authorization", "Bearer awgm_good")
	req.Header.Add("Authorization", "Bearer awgm_good")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if *hits != 0 {
		t.Fatal("next called despite duplicate Authorization headers")
	}
}

func TestKeyMiddleware_TouchCalledOnce(t *testing.T) {
	var touched []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	cfg := mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
		Verify: func(tok string) (mcpsrv.KeyInfo, bool) {
			if tok == "awgm_good" {
				return mcpsrv.KeyInfo{ID: "k1", Name: "laptop"}, true
			}
			return mcpsrv.KeyInfo{}, false
		},
		Touch: func(id string) { touched = append(touched, id) },
	}
	h := mcpsrv.KeyMiddleware(cfg, next)
	do(h, "Bearer awgm_good")
	if len(touched) != 1 || touched[0] != "k1" {
		t.Fatalf("touched = %v, want exactly one call with %q", touched, "k1")
	}
}

// TestKeyMiddleware_ThrottleSlotReleasedBeforeHandler pins the Finding-1
// fix: the in-flight throttle slot reserved by Begin must be released
// (Done called) before next runs, not after — otherwise a handful of
// concurrent, long-running MCP tool calls from one client IP would exhaust
// the burst budget and lock out unrelated traffic.
func TestKeyMiddleware_ThrottleSlotReleasedBeforeHandler(t *testing.T) {
	thr := &fakeThrottle{}
	var sawInFlight int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawInFlight = thr.inFlightNow()
		w.WriteHeader(http.StatusOK)
	})
	cfg := mcpsrv.AuthConfig{
		Enabled: func() bool { return true },
		Verify: func(tok string) (mcpsrv.KeyInfo, bool) {
			if tok == "awgm_good" {
				return mcpsrv.KeyInfo{ID: "k1", Name: "laptop"}, true
			}
			return mcpsrv.KeyInfo{}, false
		},
		Throttle: thr,
	}
	h := mcpsrv.KeyMiddleware(cfg, next)
	rec := do(h, "Bearer awgm_good")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if sawInFlight != 0 {
		t.Fatalf("inFlight = %d when next ran, want 0 (slot must be released before the handler runs)", sawInFlight)
	}
}

func TestKeyMiddleware_Throttle(t *testing.T) {
	thr := &fakeThrottle{}
	h, _ := newAuthed(true, thr)
	do(h, "Bearer awgm_bad")
	if thr.fails != 1 {
		t.Fatalf("fails = %d, want 1", thr.fails)
	}
	do(h, "Bearer awgm_good")
	thr.setBlocked(true)
	rec := do(h, "Bearer awgm_good")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked: status = %d, Retry-After = %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestKeyMiddleware_ForwardedProtoInMetadataURL(t *testing.T) {
	h, _ := newAuthed(true, nil)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:2222/mcp", nil)
	req.Host = "home.keenetic.pro"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if wa := rec.Header().Get("WWW-Authenticate"); !strings.Contains(wa, `https://home.keenetic.pro/.well-known/oauth-protected-resource`) {
		t.Fatalf("WWW-Authenticate = %q", wa)
	}
}

func TestKeyFromContext_Absent(t *testing.T) {
	if _, ok := mcpsrv.KeyFromContext(context.Background()); ok {
		t.Fatal("expected no key")
	}
}
