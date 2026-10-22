package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// PUT /api/zapret/hostlist делит путь с открытым GET, поэтому expert-гейт
// стоит внутри диспетчера по методу, и статический разбор маршрутов его не
// видит. Проверяем живым запросом: basic получает 403, expert доходит до
// разбора тела, чужой метод — 405.
func TestZapretHostlistWriteNeedsExpert(t *testing.T) {
	s, sessions := newGuardServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	token, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	do := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/zapret/hostlist", strings.NewReader("{"))
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	guest := httptest.NewRecorder()
	mux.ServeHTTP(guest, httptest.NewRequest(http.MethodPut, "/api/zapret/hostlist", strings.NewReader("{")))
	if guest.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated PUT: got %d, want 401", guest.Code)
	}
	if rec := do(http.MethodPut); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"FORBIDDEN"`) {
		t.Fatalf("basic PUT: got %d %s, want 403 FORBIDDEN", rec.Code, rec.Body.String())
	}

	if err := s.settings.Update(func(cur *storage.Settings) error {
		cur.UsageLevel = storage.UsageLevelExpert
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A malformed payload must reach the write handler's JSON validation,
	// rather than the GET handler's method rejection.
	if rec := do(http.MethodPut); rec.Code != http.StatusBadRequest {
		t.Fatalf("expert PUT: got %d %s, want 400", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodDelete); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: got %d, want 405", rec.Code)
	}
}
