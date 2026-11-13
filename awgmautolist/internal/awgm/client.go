// Package awgm contains the functional AWGM API client used by the daemon.
package awgm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

type Client struct {
	base      string
	apiKey    string
	http      *http.Client
	indexHTTP *http.Client
}

type APIError struct {
	Status  int
	Code    string
	Message string
	Body    string
}

func (e *APIError) Error() string {
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("AWGM %s: %s", e.Code, e.Message)
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("AWGM HTTP status %d", e.Status)
}

func NewClient(baseURL, apiKey string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("invalid AWGM base URL %q", baseURL)
	}
	return &Client{
		base: baseURL, apiKey: apiKey,
		http: &http.Client{Timeout: 20 * time.Second},
		// Cold generation can legitimately decompile geosite-scale sets on a
		// router. Context cancellation and AWGM's decompile budget own the
		// lifetime; there is no artificial short coverage deadline.
		indexHTTP: &http.Client{},
	}, nil
}

type responseEnvelope struct {
	Success bool            `json:"success"`
	Error   bool            `json:"error"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Code    string          `json:"code"`
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, input, out any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
	}
	return c.do(ctx, http.MethodPost, path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	if !strings.HasPrefix(path, "/api/") {
		return fmt.Errorf("invalid AWGM API path %q", path)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.http
	if path == "/api/singbox/router/rulesets/indexed-match" || path == "/api/singbox/router/rulesets/indexed-snapshot" {
		httpClient = c.indexHTTP
	}
	if httpClient == nil {
		return fmt.Errorf("AWGM HTTP client is not initialized")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("AWGM response exceeds 8 MiB")
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return &APIError{Status: resp.StatusCode, Message: "invalid JSON response", Body: string(data)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Error || !envelope.Success {
		message := envelope.Message
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Code: envelope.Code, Message: message, Body: string(data)}
	}
	if out == nil {
		return nil
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return fmt.Errorf("AWGM %s returned missing data", path)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("AWGM %s data: %w", path, err)
	}
	return nil
}
