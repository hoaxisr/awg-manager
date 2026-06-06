package downloader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpdownload"
)

type Request struct {
	Purpose       string
	URL           string
	Method        string
	Headers       http.Header
	Body          []byte
	Timeout       time.Duration
	MaxBodyBytes  int64
	UserAgent     string
	RouteOverride *Route
	CheckRedirect func(req *http.Request, via []*http.Request) error
	AllowedStatus []int
}

type ResponseMeta struct {
	StatusCode    int
	ContentLength int64
	ContentType   string
	Headers       http.Header
	Route         RouteInfo
}

type FileRequest struct {
	Request
	DestPath     string
	TempPath     string
	MaxFileBytes int64
	Mode         os.FileMode
	Atomic       bool
	Progress     func(downloaded, total int64)
}

type FileResult struct {
	Path  string
	Size  int64
	Route RouteInfo
}

func (s *Service) ReadAll(ctx context.Context, req Request) ([]byte, ResponseMeta, error) {
	if strings.TrimSpace(req.URL) == "" {
		return nil, ResponseMeta{}, fmt.Errorf("download URL is required")
	}
	if req.MaxBodyBytes <= 0 {
		return nil, ResponseMeta{}, fmt.Errorf("max body bytes must be > 0")
	}

	lease, err := s.ResolveClient(ctx, req.RouteOverride)
	if err != nil {
		return nil, ResponseMeta{}, fmt.Errorf("resolve download route: %w", err)
	}
	defer lease.Close()
	meta := ResponseMeta{Route: lease.Route}

	requestCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if req.Body != nil {
		requestBody = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(requestCtx, method, req.URL, requestBody)
	if err != nil {
		return nil, meta, fmt.Errorf("download via %s: build request: %w", lease.Route.DisplayName(), err)
	}
	if req.UserAgent != "" {
		httpReq.Header.Set("User-Agent", req.UserAgent)
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	if strings.EqualFold(httpReq.Header.Get("Connection"), "close") {
		httpReq.Close = true
	}

	client := lease.Client
	if req.CheckRedirect != nil {
		cloned := *lease.Client
		cloned.CheckRedirect = req.CheckRedirect
		client = &cloned
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, meta, wrapRequestError(lease.Route, err)
	}
	defer resp.Body.Close()
	meta.StatusCode = resp.StatusCode
	meta.ContentLength = resp.ContentLength
	meta.ContentType = resp.Header.Get("Content-Type")
	meta.Headers = resp.Header.Clone()

	if !statusAllowed(resp.StatusCode, req.AllowedStatus) {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, meta, fmt.Errorf("download via %s: status %d: %s", lease.Route.DisplayName(), resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	limited := io.LimitReader(resp.Body, req.MaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, meta, fmt.Errorf("download via %s: read body: %w", lease.Route.DisplayName(), err)
	}
	if int64(len(body)) > req.MaxBodyBytes {
		return nil, meta, fmt.Errorf("download via %s: body exceeds limit (%d bytes)", lease.Route.DisplayName(), req.MaxBodyBytes)
	}
	return body, meta, nil
}

func (s *Service) DownloadFile(ctx context.Context, req FileRequest) (FileResult, error) {
	if strings.TrimSpace(req.URL) == "" {
		return FileResult{}, fmt.Errorf("download URL is required")
	}
	if strings.TrimSpace(req.DestPath) == "" {
		return FileResult{}, fmt.Errorf("destination path is required")
	}
	if req.MaxFileBytes <= 0 {
		return FileResult{}, fmt.Errorf("max file bytes must be > 0")
	}

	lease, err := s.ResolveClient(ctx, req.RouteOverride)
	if err != nil {
		return FileResult{}, fmt.Errorf("resolve download route: %w", err)
	}
	defer lease.Close()
	result := FileResult{Route: lease.Route}

	requestCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	method := strings.TrimSpace(req.Method)
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if req.Body != nil {
		requestBody = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(requestCtx, method, req.URL, requestBody)
	if err != nil {
		return result, fmt.Errorf("download via %s: build request: %w", lease.Route.DisplayName(), err)
	}
	if req.UserAgent != "" {
		httpReq.Header.Set("User-Agent", req.UserAgent)
	}
	for k, vals := range req.Headers {
		for _, v := range vals {
			httpReq.Header.Add(k, v)
		}
	}
	if strings.EqualFold(httpReq.Header.Get("Connection"), "close") {
		httpReq.Close = true
	}

	client := lease.Client
	if req.CheckRedirect != nil {
		cloned := *lease.Client
		cloned.CheckRedirect = req.CheckRedirect
		client = &cloned
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return result, wrapRequestError(lease.Route, err)
	}
	defer resp.Body.Close()

	if !statusAllowed(resp.StatusCode, req.AllowedStatus) {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return result, fmt.Errorf("download via %s: status %d: %s", lease.Route.DisplayName(), resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if resp.ContentLength > req.MaxFileBytes {
		return result, fmt.Errorf("download via %s: content-length %d exceeds limit %d", lease.Route.DisplayName(), resp.ContentLength, req.MaxFileBytes)
	}

	destPath := req.DestPath
	tmpPath := req.TempPath
	if tmpPath == "" {
		tmpPath = fmt.Sprintf("%s.tmp-%d", destPath, time.Now().UnixNano())
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return result, fmt.Errorf("download via %s: mkdir destination: %w", lease.Route.DisplayName(), err)
	}
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o755); err != nil {
		return result, fmt.Errorf("download via %s: mkdir temp: %w", lease.Route.DisplayName(), err)
	}

	mode := req.Mode
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return result, fmt.Errorf("download via %s: create temp file: %w", lease.Route.DisplayName(), err)
	}
	closed := false
	success := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	src := io.Reader(resp.Body)
	if req.Progress != nil {
		src = httpdownload.NewReader(resp.Body, resp.ContentLength, req.Progress)
	}
	written, err := io.Copy(out, io.LimitReader(src, req.MaxFileBytes+1))
	if err != nil {
		result.Size = written
		return result, fmt.Errorf("download via %s: write file: %w", lease.Route.DisplayName(), err)
	}
	if written > req.MaxFileBytes {
		result.Size = written
		return result, fmt.Errorf("download via %s: file exceeds limit (%d bytes)", lease.Route.DisplayName(), req.MaxFileBytes)
	}
	if err := out.Sync(); err != nil {
		result.Size = written
		return result, fmt.Errorf("download via %s: sync temp file: %w", lease.Route.DisplayName(), err)
	}
	if err := out.Close(); err != nil {
		result.Size = written
		return result, fmt.Errorf("download via %s: close temp file: %w", lease.Route.DisplayName(), err)
	}
	closed = true

	if req.Atomic {
		if err := os.Rename(tmpPath, destPath); err != nil {
			result.Size = written
			return result, fmt.Errorf("download via %s: atomic move: %w", lease.Route.DisplayName(), err)
		}
	} else if tmpPath != destPath {
		if err := os.Rename(tmpPath, destPath); err != nil {
			result.Size = written
			return result, fmt.Errorf("download via %s: move file: %w", lease.Route.DisplayName(), err)
		}
	}
	success = true
	result.Path = destPath
	result.Size = written
	return result, nil
}

func statusAllowed(status int, allowed []int) bool {
	if len(allowed) == 0 {
		return status == http.StatusOK
	}
	for _, v := range allowed {
		if v == status {
			return true
		}
	}
	return false
}
