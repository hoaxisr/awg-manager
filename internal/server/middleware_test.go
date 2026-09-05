package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Паника в handler'е не имеет права ронять процесс: без recover падает весь
// демон, а вместе с ним туннели и роутинг. Ответ — ровно тот литерал, по
// которому фронт отличает внутренний сбой от прикладной ошибки.
func TestLoggingMiddleware_RecoversPanicAs500JSON(t *testing.T) {
	h := (&Server{}).loggingMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tunnels", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("код = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	const want = `{"error":true,"message":"internal server error","code":"PANIC"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("тело = %q, want %q", got, want)
	}
}

// Без паники middleware прозрачен: код, заголовки и тело — от downstream.
func TestLoggingMiddleware_PassesThroughWithoutPanic(t *testing.T) {
	h := (&Server{}).loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tunnels", nil))

	if rec.Code != http.StatusTeapot {
		t.Fatalf("код = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Fatalf("тело = %q, want %q", got, "ok")
	}
}
