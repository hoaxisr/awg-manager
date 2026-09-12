package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
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

// recordingAppLogger — приёмник записей журнала для проверок ниже.
type recordingAppLogger struct {
	mu      sync.Mutex
	entries []recordedEntry
}

type recordedEntry struct {
	level                                    logging.Level
	group, subgroup, action, target, message string
}

func (r *recordingAppLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, recordedEntry{level, group, subgroup, action, target, message})
}

// Паника обязана оставлять след в журнале. Раньше перехват отдавал 500 и молчал:
// для пользователя это «внутренняя ошибка», а для нас — вообще ничего, авария
// не воспроизводилась и не искалась. Стек нужен целиком: паника приходит из
// чужого кадра, и одно её значение места не называет.
func TestLoggingMiddleware_PanicReachesTheLog(t *testing.T) {
	rec := &recordingAppLogger{}
	s := &Server{appLog: logging.NewScopedLogger(rec, logging.GroupServer, logging.SubHTTP)}
	h := s.loggingMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("взорвалось")
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/amnezia/premium/catalog", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("код = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) != 1 {
		t.Fatalf("записей в журнале %d, ожидалась одна — паника потерялась", len(rec.entries))
	}
	e := rec.entries[0]
	if e.level != logging.LevelError {
		t.Errorf("уровень записи %v, ожидался Error", e.level)
	}
	if !strings.Contains(e.message, "взорвалось") {
		t.Errorf("в записи нет значения паники: %q", e.message)
	}
	if !strings.Contains(e.message, "/api/amnezia/premium/catalog") || !strings.Contains(e.message, http.MethodPost) {
		t.Errorf("в записи нет запроса, на котором упало: %q", e.message)
	}
	// Кадр самого перехвата — доказательство, что приехал стек, а не только текст.
	if !strings.Contains(e.message, "loggingMiddleware") {
		t.Errorf("в записи нет стека: %q", e.message)
	}
}

// Без паники журнал молчит: запись на каждый запрос — это износ флеша и шум.
func TestLoggingMiddleware_QuietWithoutPanic(t *testing.T) {
	rec := &recordingAppLogger{}
	s := &Server{appLog: logging.NewScopedLogger(rec, logging.GroupServer, logging.SubHTTP)}
	h := s.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tunnels/list", nil))

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) != 0 {
		t.Fatalf("журнал получил %d записей на обычный запрос", len(rec.entries))
	}
}
