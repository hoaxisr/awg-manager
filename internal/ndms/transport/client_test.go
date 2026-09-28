package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewWithURL(srv.URL, NewSemaphore(4))
	return c, srv
}

func TestClient_Get_DecodesJSON(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: want GET, got %s", r.Method)
		}
		if r.URL.Path != "/show/version" {
			t.Errorf("path: want /show/version, got %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"release":"4.0"}`)
	}))

	var dst struct {
		Release string `json:"release"`
	}
	if err := c.Get(context.Background(), "/show/version", &dst); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if dst.Release != "4.0" {
		t.Errorf("Release: want 4.0, got %q", dst.Release)
	}
}

func TestClient_GetRaw_ReturnsBytes(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"k":"v"}`)
	}))
	b, err := c.GetRaw(context.Background(), "/show/anything")
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if string(b) != `{"k":"v"}` {
		t.Errorf("body: want {\"k\":\"v\"}, got %s", b)
	}
}

// TestGetRaw_RcInterfaceByName_BypassesBatch проверяет, что read-путь
// /show/rc/interface/<name> идёт direct GET, минуя batcher. Причина:
// NDMS на batch-POST `{"show":{"rc":{"interface":{"<name>":{}}}}}` отдаёт
// двойную вложенность (show.rc.interface.<name>.interface.<name>.{контент}),
// которую unwrapKeys не разворачивают → парсер WGServerStore получает
// {interface:{<name>:...}} вместо {wireguard:{peer:[...]}} и теряет пиров.
// Direct GET спускается по сегментам пути и отдаёт контент напрямую.
// Verified на Keenetic 5.0.11 2026-05-24.
func TestGetRaw_RcInterfaceByName_BypassesBatch(t *testing.T) {
	var gotMethod string
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotMethod = r.Method
		mu.Unlock()
		if r.Method == http.MethodGet {
			// Direct GET: контент напрямую (форма, которую ждёт парсер).
			_, _ = io.WriteString(w, `{"wireguard":{"peer":[{"key":"K"}]}}`)
			return
		}
		// Batch POST: двойная вложенность, как реально отдаёт NDMS.
		_, _ = io.WriteString(w, `[{"show":{"rc":{"interface":{"Wireguard0":{"interface":{"Wireguard0":{"wireguard":{"peer":[{"key":"K"}]}}}}}}}}]`)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cli := NewWithURL(srv.URL, NewSemaphore(30))
	// Fast-path OFF (как в тестах): без bypass одиночный submit пошёл бы POST.
	cli.batcher = newBatcher(cli, 10*time.Millisecond, 64, 256)
	cli.batcher.Start()
	t.Cleanup(cli.batcher.Close)

	body, err := cli.GetRaw(context.Background(), "/show/rc/interface/Wireguard0")
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}

	mu.Lock()
	method := gotMethod
	mu.Unlock()
	if method != http.MethodGet {
		t.Errorf("HTTP method = %s, want GET (rc/interface/<name> must bypass batch)", method)
	}

	var rc struct {
		Wireguard *struct {
			Peer []struct {
				Key string `json:"key"`
			} `json:"peer"`
		} `json:"wireguard"`
	}
	if err := json.Unmarshal(body, &rc); err != nil {
		t.Fatalf("unmarshal body %s: %v", body, err)
	}
	if rc.Wireguard == nil || len(rc.Wireguard.Peer) != 1 {
		t.Fatalf("want wireguard.peer with 1 element (direct-GET shape), got %s", body)
	}
}

func TestClient_Get_NonOKStatus(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	var dst map[string]any
	err := c.Get(context.Background(), "/show/nope", &dst)
	if err == nil {
		t.Fatalf("Get on 503: want error, got nil")
	}
}

func TestClient_Get_DecodeError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `not-json`)
	}))
	var dst struct{}
	err := c.Get(context.Background(), "/show/bad", &dst)
	if err == nil {
		t.Fatalf("Get on invalid JSON: want error, got nil")
	}
}

func TestClient_Post_RoundTrip(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: want POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: want application/json, got %q", ct)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if got["foo"] != "bar" {
			t.Errorf("payload.foo: want bar, got %v", got["foo"])
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))

	resp, err := c.Post(context.Background(), map[string]string{"foo": "bar"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if string(resp) != `{"status":"ok"}` {
		t.Errorf("resp body: %s", resp)
	}
}

// F532: NDMS отвечает на POST show.interface по отсутствующей записи
// HTTP 200 с конвертом статус-ошибки, ВЛОЖЕННЫМ в show.interface, а не
// верхнеуровневым {"status":"error",...}, который проверяет ExtractError
// (стенд KN-1810, 5.02.A.11 — дословное тело). postJSON обязан пропустить
// его как обычный успех: разбор "unable to find" — забота
// internal/ndms/query.fetchOne; «доучи» ExtractError смотреть и во
// вложенные объекты — и первый же старт туннеля (записи ещё нет) поймает
// поддельный NDMSAppError.
func TestClient_Post_NestedStatusError_IsNotNDMSAppError(t *testing.T) {
	const body = `{"show":{"interface":{"status":[{"status":"error","code":"6553619","ident":"Network::Interface::Base","message":"unable to find \"OpkgTun99\"."}]}}}`
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))

	resp, err := c.Post(context.Background(), map[string]any{
		"show": map[string]any{"interface": map[string]any{"name": "OpkgTun99"}},
	})
	if err != nil {
		t.Fatalf("Post: want nil error (вложенный статус — не верхнеуровневый конверт), got %v", err)
	}
	if string(resp) != body {
		t.Errorf("resp body: got %s, want %s", resp, body)
	}
}

func TestClient_PostBatch_ReturnsArray(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err != nil {
			t.Errorf("decode batch body: %v", err)
		}
		if len(arr) != 2 {
			t.Errorf("batch size: want 2, got %d", len(arr))
		}
		_, _ = io.WriteString(w, `[{"a":1},{"b":2}]`)
	}))

	results, err := c.PostBatch(context.Background(), []any{
		map[string]any{"cmd": "one"},
		map[string]any{"cmd": "two"},
	})
	if err != nil {
		t.Fatalf("PostBatch: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results: want 2, got %d", len(results))
	}
	if string(results[0]) != `{"a":1}` {
		t.Errorf("results[0]: %s", results[0])
	}
	if string(results[1]) != `{"b":2}` {
		t.Errorf("results[1]: %s", results[1])
	}
}

func TestClient_SemaphoreLimitsConcurrency(t *testing.T) {
	var inFlight, peak int32
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if cur <= p || atomic.CompareAndSwapInt32(&peak, p, cur) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)

	c := NewWithURL(srv.URL, NewSemaphore(3))

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var dst map[string]any
			_ = c.Get(context.Background(), "/show/ignore", &dst)
		}()
	}
	// Let goroutines stack up.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&peak); got > 3 {
		t.Errorf("peak concurrent requests: want <=3, got %d", got)
	}
	if got := atomic.LoadInt32(&peak); got < 3 {
		t.Errorf("peak concurrent requests: semaphore should fill, got %d", got)
	}
}

// recordedEntry captures one AppLog call for assertions.
type recordedEntry struct {
	level    logging.Level
	group    string
	subgroup string
	action   string
	target   string
	message  string
}

// recordingLogger implements logging.AppLogger by storing every call.
type recordingLogger struct {
	entries []recordedEntry
}

func (r *recordingLogger) AppLog(level logging.Level, group, subgroup, action, target, message string) {
	r.entries = append(r.entries, recordedEntry{level, group, subgroup, action, target, message})
}

func TestExtractError_TopLevelEnvelope(t *testing.T) {
	body := []byte(`{"status":"error","message":"address conflict"}`)
	if got := ExtractError(body); got != "address conflict" {
		t.Errorf("ExtractError = %q, want %q", got, "address conflict")
	}
}

func TestExtractError_NoEnvelope(t *testing.T) {
	body := []byte(`{"status":"ok","data":[1,2,3]}`)
	if got := ExtractError(body); got != "" {
		t.Errorf("ExtractError = %q, want empty", got)
	}
}

func TestExtractError_MalformedJSON(t *testing.T) {
	body := []byte(`not json`)
	if got := ExtractError(body); got != "" {
		t.Errorf("ExtractError on malformed = %q, want empty", got)
	}
}

func TestExtractError_LargeBodyWithoutStatus_FastPath(t *testing.T) {
	// Большие success-ответы без поля "status" идут по байтовому fast-path
	// (без Unmarshal); семантика прежняя — пустая строка.
	body := []byte(`{"GigabitEthernet0":{"id":"GigabitEthernet0","description":"` +
		strings.Repeat("x", 4096) + `"}}`)
	if got := ExtractError(body); got != "" {
		t.Errorf("ExtractError = %q, want empty", got)
	}
}

func TestExtractError_EnvelopeWithSpacing(t *testing.T) {
	// NDMS печатает JSON с пробелами после двоеточия — precheck по `"status"`
	// не должен зависеть от форматирования значения.
	body := []byte(`{"status": "error", "message": "boom"}`)
	if got := ExtractError(body); got != "boom" {
		t.Errorf("ExtractError = %q, want %q", got, "boom")
	}
}

func TestPost_NDMSError_ReturnsTypedError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","message":"address conflict"}`)
	}))
	rec := &recordingLogger{}
	c.SetAppLogger(rec)

	data, err := c.Post(context.Background(), map[string]any{"x": 1})
	if err == nil {
		t.Fatal("expected error")
	}
	var appErr *NDMSAppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected *NDMSAppError, got %T: %v", err, err)
	}
	if appErr.Message != "address conflict" {
		t.Errorf("Message = %q, want %q", appErr.Message, "address conflict")
	}
	if data == nil {
		t.Error("expected body returned alongside error")
	}
}

func TestPost_NDMSError_LogsWarn(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"error","message":"bad"}`)
	}))
	rec := &recordingLogger{}
	c.SetAppLogger(rec)

	_, _ = c.Post(context.Background(), map[string]any{})
	if len(rec.entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(rec.entries))
	}
	if rec.entries[0].level != logging.LevelWarn {
		t.Errorf("level = %q, want warn", rec.entries[0].level)
	}
	if !strings.HasPrefix(rec.entries[0].message, "ndms-error:") {
		t.Errorf("message = %q, want prefix 'ndms-error:'", rec.entries[0].message)
	}
}

func TestPostBatch_AllSuccess_NoError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"status":"ok"},{"status":"ok"}]`)
	}))
	results, err := c.PostBatch(context.Background(), []any{
		map[string]any{"a": 1},
		map[string]any{"b": 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2", len(results))
	}
}

func TestPostBatch_PartialFailure_ReturnsBatchError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"status":"ok"},{"status":"error","message":"oops"},{"status":"ok"}]`)
	}))
	results, err := c.PostBatch(context.Background(), []any{
		map[string]any{"a": 1},
		map[string]any{"b": 2},
		map[string]any{"c": 3},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BatchError, got %T: %v", err, err)
	}
	if be.Total != 3 {
		t.Errorf("Total = %d, want 3", be.Total)
	}
	if len(be.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(be.Failures))
	}
	if be.Failures[0].Index != 1 {
		t.Errorf("Failures[0].Index = %d, want 1", be.Failures[0].Index)
	}
	if be.Failures[0].Message != "oops" {
		t.Errorf("Failures[0].Message = %q, want %q", be.Failures[0].Message, "oops")
	}
	if len(results) != 3 {
		t.Errorf("results len = %d, want 3 (results returned even on partial fail)", len(results))
	}
}

func TestPostBatch_AllFail_ReturnsBatchError(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"status":"error","message":"e1"},{"status":"error","message":"e2"}]`)
	}))
	_, err := c.PostBatch(context.Background(), []any{
		map[string]any{"a": 1},
		map[string]any{"b": 2},
	})
	var be *BatchError
	if !errors.As(err, &be) {
		t.Fatalf("expected *BatchError, got %T: %v", err, err)
	}
	if len(be.Failures) != 2 {
		t.Errorf("len(Failures) = %d, want 2", len(be.Failures))
	}
}
