package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/autolistdetect"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

// autolistLogRecorder собирает записи, отправленные в AppLogger, чтобы
// тесты видели, какие строки журнала сателлита ушли в общий журнал.
type autolistLogRecorder struct {
	mu      sync.Mutex
	actions []string
	msgs    []string
}

func (r *autolistLogRecorder) AppLog(_ logging.Level, _, _, action, _, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	r.msgs = append(r.msgs, message)
}

func (r *autolistLogRecorder) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.msgs))
	copy(out, r.msgs)
	return out
}

func newAutolistLogHandler(t *testing.T, path string) (*AutolistHandler, *autolistLogRecorder) {
	t.Helper()
	rec := &autolistLogRecorder{}
	h := NewAutolistHandler(autolistdetect.NewService())
	h.SetLogger(rec)
	h.logSync = &autolistLogSync{path: path} // путь подменён: реальный /tmp не трогаем
	return h, rec
}

func writeAutolistLog(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

func appendAutolistLog(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("append log: %v", err)
	}
}

// syncAutolistLogViaStatus тянет журнал через сам эндпоинт /status —
// проверяет и то, что sync подключён к Status, и что файл его не валит.
func syncAutolistLogViaStatus(t *testing.T, h *AutolistHandler) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/autolist/status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestAutolistLog_FirstImportBounded: файл больше 64 KiB — в общий журнал
// уходит не глубже последних 64 KiB и не больше 200 строк (хвост файла).
func TestAutolistLog_FirstImportBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	line := func(i int) string { return fmt.Sprintf("line-%04d %s", i, strings.Repeat("x", 91)) } // 102 байта с \n
	var sb strings.Builder
	for i := 0; i < 700; i++ {
		sb.WriteString(line(i) + "\n")
	}
	writeAutolistLog(t, path, sb.String()) // 70 700 байт > 64 KiB

	h, rec := newAutolistLogHandler(t, path)
	syncAutolistLogViaStatus(t, h)

	lines := rec.lines()
	if len(lines) != autolistLogImportLines {
		t.Fatalf("первый импорт: хотим %d строк, получили %d", autolistLogImportLines, len(lines))
	}
	if lines[0] != line(500) || lines[len(lines)-1] != line(699) {
		t.Errorf("импорт должен взять последние %d строк: первое=%q последнее=%q",
			autolistLogImportLines, lines[0], lines[len(lines)-1])
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "line-") {
			t.Errorf("в импорте мусор от обрезанной головы строки: %q", l)
		}
	}
	for _, a := range rec.actions {
		if a != "autolist" {
			t.Errorf("action источника должен быть %q, получили %q", "autolist", a)
		}
	}
}

// TestAutolistLog_SecondSyncNoDuplicates: повторный опрос без новых байт
// файла не дублирует уже импортированные строки.
func TestAutolistLog_SecondSyncNoDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	writeAutolistLog(t, path, "alpha\nbeta\n")

	h, rec := newAutolistLogHandler(t, path)
	h.syncAutolistLog()
	h.syncAutolistLog()
	syncAutolistLogViaStatus(t, h)

	lines := rec.lines()
	if len(lines) != 2 || lines[0] != "alpha" || lines[1] != "beta" {
		t.Fatalf("ожидали [alpha beta] ровно один раз, получили %v", lines)
	}
}

// TestAutolistLog_AppendedLineArrives: строка, дописанная между опросами,
// доезжает целиком и один раз — в том числе если писалась по кускам.
func TestAutolistLog_AppendedLineArrives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	writeAutolistLog(t, path, "one\n")

	h, rec := newAutolistLogHandler(t, path)
	h.syncAutolistLog()
	appendAutolistLog(t, path, "two\n")
	h.syncAutolistLog()
	if lines := rec.lines(); len(lines) != 2 || lines[1] != "two" {
		t.Fatalf("дописанная строка не доехла: %v", lines)
	}

	appendAutolistLog(t, path, "thr")
	h.syncAutolistLog() // неполный хвост — события нет
	if lines := rec.lines(); len(lines) != 2 {
		t.Fatalf("неполная строка не должна уходить в журнал: %v", lines)
	}
	appendAutolistLog(t, path, "ee\n")
	h.syncAutolistLog()
	lines := rec.lines()
	if len(lines) != 3 || lines[2] != "three" {
		t.Fatalf("строка, доехавшая по кускам, должна собраться целиком: %v", lines)
	}
}

// TestAutolistLog_TruncationHandled: файл усёкся (RAMLog обнуляет при
// 5 MiB) — читаем актуальное содержимое с нуля без дублей старых строк.
func TestAutolistLog_TruncationHandled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	var old strings.Builder
	for i := 1; i <= 10; i++ {
		old.WriteString(fmt.Sprintf("old-%d\n", i))
	}
	writeAutolistLog(t, path, old.String()) // 60 байт — offset после первого sync

	h, rec := newAutolistLogHandler(t, path)
	h.syncAutolistLog()

	// Ротация как у RAMLog: файл усёкся, новый размер меньше прочитанного
	// offset — всё содержимое новое.
	writeAutolistLog(t, path, "awgmautolist: log truncated\nfresh\n")
	h.syncAutolistLog()

	want := make([]string, 0, 12)
	for i := 1; i <= 10; i++ {
		want = append(want, fmt.Sprintf("old-%d", i))
	}
	want = append(want, "awgmautolist: log truncated", "fresh")
	if lines := rec.lines(); !slices.Equal(lines, want) {
		t.Fatalf("после truncation: %v", lines)
	}
}

// TestAutolistLog_MissingFileSafe: файла нет — /status живёт, событий нет;
// когда файл появился, первый импорт отрабатывает.
func TestAutolistLog_MissingFileSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgmautolist.log")
	h, rec := newAutolistLogHandler(t, path)

	syncAutolistLogViaStatus(t, h)
	syncAutolistLogViaStatus(t, h)
	if lines := rec.lines(); len(lines) != 0 {
		t.Fatalf("без файла событий быть не должно: %v", lines)
	}

	writeAutolistLog(t, path, "late\n")
	syncAutolistLogViaStatus(t, h)
	if lines := rec.lines(); len(lines) != 1 || lines[0] != "late" {
		t.Fatalf("после появления файла должен сработать первый импорт: %v", lines)
	}
}
