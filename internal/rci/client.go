package rci

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
	defaultTimeout = 10 * time.Second
)

// sharedTransport is the HTTP transport for all RCI connections.
// Reuses TCP connections instead of creating new ones per request.
// Migrated from ndms.rciTransport — same settings.
var sharedTransport = &http.Transport{
	MaxIdleConns:        50,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	DisableKeepAlives:   false,
}

// Transport returns the shared HTTP transport for RCI connections.
func Transport() *http.Transport {
	return sharedTransport
}

// Client is the RCI HTTP client for Keenetic NDMS.
type Client struct {
	http    *http.Client
	baseURL string
}

// New creates a new RCI client with default timeout (10s).
func New() *Client {
	return &Client{
		http:    &http.Client{Timeout: defaultTimeout, Transport: sharedTransport},
		baseURL: defaultBaseURL,
	}
}

// NewWithTimeout creates a new RCI client with custom timeout.
func NewWithTimeout(timeout time.Duration) *Client {
	return &Client{
		http:    &http.Client{Timeout: timeout, Transport: sharedTransport},
		baseURL: defaultBaseURL,
	}
}

// NewWithURL creates a new RCI client with a custom base URL.
// Intended for tests that point to an httptest.Server.
func NewWithURL(baseURL string) *Client {
	return &Client{
		http:    &http.Client{Timeout: defaultTimeout},
		baseURL: baseURL,
	}
}

// Get performs an HTTP GET to /rci/{path} and decodes JSON into dst.
func (c *Client) Get(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("rci GET %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rci GET %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rci GET %s: read: %w", path, err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("rci GET %s: decode: %w", path, err)
	}
	return nil
}

// GetRaw performs an HTTP GET and returns raw response bytes.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rci GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rci GET %s: status %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Post sends a single JSON payload via POST to /rci/.
func (c *Client) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	return c.postJSON(ctx, payload)
}

// PostBatch sends a JSON array of commands via POST to /rci/.
// Returns an array of responses (one per command, same order).
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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, fmt.Errorf("rci POST: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/", &buf)
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
		return nil, fmt.Errorf("rci POST: status %d: %s", resp.StatusCode, string(data))
	}

	// NDMS returns HTTP 200 even on errors — check body.
	if errMsg := ExtractError(data); errMsg != "" {
		return json.RawMessage(data), fmt.Errorf("rci POST: %s", errMsg)
	}

	return json.RawMessage(data), nil
}
