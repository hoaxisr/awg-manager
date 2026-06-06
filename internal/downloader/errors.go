package downloader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrorCode classifies download-route failures for API envelopes and UI
// humanization. Prefer reading codes via RouteErrorCode(err) instead of parsing
// error strings on the frontend.
type ErrorCode string

const (
	ErrCodeSingboxNotRunning   ErrorCode = "DOWNLOAD_SINGBOX_NOT_RUNNING"
	ErrCodeSingboxNotReady     ErrorCode = "DOWNLOAD_SINGBOX_NOT_READY"
	ErrCodeSingboxEgressFailed ErrorCode = "DOWNLOAD_SINGBOX_EGRESS_FAILED"
	ErrCodeAWGDown             ErrorCode = "DOWNLOAD_AWG_DOWN"
	ErrCodeTimeout             ErrorCode = "DOWNLOAD_TIMEOUT"
	ErrCodeNetwork             ErrorCode = "DOWNLOAD_NETWORK"
	ErrCodeRoute               ErrorCode = "DOWNLOAD_ROUTE"
)

// RouteError is a typed download failure with a stable machine code.
type RouteError struct {
	Code ErrorCode
	msg  string
	err  error
}

func newRouteError(code ErrorCode, msg string, err error) *RouteError {
	return &RouteError{Code: code, msg: msg, err: err}
}

func (e *RouteError) Error() string { return e.msg }
func (e *RouteError) Unwrap() error { return e.err }

// RouteErrorCode walks err (including wrapped errors) and returns the first
// RouteError code found, or "" when none is present.
func RouteErrorCode(err error) ErrorCode {
	for err != nil {
		var re *RouteError
		if errors.As(err, &re) {
			return re.Code
		}
		err = errors.Unwrap(err)
	}
	return ""
}

// APIErrorCode returns the envelope code for err. Falls back to fallback when
// err is not a RouteError.
func APIErrorCode(err error, fallback string) string {
	if code := RouteErrorCode(err); code != "" {
		return string(code)
	}
	return fallback
}

func wrapRequestError(route RouteInfo, err error) error {
	if err == nil {
		return nil
	}
	display := route.DisplayName()
	msg := fmt.Sprintf("download via %s: request failed: %v", display, err)
	return newRouteError(classifyRequestError(route, err), msg, err)
}

// WrapRequestError classifies an HTTP client failure for the given route.
func WrapRequestError(route RouteInfo, err error) error {
	return wrapRequestError(route, err)
}

func classifyRequestError(route RouteInfo, err error) ErrorCode {
	if isTimeout(err) {
		return ErrCodeTimeout
	}
	kind := strings.TrimSpace(route.Kind)
	switch kind {
	case "singbox", "subscription":
		if isLocalProxyDialError(err) {
			return ErrCodeSingboxNotReady
		}
		if isTransportDrop(err) {
			return ErrCodeSingboxEgressFailed
		}
	case "awg":
		if isTransportDrop(err) {
			return ErrCodeNetwork
		}
	}
	if isTransportDrop(err) {
		return ErrCodeNetwork
	}
	return ErrCodeNetwork
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded")
}

func isTransportDrop(err error) bool {
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection broken") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "malformed http response") ||
		strings.Contains(msg, "ошибка сети")
}

func isLocalProxyDialError(err error) bool {
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "connection refused") {
		return false
	}
	return strings.Contains(msg, localProxyHost) ||
		strings.Contains(msg, "127.0.0.1") ||
		strings.Contains(msg, "localhost")
}
