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
