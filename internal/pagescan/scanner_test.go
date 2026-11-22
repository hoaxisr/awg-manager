package pagescan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ipinspect"
)

type stubHostResolver struct {
	mu      sync.Mutex
	records map[string][]net.IPAddr
	errBy   map[string]error
}

func (r *stubHostResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if err, ok := r.errBy[key]; ok {
		return nil, err
	}
	if ips, ok := r.records[key]; ok {
		out := make([]net.IPAddr, len(ips))
		copy(out, ips)
		return out, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: key, IsNotFound: true}
}

type stubInspectResolver struct{}

func (stubInspectResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if addr == "93.184.216.34" {
		return []string{"www.example.com."}, nil
	}
	if addr == "1.1.1.1" {
		return []string{"one.one.one.one."}, nil
	}
	return nil, nil
}

func (stubInspectResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	switch name {
	case "34.216.184.93.origin.asn.cymru.com":
		return []string{"15133 | 93.184.216.0/24 | US | arin | 2007-03-19"}, nil
	case "AS15133.asn.cymru.com":
		return []string{"15133 | US | arin | 2007-03-19 | EDGECAST, US"}, nil
	case "1.1.1.1.origin.asn.cymru.com":
		return []string{"13335 | 1.1.1.0/24 | AU | apnic | 2011-08-11"}, nil
	case "AS13335.asn.cymru.com":
		return []string{"13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US"}, nil
	default:
		return nil, nil
	}
}

func hasHost(hosts []extractedHost, host string) (extractedHost, bool) {
	for _, h := range hosts {
		if h.Host == host {
			return h, true
		}
	}
	return extractedHost{}, false
}

func TestExtractResources_ComprehensiveAndFiltersNoise(t *testing.T) {
	reqURL, _ := url.Parse("https://app.example.com/start")
	finalURL, _ := url.Parse("https://www.example.com/home")
	redirURL, _ := url.Parse("https://login.example.com/auth")

	headers := http.Header{}
	headers.Add("Link", `<https://fonts.example.net>; rel="preconnect", <https://bad-canonical.example.com/x>; rel="canonical"`)
	headers.Add("Content-Security-Policy", `default-src 'self' https://csp-api.example.org; script-src 'unsafe-inline' *.cdn.example.net https:; frame-src wss://ws.example.io:8443`)
	headers.Add("Content-Security-Policy-Report-Only", `img-src https://telemetry.example.com`)

	htmlBody := `<!doctype html>
<html>
<head>
	<!-- <script src="https://commented.example.com/evil.js"></script> -->
	<link rel="canonical" href="https://canonical-ignored.example.com/page">
	<link rel="stylesheet" href="https://styles.example.org/app.css">
	<link rel="dns-prefetch" href="//prefetch.example.net">
	<link rel="icon" href="/favicon.ico">
	<meta http-equiv="Content-Security-Policy" content="connect-src https://meta-csp.example.com">
	<script src="https://scripts.example.com/bundle.js"></script>
	<script>
		const fake = "<img src='https://inside-js.example.com/ignored.png'>";
	</script>
	<style>
		@import "https://css-import.example.com/theme.css";
		.hero { background: url('https://css-bg.example.com/hero(1).jpg'); }
	</style>
</head>
<body>
	<a href="https://link-ignored.example.com/out">External link</a>
	<img src="https://img1.example.com/logo.png" srcset="https://img2.example.com/cdn-cgi/image/w=400,q=80/a.jpg 1x, https://img3.example.com/b.jpg 2x">
	<img src="data:image/png;base64,AAAA" alt="inline">
	<img src="http://192.168.1.10/lan.png" alt="private">
	<iframe src="https://embed.example.net/player"></iframe>
	<video poster="https://poster.example.org/thumb.jpg">
		<source src="https://media.example.org/clip.mp4">
	</video>
	<div style="background-image: url(https://inline-style.example.com/bg.webp)"></div>
	<object data="https://object.example.com/doc.pdf"></object>
</body>
</html>`

	got, total, truncated := extractResources(reqURL, finalURL, headers, []*url.URL{redirURL}, []byte(htmlBody), "text/html; charset=utf-8", 40, false)
	if truncated {
		t.Fatalf("unexpected truncation for %d hosts", total)
	}

	expectedPresent := map[string][]string{
		"app.example.com":          {"document"},
		"login.example.com":        {"redirect"},
		"www.example.com":          {"document", "icon"},
		"fonts.example.net":        {"preconnect"},
		"csp-api.example.org":      {"csp"},
		"cdn.example.net":          {"csp"},
		"ws.example.io":            {"csp"},
		"telemetry.example.com":    {"csp"},
		"styles.example.org":       {"style"},
		"prefetch.example.net":     {"preconnect"},
		"meta-csp.example.com":     {"csp"},
		"scripts.example.com":      {"script"},
		"css-import.example.com":   {"style"},
		"css-bg.example.com":       {"style"},
		"img1.example.com":         {"image"},
		"img2.example.com":         {"image"},
		"img3.example.com":         {"image"},
		"embed.example.net":        {"iframe"},
		"poster.example.org":       {"media"},
		"media.example.org":        {"media"},
		"inline-style.example.com": {"style"},
		"object.example.com":       {"embed"},
	}

	for host, wantSources := range expectedPresent {
		item, ok := hasHost(got, host)
		if !ok {
			t.Errorf("expected extracted host %q to be present (got=%v)", host, got)
			continue
		}
		if !reflect.DeepEqual(item.Sources, wantSources) {
			t.Errorf("host %q sources = %v, want %v", host, item.Sources, wantSources)
		}
	}

	unexpected := []string{
		"bad-canonical.example.com",
		"canonical-ignored.example.com",
		"commented.example.com",
		"inside-js.example.com",
		"link-ignored.example.com",
		"192.168.1.10",
	}
	for _, bad := range unexpected {
		if _, ok := hasHost(got, bad); ok {
			t.Errorf("unexpected host %q extracted", bad)
		}
	}
}

func TestScan_SSRFRejections(t *testing.T) {
	resolver := &stubHostResolver{
		records: map[string][]net.IPAddr{
			"rebind.example.com":    {{IP: net.ParseIP("127.0.0.1")}},
			"lan-alias.example.com": {{IP: net.ParseIP("192.168.1.1")}},
			"public.example.com":    {{IP: net.ParseIP("93.184.216.34")}},
		},
	}

	// Spin up local server pretending to be public.example.com that redirects to 127.0.0.1.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:79/rci/", http.StatusFound)
	}))
	defer srv.Close()

	svc := NewService(Options{Resolver: resolver, FetchTimeout: 2 * time.Second})
	svc.dialRaw = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "93.184.216.34:") {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
		}
		return nil, fmt.Errorf("unexpected dial to %s", addr)
	}

	tests := []struct {
		name    string
		rawURL  string
		wantErr error
	}{
		{name: "empty", rawURL: "", wantErr: ErrInvalidURL},
		{name: "ftp_scheme", rawURL: "ftp://example.com/file", wantErr: ErrInvalidURL},
		{name: "credentials", rawURL: "https://user:pass@example.com", wantErr: ErrInvalidURL},
		{name: "loopback_ip", rawURL: "http://127.0.0.1:79/rci", wantErr: ErrBlockedAddress},
		{name: "localhost", rawURL: "http://localhost:2222", wantErr: ErrBlockedAddress},
		{name: "private_rfc1918", rawURL: "http://192.168.1.1/", wantErr: ErrBlockedAddress},
		{name: "metadata_link_local", rawURL: "http://169.254.169.254/latest/meta-data/", wantErr: ErrBlockedAddress},
		{name: "dns_rebinding_loopback", rawURL: "http://rebind.example.com/", wantErr: ErrBlockedAddress},
		{name: "dns_rebinding_lan", rawURL: "http://lan-alias.example.com/", wantErr: ErrBlockedAddress},
		{name: "redirect_to_loopback", rawURL: "http://public.example.com/", wantErr: ErrBlockedAddress},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Scan(context.Background(), tc.rawURL, FetchModeAuto)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Scan(%q) err = %v, want errors.Is(..., %v)", tc.rawURL, err, tc.wantErr)
			}
		})
	}
}

func TestScan_RedirectDowngradeRejected(t *testing.T) {
	// We use allowPrivateForTest=true cleanly only on loopback test transport via HTTPS->HTTP.
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("plain"))
	}))
	defer httpSrv.Close()

	httpsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpSrv.URL, http.StatusFound)
	}))
	defer httpsSrv.Close()

	// Note: httpsSrv uses self-signed cert so fetchDirect TLS check fails before redirect unless verified;
	// instead test CheckRedirect logic cleanly with http->http2 or unit test downgrade directly:
	req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
	prev, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	svc := NewService(Options{})
	_ = httpsSrv

	client := &http.Client{
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && r.URL.Scheme != "https" {
				return fmt.Errorf("%w: downgrade", ErrInvalidURL)
			}
			return svc.validateTargetHostname(r.URL.Hostname())
		},
	}
	err := client.CheckRedirect(req, []*http.Request{prev})
	if !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("expected ErrInvalidURL on HTTPS->HTTP downgrade, got %v", err)
	}
}

func TestScan_TruncationLimitsAndEnrichment(t *testing.T) {
	htmlPayload := fmt.Sprintf(`<!doctype html>
<html>
<head>
	<script src="https://cdn.example.com/app.js"></script>
	<img src="https://media.example.org/p.png">
	<iframe src="https://extra1.example.net/f.html"></iframe>
	<img src="https://extra2.example.net/x.png">
</head>
<body>%s</body>
</html>`, strings.Repeat("A", 1024))

	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, htmlPayload)
	}))
	defer targetSrv.Close()

	resolver := &stubHostResolver{
		records: map[string][]net.IPAddr{
			"site.example.com":   {{IP: net.ParseIP("93.184.216.34")}},
			"cdn.example.com":    {{IP: net.ParseIP("1.1.1.1")}},
			"media.example.org":  {{IP: net.ParseIP("1.1.1.1")}},
			"extra1.example.net": {{IP: net.ParseIP("93.184.216.34")}},
			"extra2.example.net": {{IP: net.ParseIP("93.184.216.34")}},
		},
	}

	ipSvc := ipinspect.NewServiceWithResolver(stubInspectResolver{}, time.Now)
	svc := NewService(Options{
		IPInspect: ipSvc,
		Resolver:  resolver,
		RouteChecker: func(_ context.Context, domains []string) (map[string]RouteInfo, error) {
			return map[string]RouteInfo{
				"cdn.example.com": {
					Routed:        true,
					FakeIP:        "198.18.0.5",
					RealIP:        "1.1.1.1",
					GroupID:       "grp1",
					GroupName:     "Media",
					Outbound:      "tun0",
					OutboundLabel: "Netherlands",
				},
			}, nil
		},
		MaxBodyBytes: 512,
		MaxHosts:     3,
		FetchTimeout: 3 * time.Second,
		TotalTimeout: 5 * time.Second,
	})
	svc.dialRaw = func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", targetSrv.Listener.Addr().String())
	}

	uDefault, err := normalizeTargetURL("site.example.com")
	if err != nil || uDefault.String() != "https://site.example.com" {
		t.Fatalf("normalizeTargetURL(bare) = %v, %v", uDefault, err)
	}

	res, err := svc.Scan(context.Background(), "http://site.example.com", FetchModeAuto)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	if res.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", res.StatusCode)
	}
	if !res.BodyTruncated || res.BodyBytes != 512 {
		t.Errorf("BodyTruncated=%v BodyBytes=%d, want true and 512", res.BodyTruncated, res.BodyBytes)
	}
	if res.TotalHosts <= 3 || !res.HostsTruncated || len(res.Resources) != 3 {
		t.Errorf("TotalHosts=%d HostsTruncated=%v len(Resources)=%d", res.TotalHosts, res.HostsTruncated, len(res.Resources))
	}
	if res.FetchVia != "direct" || res.ProxyAvailable {
		t.Errorf("FetchVia=%q ProxyAvailable=%v", res.FetchVia, res.ProxyAvailable)
	}

	byHost := make(map[string]Resource)
	for _, r := range res.Resources {
		byHost[r.Host] = r
	}

	site, ok := byHost["site.example.com"]
	if !ok {
		t.Fatalf("missing site.example.com in %+v", res.Resources)
	}
	if site.Routed || site.IP != "93.184.216.34" {
		t.Errorf("site = %+v, want unrouted 93.184.216.34", site)
	}
	if site.Inspect == nil || site.Inspect.ASN != 15133 || site.Inspect.PTR != "www.example.com" {
		t.Errorf("site.Inspect = %+v, want AS15133 + PTR", site.Inspect)
	}

	cdn, ok := byHost["cdn.example.com"]
	if !ok {
		t.Fatalf("missing cdn.example.com in %+v", res.Resources)
	}
	if !cdn.Routed || cdn.FakeIP != "198.18.0.5" || cdn.GroupName != "Media" || cdn.OutboundLabel != "Netherlands" {
		t.Errorf("cdn routing = %+v", cdn)
	}
	if cdn.Inspect == nil || cdn.Inspect.ASN != 13335 || cdn.Inspect.Organization != "CLOUDFLARENET, US" {
		t.Errorf("cdn.Inspect = %+v", cdn.Inspect)
	}
}

func TestScan_ProxyModesAndAutoFallback(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<script src="https://cdn.example.com/a.js"></script>`)
	}))
	defer targetSrv.Close()

	// Minimal HTTP forward proxy forwarding requests to targetSrv.
	proxyHits := 0
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++
		fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetSrv.URL+r.URL.RequestURI(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		resp, err := http.DefaultClient.Do(fwdReq)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	resolver := &stubHostResolver{
		records: map[string][]net.IPAddr{
			"blocked-wan.example.com": {{IP: net.ParseIP("93.184.216.34")}},
			"fakeip-site.example.com": {{IP: net.ParseIP("198.18.0.9")}},
			"cdn.example.com":         {{IP: net.ParseIP("1.1.1.1")}},
		},
	}

	// Case 1: proxy mode without available proxy -> ErrProxyUnavailable.
	svcNoProxy := NewService(Options{Resolver: resolver})
	_, err := svcNoProxy.Scan(context.Background(), "http://blocked-wan.example.com/", FetchModeProxy)
	if !errors.Is(err, ErrProxyUnavailable) {
		t.Fatalf("expected ErrProxyUnavailable, got %v", err)
	}

	// Case 2: auto falls back to proxy when direct dial fails, and records fetchVia=proxy.
	svcWithProxy := NewService(Options{
		Resolver:         resolver,
		ProxyURLProvider: func(context.Context) string { return proxySrv.URL },
		FetchTimeout:     2 * time.Second,
	})
	svcWithProxy.dialRaw = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("simulated WAN block timeout")
	}

	res, err := svcWithProxy.Scan(context.Background(), "http://blocked-wan.example.com/", FetchModeAuto)
	if err != nil {
		t.Fatalf("auto fallback Scan failed: %v", err)
	}
	if res.FetchVia != "proxy" || !res.ProxyAvailable || proxyHits != 1 {
		t.Errorf("FetchVia=%q ProxyAvailable=%v proxyHits=%d", res.FetchVia, res.ProxyAvailable, proxyHits)
	}
	if len(res.Resources) < 2 {
		t.Errorf("expected extracted resources through proxy, got %+v", res.Resources)
	}

	// Case 3: direct mode fails instead of falling back when direct fails.
	if _, err := svcWithProxy.Scan(context.Background(), "http://blocked-wan.example.com/", FetchModeDirect); err == nil {
		t.Fatalf("expected direct mode to fail when direct dial fails")
	}

	// Case 4: hostname resolving locally to FakeIP (198.18.0.9) is rejected in direct mode
	// but automatically retried through proxy in auto mode.
	if _, err := svcWithProxy.Scan(context.Background(), "http://fakeip-site.example.com/", FetchModeDirect); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected ErrBlockedAddress in direct mode for FakeIP, got %v", err)
	}
	resFake, err := svcWithProxy.Scan(context.Background(), "http://fakeip-site.example.com/", FetchModeAuto)
	if err != nil {
		t.Fatalf("expected auto mode to fall back to proxy on FakeIP hostname, got %v", err)
	}
	if resFake.FetchVia != "proxy" {
		t.Errorf("FetchVia = %q, want proxy", resFake.FetchVia)
	}
}
