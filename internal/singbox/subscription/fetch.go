package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchOpts tunes the HTTP fetcher. Zero values produce defaults.
type FetchOpts struct {
	Timeout      time.Duration // default 20s
	MaxBodyBytes int64         // default 5 MiB
	UserAgent    string        // default "awg-manager"
}

// forbiddenHeaders are managed by Go's http client and cannot be set by users.
var forbiddenHeaders = map[string]bool{
	"host":              true,
	"content-length":    true,
	"connection":        true,
	"transfer-encoding": true,
	"upgrade":           true,
}

// Fetch GETs the URL with default + custom headers. Forbidden headers are
// silently skipped — they're managed by net/http. Body is capped at
// MaxBodyBytes (5 MiB default) to defend against runaway providers.
func Fetch(url string, headers []Header, opts FetchOpts) ([]byte, string, error) {
	return FetchWithContext(context.Background(), url, headers, opts)
}

func FetchWithContext(ctx context.Context, url string, headers []Header, opts FetchOpts) ([]byte, string, error) {
	return FetchWithRequest(ctx, buildRequest(url, headers, opts))
}

func buildRequest(url string, headers []Header, opts FetchOpts) Request {
	if opts.Timeout == 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = 5 * 1024 * 1024
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "awg-manager"
	}

	reqHeaders := make(http.Header)
	ua := opts.UserAgent
	for _, h := range headers {
		if forbiddenHeaders[strings.ToLower(h.Name)] {
			continue
		}
		if strings.EqualFold(h.Name, "User-Agent") {
			ua = h.Value
			continue
		}
		reqHeaders.Set(h.Name, h.Value)
	}
	allowed := make([]int, 0, 200)
	for code := 200; code <= 399; code++ {
		allowed = append(allowed, code)
	}
	return Request{
		Method:       http.MethodGet,
		URL:          url,
		Headers:      reqHeaders,
		Timeout:      opts.Timeout,
		MaxBodyBytes: opts.MaxBodyBytes,
		UserAgent:    ua,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
		AllowedStatus: allowed,
	}
}

type Request struct {
	Method        string
	URL           string
	Headers       http.Header
	Timeout       time.Duration
	MaxBodyBytes  int64
	UserAgent     string
	CheckRedirect func(req *http.Request, via []*http.Request) error
	AllowedStatus []int
}

func FetchWithRequest(ctx context.Context, req Request) ([]byte, string, error) {
	client := &http.Client{Timeout: req.Timeout, CheckRedirect: req.CheckRedirect}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
	if err != nil {
		return nil, "", err
	}
	if req.UserAgent != "" {
		httpReq.Header.Set("User-Agent", req.UserAgent)
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	allowed := map[int]struct{}{}
	for _, code := range req.AllowedStatus {
		allowed[code] = struct{}{}
	}
	if _, ok := allowed[resp.StatusCode]; !ok {
		return nil, "", fmt.Errorf("fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, req.MaxBodyBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > req.MaxBodyBytes {
		return nil, "", fmt.Errorf("fetch: body exceeds %d bytes", req.MaxBodyBytes)
	}
	return body, resp.Header.Get("Content-Type"), nil
}
