package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// KeyInfo identifies the MCP key that authenticated a request.
type KeyInfo struct {
	ID   string
	Name string
}

// Throttle is the anti-brute-force gate; *auth.LoginThrottle satisfies it.
type Throttle interface {
	Begin(ip string) (retryAfter time.Duration, blocked bool)
	Done(ip string)
	Fail(ip string) bool
	Success(ip string)
}

// AuthConfig wires KeyMiddleware to the host without importing storage.
type AuthConfig struct {
	// Enabled reports Settings.McpEnabled. When false the endpoint is
	// invisible: 404 without any auth hint.
	Enabled func() bool
	// Verify maps a presented bearer token to a key.
	Verify func(token string) (KeyInfo, bool)
	// Touch records key usage (optional).
	Touch func(id string)
	// Throttle limits failed attempts per client IP (optional).
	Throttle Throttle
	// Log receives one line per rejected request (optional).
	Log func(format string, args ...any)
}

type keyCtxKey struct{}

// KeyFromContext returns the authenticated key, if any.
func KeyFromContext(ctx context.Context) (KeyInfo, bool) {
	k, ok := ctx.Value(keyCtxKey{}).(KeyInfo)
	return k, ok
}

// KeyMiddleware enforces "enabled + valid MCP key" in front of next. It
// ignores the daemon's global AuthEnabled flag and session cookies on
// purpose: /mcp is reachable remotely through KeenDNS, so a key is always
// required. 401 responses carry the OAuth resource-metadata hint so a
// future OAuth layer needs no client-side change.
func KeyMiddleware(cfg AuthConfig, next http.Handler) http.Handler {
	logf := cfg.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Enabled == nil || !cfg.Enabled() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":true,"message":"not found","code":"NOT_FOUND"}`))
			return
		}
		ip := clientIP(r)
		if cfg.Throttle != nil {
			if retry, blocked := cfg.Throttle.Begin(ip); blocked {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":true,"message":"too many failed attempts","code":"THROTTLED"}`))
				return
			}
			defer cfg.Throttle.Done(ip)
		}
		token := bearerToken(r)
		key, ok := KeyInfo{}, false
		if token != "" && cfg.Verify != nil {
			key, ok = cfg.Verify(token)
		}
		if !ok {
			if cfg.Throttle != nil {
				cfg.Throttle.Fail(ip)
			}
			logf("MCP: rejected %s %s from %s (invalid or missing key)", r.Method, r.URL.Path, ip)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, externalOrigin(r)))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":true,"message":"unauthorized","code":"AUTH_REQUIRED"}`))
			return
		}
		if cfg.Throttle != nil {
			cfg.Throttle.Success(ip)
		}
		if cfg.Touch != nil {
			cfg.Touch(key.ID)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCtxKey{}, key)))
	})
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// externalOrigin reconstructs scheme://host as the client sees it. The
// NDMS reverse proxy terminates TLS and sets X-Forwarded-Proto.
func externalOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
