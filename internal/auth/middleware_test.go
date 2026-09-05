package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAuthChecker struct {
	enabled bool
	apiKey  string
}

func (f fakeAuthChecker) IsAuthEnabled() bool { return f.enabled }
func (f fakeAuthChecker) GetApiKey() string   { return f.apiKey }

type fakeAuthLogger struct {
	warnings []string
}

func (l *fakeAuthLogger) Warnf(format string, args ...interface{}) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func TestRequireAuth_Disabled_NoCookie_Passes(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	log := &fakeAuthLogger{}
	m := NewMiddleware(sessions, fakeAuthChecker{enabled: false}, log)
	nextCalled := false

	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if !nextCalled {
		t.Fatal("next was not called")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
}

func TestRequireAuth_ValidBearer_NoCookie_Passes(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	m := NewMiddleware(sessions, fakeAuthChecker{enabled: true, apiKey: "secret"}, &fakeAuthLogger{})
	nextCalled := false
	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	h(rr, req)

	if !nextCalled {
		t.Fatal("next was not called")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

// Bearer — замена куки ТОЛЬКО при совпадении с настроенным ключом. Мутант
// «любой непустой токен» проходил: пинов было два — верный ключ и пустой
// настроенный, — а неверный ключ при настроенном не проверял никто.
func TestRequireAuth_WrongBearer_Rejected(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	m := NewMiddleware(sessions, fakeAuthChecker{enabled: true, apiKey: "secret"}, &fakeAuthLogger{})
	nextCalled := false
	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer secre")
	rr := httptest.NewRecorder()
	h(rr, req)

	if nextCalled {
		t.Fatal("хендлер вызван с неверным Bearer")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(rr.Body.String(), `"AUTH_REQUIRED"`) {
		t.Fatalf("тело: %s", rr.Body.String())
	}
}

func TestRequireAuth_EmptyConfiguredAPIKey_RejectsBearer(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	m := NewMiddleware(sessions, fakeAuthChecker{enabled: true, apiKey: ""}, &fakeAuthLogger{})
	nextCalled := false
	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rr := httptest.NewRecorder()
	h(rr, req)

	if nextCalled {
		t.Fatal("next must not be called")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "AUTH_REQUIRED") {
		t.Fatalf("body %q does not contain AUTH_REQUIRED", body)
	}
}

func TestRequireAuth_MissingCookie_AuthRequired(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	m := NewMiddleware(sessions, fakeAuthChecker{enabled: true, apiKey: "secret"}, &fakeAuthLogger{})
	nextCalled := false
	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	if nextCalled {
		t.Fatal("next must not be called")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "AUTH_REQUIRED") {
		t.Fatalf("body %q does not contain AUTH_REQUIRED", body)
	}
}

func TestRequireAuth_InvalidSession_SessionExpired(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	m := NewMiddleware(sessions, fakeAuthChecker{enabled: true}, &fakeAuthLogger{})
	nextCalled := false
	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: "bad-token"})
	rr := httptest.NewRecorder()
	h(rr, req)

	if nextCalled {
		t.Fatal("next must not be called")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "SESSION_EXPIRED") {
		t.Fatalf("body %q does not contain SESSION_EXPIRED", body)
	}
}

func TestRequireAuth_ValidSession_Passes(t *testing.T) {
	sessions := NewSessionStore(nil)
	t.Cleanup(sessions.Stop)

	token, err := sessions.Create("admin")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	m := NewMiddleware(sessions, fakeAuthChecker{enabled: true}, &fakeAuthLogger{})
	nextCalled := false
	h := m.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	rr := httptest.NewRecorder()
	h(rr, req)

	if !nextCalled {
		t.Fatal("next was not called")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name string
		h    string
		want string
	}{
		{name: "empty", h: "", want: ""},
		{name: "wrong scheme", h: "Token abc", want: ""},
		{name: "bearer ok", h: "Bearer abc", want: "abc"},
		{name: "bearer trim", h: "Bearer   abc   ", want: "abc"},
		{name: "lowercase rejected", h: "bearer abc", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.h != "" {
				req.Header.Set("Authorization", tt.h)
			}
			if got := bearerToken(req); got != tt.want {
				t.Fatalf("bearerToken() = %q, want %q", got, tt.want)
			}
		})
	}
}
