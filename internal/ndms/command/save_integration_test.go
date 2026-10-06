package command

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// fakeNDMS counts hits per category and returns canonical JSON shapes
// for save + running-config reads. The matchers are deliberately broad
// — they look for distinctive substrings rather than exact JSON, so
// minor payload-format drift doesn't break the test.
type fakeNDMS struct {
	saveHits int32
	rcHits   int32
}

func (f *fakeNDMS) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bs := string(body)
		path := r.URL.Path
		switch {
		// Save command: {"system":{"configuration":{"save":...}}}
		case strings.Contains(bs, `"save"`) && strings.Contains(bs, `"configuration"`):
			atomic.AddInt32(&f.saveHits, 1)
			// emulate slow flash write
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))

		// Running-config read: GET /show/running-config (path-based)
		// or POST with body containing "running-config" (defensive fallback).
		case strings.Contains(path, "running-config"),
			strings.Contains(bs, `"running-config"`):
			atomic.AddInt32(&f.rcHits, 1)
			w.WriteHeader(http.StatusOK)
			// Minimal shape RunningConfigStore.fetch() accepts: {"message":[...]}
			_, _ = w.Write([]byte(`{"message": []}`))

		default:
			// Unknown payload — surface as 400 so test author notices wiring drift.
			http.Error(w, "fakeNDMS: unexpected payload, path="+path+" body="+bs, http.StatusBadRequest)
		}
	}
}

func (f *fakeNDMS) SaveHits() int32 { return atomic.LoadInt32(&f.saveHits) }
func (f *fakeNDMS) RCHits() int32   { return atomic.LoadInt32(&f.rcHits) }

// intgNopPublisher is a no-op StatusPublisher for integration tests.
// Naming intg* avoids collision with any other nopPublisher in package.
type intgNopPublisher struct{}

func (intgNopPublisher) Publish(eventType string, data any) {}

func TestIntegration_SaveSettleInvalidatesRunningConfig(t *testing.T) {
	fake := &fakeNDMS{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	sem := transport.NewSemaphore(8)
	client := transport.NewWithURL(srv.URL, sem)
	rcStore := query.NewRunningConfigStore(client, query.NopLogger())

	settle := 80 * time.Millisecond
	sc := NewSaveCoordinator(
		client,
		intgNopPublisher{},
		5*time.Millisecond,  // debounce
		50*time.Millisecond, // maxWait
		settle,
		rcStore,
	)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Step 1: prime the cache via a first read.
	if _, err := rcStore.Lines(ctx); err != nil {
		t.Fatalf("first Lines: %v", err)
	}
	if got := fake.RCHits(); got != 1 {
		t.Fatalf("first Lines should hit NDMS once, got %d", got)
	}

	// Step 2: trigger save. Wait for: debounce + POST save (100ms slow)
	// + settle + safety margin.
	sc.Request()
	time.Sleep(500 * time.Millisecond)

	if got := fake.SaveHits(); got != 1 {
		t.Errorf("save POST hits: want 1, got %d", got)
	}

	// Step 3: re-read running-config — should bypass cache (invalidated
	// during settle).
	if _, err := rcStore.Lines(ctx); err != nil {
		t.Fatalf("second Lines: %v", err)
	}
	if got := fake.RCHits(); got != 2 {
		t.Errorf("second Lines should re-fetch (invalidate happened), got total RC hits %d", got)
	}
}

func TestIntegration_SaveSettleDisabled_NoInvalidate(t *testing.T) {
	fake := &fakeNDMS{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	sem := transport.NewSemaphore(8)
	client := transport.NewWithURL(srv.URL, sem)
	rcStore := query.NewRunningConfigStore(client, query.NopLogger())

	// settleDelay = 0 → settle disabled
	sc := NewSaveCoordinator(
		client,
		intgNopPublisher{},
		5*time.Millisecond,
		50*time.Millisecond,
		0, rcStore,
	)
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := rcStore.Lines(ctx); err != nil {
		t.Fatalf("first Lines: %v", err)
	}

	sc.Request()
	time.Sleep(400 * time.Millisecond)

	if got := fake.SaveHits(); got != 1 {
		t.Errorf("save hits: want 1, got %d", got)
	}

	// Second read — should be cache hit (no invalidate happened).
	if _, err := rcStore.Lines(ctx); err != nil {
		t.Fatalf("second Lines: %v", err)
	}
	if got := fake.RCHits(); got != 1 {
		t.Errorf("with settle disabled, second Lines should hit cache; total RC hits=%d (want 1)", got)
	}
}
