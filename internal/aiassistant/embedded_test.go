package aiassistant

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestEmbeddedManagerReusesHealthyExternalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{
		Enabled: true, Provider: "local_embedded", Model: "qwen",
		BaseURL:     "http://127.0.0.1:" + portText + "/v1",
		LocalEngine: &LocalEngineConfig{Port: port, ContextSize: 512, Threads: 1, AutoStopMinutes: 1},
	}); err != nil {
		t.Fatal(err)
	}

	manager := NewEmbeddedManager(store)
	if err := manager.EnsureRunning(context.Background()); err != nil {
		t.Fatalf("reuse healthy server: %v", err)
	}
	status := manager.Status()
	if !status.Running || status.Managed || status.PID != 0 {
		t.Fatalf("unexpected external status: %+v", status)
	}
}

func TestEmbeddedManagerConcurrentStatusDoesNotBlock(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewEmbeddedManager(store)
	// Status() returns immediately without blocking
	done := make(chan struct{})
	go func() {
		_ = manager.Status()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Status() blocked")
	}
}

