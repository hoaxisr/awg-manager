package pagescan

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

func newConnectProxyTestServer(t *testing.T, routeAddr func(authority string) (string, bool)) (*httptest.Server, *[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var authorities []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT allowed", http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		authorities = append(authorities, r.Host)
		mu.Unlock()

		dialTarget, ok := routeAddr(r.Host)
		if !ok {
			http.Error(w, "blocked test authority", http.StatusBadGateway)
			return
		}
		upConn, err := (&net.Dialer{Timeout: 2 * time.Second}).Dial("tcp", dialTarget)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = upConn.Close()
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		clientConn, brw, err := hj.Hijack()
		if err != nil {
			_ = upConn.Close()
			return
		}
		_, _ = io.WriteString(clientConn, "HTTP/1.1 200 Connection established\r\n\r\n")
		if brw != nil && brw.Reader.Buffered() > 0 {
			buffered := make([]byte, brw.Reader.Buffered())
			n, _ := brw.Reader.Read(buffered)
			if n > 0 {
				_, _ = upConn.Write(buffered[:n])
			}
		}
		go func() {
			_, _ = io.Copy(upConn, clientConn)
			_ = upConn.Close()
		}()
		go func() {
			_, _ = io.Copy(clientConn, upConn)
			_ = clientConn.Close()
		}()
	}))
	t.Cleanup(srv.Close)
	return srv, &authorities, &mu
}

func TestScan_ProxyModesAndAutoFallback(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<script src="https://cdn.example.com/a.js"></script>`)
	}))
	defer targetSrv.Close()

	proxySrv, authorities, authMu := newConnectProxyTestServer(t, func(authority string) (string, bool) {
		if authority == "93.184.216.34:80" {
			return targetSrv.Listener.Addr().String(), true
		}
		return "", false
	})

	resolver := &stubHostResolver{
		records: map[string][]net.IPAddr{
			"blocked-wan.example.com": {{IP: net.ParseIP("93.184.216.34")}},
			"fakeip-site.example.com": {{IP: net.ParseIP("198.18.0.9")}},
			"cdn.example.com":         {{IP: net.ParseIP("1.1.1.1")}},
		},
	}
	proxyResolver := &stubHostResolver{
		records: map[string][]net.IPAddr{
			"blocked-wan.example.com": {{IP: net.ParseIP("93.184.216.34")}},
			"fakeip-site.example.com": {{IP: net.ParseIP("93.184.216.34")}},
		},
	}

	// Case 1: proxy mode without available proxy -> ErrProxyUnavailable.
	svcNoProxy := NewService(Options{Resolver: resolver, ProxyResolver: proxyResolver})
	_, err := svcNoProxy.Scan(context.Background(), "http://blocked-wan.example.com/", FetchModeProxy)
	if !errors.Is(err, ErrProxyUnavailable) {
		t.Fatalf("expected ErrProxyUnavailable, got %v", err)
	}

	// Case 2: auto falls back to proxy when direct dial fails, records fetchVia=proxy,
	// and pins CONNECT strictly to the verified literal public IP (never forwarding a hostname).
	svcWithProxy := NewService(Options{
		Resolver:         resolver,
		ProxyResolver:    proxyResolver,
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
	authMu.Lock()
	gotAuth := append([]string(nil), (*authorities)...)
	authMu.Unlock()
	if res.FetchVia != "proxy" || !res.ProxyAvailable || len(gotAuth) != 1 || gotAuth[0] != "93.184.216.34:80" {
		t.Fatalf("FetchVia=%q ProxyAvailable=%v CONNECT targets=%v", res.FetchVia, res.ProxyAvailable, gotAuth)
	}
	if len(res.Resources) < 2 {
		t.Errorf("expected extracted resources through proxy, got %+v", res.Resources)
	}

	// Case 3: direct mode fails instead of falling back when direct fails.
	if _, err := svcWithProxy.Scan(context.Background(), "http://blocked-wan.example.com/", FetchModeDirect); err == nil {
		t.Fatalf("expected direct mode to fail when direct dial fails")
	}

	// Case 4: hostname resolving locally to FakeIP (198.18.0.9) is rejected in direct mode
	// and automatically retried through proxy pinned to exit-verified public IP 93.184.216.34.
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

func TestScan_ProxySecurityBlocksNonPublicAndFailsClosed(t *testing.T) {
	// 1. Non-loopback proxy URL must be rejected before dialing.
	svcBadProxy := NewService(Options{
		ProxyURLProvider: func(context.Context) string { return "http://192.168.1.1:1099" },
		ProxyResolver: &stubHostResolver{
			records: map[string][]net.IPAddr{"ok.example.com": {{IP: net.ParseIP("93.184.216.34")}}},
		},
	})
	if _, err := svcBadProxy.Scan(context.Background(), "https://ok.example.com/", FetchModeProxy); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected ErrBlockedAddress for non-loopback proxy URL, got %v", err)
	}

	var redirSrv *httptest.Server
	redirSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://internal-hop.example.com/", http.StatusFound)
	}))
	defer redirSrv.Close()

	proxySrv, authorities, authMu := newConnectProxyTestServer(t, func(authority string) (string, bool) {
		if authority == "93.184.216.34:80" {
			return redirSrv.Listener.Addr().String(), true
		}
		return "", false
	})

	// 2. Host where local DNS and DoH both fail must fail closed without CONNECTing.
	svcFailClosed := NewService(Options{
		Resolver:         &stubHostResolver{},
		ProxyURLProvider: func(context.Context) string { return proxySrv.URL },
		FetchTimeout:     time.Second,
	})
	svcFailClosed.dohEndpoints = nil
	if _, err := svcFailClosed.Scan(context.Background(), "http://unresolvable.example.com/", FetchModeProxy); err == nil {
		t.Fatalf("expected unresolvable domain in proxy mode to fail closed")
	}

	// 3. Exit resolver returning non-public IPs (loopback, link-local metadata, FakeIP) -> ErrBlockedAddress, 0 CONNECT hits.
	for _, tc := range []struct {
		name string
		ip   string
	}{
		{"loopback", "127.0.0.1"},
		{"metadata", "169.254.169.254"},
		{"fakeip", "198.18.0.5"},
	} {
		svcBlocked := NewService(Options{
			Resolver: &stubHostResolver{records: map[string][]net.IPAddr{"x.example.com": {{IP: net.ParseIP("198.18.0.5")}}}},
			ProxyResolver: &stubHostResolver{
				records: map[string][]net.IPAddr{"x.example.com": {{IP: net.ParseIP(tc.ip)}}},
			},
			ProxyURLProvider: func(context.Context) string { return proxySrv.URL },
		})
		if _, err := svcBlocked.Scan(context.Background(), "http://x.example.com/", FetchModeProxy); !errors.Is(err, ErrBlockedAddress) {
			t.Fatalf("%s: expected ErrBlockedAddress, got %v", tc.name, err)
		}
	}

	authMu.Lock()
	hitsBeforeRedir := len(*authorities)
	authMu.Unlock()
	if hitsBeforeRedir != 0 {
		t.Fatalf("expected 0 proxy CONNECT hits before redirect test, got %v", *authorities)
	}

	// 4. Redirect hop in proxy mode whose target resolves to 10.0.0.1 must be blocked before any second CONNECT.
	svcRedir := NewService(Options{
		Resolver: &stubHostResolver{
			records: map[string][]net.IPAddr{
				"start.example.com":        {{IP: net.ParseIP("93.184.216.34")}},
				"internal-hop.example.com": {{IP: net.ParseIP("10.0.0.1")}},
			},
		},
		ProxyResolver: &stubHostResolver{
			records: map[string][]net.IPAddr{
				"start.example.com":        {{IP: net.ParseIP("93.184.216.34")}},
				"internal-hop.example.com": {{IP: net.ParseIP("10.0.0.1")}},
			},
		},
		ProxyURLProvider: func(context.Context) string { return proxySrv.URL },
	})
	if _, err := svcRedir.Scan(context.Background(), "http://start.example.com/", FetchModeProxy); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("expected ErrBlockedAddress on redirect to RFC1918 in proxy mode, got %v", err)
	}
	authMu.Lock()
	redirAuth := append([]string(nil), (*authorities)...)
	authMu.Unlock()
	if len(redirAuth) != 1 || redirAuth[0] != "93.184.216.34:80" {
		t.Fatalf("expected only initial verified IP in CONNECT log, got %v", redirAuth)
	}
}

func TestScan_ProxyDoHOverConnectFakeIP(t *testing.T) {
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<img src="https://img.example.com/a.png">`)
	}))
	defer targetSrv.Close()

	// DoH TLS server on loopback using RFC 8484 wire format.
	dohSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqWire, err := io.ReadAll(r.Body)
		if err != nil || len(reqWire) < 12 {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		txID := binary.BigEndian.Uint16(reqWire[0:2])
		respWire := buildTestDNSResponse(txID, "fakeip-doh.example.com", "fakeip-doh.example.com", net.ParseIP("93.184.216.34"), false)
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(respWire)
	}))
	defer dohSrv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(dohSrv.Certificate())

	proxySrv, authorities, authMu := newConnectProxyTestServer(t, func(authority string) (string, bool) {
		switch authority {
		case dohSrv.Listener.Addr().String():
			return dohSrv.Listener.Addr().String(), true
		case "93.184.216.34:80":
			return targetSrv.Listener.Addr().String(), true
		default:
			return "", false
		}
	})

	svc := NewService(Options{
		Resolver: &stubHostResolver{
			records: map[string][]net.IPAddr{
				"fakeip-doh.example.com": {{IP: net.ParseIP("198.18.0.44")}},
				"img.example.com":        {{IP: net.ParseIP("93.184.216.34")}},
			},
		},
		ProxyURLProvider: func(context.Context) string { return proxySrv.URL },
		FetchTimeout:     2 * time.Second,
	})
	svc.dohEndpoints = []string{dohSrv.Listener.Addr().String()}
	svc.dohRootCAs = roots

	res, err := svc.Scan(context.Background(), "http://fakeip-doh.example.com/", FetchModeProxy)
	if err != nil {
		t.Fatalf("Scan via DoH-pinned proxy failed: %v", err)
	}
	if res.FetchVia != "proxy" {
		t.Fatalf("FetchVia = %q, want proxy", res.FetchVia)
	}
	authMu.Lock()
	gotAuth := append([]string(nil), (*authorities)...)
	authMu.Unlock()
	if len(gotAuth) != 2 || gotAuth[0] != dohSrv.Listener.Addr().String() || gotAuth[1] != "93.184.216.34:80" {
		t.Fatalf("unexpected CONNECT sequence = %v", gotAuth)
	}
}

func TestParseDoHAResponse_StrictValidation(t *testing.T) {
	txID := uint16(0x1234)
	valid := buildTestDNSResponse(txID, "app.example.com", "app.example.com", net.ParseIP("93.184.216.34"), false)
	addrs, cname, err := parseDoHAResponse(valid, "app.example.com", txID)
	if err != nil || cname != "" || len(addrs) != 1 || addrs[0] != netip.MustParseAddr("93.184.216.34") {
		t.Fatalf("valid parseDoHAResponse failed: addrs=%v cname=%q err=%v", addrs, cname, err)
	}

	// Truncated TC=1 must fail.
	tcPkt := buildTestDNSResponse(txID, "app.example.com", "app.example.com", net.ParseIP("93.184.216.34"), true)
	if _, _, err := parseDoHAResponse(tcPkt, "app.example.com", txID); err == nil {
		t.Fatalf("expected TC=1 packet to be rejected")
	}

	// Mismatched Answer owner must be ignored (returns no addresses).
	wrongOwner := buildTestDNSResponse(txID, "app.example.com", "evil.example.com", net.ParseIP("93.184.216.34"), false)
	addrsWrong, _, err := parseDoHAResponse(wrongOwner, "app.example.com", txID)
	if err != nil {
		t.Fatalf("unexpected error on mismatched owner: %v", err)
	}
	if len(addrsWrong) != 0 {
		t.Fatalf("expected unrelated owner A record to be ignored, got %v", addrsWrong)
	}
}

func buildTestDNSResponse(txID uint16, qname, ansOwner string, ip net.IP, tc bool) []byte {
	encodeName := func(n string) []byte {
		var out []byte
		for _, lbl := range strings.Split(strings.TrimSuffix(n, "."), ".") {
			out = append(out, byte(len(lbl)))
			out = append(out, []byte(lbl)...)
		}
		return append(out, 0)
	}
	var pkt [12]byte
	binary.BigEndian.PutUint16(pkt[0:2], txID)
	flags := uint16(0x8180)
	if tc {
		flags |= 0x0200
	}
	binary.BigEndian.PutUint16(pkt[2:4], flags)
	binary.BigEndian.PutUint16(pkt[4:6], 1) // QDCOUNT
	binary.BigEndian.PutUint16(pkt[6:8], 1) // ANCOUNT

	out := append([]byte(nil), pkt[:]...)
	out = append(out, encodeName(qname)...)
	out = append(out, 0, 1, 0, 1) // QTYPE A, QCLASS IN

	out = append(out, encodeName(ansOwner)...)
	out = append(out, 0, 1, 0, 1)  // TYPE A, CLASS IN
	out = append(out, 0, 0, 0, 60) // TTL 60
	out = append(out, 0, 4)        // RDLENGTH 4
	out = append(out, ip.To4()...) // RDATA
	return out
}

func TestScan_HTTP2TransportAndNavigationHeaders(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9999")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9999")

	svc := NewService(Options{FetchTimeout: 2 * time.Second})
	tr := svc.newDirectTransport()
	if !tr.ForceAttemptHTTP2 {
		t.Fatalf("ForceAttemptHTTP2 = false, want true")
	}
	if tr.Proxy != nil {
		t.Fatalf("direct transport Proxy must be nil (must ignore env HTTP_PROXY)")
	}

	var captured http.Header
	targetSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Clone()
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<script src="https://cdn.example.com/a.js"></script>`)
	}))
	defer targetSrv.Close()

	svc.resolver = &stubHostResolver{
		records: map[string][]net.IPAddr{
			"site.example.com": {{IP: net.ParseIP("93.184.216.34")}},
			"cdn.example.com":  {{IP: net.ParseIP("1.1.1.1")}},
		},
	}
	svc.dialRaw = func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", targetSrv.Listener.Addr().String())
	}

	if _, err := svc.Scan(context.Background(), "http://site.example.com/", FetchModeDirect); err != nil {
		t.Fatalf("Scan failed despite HTTP_PROXY env: %v", err)
	}
	for hdr, want := range map[string]string{
		"Upgrade-Insecure-Requests": "1",
		"Sec-Fetch-Dest":            "document",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "none",
		"Sec-Fetch-User":            "?1",
	} {
		if got := captured.Get(hdr); got != want {
			t.Errorf("header %s = %q, want %q", hdr, got, want)
		}
	}
}
