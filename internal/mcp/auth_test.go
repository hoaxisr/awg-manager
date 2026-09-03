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
	mu      sync.Mutex
	fails   int
	blocked bool
}

func (f *fakeThrottle) Begin(string) (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked {
		return 30 * time.Second, true
	}
	return 0, false
}
func (f *fakeThrottle) Done(string)    {}
func (f *fakeThrottle) Success(string) {}
func (f *fakeThrottle) Fail(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails++
	return false
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
		})
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
	thr.blocked = true
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
