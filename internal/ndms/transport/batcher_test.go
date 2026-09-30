package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestBatcher constructs a Batcher backed by a custom HTTP handler
// (mock NDMS). Returns the batcher and a cleanup func.
func newTestBatcher(t *testing.T, handler http.HandlerFunc, window time.Duration) (*Batcher, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	addr := strings.TrimPrefix(srv.URL, "http://")
	cli := NewWithURL("http://"+addr, NewSemaphore(30))
	b := newBatcher(cli, window, 64, 256)
	b.Start()
	cleanup := func() {
		b.Close()
		srv.Close()
	}
	return b, cleanup
}

// echoBatchHandler returns array of `[{"echo": <pathN>}]` for each item
// in the POST batch — позволяет тестам verify ordering и distribution.
func echoBatchHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			http.Error(w, "bad batch", http.StatusBadRequest)
			return
		}
		responses := make([]json.RawMessage, len(batch))
		for i, item := range batch {
			responses[i] = json.RawMessage(`{"echo":` + string(item) + `}`)
		}
		out, _ := json.Marshal(responses)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	}
}

func TestBatcher_MultipleSubmits_OneBatch(t *testing.T) {
	var batchCount atomic.Uint32
	handler := func(w http.ResponseWriter, r *http.Request) {
		batchCount.Add(1)
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		responses := make([]json.RawMessage, len(batch))
		for i := range batch {
			responses[i] = json.RawMessage(`{"ok":true,"i":` + strconv.Itoa(i) + `}`)
		}
		out, _ := json.Marshal(responses)
		_, _ = w.Write(out)
	}
	b, cleanup := newTestBatcher(t, handler, 30*time.Millisecond)
	defer cleanup()

	ctx := context.Background()
	results := make(chan error, 5)
	for _, p := range []string{
		"/show/interface/",
		"/show/sc/dns-proxy/route",
		"/show/rc/object-group/fqdn",
		"/show/running-config",
		"/show/ip/route",
	} {
		go func(path string) {
			_, err := b.Submit(ctx, path)
			results <- err
		}(p)
	}
	for i := 0; i < 5; i++ {
		if err := <-results; err != nil {
			t.Errorf("submit %d err: %v", i, err)
		}
	}
	if got := batchCount.Load(); got != 1 {
		t.Errorf("HTTP POST count = %d, want 1 (all in one batch)", got)
	}
}

func TestBatcher_CoalescesIdenticalPaths(t *testing.T) {
	var batchSizes []int
	var mu sync.Mutex
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		mu.Lock()
		batchSizes = append(batchSizes, len(batch))
		mu.Unlock()
		responses := make([]json.RawMessage, len(batch))
		for i := range batch {
			responses[i] = json.RawMessage(`{"data":"x"}`)
		}
		out, _ := json.Marshal(responses)
		_, _ = w.Write(out)
	}
	b, cleanup := newTestBatcher(t, handler, 30*time.Millisecond)
	defer cleanup()

	ctx := context.Background()
	results := make(chan []byte, 5)
	for i := 0; i < 5; i++ {
		go func() {
			body, _ := b.Submit(ctx, "/show/interface/")
			results <- body
		}()
	}
	bodies := make([][]byte, 0, 5)
	for i := 0; i < 5; i++ {
		bodies = append(bodies, <-results)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batchSizes) != 1 {
		t.Errorf("HTTP POST count = %d, want 1", len(batchSizes))
	}
	if batchSizes[0] != 1 {
		t.Errorf("batch size = %d, want 1 (coalesced)", batchSizes[0])
	}
	// All 5 callers should get the same body.
	for i, b := range bodies {
		if string(b) != string(bodies[0]) {
			t.Errorf("body[%d] != body[0]: %q vs %q", i, b, bodies[0])
		}
	}
}

func TestBatcher_MaxBatchTriggersImmediateFlush(t *testing.T) {
	var batchSizes []int
	var mu sync.Mutex
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		mu.Lock()
		batchSizes = append(batchSizes, len(batch))
		mu.Unlock()
		responses := make([]json.RawMessage, len(batch))
		for i := range batch {
			responses[i] = json.RawMessage(`{}`)
		}
		out, _ := json.Marshal(responses)
		_, _ = w.Write(out)
	}
	// Long window — only maxBatch should trigger flush.
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	cli := NewWithURL(srv.URL, NewSemaphore(30))
	b := newBatcher(cli, 5*time.Second, 4, 256) // maxBatch=4
	b.Start()
	defer b.Close()

	ctx := context.Background()
	start := time.Now()
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func(i int) {
			_, err := b.Submit(ctx, fmt.Sprintf("/show/path-%d/", i))
			results <- err
		}(i)
	}
	for i := 0; i < 4; i++ {
		<-results
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Errorf("flush took %v — should be immediate via maxBatch trigger", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batchSizes) != 1 || batchSizes[0] != 4 {
		t.Errorf("batches = %v, want [4]", batchSizes)
	}
}

func TestBatcher_SingleSubmit_FlushesAfterWindow(t *testing.T) {
	b, cleanup := newTestBatcher(t, echoBatchHandler(t), 10*time.Millisecond)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	body, err := b.Submit(ctx, "/show/interface/")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !strings.Contains(string(body), `"echo"`) {
		t.Errorf("body = %s, want contains echo wrapper", string(body))
	}
}

func TestBatcher_CancelledSubmitsFilteredPreFlush(t *testing.T) {
	var batchSizes []int
	var mu sync.Mutex
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		mu.Lock()
		batchSizes = append(batchSizes, len(batch))
		mu.Unlock()
		responses := make([]json.RawMessage, len(batch))
		for i := range batch {
			responses[i] = json.RawMessage(`{}`)
		}
		out, _ := json.Marshal(responses)
		_, _ = w.Write(out)
	}
	b, cleanup := newTestBatcher(t, handler, 50*time.Millisecond)
	defer cleanup()

	// 3 cancellable submits (will be cancelled), 2 stable submits.
	var wg sync.WaitGroup
	cancellableCtxs := make([]context.CancelFunc, 3)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancellableCtxs[i] = cancel
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = b.Submit(ctx, fmt.Sprintf("/show/cancelled-%d/", i))
		}(i)
	}
	stableResults := make(chan []byte, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			body, _ := b.Submit(context.Background(), fmt.Sprintf("/show/stable-%d/", i))
			stableResults <- body
		}(i)
	}

	// Sleep so all 5 enqueue, then cancel 3 before window fires.
	time.Sleep(10 * time.Millisecond)
	for _, c := range cancellableCtxs {
		c()
	}

	// Wait for stable results.
	for i := 0; i < 2; i++ {
		<-stableResults
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(batchSizes) != 1 || batchSizes[0] != 2 {
		t.Errorf("batch sizes = %v, want [2] (3 cancelled dropped)", batchSizes)
	}
	if got := b.cancelledDrops.Load(); got != 3 {
		t.Errorf("cancelledDrops = %d, want 3", got)
	}
}

func TestBatcher_AllCancelled_NoPostMade(t *testing.T) {
	var posted atomic.Uint32
	handler := func(w http.ResponseWriter, r *http.Request) {
		posted.Add(1)
		_, _ = w.Write([]byte("[]"))
	}
	b, cleanup := newTestBatcher(t, handler, 50*time.Millisecond)
	defer cleanup()

	var wg sync.WaitGroup
	cancels := make([]context.CancelFunc, 3)
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = b.Submit(ctx, fmt.Sprintf("/show/x-%d/", i))
		}(i)
	}
	time.Sleep(5 * time.Millisecond) // give time to enqueue
	for _, c := range cancels {
		c()
	}
	time.Sleep(100 * time.Millisecond) // wait past window
	wg.Wait()

	if got := posted.Load(); got != 0 {
		t.Errorf("HTTP POST count = %d, want 0 (all cancelled)", got)
	}
}

func TestBatcher_TransportError_AllReceiversGetError(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}
	b, cleanup := newTestBatcher(t, handler, 10*time.Millisecond)
	defer cleanup()

	ctx := context.Background()
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			_, err := b.Submit(ctx, fmt.Sprintf("/show/x-%d/", i))
			errs <- err
		}(i)
	}
	for i := 0; i < 3; i++ {
		err := <-errs
		if err == nil {
			t.Errorf("submit %d: want error, got nil", i)
			continue
		}
		if !strings.Contains(err.Error(), "rci batch") && !strings.Contains(err.Error(), "500") {
			t.Errorf("submit %d: err = %v, want batch/transport wrapper", i, err)
		}
	}
}

func TestBatcher_ResponseNotArray_AllFailWithShape(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"not":"an array"}`))
	}
	b, cleanup := newTestBatcher(t, handler, 10*time.Millisecond)
	defer cleanup()

	_, err := b.Submit(context.Background(), "/show/x/")
	if !errors.Is(err, ErrBatchResponseShape) {
		t.Errorf("err = %v, want ErrBatchResponseShape", err)
	}
}

func TestBatcher_ResponseLengthMismatch_AllFailSentinel(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		// Always return single-element array regardless of batch size.
		_, _ = w.Write([]byte(`[{"one":"only"}]`))
	}
	b, cleanup := newTestBatcher(t, handler, 30*time.Millisecond)
	defer cleanup()

	ctx := context.Background()
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, err := b.Submit(ctx, fmt.Sprintf("/show/x-%d/", i))
			errs <- err
		}(i)
	}
	for i := 0; i < 2; i++ {
		err := <-errs
		if !errors.Is(err, ErrBatchLengthMismatch) {
			t.Errorf("err = %v, want ErrBatchLengthMismatch", err)
		}
	}
}

func TestBatcher_PerItemNDMSError_OnlyThatReceiverFails(t *testing.T) {
	// Handler inspects each batch element's raw JSON: if it contains the
	// string "err-path" it returns an NDMS error envelope, otherwise ok.
	handler := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		responses := make([]json.RawMessage, len(batch))
		for i, item := range batch {
			if strings.Contains(string(item), "err-path") {
				// NDMS error envelope (per errors.go:ExtractError: status==error + message).
				responses[i] = json.RawMessage(`{"status":"error","message":"some error"}`)
			} else {
				responses[i] = json.RawMessage(`{"data":"ok"}`)
			}
		}
		out, _ := json.Marshal(responses)
		_, _ = w.Write(out)
	}
	b, cleanup := newTestBatcher(t, handler, 30*time.Millisecond)
	defer cleanup()

	ctx := context.Background()
	var (
		errOK0, errOK1, errFail error
		wg                      sync.WaitGroup
	)
	wg.Add(3)
	go func() { defer wg.Done(); _, errOK0 = b.Submit(ctx, "/show/ok-path-0/") }()
	go func() { defer wg.Done(); _, errFail = b.Submit(ctx, "/show/err-path/") }()
	go func() { defer wg.Done(); _, errOK1 = b.Submit(ctx, "/show/ok-path-1/") }()
	wg.Wait()

	if errOK0 != nil {
		t.Errorf("ok-path-0: err = %v, want nil", errOK0)
	}
	if errOK1 != nil {
		t.Errorf("ok-path-1: err = %v, want nil", errOK1)
	}
	if errFail == nil {
		t.Errorf("err-path: want error, got nil")
	} else {
		var ndmsErr *NDMSAppError
		if !errors.As(errFail, &ndmsErr) {
			t.Errorf("err-path: err = %v, want *NDMSAppError", errFail)
		}
	}
}

func TestBatcher_ContextCancelledInSubmit_ReturnsCtxErr(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // slow NDMS
		_, _ = w.Write([]byte(`[{}]`))
	}
	b, cleanup := newTestBatcher(t, handler, 10*time.Millisecond)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := b.Submit(ctx, "/show/slow/")
		errs <- err
	}()
	time.Sleep(20 * time.Millisecond) // ensure enqueued
	cancel()

	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit didn't return after ctx cancel")
	}
}

func TestBatcher_Close_FinalFlushOfPending(t *testing.T) {
	var posted atomic.Uint32
	handler := func(w http.ResponseWriter, r *http.Request) {
		posted.Add(1)
		body, _ := io.ReadAll(r.Body)
		var batch []json.RawMessage
		_ = json.Unmarshal(body, &batch)
		responses := make([]json.RawMessage, len(batch))
		for i := range batch {
			responses[i] = json.RawMessage(`{}`)
		}
		out, _ := json.Marshal(responses)
		_, _ = w.Write(out)
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	cli := NewWithURL(srv.URL, NewSemaphore(30))
	b := newBatcher(cli, 5*time.Second, 64, 256) // long window
	b.Start()

	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func(i int) {
			_, err := b.Submit(context.Background(), fmt.Sprintf("/show/x-%d/", i))
			results <- err
		}(i)
	}
	time.Sleep(20 * time.Millisecond) // enqueued
	b.Close()                         // should trigger final flush

	for i := 0; i < 3; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("submit %d err: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("submit %d didn't complete after Close", i)
		}
	}
	if got := posted.Load(); got != 1 {
		t.Errorf("HTTP POST count = %d, want 1 (final flush)", got)
	}
}

func TestBatcher_SubmitAfterClose_ReturnsErrClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	cli := NewWithURL(srv.URL, NewSemaphore(30))
	b := newBatcher(cli, 10*time.Millisecond, 64, 256)
	b.Start()
	b.Close()

	_, err := b.Submit(context.Background(), "/show/x/")
	if !errors.Is(err, ErrBatcherClosed) {
		t.Errorf("err = %v, want ErrBatcherClosed", err)
	}
}
