package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// loopbackRejectLogInterval bounds how often an unauthenticated request
// from the reverse proxy (every remote client looks like 127.0.0.1) is
// written to the journal. The LAN throttle does not apply there, so a
// scanner at line rate would otherwise fill the log ring with one line
// per request and bury everything else.
const loopbackRejectLogInterval = time.Minute

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
	// Throttle limits failed attempts per client IP (optional). It is
	// bypassed for loopback callers — see KeyMiddleware.
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
	// Sampling state for rejections from loopback: last log time and how
	// many were dropped since. Atomics: this closure runs on every request.
	var lastLoopbackReject atomic.Int64 // unix nanos
	var suppressedLoopback atomic.Int64
	logReject := func(r *http.Request, ip string) {
		if !isLoopback(ip) {
			logf("MCP: rejected %s %q from %s (invalid or missing key)", r.Method, r.URL.Path, ip)
			return
		}
		now := time.Now().UnixNano()
		last := lastLoopbackReject.Load()
		if now-last < int64(loopbackRejectLogInterval) || !lastLoopbackReject.CompareAndSwap(last, now) {
			suppressedLoopback.Add(1)
			return
		}
		if n := suppressedLoopback.Swap(0); n > 0 {
			logf("MCP: rejected %s %q from %s (invalid or missing key; %d more rejections via the proxy suppressed in the last %s)", r.Method, r.URL.Path, ip, n, loopbackRejectLogInterval)
			return
		}
		logf("MCP: rejected %s %q from %s (invalid or missing key)", r.Method, r.URL.Path, ip)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.Enabled == nil || !cfg.Enabled() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":true,"message":"not found","code":"NOT_FOUND"}`))
			return
		}
		ip := clientIP(r)
		// The per-IP throttle only has meaning for DIRECT clients. Behind the
		// KeenDNS reverse proxy every remote client is observed as 127.0.0.1,
		// so accounting there would be a remote DoS lever, not a defence:
		// anyone could send a handful of bad keys and 429 every other remote
		// user, repeatably (and each legitimate call would reset the
		// attacker's counter via Success). What actually protects the
		// endpoint from the internet is the 256-bit key. So: throttled on the
		// LAN, not throttled through the proxy.
		throttle := cfg.Throttle
		if isLoopback(ip) {
			throttle = nil
		}
		if throttle != nil {
			if retry, blocked := throttle.Begin(ip); blocked {
				logf("MCP: throttled %s %q from %s (retry in %s)", r.Method, r.URL.Path, ip, retry.Round(time.Second))
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
		// Otherwise a handful of concurrent tool calls from one LAN client
		// would exhaust the burst budget and lock out unrelated,
		// perfectly-authenticated traffic. The decision runs in a closure so
		// `defer throttle.Done(ip)` releases the slot on every exit path,
		// including a panic during Verify.
		var key KeyInfo
		var ok bool
		func() {
			if throttle != nil {
				defer throttle.Done(ip)
			}
			token, wellFormed := bearerToken(r)
			if wellFormed && cfg.Verify != nil {
				key, ok = cfg.Verify(token)
			}
			if throttle != nil {
				if ok {
					throttle.Success(ip)
				} else if throttle.Fail(ip) {
					// The moment the block arms is the brute-force signal;
					// the same single line the web login writes.
					logf("MCP: %s blocked after repeated failed keys (anti-bruteforce)", ip)
				}
			}
		}()

		if !ok {
			logReject(r, ip)
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

// isLoopback reports whether ip is 127.0.0.0/8 or ::1 — i.e. the router's
// own reverse proxy rather than an identifiable client.
func isLoopback(ip string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	return parsed != nil && parsed.IsLoopback()
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
