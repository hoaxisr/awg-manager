package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchChangelog_CachesAcrossCalls(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("## [1.0.0] - 2026-01-01\n\n### Added\n- item\n"))
	}))
	defer srv.Close()

	c := newChangelogFetcher(srv.URL, 10*time.Minute)

	for i := 0; i < 3; i++ {
		entries, err := c.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := entries["1.0.0"]; !ok {
			t.Fatalf("iter %d: 1.0.0 missing", i)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected 1 HTTP hit, got %d", got)
	}
}

func TestFetchChangelog_ErrorNotCached(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newChangelogFetcher(srv.URL, 10*time.Minute)

	for i := 0; i < 2; i++ {
		if _, err := c.Fetch(context.Background()); err == nil {
			t.Fatalf("iter %d: expected error, got nil", i)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("errors must NOT populate cache; expected 2 HTTP hits, got %d", got)
	}
}
