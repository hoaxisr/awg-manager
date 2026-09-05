package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/env"
)

const (
	defaultBaseURL = "http://127.0.0.1:79/rci"
	// defaultTimeout is the backstop for a single RCI HTTP exchange.
	// Per-call context deadlines still win when shorter. 30s allows
	// slow NDMS operations (interface create, flash commits, running
	// a ping-check re-setup under load) to complete without leaving
	// the router in a partially-configured state from a client-side
	// timeout.
	defaultTimeout = 30 * time.Second

	// Default Batcher configuration (overridable via env-vars).
	defaultBatchWindowMs = 15
	defaultBatchMaxSize  = 64
	defaultBatchSubmit   = 256
)

// Client is the NDMS RCI HTTP client. Every request Acquires a slot from
// the embedded semaphore before doing I/O; callers never bypass the gate.
type Client struct {
	http    *http.Client
	baseURL string
	sem     *Semaphore
	appLog  *logging.ScopedLogger

	// batcher coalesce'ит read-запросы в один POST. nil = legacy path.
	batcher *Batcher
}

// recordSlow debug-логирует RCI запросы дольше 500ms.
func (c *Client) recordSlow(start time.Time, method, path string) {
	durMs := time.Since(start).Milliseconds()
	if durMs > 500 && c.appLog != nil {
		c.appLog.Debug(method, path, fmt.Sprintf("perf: slow rci %dms", durMs))
	}
}

// SetAppLogger wires the UI-visible logger into the client. Optional;
// nil-safe. Call once after construction. All HTTP exchanges go through
// this scoped logger at debug level — visible only when log level=debug.
func (c *Client) SetAppLogger(appLogger logging.AppLogger) {
	c.appLog = logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubNDMS)
	if c.batcher != nil {
		c.batcher.SetAppLogger(c.appLog)
	}
}

// New constructs a production Client pointing at 127.0.0.1:79/rci with
// the default 10s timeout. Batcher is enabled by default; set
// AWG_NDMS_BATCH=0 to disable.
func New(sem *Semaphore) *Client {
	c := &Client{
		http:    &http.Client{Timeout: defaultTimeout, Transport: sharedTransport},
		baseURL: defaultBaseURL,
		sem:     sem,
	}
	if env.IntDefault("AWG_NDMS_BATCH", 1) != 0 {
		windowMs := env.IntDefault("AWG_NDMS_BATCH_WINDOW_MS", defaultBatchWindowMs)
		maxSize := env.IntDefault("AWG_NDMS_BATCH_MAX_SIZE", defaultBatchMaxSize)
		submitBuf := env.IntDefault("AWG_NDMS_BATCH_SUBMIT_BUF", defaultBatchSubmit)
		c.batcher = newBatcher(c, time.Duration(windowMs)*time.Millisecond, maxSize, submitBuf)
		c.batcher.EnableFastPath() // production: single-path через direct GET (perf win)
		c.batcher.Start()
	}
	return c
}

// Close gracefully shuts down the batcher. Idempotent.
func (c *Client) Close() {
	if c.batcher != nil {
		c.batcher.Close()
	}
}

// NewWithURL constructs a Client pointing at a custom base URL. Intended
// for tests that wire up an httptest.Server. Deliberately skips
// sharedTransport — each test gets its own connection pool so tests
// don't interact through a shared keep-alive cache.
func NewWithURL(baseURL string, sem *Semaphore) *Client {
	return &Client{
		http:    &http.Client{Timeout: defaultTimeout},
		baseURL: baseURL,
		sem:     sem,
	}
}

// Get performs GET {baseURL}{path} and decodes JSON into dst.
func (c *Client) Get(ctx context.Context, path string, dst any) error {
	body, err := c.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("rci GET %s: decode: %w", path, err)
	}
	return nil
}

// Post sends a single JSON payload via POST {baseURL}/. Returns raw bytes.
func (c *Client) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	return c.postJSON(ctx, payload)
}

// PostBatch sends commands as a JSON array via POST /. Returns one raw
// response per command in the same order. Per-element NDMS error envelopes
// trigger a *BatchError aggregating all failures.
func (c *Client) PostBatch(ctx context.Context, commands []any) ([]json.RawMessage, error) {
	raw, err := c.postJSON(ctx, commands)
	if err != nil {
		return nil, err
	}
	var results []json.RawMessage
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("rci batch: decode array: %w", err)
	}

	// Per-element envelope check. Each element of the array may carry the
	// NDMS error envelope independently — silent partial failures must not
	// slip through.
	var failures []BatchElementError
	for i, elem := range results {
		if msg := ExtractError(elem); msg != "" {
			failures = append(failures, BatchElementError{Index: i, Message: msg})
		}
	}
	if len(failures) > 0 {
		c.appLog.Warn("POST", "/",
			fmt.Sprintf("batch ndms-errors: %d/%d failed", len(failures), len(results)))
		return results, &BatchError{Failures: failures, Total: len(results), Body: raw}
	}
	return results, nil
}

func (c *Client) postJSON(ctx context.Context, payload any) (json.RawMessage, error) {
	start := time.Now()
	defer c.recordSlow(start, "POST", "/")
	if err := c.sem.acquireWithBackstop(ctx); err != nil {
		c.appLog.Error("POST", "/", fmt.Sprintf("semaphore: %v", err))
		return nil, fmt.Errorf("rci POST: %w", err)
	}
	defer c.sem.Release()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		c.appLog.Error("POST", "/", fmt.Sprintf("marshal: %v", err))
		return nil, fmt.Errorf("rci POST: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/", &buf)
	if err != nil {
		c.appLog.Error("POST", "/", fmt.Sprintf("build request: %v", err))
		return nil, fmt.Errorf("rci POST: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		c.appLog.Error("POST", "/", fmt.Sprintf("transport: %v", err))
		return nil, fmt.Errorf("rci POST: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.appLog.Error("POST", "/", fmt.Sprintf("read body: %v", err))
		return nil, fmt.Errorf("rci POST: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		c.appLog.Error("POST", "/", fmt.Sprintf("status %d", resp.StatusCode))
		return nil, &HTTPError{Method: "POST", Path: "/", Status: resp.StatusCode, Body: data}
	}

	// NDMS returns HTTP 200 even on application errors — check body envelope.
	if msg := ExtractError(data); msg != "" {
		c.appLog.Warn("POST", "/", fmt.Sprintf("ndms-error: %s", msg))
		return data, &NDMSAppError{Method: "POST", Path: "/", Message: msg, Body: data}
	}

	return json.RawMessage(data), nil
}

// GetRaw делегирует в Batcher (по умолчанию). Если batcher отключён,
// nil, или путь в bypass-списке — fallback на legacy direct GET.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if c.batcher != nil && !bypassBatch(path) {
		return c.batcher.Submit(ctx, path)
	}
	return c.getRawDirect(ctx, path)
}

// bypassBatch сообщает, что путь нельзя гонять через batch-POST: его
// NDMS-ответ в batch-форме не совпадает по shape с direct GET, и
// unwrapKeys не могут это восстановить.
//
// `/show/interface/<name>/summary` здесь БЫЛ и снят: сводку никто не берёт
// сырым GET'ом — FetchSummary ходит командой show interface через батчер
// (query/interfaces.go), а ветка не могла сработать с 26079ef4b (FetchSummary
// переведён на POST). Понадобится снова сырой путь — вернуть вместе с
// вызывающим.
func bypassBatch(path string) bool {
	if strings.HasPrefix(path, "/show/rc/interface/") {
		return true
	}
	return false
}

// getRawDirect — legacy single-GET path. Используется когда Batcher
// отключён через AWG_NDMS_BATCH=0, либо в Client'ах без batcher'а
// (тесты через NewWithURL).
func (c *Client) getRawDirect(ctx context.Context, path string) ([]byte, error) {
	start := time.Now()
	defer c.recordSlow(start, "GET", path)
	if err := c.sem.acquireWithBackstop(ctx); err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("semaphore: %v", err))
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer c.sem.Release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("build request: %v", err))
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("transport: %v", err))
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("read body: %v", err))
		return nil, fmt.Errorf("rci GET %s: read: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		c.appLog.Error("GET", path, fmt.Sprintf("status %d", resp.StatusCode))
		return nil, &HTTPError{Method: "GET", Path: path, Status: resp.StatusCode, Body: body}
	}
	return body, nil
}

// GetStream performs GET {baseURL}{path} and calls fn with the response body
// reader directly, without buffering the full response into memory. fn is
// invoked only on HTTP 200; the body is closed automatically after fn returns.
// Use instead of GetRaw when the caller immediately decodes the body (e.g.
// json.NewDecoder) and does not need to retain the raw bytes.
func (c *Client) GetStream(ctx context.Context, path string, fn func(io.Reader) error) error {
	if err := c.sem.acquireWithBackstop(ctx); err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("semaphore: %v", err))
		return fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer c.sem.Release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("build request: %v", err))
		return fmt.Errorf("rci GET %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("transport: %v", err))
		return fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		c.appLog.Error("GET", path, fmt.Sprintf("status %d", resp.StatusCode))
		return &HTTPError{Method: "GET", Path: path, Status: resp.StatusCode, Body: body}
	}
	if err := fn(resp.Body); err != nil {
		c.appLog.Error("GET", path, fmt.Sprintf("process: %v", err))
		return fmt.Errorf("rci GET %s: %w", path, err)
	}
	return nil
}
