package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	defaultBaseURL = "http://localhost:79/rci"
	// defaultTimeout is the backstop for a single RCI HTTP exchange.
	// Per-call context deadlines still win when shorter. 30s allows
	// slow NDMS operations (interface create, flash commits, running
	// a ping-check re-setup under load) to complete without leaving
	// the router in a partially-configured state from a client-side
	// timeout.
	defaultTimeout = 30 * time.Second
)

// Client is the NDMS RCI HTTP client. Every request Acquires a slot from
// the embedded semaphore before doing I/O; callers never bypass the gate.
type Client struct {
	http    *http.Client
	baseURL string
	sem     *Semaphore
}

// New constructs a production Client pointing at localhost:79/rci with
// the default 10s timeout.
func New(sem *Semaphore) *Client {
	return &Client{
		http:    &http.Client{Timeout: defaultTimeout, Transport: sharedTransport},
		baseURL: defaultBaseURL,
		sem:     sem,
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
// response per command in the same order.
func (c *Client) PostBatch(ctx context.Context, commands []any) ([]json.RawMessage, error) {
	raw, err := c.postJSON(ctx, commands)
	if err != nil {
		return nil, err
	}
	var results []json.RawMessage
	if err := json.Unmarshal(raw, &results); err != nil {
		return nil, fmt.Errorf("rci batch: decode array: %w", err)
	}
	return results, nil
}

func (c *Client) postJSON(ctx context.Context, payload any) (json.RawMessage, error) {
	if err := c.sem.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("rci POST: %w", err)
	}
	defer c.sem.Release()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, fmt.Errorf("rci POST: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/", &buf)
	if err != nil {
		return nil, fmt.Errorf("rci POST: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rci POST: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rci POST: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Method: "POST", Path: "/", Status: resp.StatusCode, Body: data}
	}
	return json.RawMessage(data), nil
}

// GetRaw performs GET {baseURL}{path} and returns the raw body bytes.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if err := c.sem.Acquire(ctx); err != nil {
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer c.sem.Release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rci GET %s: read: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Method: "GET", Path: path, Status: resp.StatusCode, Body: body}
	}
	return body, nil
}
