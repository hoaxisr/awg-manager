package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/auth"
)

func TestZapretRoutesDispatchWrites(t *testing.T) {
	s, sessions := newGuardServer(t)
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	token, err := sessions.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/zapret/hostlist", "/api/zapret/strategies"} {
		t.Run(path, func(t *testing.T) {
			// A malformed payload must reach the write handler's JSON
			// validation, rather than the GET handler's method rejection.
			req := httptest.NewRequest(http.MethodPut, path, strings.NewReader("{"))
			req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT: got %d, want 400: %s", rec.Code, rec.Body.String())
			}
			guest := httptest.NewRecorder()
			mux.ServeHTTP(guest, httptest.NewRequest(http.MethodPut, path, strings.NewReader("{")))
			if guest.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated PUT: got %d, want 401", guest.Code)
			}
			req = httptest.NewRequest(http.MethodDelete, path, nil)
			req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("DELETE: got %d, want 405", rec.Code)
			}
		})
	}
}
