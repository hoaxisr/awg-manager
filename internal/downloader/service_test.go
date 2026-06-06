package downloader

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeOutboundsProvider struct {
	items []Outbound
}

func (f *fakeOutboundsProvider) ListDownloadOutbounds(context.Context) []Outbound {
	out := make([]Outbound, len(f.items))
	copy(out, f.items)
	return out
}

type fakeRouteProvider struct {
	route *Route
	err   error
}

func (f *fakeRouteProvider) GetDownloadRoute(context.Context) (*Route, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.route, nil
}

type fakeTransportResolver struct {
	spec    TransportSpec
	err     error
	avail   bool
	resolve func(ctx context.Context, ob Outbound) (TransportSpec, error)
}

func (f *fakeTransportResolver) Resolve(ctx context.Context, ob Outbound) (TransportSpec, error) {
	if f.resolve != nil {
		return f.resolve(ctx, ob)
	}
	if f.err != nil {
		return TransportSpec{}, f.err
	}
	return f.spec, nil
}

func (f *fakeTransportResolver) IsAvailable(context.Context, Outbound) bool {
	return f.avail
}

func TestListOutbounds_NoProvider(t *testing.T) {
	svc := NewService(Deps{})
	got := svc.ListOutbounds(context.Background())
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Tag != "direct" || !got[0].Available {
		t.Fatalf("unexpected fallback outbound: %+v", got[0])
	}
}

func TestResolveClient_Direct(t *testing.T) {
	svc := NewService(Deps{})
	lease, err := svc.ResolveClient(context.Background(), nil)
	if err != nil {
		t.Fatalf("resolve direct nil route: %v", err)
	}
	if lease == nil || lease.Client == nil {
		t.Fatal("direct route should return lease with non-nil client")
	}
	if lease.Route.Tag != "direct" {
		t.Fatalf("route tag: got %q want direct", lease.Route.Tag)
	}
	lease.Close()
}

func TestResolveClient_NonDirectWithoutResolver(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{{Tag: "awg-a", Kind: "awg", Detail: "opkgtun1"}},
		},
	})
	_, err := svc.ResolveClient(context.Background(), &Route{Tag: "awg-a"})
	if err == nil || !strings.Contains(err.Error(), "transport resolver is not configured") {
		t.Fatalf("expected resolver not configured error, got %v", err)
	}
}

func TestResolveClient_UsesRouteProviderWhenRouteNil(t *testing.T) {
	svc := NewService(Deps{
		RouteProvider: &fakeRouteProvider{route: &Route{Tag: "direct"}},
	})
	lease, err := svc.ResolveClient(context.Background(), nil)
	if err != nil {
		t.Fatalf("resolve with route provider: %v", err)
	}
	if lease.Route.Tag != "direct" {
		t.Fatalf("route tag = %q, want direct", lease.Route.Tag)
	}
	lease.Close()
}

func TestResolveClient_ExplicitRouteOverridesProvider(t *testing.T) {
	svc := NewService(Deps{
		RouteProvider: &fakeRouteProvider{route: &Route{Tag: "awg-test"}},
	})
	lease, err := svc.ResolveClient(context.Background(), &Route{Tag: "direct"})
	if err != nil {
		t.Fatalf("resolve explicit route: %v", err)
	}
	if lease.Route.Tag != "direct" {
		t.Fatalf("route tag = %q, want direct", lease.Route.Tag)
	}
	lease.Close()
}

func TestResolveClient_RouteProviderError(t *testing.T) {
	svc := NewService(Deps{
		RouteProvider: &fakeRouteProvider{err: errors.New("settings read failed")},
	})
	_, err := svc.ResolveClient(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "load download route settings") {
		t.Fatalf("expected route provider error, got %v", err)
	}
}

func TestResolveClient_AWGBind(t *testing.T) {
	sysNet := t.TempDir()
	if err := os.Mkdir(filepath.Join(sysNet, "opkgtun2"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolver := NewTransportResolver(TransportResolverDeps{SysClassNet: sysNet})
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "direct", Kind: "direct", Label: "Direct (WAN)"},
				{Tag: "awg-test", Kind: "awg", Label: "AWG test", Detail: "opkgtun2"},
			},
		},
		TransportResolver: resolver,
	})

	lease, err := svc.ResolveClient(context.Background(), &Route{Tag: "awg-test"})
	if err != nil {
		t.Fatalf("resolve routed lease: %v", err)
	}
	if lease.Route.Tag != "awg-test" {
		t.Fatalf("route tag = %q, want awg-test", lease.Route.Tag)
	}
	assertBoundTransport(t, lease.Client, "opkgtun2")
	lease.Close()

	lease2, err := svc.ResolveClient(context.Background(), &Route{Tag: "awg-test"})
	if err != nil {
		t.Fatalf("second resolve should not block: %v", err)
	}
	lease2.Close()
}

func TestResolveClient_SingboxProxy(t *testing.T) {
	resolver := NewTransportResolver(TransportResolverDeps{
		Tunnels:       &fakeTunnelPorts{ports: map[string]int{"DE": 1081}},
		Singbox:       &fakeSingboxRuntime{running: true, ready: true},
		ProxyPortOpen: proxyPortAlwaysOpen,
	})
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "DE", Kind: "singbox", Label: "DE"},
			},
		},
		TransportResolver: resolver,
	})
	lease, err := svc.ResolveClient(context.Background(), &Route{Tag: "DE", Kind: "singbox"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertProxyTransport(t, lease.Client, "127.0.0.1:1081")
	lease.Close()
}

func TestLeaseClose_Idempotent(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	lease := &Lease{
		cleanup: func() {
			mu.Lock()
			defer mu.Unlock()
			calls++
		},
	}
	lease.Close()
	lease.Close()
	lease.Close()
	if calls != 1 {
		t.Fatalf("cleanup calls: got %d want 1", calls)
	}
}

func TestListOutbounds_AWGAvailableWithoutSingbox(t *testing.T) {
	sysNet := t.TempDir()
	if err := os.Mkdir(filepath.Join(sysNet, "opkgtun1"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "direct", Kind: "direct", Label: "Direct (WAN)"},
				{Tag: "awg-test", Kind: "awg", Label: "AWG test", Detail: "opkgtun1"},
			},
		},
		TransportResolver: NewTransportResolver(TransportResolverDeps{SysClassNet: sysNet}),
	})

	got := svc.ListOutbounds(context.Background())
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !got[0].Available {
		t.Fatalf("direct must be available: %+v", got[0])
	}
	if !got[1].Available {
		t.Fatalf("AWG must be available without sing-box: %+v", got[1])
	}
}

func TestListOutbounds_SingboxUnavailableWhenDown(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "DE", Kind: "singbox", Label: "DE"},
			},
		},
		TransportResolver: NewTransportResolver(TransportResolverDeps{
			Tunnels:       &fakeTunnelPorts{ports: map[string]int{"DE": 1080}},
			Singbox:       &fakeSingboxRuntime{running: false},
			ProxyPortOpen: proxyPortAlwaysOpen,
		}),
	})
	got := svc.ListOutbounds(context.Background())
	if len(got) != 1 || got[0].Available {
		t.Fatalf("singbox outbound must be unavailable when down: %+v", got)
	}
}

func assertProxyTransport(t *testing.T, client *http.Client, wantHost string) {
	t.Helper()
	if client == nil {
		t.Fatal("client is nil")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	reqURL, err := url.Parse("https://example.org/file.dat")
	if err != nil {
		t.Fatalf("parse request url: %v", err)
	}
	proxyURL, err := tr.Proxy(&http.Request{URL: reqURL})
	if err != nil {
		t.Fatalf("proxy func returned error: %v", err)
	}
	if proxyURL == nil {
		t.Fatal("expected non-nil proxy URL for routed transport")
	}
	if proxyURL.Host != wantHost {
		t.Fatalf("proxy host = %q, want %q", proxyURL.Host, wantHost)
	}
}

func assertBoundTransport(t *testing.T, client *http.Client, wantIface string) {
	t.Helper()
	if client == nil {
		t.Fatal("client is nil")
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	reqURL, err := url.Parse("https://example.org/file.dat")
	if err != nil {
		t.Fatalf("parse request url: %v", err)
	}
	if tr.Proxy != nil {
		proxyURL, err := tr.Proxy(&http.Request{URL: reqURL})
		if err != nil {
			t.Fatalf("proxy func: %v", err)
		}
		if proxyURL != nil {
			t.Fatalf("bind transport must not use proxy, got %v", proxyURL)
		}
	}
	if tr.DialContext == nil {
		t.Fatal("bind transport must set DialContext")
	}
	_ = wantIface
}

func TestReadAll_Direct(t *testing.T) {
	svc := NewService(Deps{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok-body"))
	}))
	defer ts.Close()

	body, meta, err := svc.ReadAll(context.Background(), Request{
		Purpose:      "test-readall",
		URL:          ts.URL,
		MaxBodyBytes: 64,
		RouteOverride: &Route{
			Tag: "direct",
		},
	})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != "ok-body" {
		t.Fatalf("body = %q, want ok-body", string(body))
	}
	if meta.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", meta.StatusCode)
	}
	if meta.Route.Tag != "direct" {
		t.Fatalf("route tag = %q, want direct", meta.Route.Tag)
	}
}

func TestReadAll_ExceedsLimit(t *testing.T) {
	svc := NewService(Deps{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1234567890"))
	}))
	defer ts.Close()

	_, _, err := svc.ReadAll(context.Background(), Request{
		Purpose:      "test-readall-limit",
		URL:          ts.URL,
		MaxBodyBytes: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected exceeds limit error, got %v", err)
	}
}

func TestReadAll_DirectFollowsRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("final-body"))
	}))
	defer final.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	svc := NewService(Deps{})
	body, meta, err := svc.ReadAll(context.Background(), Request{
		Purpose:      "test-readall-redirect",
		URL:          redirect.URL,
		MaxBodyBytes: 128,
	})
	if err != nil {
		t.Fatalf("ReadAll redirect: %v", err)
	}
	if string(body) != "final-body" {
		t.Fatalf("body = %q, want final-body", string(body))
	}
	if meta.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", meta.StatusCode)
	}
}

func TestDownloadFile_Atomic(t *testing.T) {
	svc := NewService(Deps{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("file-content"))
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "pkg.ipk")
	res, err := svc.DownloadFile(context.Background(), FileRequest{
		Request: Request{
			Purpose: "test-download",
			URL:     ts.URL,
		},
		DestPath:     dest,
		MaxFileBytes: 128,
		Atomic:       true,
	})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if res.Path != dest {
		t.Fatalf("result path = %q, want %q", res.Path, dest)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != "file-content" {
		t.Fatalf("dest body = %q, want file-content", string(data))
	}
}

func TestDownloadFile_OverLimitCleansTemp(t *testing.T) {
	svc := NewService(Deps{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("1234567890"))
	}))
	defer ts.Close()

	tmp := t.TempDir()
	dest := filepath.Join(tmp, "pkg.ipk")
	tmpPath := filepath.Join(tmp, "pkg.tmp")
	_, err := svc.DownloadFile(context.Background(), FileRequest{
		Request: Request{
			Purpose: "test-overlimit",
			URL:     ts.URL,
		},
		DestPath:     dest,
		TempPath:     tmpPath,
		MaxFileBytes: 4,
		Atomic:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected over-limit error, got %v", err)
	}
	if _, statErr := os.Stat(tmpPath); !os.IsNotExist(statErr) {
		t.Fatalf("temp file should be removed, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest file should not exist, stat err=%v", statErr)
	}
}

func TestDownloadFile_ReportsProgress(t *testing.T) {
	svc := NewService(Deps{})
	body := []byte("progress-body")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	dest := filepath.Join(t.TempDir(), "progress.bin")
	var lastDownloaded int64
	var lastTotal int64

	res, err := svc.DownloadFile(context.Background(), FileRequest{
		Request: Request{
			Purpose: "test-progress",
			URL:     ts.URL,
		},
		DestPath:     dest,
		MaxFileBytes: 128,
		Atomic:       true,
		Progress: func(downloaded, total int64) {
			lastDownloaded = downloaded
			lastTotal = total
		},
	})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if res.Size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", res.Size, len(body))
	}
	if lastDownloaded != int64(len(body)) {
		t.Fatalf("last downloaded = %d, want %d", lastDownloaded, len(body))
	}
	if lastTotal != int64(len(body)) {
		t.Fatalf("last total = %d, want %d", lastTotal, len(body))
	}
}

func TestValidateRoute(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "direct", Kind: "direct", Label: "Direct (WAN)"},
				{Tag: "awg-1", Kind: "awg", Label: "AWG"},
			},
		},
	})

	info, err := svc.ValidateRoute(context.Background(), &Route{Tag: "awg-1"})
	if err != nil {
		t.Fatalf("ValidateRoute known: %v", err)
	}
	if info.Tag != "awg-1" || info.Kind != "awg" {
		t.Fatalf("unexpected info: %+v", info)
	}

	if _, err := svc.ValidateRoute(context.Background(), &Route{Tag: "missing"}); err == nil {
		t.Fatal("expected unknown route error")
	}
}

func TestValidateRoute_RespectsKind(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "same-tag", Kind: "subscription", Label: "Sub same"},
			},
		},
	})

	info, err := svc.ValidateRoute(context.Background(), &Route{Tag: "same-tag", Kind: "subscription"})
	if err != nil {
		t.Fatalf("ValidateRoute kind match: %v", err)
	}
	if info.Tag != "same-tag" || info.Kind != "subscription" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestValidateRoute_RejectsMismatchedKind(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "same-tag", Kind: "subscription", Label: "Sub same"},
			},
		},
	})

	_, err := svc.ValidateRoute(context.Background(), &Route{Tag: "same-tag", Kind: "awg"})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), `kind "awg"`) {
		t.Fatalf("expected kind-aware error, got: %v", err)
	}
}

func TestDescribeRoute_RespectsKind(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "same-tag", Kind: "subscription", Label: "Sub same"},
			},
		},
	})

	info := svc.DescribeRoute(context.Background(), &Route{Tag: "same-tag", Kind: "subscription"})
	if info.Tag != "same-tag" || info.Kind != "subscription" || info.Label != "Sub same" {
		t.Fatalf("unexpected describe info: %+v", info)
	}
}

func TestResolveClient_RespectsKind(t *testing.T) {
	resolver := NewTransportResolver(TransportResolverDeps{
		Subs:          &fakeSubPorts{ports: map[string]int{"same-tag": 11002}},
		Singbox:       &fakeSingboxRuntime{running: true, ready: true},
		ProxyPortOpen: proxyPortAlwaysOpen,
	})
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "direct", Kind: "direct", Label: "Direct (WAN)"},
				{Tag: "same-tag", Kind: "subscription", Label: "Sub same"},
			},
		},
		TransportResolver: resolver,
	})

	lease, err := svc.ResolveClient(context.Background(), &Route{Tag: "same-tag", Kind: "subscription"})
	if err != nil {
		t.Fatalf("ResolveClient kind match: %v", err)
	}
	if lease.Route.Tag != "same-tag" || lease.Route.Kind != "subscription" {
		t.Fatalf("unexpected lease route: %+v", lease.Route)
	}
	assertProxyTransport(t, lease.Client, "127.0.0.1:11002")
	lease.Close()
}

func TestValidateRoute_ErrorMessagePreservesPercentLiterals(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "same", Kind: "subscription", Label: "Sub same"},
			},
		},
	})

	_, err := svc.ValidateRoute(context.Background(), &Route{Tag: "bad%v", Kind: "awg"})
	if err == nil {
		t.Fatal("expected unknown route error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `bad%v`) {
		t.Fatalf("expected literal percent tag in message, got: %q", msg)
	}
	if strings.Contains(msg, "%!v(MISSING)") {
		t.Fatalf("unexpected fmt placeholder expansion in message: %q", msg)
	}
}

func TestValidateRoute_RejectsAmbiguousRuntimeTag(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "same-tag", Kind: "awg", Label: "A"},
				{Tag: "same-tag", Kind: "subscription", Label: "B"},
			},
		},
	})

	_, err := svc.ValidateRoute(context.Background(), &Route{Tag: "same-tag", Kind: "subscription"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous runtime tag error, got: %v", err)
	}
}

func TestResolveClient_RejectsAmbiguousRuntimeTag(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "direct", Kind: "direct", Label: "Direct (WAN)"},
				{Tag: "same-tag", Kind: "awg", Label: "A"},
				{Tag: "same-tag", Kind: "subscription", Label: "B"},
			},
		},
		TransportResolver: &fakeTransportResolver{avail: true},
	})

	_, err := svc.ResolveClient(context.Background(), &Route{Tag: "same-tag", Kind: "subscription"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous runtime tag error, got: %v", err)
	}
}

func TestValidateRoute_AllowsKnownUnavailableOutbound(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{
				{Tag: "route-a", Kind: "subscription", Label: "Route A", Available: false},
			},
		},
	})
	info, err := svc.ValidateRoute(context.Background(), &Route{Tag: "route-a", Kind: "subscription"})
	if err != nil {
		t.Fatalf("ValidateRoute unavailable known outbound: %v", err)
	}
	if info.Tag != "route-a" || info.Kind != "subscription" {
		t.Fatalf("unexpected route info: %+v", info)
	}
}

func TestResolveClient_TransportResolverError(t *testing.T) {
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{{Tag: "DE", Kind: "singbox"}},
		},
		TransportResolver: &fakeTransportResolver{
			err: errors.New("sing-box is not running"),
		},
	})
	_, err := svc.ResolveClient(context.Background(), &Route{Tag: "DE", Kind: "singbox"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected transport error, got %v", err)
	}
}

func TestResolveClient_ConcurrentNoDeadlock(t *testing.T) {
	sysNet := t.TempDir()
	if err := os.Mkdir(filepath.Join(sysNet, "opkgtun9"), 0o755); err != nil {
		t.Fatal(err)
	}
	svc := NewService(Deps{
		Outbounds: &fakeOutboundsProvider{
			items: []Outbound{{Tag: "awg-x", Kind: "awg", Detail: "opkgtun9"}},
		},
		TransportResolver: NewTransportResolver(TransportResolverDeps{SysClassNet: sysNet}),
	})

	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			lease, err := svc.ResolveClient(context.Background(), &Route{Tag: "awg-x"})
			if err != nil {
				done <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
			lease.Close()
			done <- nil
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent resolve: %v", err)
		}
	}
}
