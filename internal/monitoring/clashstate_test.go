package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClashProxiesServer responds to GET /proxies with the provided body.
func fakeClashProxiesServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/proxies") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestClashState_LatencyForOutbound_HappyPath(t *testing.T) {
	upstream := fakeClashProxiesServer(t, `{
		"proxies": {
			"vless-1": {"name":"vless-1","type":"VLESS","history":[{"delay":45}]},
			"vless-2": {"name":"vless-2","type":"VLESS","history":[{"delay":78},{"delay":92}]}
		}
	}`)
	t.Cleanup(upstream.Close)

	cs := NewClashState(func() string { return upstream.URL }, upstream.Client())
	cs.cacheTTL = 1 * time.Second

	delay, ok := cs.LatencyForOutbound(context.Background(), "vless-1")
	if !ok || delay != 45 {
		t.Fatalf("vless-1: got (%d, %v), want (45, true)", delay, ok)
	}
	delay, ok = cs.LatencyForOutbound(context.Background(), "vless-2")
	if !ok || delay != 92 {
		t.Fatalf("vless-2: got (%d, %v), want (92, true)", delay, ok)
	}
	delay, ok = cs.LatencyForOutbound(context.Background(), "missing-tag")
	if ok || delay != 0 {
		t.Errorf("missing-tag: got (%d, %v), want (0, false)", delay, ok)
	}
}

func TestClashState_CacheHit(t *testing.T) {
	// Счётчик атомарный: инкремент делает горутина обработчика httptest, а
	// читает его тест. Обычный int здесь — гонка данных; сегодня она не
	// вылетает под -race только потому, что запросы синхронные.
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"proxies":{"v":{"history":[{"delay":50}]}}}`))
	}))
	t.Cleanup(upstream.Close)

	cs := NewClashState(func() string { return upstream.URL }, upstream.Client())
	cs.cacheTTL = 5 * time.Second

	for i := 0; i < 5; i++ {
		_, _ = cs.LatencyForOutbound(context.Background(), "v")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 upstream call within TTL, got %d", got)
	}
}

func TestClashState_CacheExpiry(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"proxies":{"v":{"history":[{"delay":50}]}}}`))
	}))
	t.Cleanup(upstream.Close)

	cs := NewClashState(func() string { return upstream.URL }, upstream.Client())
	cs.cacheTTL = 5 * time.Millisecond

	_, _ = cs.LatencyForOutbound(context.Background(), "v")
	time.Sleep(10 * time.Millisecond)
	_, _ = cs.LatencyForOutbound(context.Background(), "v")
	if got := calls.Load(); got != 2 {
		t.Errorf("expected 2 upstream calls after TTL expiry, got %d", got)
	}
}

func TestClashState_Unreachable(t *testing.T) {
	cs := NewClashState(func() string { return "http://127.0.0.1:1" }, &http.Client{Timeout: 50 * time.Millisecond})
	delay, ok := cs.LatencyForOutbound(context.Background(), "anything")
	if ok || delay != 0 {
		t.Errorf("unreachable: got (%d, %v), want (0, false)", delay, ok)
	}
}

func TestClashState_ZeroDelay(t *testing.T) {
	upstream := fakeClashProxiesServer(t, `{"proxies":{"v":{"history":[{"delay":0}]}}}`)
	t.Cleanup(upstream.Close)
	cs := NewClashState(func() string { return upstream.URL }, upstream.Client())
	delay, ok := cs.LatencyForOutbound(context.Background(), "v")
	if ok || delay != 0 {
		t.Errorf("zero-delay history entry must report (0, false), got (%d, %v)", delay, ok)
	}
}
