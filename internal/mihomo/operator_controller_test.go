package mihomo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperator_RefreshProvider_Success(t *testing.T) {
	var requestedPath string
	var authHeader string
	var requestsCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestsCount, 1)
		requestedPath = r.URL.RequestURI()
		authHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	op := NewOperator("test-bin", filepath.Join(t.TempDir(), "mihomo"))
	op.controllerAddr = u.Host
	op.controllerSecret = "super-secret-token"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = op.RefreshProvider(ctx, "provider with spaces & special")
	if err != nil {
		t.Fatalf("expected RefreshProvider success, got: %v", err)
	}

	if atomic.LoadInt32(&requestsCount) != 1 {
		t.Fatalf("expected 1 request, got: %d", requestsCount)
	}

	expectedPath := "/providers/proxies/" + url.PathEscape("provider with spaces & special")
	if requestedPath != expectedPath {
		t.Fatalf("expected path %q, got: %q", expectedPath, requestedPath)
	}

	expectedAuth := "Bearer super-secret-token"
	if authHeader != expectedAuth {
		t.Fatalf("expected auth %q, got: %q", expectedAuth, authHeader)
	}
}

func TestOperator_RefreshProvider_Non2xxReturnsRedactedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	secret := "secret-key-to-redact"
	op := NewOperator("test-bin", filepath.Join(t.TempDir(), "mihomo"))
	op.controllerAddr = u.Host
	op.controllerSecret = secret

	err = op.RefreshProvider(context.Background(), "my-provider")
	if err == nil {
		t.Fatal("expected error on 400 Bad Request, got nil")
	}

	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error must not expose controller secret, got: %v", err)
	}
}

func TestOperator_RefreshProvider_ContextCancellation(t *testing.T) {
	blockCh := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockCh
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(blockCh)

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	op := NewOperator("test-bin", filepath.Join(t.TempDir(), "mihomo"))
	op.controllerAddr = u.Host

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err = op.RefreshProvider(ctx, "my-provider")
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}

func TestOperator_ControllerTarget(t *testing.T) {
	op := NewOperator("test-bin", filepath.Join(t.TempDir(), "mihomo"))
	op.controllerAddr = "127.0.0.1:9191"
	op.controllerSecret = "my-secret"

	addr, secret := op.ControllerTarget()
	if addr != "127.0.0.1:9191" {
		t.Fatalf("expected addr 127.0.0.1:9191, got: %q", addr)
	}
	if secret != "my-secret" {
		t.Fatalf("expected secret my-secret, got: %q", secret)
	}
}
