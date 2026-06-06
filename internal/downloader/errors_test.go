package downloader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

func TestRouteErrorCode_RouteError(t *testing.T) {
	inner := errors.New("EOF")
	err := newRouteError(ErrCodeSingboxEgressFailed, "download via RelayCH (singbox): request failed: EOF", inner)
	if got := RouteErrorCode(err); got != ErrCodeSingboxEgressFailed {
		t.Fatalf("RouteErrorCode = %q, want %q", got, ErrCodeSingboxEgressFailed)
	}
	if got := RouteErrorCode(fmt.Errorf("wrap: %w", err)); got != ErrCodeSingboxEgressFailed {
		t.Fatalf("wrapped RouteErrorCode = %q", got)
	}
}

func TestClassifyRequestError_SingboxEgress(t *testing.T) {
	err := classifyRequestError(RouteInfo{Tag: "RelayCH", Kind: "singbox"}, errors.New("Get \"https://example.com\": EOF"))
	if err != ErrCodeSingboxEgressFailed {
		t.Fatalf("code = %q, want %q", err, ErrCodeSingboxEgressFailed)
	}
}

func TestClassifyRequestError_SingboxProxyNotReady(t *testing.T) {
	err := classifyRequestError(
		RouteInfo{Tag: "DE", Kind: "singbox"},
		errors.New(`Get "https://example.com": dial tcp 127.0.0.1:1080: connect: connection refused`),
	)
	if err != ErrCodeSingboxNotReady {
		t.Fatalf("code = %q, want %q", err, ErrCodeSingboxNotReady)
	}
}

func TestClassifyRequestError_DirectNetwork(t *testing.T) {
	err := classifyRequestError(RouteInfo{Tag: "direct", Kind: "direct"}, errors.New("connection reset by peer"))
	if err != ErrCodeNetwork {
		t.Fatalf("code = %q, want %q", err, ErrCodeNetwork)
	}
}

func TestClassifyRequestError_Timeout(t *testing.T) {
	err := classifyRequestError(RouteInfo{Tag: "DE", Kind: "singbox"}, context.DeadlineExceeded)
	if err != ErrCodeTimeout {
		t.Fatalf("code = %q, want %q", err, ErrCodeTimeout)
	}
}

func TestIsTransportDrop_EOF(t *testing.T) {
	if !isTransportDrop(net.ErrClosed) {
		t.Fatal("expected net.ErrClosed to be a transport drop")
	}
}
