package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoverageIndexHeadlessRuleParity(t *testing.T) {
	entry, err := compileCoverageIndexEntry([]map[string]any{
		{
			"domain":         []any{"Example.COM"},
			"domain_suffix":  []any{".example.org"},
			"domain_keyword": []any{"Video"},
			"domain_regex":   []any{`^cdn\.`},
			"ip_cidr":        []any{"192.0.2.0/24", "2001:db8::/32", "198.51.100.7"},
		},
		{"type": "logical", "mode": "and", "rules": []any{
			map[string]any{"domain_suffix": []any{"example.net"}},
			map[string]any{"domain_keyword": []any{"login"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches := []string{
		"example.com", "EXAMPLE.COM.", "example.org", "a.b.example.org",
		"my-video-site.test", "cdn.example", "192.0.2.44", "2001:db8::1",
		"198.51.100.7", "login.example.net",
	}
	for _, input := range matches {
		if !entry.match(input) {
			t.Errorf("expected match: %s", input)
		}
	}
	negatives := []string{
		"www.example.com", "badexample.org", "example.net", "other.example.net",
		"192.0.3.1", "2001:db9::1", "198.51.100.8", "8.8.8.8", "100.64.0.1",
		"10.0.0.1", "127.0.0.1", "224.0.0.1",
	}
	for _, input := range negatives {
		if entry.match(input) {
			t.Errorf("unexpected match: %s", input)
		}
	}

	inverted, err := compileCoverageIndexEntry([]map[string]any{{"domain": []any{"blocked.example"}, "invert": true}})
	if err != nil {
		t.Fatal(err)
	}
	if inverted.match("blocked.example") || !inverted.match("allowed.example") {
		t.Fatal("invert semantics differ")
	}
	contextual, err := compileCoverageIndexEntry([]map[string]any{
		{"ip_cidr": []any{"34.0.0.0/15"}},
		{"network": "udp", "ip_cidr": []any{"35.192.0.0/11"}, "port_range": []any{"50000:50099"}},
	})
	if err != nil || !contextual.match("34.0.0.1") || contextual.match("35.192.0.1") {
		t.Fatalf("network/port context semantics differ: %v", err)
	}
}

func TestCoverageIndexRejectsUnsupportedGeneration(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"unknown field":  {"source_ip_cidr": []any{"192.0.2.0/24"}},
		"not headless":   {"ip_is_private": true},
		"invalid regex":  {"domain_regex": []any{"["}},
		"invalid CIDR":   {"ip_cidr": []any{"192.0.2.0/99"}},
		"empty logical":  {"type": "logical", "mode": "or", "rules": []any{}},
		"bad logical":    {"type": "logical", "mode": "xor", "rules": []any{}},
		"default nested": {"rules": []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := compileCoverageIndexEntry([]map[string]any{raw}); err == nil {
				t.Fatal("expected failed generation fixture")
			}
		})
	}
}

func testCoverageBuilder(calls *atomic.Int32, started chan<- int, block <-chan struct{}) coverageIndexBuildFunc {
	return func(ctx context.Context, tags []string) (*coverageIndexSnapshot, error) {
		id := int(calls.Add(1))
		if started != nil {
			started <- id
		}
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		requested := map[string]struct{}{"base": {}}
		entries := map[string]*coverageIndexEntry{}
		base, _ := compileCoverageIndexEntry([]map[string]any{{"domain_suffix": []any{"example.com"}}})
		entries["base"] = base
		for _, tag := range tags {
			requested[tag] = struct{}{}
			entries[tag] = base
		}
		return &coverageIndexSnapshot{slot: "router", entries: entries, requested: requested}, nil
	}
}

func TestCoverageIndexWarmReadsDoNotRebuild(t *testing.T) {
	var calls atomic.Int32
	idx := newCoverageIndex(testCoverageBuilder(&calls, nil, nil), nil)
	if _, err := idx.snapshotRead(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 100; n++ {
		input := "missing.example"
		want := false
		if n%2 == 0 {
			input = "www.example.com"
			want = true
		}
		got, err := idx.indexedMatch(context.Background(), input, []string{"base"})
		if err != nil || got.Results[0].Matched != want || got.Generation == 0 {
			t.Fatalf("match=%+v err=%v", got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("warm reads rebuilt %d times", calls.Load())
	}
}

func TestCoverageIndexExclusionReplacement(t *testing.T) {
	var calls atomic.Int32
	idx := newCoverageIndex(testCoverageBuilder(&calls, nil, nil), nil)
	first, err := idx.snapshotRead(context.Background(), []string{"geo"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := idx.snapshotRead(context.Background(), []string{"geo"})
	if err != nil || again.Generation != first.Generation {
		t.Fatalf("same exclusions rebuilt: %+v %+v %v", first, again, err)
	}
	removed, err := idx.snapshotRead(context.Background(), nil)
	if err != nil || removed.Generation == first.Generation {
		t.Fatalf("removal did not create generation: %+v %+v %v", first, removed, err)
	}
	idx.mu.Lock()
	_, retained := idx.snapshot.entries["geo"]
	idx.mu.Unlock()
	if retained {
		t.Fatal("removed exclusion remained in working set")
	}
	if calls.Load() != 2 {
		t.Fatalf("builds=%d, want 2", calls.Load())
	}
}

func TestCoverageIndexFailureIsFailClosedAndRetryable(t *testing.T) {
	var calls atomic.Int32
	idx := newCoverageIndex(func(context.Context, []string) (*coverageIndexSnapshot, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("corrupt source")
		}
		return testCoverageBuilder(&atomic.Int32{}, nil, nil)(context.Background(), nil)
	}, nil)
	if _, err := idx.snapshotRead(context.Background(), nil); err == nil {
		t.Fatal("corrupt generation became a negative result")
	}
	if _, err := idx.snapshotRead(context.Background(), nil); err == nil {
		t.Fatal("persistent failure bypassed retry cooldown")
	}
	if calls.Load() != 1 {
		t.Fatalf("cooldown launched %d builds", calls.Load())
	}
	idx.invalidate() // explicit content/apply invalidation forces a fresh retry
	if _, err := idx.snapshotRead(context.Background(), nil); err != nil {
		t.Fatalf("invalidation did not retry: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("builds=%d", calls.Load())
	}
}

func TestCoverageIndexLatestGenerationWins(t *testing.T) {
	var calls atomic.Int32
	started := make(chan int, 2)
	idx := newCoverageIndex(func(ctx context.Context, tags []string) (*coverageIndexSnapshot, error) {
		id := calls.Add(1)
		started <- int(id)
		if id == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return testCoverageBuilder(&atomic.Int32{}, nil, nil)(ctx, tags)
	}, nil)
	resultCh := make(chan CoverageSnapshot, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := idx.snapshotRead(context.Background(), nil)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()
	if id := <-started; id != 1 {
		t.Fatalf("first build=%d", id)
	}
	idx.invalidate()
	if id := <-started; id != 2 {
		t.Fatalf("second build=%d", id)
	}
	select {
	case result := <-resultCh:
		if result.Generation != 2 {
			t.Fatalf("published generation=%d, want latest 2", result.Generation)
		}
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("latest generation did not finish")
	}
}

func TestCoverageIndexUpdatesBeforeChecks(t *testing.T) {
	var calls atomic.Int32
	secondStarted := make(chan struct{})
	allowSecond := make(chan struct{})
	idx := newCoverageIndex(func(ctx context.Context, tags []string) (*coverageIndexSnapshot, error) {
		if calls.Add(1) == 2 {
			close(secondStarted)
			select {
			case <-allowSecond:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return testCoverageBuilder(&atomic.Int32{}, nil, nil)(ctx, tags)
	}, nil)
	if _, err := idx.snapshotRead(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		readDone <- idx.use(context.Background(), nil, false, func(*coverageIndexSnapshot) {
			close(readStarted)
			<-releaseRead
		})
	}()
	<-readStarted
	idx.invalidate()
	newRead := make(chan error, 1)
	go func() {
		_, err := idx.indexedMatch(context.Background(), "www.example.com", []string{"base"})
		newRead <- err
	}()
	select {
	case <-secondStarted:
		t.Fatal("update started before the active read finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseRead)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("update did not start")
	}
	select {
	case err := <-newRead:
		t.Fatalf("new read overtook update: %v", err)
	default:
	}
	close(allowSecond)
	if err := <-newRead; err != nil {
		t.Fatal(err)
	}
}

func TestCoverageIndexConcurrentReadersSingleFlightAndCancel(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan int, 1)
	idx := newCoverageIndex(testCoverageBuilder(&calls, started, release), nil)
	const readers = 24
	var wg sync.WaitGroup
	errCh := make(chan error, readers)
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := idx.snapshotRead(context.Background(), nil)
			errCh <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	for range readers {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent readers started %d builds", calls.Load())
	}

	block := make(chan struct{})
	idx = newCoverageIndex(testCoverageBuilder(&calls, nil, block), nil)
	ctx, cancel := context.WithCancel(context.Background())
	waitErr := make(chan error, 1)
	go func() {
		_, err := idx.snapshotRead(ctx, nil)
		waitErr <- err
	}()
	for idx.coverageBuildingCount() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-waitErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	close(block)
	idx.mu.Lock()
	done := idx.buildDone
	idx.mu.Unlock()
	<-done
}

func (i *coverageIndex) coverageBuildingCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.building {
		return 1
	}
	return 0
}

func TestCoverageIndexScheduledRefresh(t *testing.T) {
	var calls atomic.Int32
	idx := newCoverageIndex(func(ctx context.Context, tags []string) (*coverageIndexSnapshot, error) {
		snapshot, err := testCoverageBuilder(&calls, nil, nil)(ctx, tags)
		if snapshot != nil {
			snapshot.refreshAfter = 20 * time.Millisecond
		}
		return snapshot, err
	}, nil)
	if _, err := idx.snapshotRead(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		idx.mu.Lock()
		ready := !idx.building && idx.generation >= 2 && idx.snapshot != nil
		idx.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled refresh did not publish generation 2; builds=%d", calls.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCoverageRemotePublishReceivesGenerationContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":1,"rules":[]}`))
	}))
	defer server.Close()
	cache := newRuleSetCache(t.TempDir())
	var markers []bool
	cache.onPublish = func(ctx context.Context) {
		building, _ := ctx.Value(coverageIndexBuildContextMarker{}).(bool)
		markers = append(markers, building)
	}
	ctx := context.WithValue(context.Background(), coverageIndexBuildContextMarker{}, true)
	if _, err := cache.getOrDownloadContext(ctx, server.URL+"/one", "source", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.getOrDownload(server.URL+"/two", "source", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(markers) != 2 || !markers[0] || markers[1] {
		t.Fatalf("publish markers=%v", markers)
	}
	restarted := newRuleSetCache(cache.cacheDir)
	if _, ok := restarted.freshCachedFile(server.URL+"/one", "source"); !ok {
		t.Fatal("fresh on-disk file was not reusable after cache restart")
	}
	if _, ok := restarted.freshCachedFile(server.URL+"/missing", "source"); ok {
		t.Fatal("missing cache file reported fresh")
	}
}

func TestCoverageIndexErrorRedaction(t *testing.T) {
	err := redactCoverageIndexError(fmt.Errorf("download http://127.0.0.1/x?kind=geoip&token=secret-value&tag=RU failed"))
	if err == nil || strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "token=<redacted>") {
		t.Fatalf("unredacted error: %v", err)
	}
}
