// Package httpclient provides a lightweight HTTP client with interface binding,
// proxy support, and precise timing metrics. It replaces curl CLI invocations
// for all runtime HTTP operations in the application.
package httpclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"time"
)

// Metrics mirrors curl's -w output fields used across the codebase.
type Metrics struct {
	HTTPCode       int     // e.g. 204, 200, 404
	TimeNameLookup float64 // Cumulative: time from start to DNS done (seconds)
	TimeConnect    float64 // Cumulative: time from start to TCP connect done (seconds)
	TimeTotal      float64 // Cumulative: full request duration (seconds)
}

// Result is the unified return type for all operations.
type Result struct {
	Body    string      // Response body (may be empty when discardBody is used)
	Headers http.Header // Response headers
	Metrics Metrics
}

// CallConfig captures all the variation in how curl is invoked.
// Every field is optional — zero value means "not applicable".
type CallConfig struct {
	// URL is the request target. Required.
	URL string

	// Interface binds the request to a kernel device (curl --interface).
	// Implemented via SO_BINDTODEVICE on the dialer.
	Interface string

	// DNSServers are used for hostname resolution when Interface is set.
	// Queries go over UDP bound to the same interface (tunnel DNS).
	DNSServers []string

	// ProxyURL routes through an HTTP/SOCKS proxy (curl -x / --proxy).
	// Supports "http://", "socks5://", "socks5h://" schemes.
	ProxyURL string

	// Proxy — что делать с прокси из окружения. Нулевое значение
	// (ProxyInheritEnv) — прежнее поведение: непривязанный транспорт
	// наследует HTTP(S)_PROXY. ProxyDirect снимает наследование там, где
	// «прямой выход» — часть смысла запроса, а не умолчание.
	Proxy ProxyPolicy

	// Method defaults to GET; set to "HEAD" for monitoring probes.
	Method string

	// MaxTime is the overall request timeout (curl --max-time).
	MaxTime time.Duration

	// ConnectTimeout is the TCP connect timeout (curl --connect-timeout).
	ConnectTimeout time.Duration

	// DiscardBody when true skips reading the response body (curl -o /dev/null).
	DiscardBody bool
}

// HTTPDoer is the interface satisfied by Client for test stubs.
type HTTPDoer interface {
	Do(ctx context.Context, cfg CallConfig) (*Result, error)
}

// Client wraps http.Client with interface-binding dialer.
// Safe for concurrent use after construction.
// Reuse the same Client instance; per-call state (interface, proxy) is
// isolated inside each Do call.
type Client struct {
	// baseTransport is pre-built with clean defaults. Cloned per-call
	// when interface or proxy override is needed.
	baseTransport *http.Transport
}

// New creates a Client with sensible defaults.
func New() *Client {
	return &Client{
		baseTransport: &http.Transport{
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			DisableKeepAlives:     true, // Most calls are one-shot probes
			TLSHandshakeTimeout:   5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// Match curl behaviour: HTTP/1.1 only. Not every target
			// (especially inside VPNs) handles HTTP/2 well.
			ForceAttemptHTTP2: false,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
		},
	}
}

// DefaultClient is the package-level client for convenient use.
var DefaultClient = New()

// Do executes an HTTP request per cfg and returns the result and metrics.
// Context cancellation and timeouts are respected.
func (c *Client) Do(ctx context.Context, cfg CallConfig) (*Result, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("httpclient: URL is required")
	}

	method := cfg.Method
	if method == "" {
		method = http.MethodGet
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("httpclient: new request: %w", err)
	}

	// Mimic curl's User-Agent so IP-check services (e.g. 2ip.ru) that
	// block Go's default "Go-http-client/1.1" return plain-text responses.
	req.Header.Set("User-Agent", "curl/8.7.1")

	// Timing trace setup — mirrors curl -w %{time_namelookup}|%{time_connect}
	timings := &traceTimings{}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), buildTrace(timings)))

	// Validate proxy URL early so traffic never leaks via WAN on parse failure.
	var parsedProxy *url.URL
	if cfg.ProxyURL != "" {
		var err error
		parsedProxy, err = url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("httpclient: invalid proxy URL %q: %w", cfg.ProxyURL, err)
		}
	}

	// Build per-call transport (interface binding + proxy).
	transport := c.buildTransport(cfg, parsedProxy)
	client := &http.Client{
		Transport: transport,
		Timeout:   cfg.MaxTime,
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		// Map timeout errors to predictable message, matching curl behaviour.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("httpclient: %w", ctxErr)
		}
		if client.Timeout > 0 && time.Since(start) >= client.Timeout {
			return nil, fmt.Errorf("httpclient: request timed out")
		}
		return nil, fmt.Errorf("httpclient: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read metrics from response and timing.
	metrics := Metrics{
		HTTPCode: resp.StatusCode,
	}
	// Cumulative-from-start timings, matching curl's time_namelookup /
	// time_connect semantics so downstream `TimeConnect - TimeNameLookup`
	// yields pure TCP-RTT.
	if !timings.dnsDone.IsZero() {
		metrics.TimeNameLookup = timings.dnsDone.Sub(start).Seconds()
	}
	if !timings.connectDone.IsZero() {
		metrics.TimeConnect = timings.connectDone.Sub(start).Seconds()
	}

	// Read body (or discard).
	var body string
	if cfg.DiscardBody {
		_, _ = io.Copy(io.Discard, resp.Body)
	} else {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		body = string(data)
	}

	metrics.TimeTotal = time.Since(start).Seconds()

	return &Result{
		Body:    body,
		Headers: resp.Header,
		Metrics: metrics,
	}, nil
}

// traceTimings holds intermediate timing data collected by httptrace.
type traceTimings struct {
	dnsStart     time.Time
	dnsDone      time.Time
	connectStart time.Time
	connectDone  time.Time
}

func buildTrace(t *traceTimings) *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:     func(_ httptrace.DNSStartInfo) { t.dnsStart = time.Now() },
		DNSDone:      func(_ httptrace.DNSDoneInfo) { t.dnsDone = time.Now() },
		ConnectStart: func(_, _ string) { t.connectStart = time.Now() },
		ConnectDone:  func(_, _ string, _ error) { t.connectDone = time.Now() },
	}
}

// buildTransport creates a *http.Transport for a specific call config.
// Each call gets its own transport because:
//   - Interface binding is per-socket (SO_BINDTODEVICE on the dialer).
//   - Proxy can vary per call.
func (c *Client) buildTransport(cfg CallConfig, parsedProxy *url.URL) *http.Transport {
	t := c.baseTransport.Clone()

	// Set up the custom dialer with interface binding.
	connectTimeout := cfg.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = 5 * time.Second
	}
	t.DialContext = buildDialContext(cfg.Interface, cfg.DNSServers, connectTimeout)

	// Set up proxy. Explicit URL wins; otherwise honour HTTP_PROXY/HTTPS_PROXY
	// for direct (unbound) egress. Bind mode must not use env proxy — traffic
	// must stay on the tunnel interface.
	//
	// cfg.Proxy == ProxyDirect снимает наследование окружения у НЕпривязанного
	// транспорта: «не передавать ProxyURL» для прямого выхода недостаточно, и
	// этот вход тоже должен уметь его потребовать — он шире, чем NewTransport,
	// и через него ходят диагностика, пробы связи и измерение «прямого» IP.
	if parsedProxy != nil {
		t.Proxy = http.ProxyURL(parsedProxy)
	} else if cfg.Interface == "" && cfg.Proxy != ProxyDirect {
		t.Proxy = http.ProxyFromEnvironment
	}

	// Enforce ForceAttemptHTTP2 = false — some VPN endpoints behind
	// restrictive firewalls choke on HTTP/2 ALPN negotiation.
	t.ForceAttemptHTTP2 = false

	// Pin ALPN to HTTP/1.1. ForceAttemptHTTP2=false alone is not enough:
	// on Go 1.24+ the TLS layer still advertises "h2" in ALPN, so servers
	// negotiate HTTP/2 while this transport only speaks HTTP/1.1. The result
	// is a bare "EOF" on strict servers (Fastly / raw.githubusercontent.com)
	// and a "malformed HTTP response \x00\x00\x12\x04…" (an h2 SETTINGS frame)
	// on lenient ones (Cloudflare / cp.amnezia.org). Cloning is required so we
	// never mutate the shared base transport's TLS config.
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{}
	} else {
		t.TLSClientConfig = t.TLSClientConfig.Clone()
	}
	t.TLSClientConfig.NextProtos = []string{"http/1.1"}

	return t
}

// SecToMs converts seconds (float64) to milliseconds (int) with the same
// floor-to-1ms rule used by the existing curl-based callers.
func SecToMs(sec float64) int {
	if sec <= 0 {
		return 0
	}
	ms := int(sec * 1000)
	if ms < 1 {
		ms = 1
	}
	return ms
}

// ParseTime parses an HTTP Date header value. Returns zero time on failure.
func ParseTime(s string) time.Time {
	t, err := http.ParseTime(s)
	if err != nil {
		return time.Time{}
	}
	return t
}
