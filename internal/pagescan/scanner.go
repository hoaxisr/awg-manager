package pagescan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ipinspect"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

const (
	defaultMaxBodyBytes = 512 * 1024
	defaultFetchTimeout = 8 * time.Second
	defaultTotalTimeout = 20 * time.Second
	defaultConcurrency  = 6
	defaultUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	// ErrInvalidURL indicates the supplied target URL is malformed or unsupported.
	ErrInvalidURL = errors.New("invalid target URL")
	// ErrBlockedAddress indicates the target or redirect resolves to an internal/non-public IP address.
	ErrBlockedAddress = errors.New("target address is not public")
	// ErrInvalidFetchMode indicates an unsupported fetch mode.
	ErrInvalidFetchMode = errors.New("invalid fetch mode")
	// ErrProxyUnavailable indicates proxy fetch mode was requested while no local proxy is listening.
	ErrProxyUnavailable = errors.New("local proxy is unavailable")

	errFakeIPAddress = fmt.Errorf("%w: resolved to local FakeIP range", ErrBlockedAddress)
)

// FetchMode controls how the scanner fetches the target HTML document.
type FetchMode string

const (
	FetchModeAuto   FetchMode = "auto"
	FetchModeDirect FetchMode = "direct"
	FetchModeProxy  FetchMode = "proxy"
)

// HostResolver resolves hostnames to IP addresses.
type HostResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// RouteInfo describes how FIRC routes a single hostname.
type RouteInfo struct {
	Routed        bool   `json:"routed"`
	FakeIP        string `json:"fakeIp,omitempty"`
	RealIP        string `json:"realIp,omitempty"`
	GroupID       string `json:"groupId,omitempty"`
	GroupName     string `json:"groupName,omitempty"`
	Outbound      string `json:"outbound,omitempty"`
	OutboundLabel string `json:"outboundLabel,omitempty"`
	Error         string `json:"error,omitempty"`
}

// RouteChecker resolves routing status for a batch of hostnames.
type RouteChecker func(ctx context.Context, domains []string) (map[string]RouteInfo, error)

// ProxyURLProvider returns an active local HTTP proxy URL (or empty string if unavailable).
type ProxyURLProvider func(ctx context.Context) string

// Options configures a page scanner Service.
type Options struct {
	IPInspect        *ipinspect.Service
	RouteChecker     RouteChecker
	ProxyURLProvider ProxyURLProvider
	Resolver         HostResolver
	MaxBodyBytes     int64
	MaxHosts         int
	FetchTimeout     time.Duration
	TotalTimeout     time.Duration
	Concurrency      int
}

// Resource describes one hostname extracted from the target page along with routing & ASN metadata.
type Resource struct {
	Host          string            `json:"host"`
	Sources       []string          `json:"sources"`
	IP            string            `json:"ip,omitempty"`
	FakeIP        string            `json:"fakeIp,omitempty"`
	Routed        bool              `json:"routed"`
	GroupID       string            `json:"groupId,omitempty"`
	GroupName     string            `json:"groupName,omitempty"`
	Outbound      string            `json:"outbound,omitempty"`
	OutboundLabel string            `json:"outboundLabel,omitempty"`
	Inspect       *ipinspect.Result `json:"inspect,omitempty"`
	ResolveError  string            `json:"resolveError,omitempty"`
}

// Result is returned by Service.Scan.
type Result struct {
	RequestedURL   string     `json:"requestedUrl"`
	FinalURL       string     `json:"finalUrl"`
	StatusCode     int        `json:"statusCode"`
	ContentType    string     `json:"contentType,omitempty"`
	FetchVia       string     `json:"fetchVia"`
	ProxyAvailable bool       `json:"proxyAvailable"`
	BodyBytes      int        `json:"bodyBytes"`
	BodyTruncated  bool       `json:"bodyTruncated,omitempty"`
	TotalHosts     int        `json:"totalHosts"`
	HostsTruncated bool       `json:"hostsTruncated,omitempty"`
	DurationMs     int64      `json:"durationMs"`
	Resources      []Resource `json:"resources"`
}

// Service performs SSRF-safe, bounded on-demand page resource scanning and IP/ASN/route enrichment.
type Service struct {
	ipInspect        *ipinspect.Service
	routeChecker     RouteChecker
	proxyURLProvider ProxyURLProvider
	resolver         HostResolver
	maxBodyBytes     int64
	maxHosts         int
	fetchTimeout     time.Duration
	totalTimeout     time.Duration
	concurrency      int

	// Test-only seam.
	dialRaw func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewService constructs a page resource scanner Service.
func NewService(opts Options) *Service {
	res := opts.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxBodyBytes
	}
	maxHosts := opts.MaxHosts
	if maxHosts <= 0 {
		maxHosts = defaultMaxHosts
	}
	fetchTimeout := opts.FetchTimeout
	if fetchTimeout <= 0 {
		fetchTimeout = defaultFetchTimeout
	}
	totalTimeout := opts.TotalTimeout
	if totalTimeout <= 0 {
		totalTimeout = defaultTotalTimeout
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	return &Service{
		ipInspect:        opts.IPInspect,
		routeChecker:     opts.RouteChecker,
		proxyURLProvider: opts.ProxyURLProvider,
		resolver:         res,
		maxBodyBytes:     maxBody,
		maxHosts:         maxHosts,
		fetchTimeout:     fetchTimeout,
		totalTimeout:     totalTimeout,
		concurrency:      concurrency,
		dialRaw:          (&net.Dialer{Timeout: fetchTimeout}).DialContext,
	}
}

// Scan fetches the requested URL, extracts referenced hosts, checks routing, and inspects IPs.
func (s *Service) Scan(ctx context.Context, rawURL string, mode FetchMode) (*Result, error) {
	if mode == "" {
		mode = FetchModeAuto
	}
	if mode != FetchModeAuto && mode != FetchModeDirect && mode != FetchModeProxy {
		return nil, fmt.Errorf("%w: %q", ErrInvalidFetchMode, mode)
	}

	parsedURL, err := normalizeTargetURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := s.validateTargetHostname(parsedURL.Hostname()); err != nil {
		return nil, err
	}

	scanCtx, cancel := context.WithTimeout(ctx, s.totalTimeout)
	defer cancel()
	started := time.Now()

	proxyURL := ""
	if s.proxyURLProvider != nil {
		proxyURL = strings.TrimSpace(s.proxyURLProvider(scanCtx))
	}
	proxyAvailable := proxyURL != ""

	var fetchOut *fetchResult
	fetchVia := "direct"

	switch mode {
	case FetchModeProxy:
		if !proxyAvailable {
			return nil, ErrProxyUnavailable
		}
		fetchVia = "proxy"
		fetchOut, err = s.fetchViaProxy(scanCtx, parsedURL, proxyURL)
	case FetchModeDirect:
		fetchVia = "direct"
		fetchOut, err = s.fetchDirect(scanCtx, parsedURL)
	case FetchModeAuto:
		fetchOut, err = s.fetchDirect(scanCtx, parsedURL)
		if err != nil {
			isNonFakeBlock := errors.Is(err, ErrBlockedAddress) && !errors.Is(err, errFakeIPAddress)
			if errors.Is(err, ErrInvalidURL) || isNonFakeBlock {
				return nil, err
			}
			if proxyAvailable {
				proxyOut, proxyErr := s.fetchViaProxy(scanCtx, parsedURL, proxyURL)
				if proxyErr == nil {
					fetchOut = proxyOut
					fetchVia = "proxy"
					err = nil
				} else if errors.Is(proxyErr, ErrInvalidURL) || errors.Is(proxyErr, ErrBlockedAddress) {
					return nil, proxyErr
				} else {
					err = fmt.Errorf("direct fetch failed (%v); proxy fetch failed (%v)", err, proxyErr)
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}

	extracted, totalHosts, hostsTruncated := extractResources(
		parsedURL,
		fetchOut.finalURL,
		fetchOut.headers,
		fetchOut.redirectURLs,
		fetchOut.body,
		fetchOut.contentType,
		s.maxHosts,
		false,
	)

	resources := s.enrichResources(scanCtx, extracted)

	finalStr := parsedURL.String()
	if fetchOut.finalURL != nil {
		finalStr = fetchOut.finalURL.String()
	}

	return &Result{
		RequestedURL:   parsedURL.String(),
		FinalURL:       finalStr,
		StatusCode:     fetchOut.statusCode,
		ContentType:    fetchOut.contentType,
		FetchVia:       fetchVia,
		ProxyAvailable: proxyAvailable,
		BodyBytes:      len(fetchOut.body),
		BodyTruncated:  fetchOut.bodyTruncated,
		TotalHosts:     totalHosts,
		HostsTruncated: hostsTruncated,
		DurationMs:     time.Since(started).Milliseconds(),
		Resources:      resources,
	}, nil
}

type fetchResult struct {
	finalURL      *url.URL
	redirectURLs  []*url.URL
	statusCode    int
	contentType   string
	headers       http.Header
	body          []byte
	bodyTruncated bool
}

func (s *Service) fetchDirect(ctx context.Context, target *url.URL) (*fetchResult, error) {
	tr, err := httpclient.NewTransport(httpclient.TransportConfig{
		Proxy: httpclient.ProxyDirect,
	})
	if err != nil {
		return nil, err
	}
	defer tr.CloseIdleConnections()

	tr.DialContext = s.safeDirectDialContext

	var redirectURLs []*url.URL
	client := &http.Client{
		Transport: tr,
		Timeout:   s.fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= httpclient.MaxRedirectHops {
				return fmt.Errorf("too many redirects (limit %d)", httpclient.MaxRedirectHops)
			}
			if len(via) > 0 {
				if prev := via[len(via)-1]; prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
					return fmt.Errorf("%w: HTTPS to %s downgrade redirect rejected", ErrInvalidURL, req.URL.Scheme)
				}
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: unsupported redirect scheme %q", ErrInvalidURL, req.URL.Scheme)
			}
			if req.URL.User != nil {
				return fmt.Errorf("%w: credentials in redirect URL rejected", ErrInvalidURL)
			}
			if err := s.validateTargetHostname(req.URL.Hostname()); err != nil {
				return err
			}
			redirectURLs = append(redirectURLs, cloneURL(req.URL))
			return nil
		},
	}

	res, err := s.executeFetch(ctx, client, target)
	if err != nil {
		return nil, err
	}
	res.redirectURLs = redirectURLs
	return res, nil
}

func (s *Service) fetchViaProxy(ctx context.Context, target *url.URL, proxyURL string) (*fetchResult, error) {
	if err := s.validateProxyTargetHost(ctx, target.Hostname()); err != nil {
		return nil, err
	}

	tr, err := httpclient.NewTransport(httpclient.TransportConfig{
		ProxyURL: proxyURL,
	})
	if err != nil {
		return nil, err
	}
	defer tr.CloseIdleConnections()

	var redirectURLs []*url.URL
	client := &http.Client{
		Transport: tr,
		Timeout:   s.fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= httpclient.MaxRedirectHops {
				return fmt.Errorf("too many redirects (limit %d)", httpclient.MaxRedirectHops)
			}
			if len(via) > 0 {
				if prev := via[len(via)-1]; prev.URL.Scheme == "https" && req.URL.Scheme != "https" {
					return fmt.Errorf("%w: HTTPS to %s downgrade redirect rejected", ErrInvalidURL, req.URL.Scheme)
				}
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: unsupported redirect scheme %q", ErrInvalidURL, req.URL.Scheme)
			}
			if req.URL.User != nil {
				return fmt.Errorf("%w: credentials in redirect URL rejected", ErrInvalidURL)
			}
			if err := s.validateProxyTargetHost(req.Context(), req.URL.Hostname()); err != nil {
				return err
			}
			redirectURLs = append(redirectURLs, cloneURL(req.URL))
			return nil
		},
	}

	res, err := s.executeFetch(ctx, client, target)
	if err != nil {
		return nil, err
	}
	res.redirectURLs = redirectURLs
	return res, nil
}

func (s *Service) executeFetch(ctx context.Context, client *http.Client, target *url.URL) (*fetchResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited, err := io.ReadAll(io.LimitReader(resp.Body, s.maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	truncated := false
	if int64(len(limited)) > s.maxBodyBytes {
		truncated = true
		limited = limited[:s.maxBodyBytes]
	}

	finalURL := resp.Request.URL
	if finalURL == nil {
		finalURL = target
	}

	return &fetchResult{
		finalURL:      cloneURL(finalURL),
		statusCode:    resp.StatusCode,
		contentType:   strings.TrimSpace(resp.Header.Get("Content-Type")),
		headers:       resp.Header.Clone(),
		body:          limited,
		bodyTruncated: truncated,
	}, nil
}

func (s *Service) safeDirectDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return nil, fmt.Errorf("%w: empty dial host", ErrInvalidURL)
	}

	if parsedIP, parseErr := netip.ParseAddr(host); parseErr == nil {
		if scopeErr := s.checkDialAddr(parsedIP); scopeErr != nil {
			return nil, scopeErr
		}
		return s.dialRaw(ctx, network, addr)
	}

	if err := s.validateTargetHostname(host); err != nil {
		return nil, err
	}

	ips, err := s.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP addresses resolved for %s", host)
	}

	validAddrs := make([]string, 0, len(ips))
	for _, ipAddr := range ips {
		parsed, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok {
			continue
		}
		parsed = parsed.Unmap()
		if scopeErr := s.checkDialAddr(parsed); scopeErr != nil {
			return nil, fmt.Errorf("%s resolves to blocked IP %s: %w", host, parsed.String(), scopeErr)
		}
		validAddrs = append(validAddrs, parsed.String())
	}
	if len(validAddrs) == 0 {
		return nil, fmt.Errorf("%w: no usable IP address for %s", ErrBlockedAddress, host)
	}

	var lastErr error
	for _, ipStr := range validAddrs {
		conn, dialErr := s.dialRaw(ctx, network, net.JoinHostPort(ipStr, port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func (s *Service) checkDialAddr(addr netip.Addr) error {
	_, scope := ipinspect.ClassifyAddr(addr)
	switch scope {
	case "public":
		return nil
	case "benchmark":
		return errFakeIPAddress
	default:
		return fmt.Errorf("%w (%s)", ErrBlockedAddress, scope)
	}
}

func (s *Service) validateTargetHostname(host string) error {
	host = normalizeExtractedHost(host)
	if host == "" {
		return fmt.Errorf("%w: empty hostname", ErrInvalidURL)
	}

	if parsed, err := netip.ParseAddr(host); err == nil {
		_, scope := ipinspect.ClassifyAddr(parsed)
		if scope != "public" {
			return fmt.Errorf("%w: IP address %s has non-public scope %q", ErrBlockedAddress, parsed.Unmap().String(), scope)
		}
		return nil
	}

	if !isAcceptableHostname(host) {
		return fmt.Errorf("%w: host %q is blocked or not a public domain", ErrBlockedAddress, host)
	}
	return nil
}

func (s *Service) validateProxyTargetHost(ctx context.Context, host string) error {
	if err := s.validateTargetHostname(host); err != nil {
		return err
	}
	if _, err := netip.ParseAddr(normalizeExtractedHost(host)); err == nil {
		return nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := s.resolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		// DNS failure locally (e.g. ISP DNS drop) should still allow remote resolution by the proxy.
		return nil
	}
	for _, ipAddr := range ips {
		parsed, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok {
			continue
		}
		_, scope := ipinspect.ClassifyAddr(parsed)
		// Benchmark (198.18.0.0/15) is permitted here because a routed domain resolves locally to FakeIP
		// while the remote proxy still resolves the real public domain over the tunnel.
		if scope != "public" && scope != "benchmark" {
			return fmt.Errorf("%w: host %s resolves to non-public address %s (%s)", ErrBlockedAddress, host, parsed.Unmap().String(), scope)
		}
	}
	return nil
}

func (s *Service) enrichResources(ctx context.Context, extracted []extractedHost) []Resource {
	resources := make([]Resource, len(extracted))
	if len(extracted) == 0 {
		return resources
	}

	domains := make([]string, len(extracted))
	for i, item := range extracted {
		domains[i] = item.Host
	}

	var routeMap map[string]RouteInfo
	if s.routeChecker != nil {
		routeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rm, err := s.routeChecker(routeCtx, domains)
		cancel()
		if err == nil {
			routeMap = rm
		}
	}

	sem := make(chan struct{}, s.concurrency)
	var wg sync.WaitGroup

	for i, item := range extracted {
		i, item := i, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := Resource{
				Host:    item.Host,
				Sources: item.Sources,
			}

			if routeMap != nil {
				if info, ok := routeMap[strings.ToLower(item.Host)]; ok && info.Error == "" {
					res.Routed = info.Routed
					res.FakeIP = info.FakeIP
					res.GroupID = info.GroupID
					res.GroupName = info.GroupName
					res.Outbound = info.Outbound
					res.OutboundLabel = info.OutboundLabel
					res.IP = info.RealIP
				}
			}

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				res.ResolveError = ctx.Err().Error()
				resources[i] = res
				return
			}

			if res.IP == "" {
				if addr, err := netip.ParseAddr(item.Host); err == nil {
					res.IP = addr.Unmap().String()
				} else {
					dnsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
					ips, err := s.resolver.LookupIPAddr(dnsCtx, item.Host)
					cancel()
					if err != nil || len(ips) == 0 {
						if err != nil {
							res.ResolveError = err.Error()
						} else {
							res.ResolveError = "no IP addresses returned"
						}
					} else {
						res.IP = pickPreferredIP(ips)
						if res.IP == "" {
							res.ResolveError = "no valid IP address returned"
						}
					}
				}
			}

			if res.IP != "" && s.ipInspect != nil {
				inspectCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
				inspected, err := s.ipInspect.Inspect(inspectCtx, res.IP)
				cancel()
				if err == nil {
					cp := inspected
					res.Inspect = &cp
				}
			}

			resources[i] = res
		}()
	}

	wg.Wait()
	return resources
}

func pickPreferredIP(ips []net.IPAddr) string {
	fallback := ""
	for _, entry := range ips {
		addr, ok := netip.AddrFromSlice(entry.IP)
		if !ok {
			continue
		}
		addr = addr.Unmap()
		if addr.Is4() {
			return addr.String()
		}
		if fallback == "" {
			fallback = addr.String()
		}
	}
	return fallback
}

func normalizeTargetURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: URL is required", ErrInvalidURL)
	}
	lower := strings.ToLower(trimmed)
	for _, forbiddenPrefix := range []string{"javascript:", "data:", "blob:", "about:", "mailto:", "file:", "ftp:", "tel:"} {
		if strings.HasPrefix(lower, forbiddenPrefix) {
			return nil, fmt.Errorf("%w: unsupported scheme", ErrInvalidURL)
		}
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrInvalidURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL are not allowed", ErrInvalidURL)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%w: missing host", ErrInvalidURL)
	}
	if p := u.Port(); p != "" {
		portNum, err := strconv.Atoi(p)
		if err != nil || portNum < 1 || portNum > 65535 {
			return nil, fmt.Errorf("%w: invalid port %q", ErrInvalidURL, p)
		}
	}
	u.Fragment = ""
	return u, nil
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	cp := *u
	if u.User != nil {
		usr := *u.User
		cp.User = &usr
	}
	return &cp
}
