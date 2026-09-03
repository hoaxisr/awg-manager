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
		}

		// The in-flight throttle slot reserved by Begin above is a
		// concurrency reservation, not an audit marker: it must be released
		// as soon as the authentication decision is known, not held for the
		// lifetime of the (potentially long) downstream MCP tool call.
		// Otherwise a handful of concurrent tool calls from one client IP
		// (e.g. behind the KeenDNS reverse proxy, where many remote clients
		// share one observed IP) would exhaust the burst budget and lock out
		// unrelated, perfectly-authenticated traffic. The decision runs in a
		// closure so `defer cfg.Throttle.Done(ip)` releases the slot on every
		// exit path, including a panic during Verify.
		var key KeyInfo
		var ok bool
		func() {
			if cfg.Throttle != nil {
				defer cfg.Throttle.Done(ip)
			}
			token, wellFormed := bearerToken(r)
			if wellFormed && cfg.Verify != nil {
				key, ok = cfg.Verify(token)
			}
			if cfg.Throttle != nil {
				if ok {
					cfg.Throttle.Success(ip)
				} else {
					cfg.Throttle.Fail(ip)
				}
			}
		}()

		if !ok {
			logf("MCP: rejected %s %q from %s (invalid or missing key)", r.Method, r.URL.Path, ip)
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, externalOrigin(r)))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":true,"message":"unauthorized","code":"AUTH_REQUIRED"}`))
			return
		}
		if cfg.Touch != nil {
			cfg.Touch(key.ID)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyCtxKey{}, key)))
	})
}

// bearerToken extracts the token from a single, well-formed "Bearer <token>"
// Authorization header. The scheme is matched case-insensitively per RFC
// 7235; the token itself is returned verbatim (case-sensitive). Anything
// ambiguous — no header, more than one Authorization header (two proxies
// disagreeing about which value wins is exactly the kind of thing to reject
// outright), wrong scheme, or an empty/whitespace-only token — reports
// wellFormed=false so the caller never calls Verify with garbage.
func bearerToken(r *http.Request) (token string, wellFormed bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	const prefix = "Bearer "
	h := values[0]
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token = strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
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
