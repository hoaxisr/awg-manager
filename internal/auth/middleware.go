package auth

import (
	"net/http"
)

// AuthChecker checks if authentication is enabled.
type AuthChecker interface {
	IsAuthEnabled() bool
}

// AuthLogger provides structured logging for auth events.
type AuthLogger interface {
	Warnf(format string, args ...interface{})
}

// Middleware provides HTTP middleware for authentication.
type Middleware struct {
	sessions    *SessionStore
	authChecker AuthChecker
	log         AuthLogger
}

// NewMiddleware creates a new auth middleware.
func NewMiddleware(sessions *SessionStore, authChecker AuthChecker, log AuthLogger) *Middleware {
	return &Middleware{
		sessions:    sessions,
		authChecker: authChecker,
		log:         log,
	}
}

// RequireAuthFunc wraps an http.HandlerFunc and requires authentication.
func (m *Middleware) RequireAuthFunc(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Skip auth check if disabled
		if m.authChecker != nil && !m.authChecker.IsAuthEnabled() {
			next(w, r)
			return
		}

		cookie, err := r.Cookie(SessionCookie)
		if err != nil {
			if m.log != nil {
				m.log.Warnf("Auth: no session cookie, rejected %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":true,"message":"unauthorized","code":"AUTH_REQUIRED"}`))
			return
		}

		session := m.sessions.Get(cookie.Value)
		if session == nil {
			if m.log != nil {
				m.log.Warnf("Auth: invalid/expired session, rejected %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":true,"message":"session expired","code":"SESSION_EXPIRED"}`))
			return
		}

		// Session is valid, proceed
		next(w, r)
	}
}
