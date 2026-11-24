package pagescan

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
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
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ipinspect"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
	"golang.org/x/net/proxy"
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
	// ErrInvalidInterface indicates the supplied interface is not among the offered tunnel interfaces.
	ErrInvalidInterface = errors.New("invalid network interface")
	// ErrInvalidProxy indicates the supplied manual proxy URL is malformed or unsupported.
	ErrInvalidProxy = errors.New("invalid proxy URL")

	errFakeIPAddress = fmt.Errorf("%w: resolved to local FakeIP range", ErrBlockedAddress)
)

// FetchMode controls how the scanner fetches the target HTML document.
type FetchMode string

const (
	FetchModeAuto      FetchMode = "auto"
	FetchModeDirect    FetchMode = "direct"
	FetchModeProxy     FetchMode = "proxy"
	FetchModeInterface FetchMode = "interface"
)

// ScanRequest parameterizes a page resource scan including optional explicit egress selection.
type ScanRequest struct {
	URL       string
	Mode      FetchMode
	Interface string
	ProxyURL  string
}

// InterfaceOption describes an active network/tunnel interface available for bound scanning.
type InterfaceOption struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// ProxyOption describes a detected local proxy listener available for proxied scanning.
type ProxyOption struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// ScanOptionsResult lists available exit interfaces and local proxies for page scanning.
type ScanOptionsResult struct {
	DefaultProxy      string            `json:"defaultProxy,omitempty"`
	DefaultProxyLabel string            `json:"defaultProxyLabel,omitempty"`
	Proxies           []ProxyOption     `json:"proxies"`
	Interfaces        []InterfaceOption `json:"interfaces"`
}

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
	IPInspect          *ipinspect.Service
	RouteChecker       RouteChecker
	ProxyURLProvider   ProxyURLProvider
	InterfaceLister    func(ctx context.Context) []InterfaceOption
	ProxyLister        func(ctx context.Context) []ProxyOption
	RouterProxyMatcher func(ctx context.Context, host, port string) bool
	Resolver           HostResolver
	// ProxyResolver resolves target hostnames on behalf of proxy mode for verification and IP pinning.
	// When nil, proxy mode probes local DNS and falls back to DNS-over-HTTPS through the same proxy
	// (never forwarding an unverified hostname to the proxy); socks5h skips the local probe.
	ProxyResolver HostResolver
	MaxBodyBytes  int64
	MaxHosts      int
	FetchTimeout  time.Duration
	TotalTimeout  time.Duration
	Concurrency   int
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
	// RouteUnknown: маршрут не проверялся — имя нельзя отдавать DNS роутера (socks5h).
	RouteUnknown bool `json:"routeUnknown,omitempty"`
}

// Result is returned by Service.ScanWithOptions.
type Result struct {
	RequestedURL     string     `json:"requestedUrl"`
	FinalURL         string     `json:"finalUrl"`
	StatusCode       int        `json:"statusCode"`
	ContentType      string     `json:"contentType,omitempty"`
	FetchVia         string     `json:"fetchVia"`
	FetchTarget      string     `json:"fetchTarget,omitempty"`
	FetchTargetLabel string     `json:"fetchTargetLabel,omitempty"`
	DefaultProxy     string     `json:"defaultProxy,omitempty"`
	ProxyAvailable   bool       `json:"proxyAvailable"`
	BodyBytes        int        `json:"bodyBytes"`
	BodyTruncated    bool       `json:"bodyTruncated,omitempty"`
	TotalHosts       int        `json:"totalHosts"`
	HostsTruncated   bool       `json:"hostsTruncated,omitempty"`
	DurationMs       int64      `json:"durationMs"`
	Resources        []Resource `json:"resources"`
}

// Service performs SSRF-safe, bounded on-demand page resource scanning and IP/ASN/route enrichment.
type Service struct {
	ipInspect          *ipinspect.Service
	routeChecker       RouteChecker
	proxyURLProvider   ProxyURLProvider
	interfaceLister    func(ctx context.Context) []InterfaceOption
	proxyLister        func(ctx context.Context) []ProxyOption
	routerProxyMatcher func(ctx context.Context, host, port string) bool
	resolver           HostResolver
	proxyResolver      HostResolver
	dohEndpoints       []string
	dohRootCAs         *x509.CertPool
	maxBodyBytes       int64
	maxHosts           int
	fetchTimeout       time.Duration
	totalTimeout       time.Duration
	concurrency        int

	dohTxSeq atomic.Uint32

	// Test-only seams.
	dialRaw             func(ctx context.Context, network, addr string) (net.Conn, error)
	dialProxy           func(ctx context.Context, network, addr string) (net.Conn, error)
	dialInterface       func(ctx context.Context, iface, network, addr string) (net.Conn, error)
	interfaceLookupIPv4 func(ctx context.Context, host, iface string) ([]string, error)
	isRouterAddr        func(addr netip.Addr) bool
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
	defaultDial := (&net.Dialer{Timeout: fetchTimeout}).DialContext
	return &Service{
		ipInspect:          opts.IPInspect,
		routeChecker:       opts.RouteChecker,
		proxyURLProvider:   opts.ProxyURLProvider,
		interfaceLister:    opts.InterfaceLister,
		proxyLister:        opts.ProxyLister,
		routerProxyMatcher: opts.RouterProxyMatcher,
		resolver:           res,
		proxyResolver:      opts.ProxyResolver,
		dohEndpoints:       []string{"1.1.1.1", "8.8.8.8"},
		maxBodyBytes:       maxBody,
		maxHosts:           maxHosts,
		fetchTimeout:       fetchTimeout,
		totalTimeout:       totalTimeout,
		concurrency:        concurrency,
		dialRaw:            defaultDial,
		dialProxy:          defaultDial,
		dialInterface: func(ctx context.Context, iface, _ string, addr string) (net.Conn, error) {
			return httpclient.DialTCP(ctx, iface, addr, fetchTimeout)
		},
		interfaceLookupIPv4: func(ctx context.Context, host, iface string) ([]string, error) {
			return httpclient.LookupAllIPv4ForBind(ctx, host, []string{"1.1.1.1", "8.8.8.8"}, iface)
		},
		isRouterAddr: isRouterAddr,
	}
}

// Options discovers available router tunnel interfaces and local proxies for page scanning.
func (s *Service) Options(ctx context.Context) ScanOptionsResult {
	res := ScanOptionsResult{
		Proxies:    []ProxyOption{},
		Interfaces: []InterfaceOption{},
	}
	if s == nil {
		return res
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if s.proxyURLProvider != nil {
		if raw := strings.TrimSpace(s.proxyURLProvider(probeCtx)); raw != "" {
			if redacted := redactProxyURL(raw); redacted != "" {
				res.DefaultProxy = redacted
			}
		}
	}
	if s.proxyLister != nil {
		for _, item := range s.proxyLister(probeCtx) {
			redacted := redactProxyURL(item.URL)
			if redacted == "" {
				continue
			}
			item.URL = redacted
			if item.Label == "" {
				item.Label = redacted
			}
			res.Proxies = append(res.Proxies, item)
			if res.DefaultProxy != "" && strings.EqualFold(redacted, res.DefaultProxy) && res.DefaultProxyLabel == "" {
				res.DefaultProxyLabel = item.Label
			}
		}
	}
	if res.DefaultProxy != "" {
		found := false
		for _, p := range res.Proxies {
			if strings.EqualFold(p.URL, res.DefaultProxy) {
				found = true
				break
			}
		}
		if !found {
			label := res.DefaultProxy
			res.DefaultProxyLabel = label
			res.Proxies = append([]ProxyOption{{URL: res.DefaultProxy, Label: label}}, res.Proxies...)
		} else if res.DefaultProxyLabel == "" {
			res.DefaultProxyLabel = res.DefaultProxy
		}
	}
	if s.interfaceLister != nil {
		for _, item := range s.interfaceLister(probeCtx) {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				continue
			}
			if item.Label == "" {
				item.Label = name
			}
			item.Name = name
			res.Interfaces = append(res.Interfaces, item)
		}
	}
	return res
}

// ScanWithOptions fetches the requested URL using explicit fetch mode, interface binding, or manual proxy URL.
func (s *Service) ScanWithOptions(ctx context.Context, req ScanRequest) (*Result, error) {
	mode := FetchMode(strings.ToLower(strings.TrimSpace(string(req.Mode))))
	iface := strings.TrimSpace(req.Interface)
	manualProxyRaw := strings.TrimSpace(req.ProxyURL)

	if mode == "" {
		switch {
		case iface != "":
			mode = FetchModeInterface
		case manualProxyRaw != "":
			mode = FetchModeProxy
		default:
			mode = FetchModeAuto
		}
	}
	if mode != FetchModeAuto && mode != FetchModeDirect && mode != FetchModeProxy && mode != FetchModeInterface {
		return nil, fmt.Errorf("%w: %q", ErrInvalidFetchMode, req.Mode)
	}
	// Лишний параметр — отказ: молча выйти не через тот выход, который указан, нельзя.
	if iface != "" && mode != FetchModeInterface {
		return nil, fmt.Errorf("%w: interface is not allowed with mode %q", ErrInvalidFetchMode, mode)
	}
	if manualProxyRaw != "" && mode != FetchModeProxy {
		return nil, fmt.Errorf("%w: proxyUrl is not allowed with mode %q", ErrInvalidFetchMode, mode)
	}

	parsedURL, err := normalizeTargetURL(req.URL)
	if err != nil {
		return nil, err
	}
	if err := s.validateTargetHostname(parsedURL.Hostname()); err != nil {
		return nil, err
	}

	scanCtx, cancel := context.WithTimeout(ctx, s.totalTimeout)
	defer cancel()
	started := time.Now()

	rawDefaultProxy := ""
	if s.proxyURLProvider != nil {
		rawDefaultProxy = strings.TrimSpace(s.proxyURLProvider(scanCtx))
	}
	defaultProxyDisplay := redactProxyURL(rawDefaultProxy)
	proxyAvailable := rawDefaultProxy != ""

	var fetchOut *fetchResult
	fetchVia := "direct"
	fetchTarget := ""
	fetchTargetLabel := ""
	var pinned *sync.Map

	switch mode {
	case FetchModeInterface:
		opt, optErr := s.findInterface(scanCtx, iface)
		if optErr != nil {
			return nil, optErr
		}
		fetchVia = "interface"
		fetchTarget = opt.Name
		fetchTargetLabel = opt.Label
		fetchOut, err = s.fetchViaInterface(scanCtx, parsedURL, opt.Name)
	case FetchModeProxy:
		var spec proxySpec
		if manualProxyRaw != "" {
			spec, err = s.parseManualProxySpec(scanCtx, manualProxyRaw)
		} else {
			if rawDefaultProxy == "" {
				return nil, ErrProxyUnavailable
			}
			spec, err = parseTrustedLoopbackProxySpec(rawDefaultProxy)
		}
		if err != nil {
			return nil, err
		}
		fetchVia = "proxy"
		fetchTarget = spec.DisplayURL
		fetchTargetLabel = spec.label()
		pinned = spec.Pinned
		fetchOut, err = s.fetchViaProxySpec(scanCtx, parsedURL, spec)
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
				spec, pErr := parseTrustedLoopbackProxySpec(rawDefaultProxy)
				if pErr != nil {
					return nil, pErr
				}
				proxyOut, proxyErr := s.fetchViaProxySpec(scanCtx, parsedURL, spec)
				if proxyErr == nil {
					fetchOut = proxyOut
					fetchVia = "proxy"
					fetchTarget = spec.DisplayURL
					fetchTargetLabel = spec.label()
					err = nil
				} else if errors.Is(proxyErr, ErrInvalidURL) || errors.Is(proxyErr, ErrBlockedAddress) {
					return nil, proxyErr
				} else {
					err = fmt.Errorf("direct fetch failed (%v); proxy (%s) fetch failed (%v)", err, spec.DisplayURL, proxyErr)
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

	resources := s.enrichResources(scanCtx, extracted, pinned)

	finalStr := parsedURL.String()
	if fetchOut.finalURL != nil {
		finalStr = fetchOut.finalURL.String()
	}

	return &Result{
		RequestedURL:     parsedURL.String(),
		FinalURL:         finalStr,
		StatusCode:       fetchOut.statusCode,
		ContentType:      fetchOut.contentType,
		FetchVia:         fetchVia,
		FetchTarget:      fetchTarget,
		FetchTargetLabel: fetchTargetLabel,
		DefaultProxy:     defaultProxyDisplay,
		ProxyAvailable:   proxyAvailable,
		BodyBytes:        len(fetchOut.body),
		BodyTruncated:    fetchOut.bodyTruncated,
		TotalHosts:       totalHosts,
		HostsTruncated:   hostsTruncated,
		DurationMs:       time.Since(started).Milliseconds(),
		Resources:        resources,
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

type proxySpec struct {
	Scheme     string
	DialAddrs  []string
	DisplayURL string
	Label      string
	Username   string
	Password   string
	// Pinned непуст только у socks5h: имена целей не отдаём DNS роутера, а
	// резолвим через сам прокси; здесь «имя → закреплённый адрес».
	Pinned *sync.Map
}

func (p proxySpec) label() string {
	if p.Label != "" {
		return p.Label
	}
	return p.DisplayURL
}

// findInterface пускает только интерфейсы из предложенного списка: он уже
// отфильтрован до поднятых туннелей.
func (s *Service) findInterface(ctx context.Context, iface string) (InterfaceOption, error) {
	if s.interfaceLister != nil {
		for _, item := range s.interfaceLister(ctx) {
			item.Name = strings.TrimSpace(item.Name)
			if item.Name == "" || item.Name != iface {
				continue
			}
			if item.Label = strings.TrimSpace(item.Label); item.Label == "" {
				item.Label = item.Name
			}
			return item, nil
		}
	}
	return InterfaceOption{}, fmt.Errorf("%w: %q is not an active router tunnel", ErrInvalidInterface, iface)
}

func redactProxyURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return ""
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	} else if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		host = addr.Unmap().String()
	} else {
		host = strings.ToLower(host)
	}
	portStr := strings.TrimSpace(parsed.Port())
	authority := host
	if portStr != "" {
		authority = net.JoinHostPort(host, portStr)
	} else if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	return scheme + "://" + authority
}

func (s *Service) newDirectTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // explicit direct exit; never inherit HTTP_PROXY/HTTPS_PROXY
		DialContext:           s.safeDirectDialContext,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		MaxIdleConns:          4,
		TLSHandshakeTimeout:   s.fetchTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func (s *Service) newInterfaceTransport(iface string) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           s.safeInterfaceDialContext(iface),
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		MaxIdleConns:          4,
		TLSHandshakeTimeout:   s.fetchTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// proxyURLHint не включает сам URL: в нём пароль.
const proxyURLHint = "expected scheme://[user:password@]host:port; special characters in user and password must be percent-encoded (@ is %40, / is %2F, # is %23, % is %25)"

func parseProxySpecCommon(rawProxyURL string, invalidErr error) (scheme, host, portStr, username, password string, err error) {
	trimmed := strings.TrimSpace(rawProxyURL)
	if trimmed == "" {
		return "", "", "", "", "", ErrProxyUnavailable
	}
	parsed, parseErr := url.Parse(trimmed)
	if parseErr != nil || parsed.Host == "" {
		return "", "", "", "", "", fmt.Errorf("%w: %s", invalidErr, proxyURLHint)
	}
	scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", "", "", "", fmt.Errorf("%w: proxy URL must not include path, query, or fragment; %s", invalidErr, proxyURLHint)
	}
	portStr = strings.TrimSpace(parsed.Port())
	if portStr == "" {
		return "", "", "", "", "", fmt.Errorf("%w: proxy URL must include an explicit port", invalidErr)
	}
	portNum, convErr := strconv.Atoi(portStr)
	if convErr != nil || portNum < 1 || portNum > 65535 {
		return "", "", "", "", "", fmt.Errorf("%w: invalid proxy port %q", invalidErr, portStr)
	}
	host = strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", "", "", "", "", fmt.Errorf("%w: missing proxy host", invalidErr)
	}
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	return scheme, host, portStr, username, password, nil
}

func parseTrustedLoopbackProxySpec(rawProxyURL string) (proxySpec, error) {
	scheme, host, portStr, username, password, err := parseProxySpecCommon(rawProxyURL, ErrBlockedAddress)
	if err != nil {
		return proxySpec{}, err
	}
	if username != "" || password != "" || strings.Contains(rawProxyURL, "@") {
		return proxySpec{}, fmt.Errorf("%w: proxy credentials not allowed for default proxy", ErrBlockedAddress)
	}
	if scheme != "http" {
		return proxySpec{}, fmt.Errorf("%w: unsupported proxy scheme %q", ErrBlockedAddress, scheme)
	}
	if strings.EqualFold(host, "localhost") {
		addr := net.JoinHostPort("127.0.0.1", portStr)
		return proxySpec{
			Scheme:     "http",
			DialAddrs:  []string{addr},
			DisplayURL: "http://" + addr,
		}, nil
	}
	addr, addrErr := netip.ParseAddr(host)
	if addrErr != nil || !addr.Unmap().IsLoopback() {
		return proxySpec{}, fmt.Errorf("%w: proxy host %q is not loopback", ErrBlockedAddress, host)
	}
	dialAddr := net.JoinHostPort(addr.Unmap().String(), portStr)
	return proxySpec{
		Scheme:     "http",
		DialAddrs:  []string{dialAddr},
		DisplayURL: "http://" + dialAddr,
	}, nil
}

// isRouterAddr сообщает, что адрес принадлежит самому роутеру. Нет списка
// адресов — считаем своим: лучше потребовать известный порт, чем пропустить.
func isRouterAddr(addr netip.Addr) bool {
	if addr.IsLoopback() || addr.IsUnspecified() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return true
	}
	ip := net.IP(addr.AsSlice())
	for _, item := range addrs {
		if ipNet, ok := item.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
			return true
		}
	}
	return false
}

// knownRouterProxy ищет host:port среди известных прокси роутера.
func (s *Service) knownRouterProxy(ctx context.Context, host, port string) (label string, ok bool) {
	if s.proxyLister != nil {
		want := net.JoinHostPort(host, port)
		for _, item := range s.proxyLister(ctx) {
			if _, hostPort, _ := strings.Cut(redactProxyURL(item.URL), "://"); hostPort == want {
				return strings.TrimSpace(item.Label), true
			}
		}
	}
	return "", s.routerProxyMatcher != nil && s.routerProxyMatcher(ctx, host, port)
}

func (s *Service) parseManualProxySpec(ctx context.Context, rawProxyURL string) (proxySpec, error) {
	scheme, host, portStr, username, password, err := parseProxySpecCommon(rawProxyURL, ErrInvalidProxy)
	if err != nil {
		return proxySpec{}, err
	}
	spec := proxySpec{Scheme: scheme, Username: username, Password: password}
	switch scheme {
	case "http", "socks5":
	case "socks5h":
		spec.Scheme = "socks5"
		spec.Pinned = &sync.Map{}
	default:
		return proxySpec{}, fmt.Errorf("%w: unsupported proxy scheme %q", ErrInvalidProxy, scheme)
	}

	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		addr = addr.Unmap()
		dialAddr := net.JoinHostPort(addr.String(), portStr)
		spec.DialAddrs = []string{dialAddr}
		spec.DisplayURL = scheme + "://" + dialAddr

		routerAddr := s.isRouterAddr(addr)
		scopeErr := s.checkDialAddr(addr)
		if scopeErr == nil && !routerAddr {
			return spec, nil
		}
		// На адресах роутера слушают его же службы (RCI, SSH, веб-морда):
		// туда пускаем только на порты известных прокси.
		label, known := s.knownRouterProxy(ctx, addr.String(), portStr)
		switch {
		case known:
			spec.Label = label
			return spec, nil
		case routerAddr:
			return proxySpec{}, fmt.Errorf("%w: %s belongs to the router, only ports of known router proxies are allowed", ErrInvalidProxy, dialAddr)
		default:
			return proxySpec{}, fmt.Errorf("%w: proxy host %s is blocked", ErrBlockedAddress, addr)
		}
	}

	if err := s.validateTargetHostname(host); err != nil {
		return proxySpec{}, err
	}
	ips, err := s.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return proxySpec{}, fmt.Errorf("%w: proxy DNS lookup failed for %s: %v", ErrInvalidProxy, host, err)
	}
	validIPs, err := s.validatePublicIPList(host, ips)
	if err != nil {
		return proxySpec{}, err
	}
	for _, ip := range orderIPsIPv4First(validIPs) {
		// Имя, ведущее на сам роутер, — обход проверки порта выше.
		if s.isRouterAddr(ip) {
			return proxySpec{}, fmt.Errorf("%w: %s resolves to the router itself", ErrInvalidProxy, host)
		}
		spec.DialAddrs = append(spec.DialAddrs, net.JoinHostPort(ip.String(), portStr))
	}
	spec.DisplayURL = scheme + "://" + net.JoinHostPort(strings.ToLower(host), portStr)
	return spec, nil
}

func (s *Service) newProxyTransportForSpec(spec proxySpec) *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // we dial proxy explicitly with verified literal IPs only
		DialContext:           s.safeProxyDialContext(spec),
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		MaxIdleConns:          4,
		TLSHandshakeTimeout:   s.fetchTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func (s *Service) buildCheckRedirect(validateHost func(req *http.Request) error, redirectURLs *[]*url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
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
		if err := validateHost(req); err != nil {
			return err
		}
		*redirectURLs = append(*redirectURLs, cloneURL(req.URL))
		return nil
	}
}

func (s *Service) fetchWithTransport(ctx context.Context, target *url.URL, tr *http.Transport) (*fetchResult, error) {
	defer tr.CloseIdleConnections()

	var redirectURLs []*url.URL
	client := &http.Client{
		Transport: tr,
		Timeout:   s.fetchTimeout,
		CheckRedirect: s.buildCheckRedirect(func(req *http.Request) error {
			return s.validateTargetHostname(req.URL.Hostname())
		}, &redirectURLs),
	}

	res, err := s.executeFetch(ctx, client, target)
	if err != nil {
		return nil, err
	}
	res.redirectURLs = redirectURLs
	return res, nil
}

func (s *Service) fetchDirect(ctx context.Context, target *url.URL) (*fetchResult, error) {
	return s.fetchWithTransport(ctx, target, s.newDirectTransport())
}

func (s *Service) fetchViaInterface(ctx context.Context, target *url.URL, iface string) (*fetchResult, error) {
	if err := s.validateTargetHostname(target.Hostname()); err != nil {
		return nil, err
	}
	return s.fetchWithTransport(ctx, target, s.newInterfaceTransport(iface))
}

func (s *Service) fetchViaProxySpec(ctx context.Context, target *url.URL, spec proxySpec) (*fetchResult, error) {
	if err := s.validateTargetHostname(target.Hostname()); err != nil {
		return nil, err
	}
	return s.fetchWithTransport(ctx, target, s.newProxyTransportForSpec(spec))
}

func (s *Service) executeFetch(ctx context.Context, client *http.Client, target *url.URL) (*fetchResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")

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

func (s *Service) safeInterfaceDialContext(iface string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
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
			return s.dialInterface(ctx, iface, network, net.JoinHostPort(parsedIP.Unmap().String(), port))
		}

		if err := s.validateTargetHostname(host); err != nil {
			return nil, err
		}

		ips, err := s.resolveInterfaceTargetIPs(ctx, iface, host)
		if err != nil {
			return nil, err
		}
		ordered := orderIPsIPv4First(ips)
		var lastErr error
		for _, ip := range ordered {
			conn, dialErr := s.dialInterface(ctx, iface, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			return nil, fmt.Errorf("%w: no usable public IP for %s", ErrBlockedAddress, host)
		}
		return nil, lastErr
	}
}

func (s *Service) resolveInterfaceTargetIPs(ctx context.Context, iface, host string) ([]netip.Addr, error) {
	if s.interfaceLookupIPv4 != nil {
		dnsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		ipStrs, err := s.interfaceLookupIPv4(dnsCtx, host, iface)
		cancel()
		if err == nil && len(ipStrs) > 0 {
			sawBenchmark := false
			publicAddrs := make([]netip.Addr, 0, len(ipStrs))
			for _, rawIP := range ipStrs {
				parsed, parseErr := netip.ParseAddr(strings.TrimSpace(rawIP))
				if parseErr != nil {
					continue
				}
				parsed = parsed.Unmap()
				_, scope := ipinspect.ClassifyAddr(parsed)
				switch scope {
				case "public":
					publicAddrs = append(publicAddrs, parsed)
				case "benchmark":
					sawBenchmark = true
				default:
					return nil, fmt.Errorf("%w: host %s resolves to non-public address %s (%s)", ErrBlockedAddress, host, parsed.String(), scope)
				}
			}
			if !sawBenchmark && len(publicAddrs) > 0 {
				return publicAddrs, nil
			}
		}
	}
	return s.lookupPublicDoHWithDialer(ctx, func(dCtx context.Context, addr string) (net.Conn, error) {
		return s.dialInterface(dCtx, iface, "tcp", addr)
	}, host)
}

func (s *Service) safeProxyDialContext(spec proxySpec) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
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
			return s.dialViaProxySpec(ctx, spec, net.JoinHostPort(parsedIP.Unmap().String(), port))
		}

		if err := s.validateTargetHostname(host); err != nil {
			return nil, err
		}

		ips, err := s.resolveProxyTargetIPs(ctx, spec, host)
		if err != nil {
			return nil, err
		}
		ordered := orderIPsIPv4First(ips)
		var lastErr error
		for _, ip := range ordered {
			conn, dialErr := s.dialViaProxySpec(ctx, spec, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			return nil, fmt.Errorf("%w: no usable public IP for %s", ErrBlockedAddress, host)
		}
		return nil, lastErr
	}
}

func orderIPsIPv4First(addrs []netip.Addr) []netip.Addr {
	seen := make(map[netip.Addr]struct{}, len(addrs))
	v4 := make([]netip.Addr, 0, len(addrs))
	v6 := make([]netip.Addr, 0, len(addrs))
	for _, raw := range addrs {
		addr := raw.Unmap()
		if !addr.IsValid() {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		if addr.Is4() {
			v4 = append(v4, addr)
		} else {
			v6 = append(v6, addr)
		}
	}
	return append(v4, v6...)
}

func (s *Service) resolveProxyTargetIPs(ctx context.Context, spec proxySpec, host string) ([]netip.Addr, error) {
	if s.proxyResolver != nil {
		ips, err := s.proxyResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("proxy DNS lookup failed for %s: %w", host, err)
		}
		return s.validatePublicIPList(host, ips)
	}

	if spec.Pinned != nil {
		addrs, err := s.lookupPublicDoHWithDialer(ctx, func(dCtx context.Context, addr string) (net.Conn, error) {
			return s.dialViaProxySpec(dCtx, spec, addr)
		}, host)
		if err == nil {
			spec.Pinned.Store(normalizeExtractedHost(host), addrs[0].String())
		}
		return addrs, err
	}

	// Probe local DNS first to catch LAN/split-horizon names and FakeIP redirects immediately.
	localCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ips, err := s.resolver.LookupIPAddr(localCtx, host)
	cancel()
	if err == nil && len(ips) > 0 {
		sawBenchmark := false
		publicAddrs := make([]netip.Addr, 0, len(ips))
		for _, entry := range ips {
			parsed, ok := netip.AddrFromSlice(entry.IP)
			if !ok {
				continue
			}
			parsed = parsed.Unmap()
			_, scope := ipinspect.ClassifyAddr(parsed)
			switch scope {
			case "public":
				publicAddrs = append(publicAddrs, parsed)
			case "benchmark":
				sawBenchmark = true
			default:
				return nil, fmt.Errorf("%w: host %s resolves to non-public address %s (%s)", ErrBlockedAddress, host, parsed.String(), scope)
			}
		}
		if !sawBenchmark && len(publicAddrs) > 0 {
			return publicAddrs, nil
		}
	}

	// Local lookup failed or returned FakeIP: resolve on the proxy exit side via DoH over the same
	// proxy so we never pass an unverified hostname in proxy CONNECT/SOCKS5/GET requests.
	return s.lookupPublicDoHWithDialer(ctx, func(dCtx context.Context, addr string) (net.Conn, error) {
		return s.dialViaProxySpec(dCtx, spec, addr)
	}, host)
}

func (s *Service) validatePublicIPList(host string, ips []net.IPAddr) ([]netip.Addr, error) {
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no public IP addresses resolved for %s", ErrBlockedAddress, host)
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, entry := range ips {
		parsed, ok := netip.AddrFromSlice(entry.IP)
		if !ok {
			continue
		}
		parsed = parsed.Unmap()
		if err := s.checkDialAddr(parsed); err != nil {
			return nil, fmt.Errorf("%s resolves to blocked IP %s: %w", host, parsed.String(), err)
		}
		out = append(out, parsed)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no usable public IP address for %s", ErrBlockedAddress, host)
	}
	return out, nil
}

type prefixedConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

func (s *Service) openProxyBaseConn(ctx context.Context, spec proxySpec) (net.Conn, error) {
	var lastErr error
	for _, proxyAddr := range spec.DialAddrs {
		conn, err := s.dialProxy(ctx, "tcp", proxyAddr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		return nil, ErrProxyUnavailable
	}
	return nil, lastErr
}

func (s *Service) dialViaProxySpec(ctx context.Context, spec proxySpec, targetAddr string) (net.Conn, error) {
	if spec.Scheme == "socks5" {
		return s.dialViaSOCKS5(ctx, spec, targetAddr)
	}
	return s.dialViaHTTPConnect(ctx, spec, targetAddr)
}

func (s *Service) dialViaHTTPConnect(ctx context.Context, spec proxySpec, targetAddr string) (net.Conn, error) {
	conn, err := s.openProxyBaseConn(ctx, spec)
	if err != nil {
		return nil, err
	}

	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.Close()
	})
	cleanupOnErr := func() {
		stopCancel()
		_ = conn.Close()
	}

	if deadline, has := ctx.Deadline(); has {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(s.fetchTimeout))
	}

	var reqBuf strings.Builder
	fmt.Fprintf(&reqBuf, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", targetAddr, targetAddr)
	if spec.Username != "" || spec.Password != "" {
		creds := base64.StdEncoding.EncodeToString([]byte(spec.Username + ":" + spec.Password))
		fmt.Fprintf(&reqBuf, "Proxy-Authorization: Basic %s\r\n", creds)
	}
	reqBuf.WriteString("\r\n")

	if _, err := io.WriteString(conn, reqBuf.String()); err != nil {
		cleanupOnErr()
		return nil, err
	}

	br := bufio.NewReader(io.LimitReader(conn, 16*1024))
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		cleanupOnErr()
		return nil, fmt.Errorf("proxy CONNECT %s: %w", targetAddr, err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cleanupOnErr()
		return nil, fmt.Errorf("proxy CONNECT %s failed: HTTP %d", targetAddr, resp.StatusCode)
	}

	if !stopCancel() {
		_ = conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})

	if br.Buffered() > 0 {
		buf := make([]byte, br.Buffered())
		n, _ := br.Read(buf)
		return &prefixedConn{Conn: conn, prefix: buf[:n]}, nil
	}
	return conn, nil
}

// pinnedProxyDialer отдаёт SOCKS-клиенту соединение с уже проверенными
// адресами прокси, игнорируя адрес, который назовёт библиотека.
type pinnedProxyDialer struct {
	s    *Service
	spec proxySpec
}

func (d pinnedProxyDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (d pinnedProxyDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	return d.s.openProxyBaseConn(ctx, d.spec)
}

func (s *Service) dialViaSOCKS5(ctx context.Context, spec proxySpec, targetAddr string) (net.Conn, error) {
	// Библиотека отправила бы имя прокси как есть — мимо проверки адреса.
	if _, err := netip.ParseAddrPort(targetAddr); err != nil {
		return nil, fmt.Errorf("%w: SOCKS5 requires literal IP target %q", ErrBlockedAddress, targetAddr)
	}
	var auth *proxy.Auth
	if spec.Username != "" || spec.Password != "" {
		auth = &proxy.Auth{User: spec.Username, Password: spec.Password}
	}
	dialer, err := proxy.SOCKS5("tcp", spec.DialAddrs[0], auth, pinnedProxyDialer{s: s, spec: spec})
	if err != nil {
		return nil, err
	}
	cd, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS dialer has no cancellation support")
	}
	dialCtx, cancel := context.WithTimeout(ctx, s.fetchTimeout)
	defer cancel()
	return cd.DialContext(dialCtx, "tcp", targetAddr)
}

func (s *Service) lookupPublicDoHWithDialer(ctx context.Context, dialTarget func(context.Context, string) (net.Conn, error), domain string) ([]netip.Addr, error) {
	return s.lookupPublicDoHDepthWithDialer(ctx, dialTarget, domain, 0)
}

func (s *Service) lookupPublicDoHDepthWithDialer(ctx context.Context, dialTarget func(context.Context, string) (net.Conn, error), domain string, depth int) ([]netip.Addr, error) {
	domain = normalizeExtractedHost(domain)
	if err := s.validateTargetHostname(domain); err != nil {
		return nil, err
	}
	if depth > 4 {
		return nil, fmt.Errorf("exit DoH CNAME chain too deep for %s", domain)
	}

	var lastErr error
	for _, endpoint := range s.dohEndpoints {
		addrs, cname, err := s.queryDoHServerWithDialer(ctx, dialTarget, endpoint, domain)
		if err != nil {
			if errors.Is(err, ErrBlockedAddress) {
				return nil, err
			}
			lastErr = err
			continue
		}
		if len(addrs) > 0 {
			valid := make([]netip.Addr, 0, len(addrs))
			for _, addr := range addrs {
				addr = addr.Unmap()
				if checkErr := s.checkDialAddr(addr); checkErr != nil {
					return nil, fmt.Errorf("%s resolved via exit DoH to blocked IP %s: %w", domain, addr.String(), checkErr)
				}
				valid = append(valid, addr)
			}
			return valid, nil
		}
		if cname != "" {
			return s.lookupPublicDoHDepthWithDialer(ctx, dialTarget, cname, depth+1)
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("exit DoH resolution failed for %s: %w", domain, lastErr)
	}
	return nil, fmt.Errorf("%w: exit DoH returned no public A records for %s", ErrBlockedAddress, domain)
}

func (s *Service) queryDoHServerWithDialer(ctx context.Context, dialTarget func(context.Context, string) (net.Conn, error), dohHost, domain string) ([]netip.Addr, string, error) {
	dohTarget := dohHost
	if _, _, err := net.SplitHostPort(dohTarget); err != nil {
		dohTarget = net.JoinHostPort(dohHost, "443")
	}
	serverName, _, err := net.SplitHostPort(dohTarget)
	if err != nil {
		serverName = dohHost
	}

	txID := uint16(s.dohTxSeq.Add(1))
	if txID == 0 {
		txID = uint16(s.dohTxSeq.Add(1))
	}
	wireQuery, err := encodeDoHQueryA(txID, domain)
	if err != nil {
		return nil, "", err
	}

	tr := &http.Transport{
		Proxy: nil,
		DialTLSContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
			rawConn, err := dialTarget(dialCtx, dohTarget)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(rawConn, &tls.Config{
				ServerName: strings.Trim(serverName, "[]"),
				RootCAs:    s.dohRootCAs,
				MinVersion: tls.VersionTLS12,
			})
			if err := tlsConn.HandshakeContext(dialCtx); err != nil {
				_ = tlsConn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
		ForceAttemptHTTP2:   false,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: s.fetchTimeout,
	}
	defer tr.CloseIdleConnections()

	dohCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dohCtx, http.MethodPost, "https://"+dohTarget+"/dns-query", bytes.NewReader(wireQuery))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := (&http.Client{Transport: tr, Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("DoH HTTP %d", resp.StatusCode)
	}
	wireResp, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return nil, "", err
	}
	return parseDoHAResponse(wireResp, domain, txID)
}

func encodeDoHQueryA(txID uint16, domain string) ([]byte, error) {
	labels := strings.Split(strings.TrimSuffix(normalizeExtractedHost(domain), "."), ".")
	wireLen := 1
	for _, lbl := range labels {
		if len(lbl) == 0 || len(lbl) > 63 {
			return nil, fmt.Errorf("invalid DNS name %q", domain)
		}
		wireLen += 1 + len(lbl)
	}
	if wireLen > 255 {
		return nil, fmt.Errorf("DNS name too long %q", domain)
	}

	pkt := make([]byte, 12+wireLen+4)
	binary.BigEndian.PutUint16(pkt[0:2], txID)
	binary.BigEndian.PutUint16(pkt[2:4], 0x0100) // RD=1
	binary.BigEndian.PutUint16(pkt[4:6], 1)      // QDCOUNT=1
	off := 12
	for _, lbl := range labels {
		pkt[off] = byte(len(lbl))
		copy(pkt[off+1:], lbl)
		off += 1 + len(lbl)
	}
	pkt[off] = 0
	off++
	binary.BigEndian.PutUint16(pkt[off:off+2], 1)   // QTYPE=A
	binary.BigEndian.PutUint16(pkt[off+2:off+4], 1) // QCLASS=IN
	return pkt, nil
}

func parseDoHAResponse(pkt []byte, domain string, expectedTxID uint16) ([]netip.Addr, string, error) {
	if len(pkt) < 12 {
		return nil, "", fmt.Errorf("short DoH DNS response")
	}
	if binary.BigEndian.Uint16(pkt[0:2]) != expectedTxID {
		return nil, "", fmt.Errorf("DoH DNS TXID mismatch")
	}
	flags := binary.BigEndian.Uint16(pkt[2:4])
	if (flags>>15)&1 != 1 {
		return nil, "", fmt.Errorf("unexpected DoH DNS query packet (QR=0)")
	}
	if opcode := (flags >> 11) & 0x0f; opcode != 0 {
		return nil, "", fmt.Errorf("unexpected DoH DNS opcode %d", opcode)
	}
	if (flags>>9)&1 != 0 {
		return nil, "", fmt.Errorf("DoH DNS response truncated (TC=1)")
	}

	wantDomain := strings.ToLower(strings.TrimSuffix(normalizeExtractedHost(domain), "."))
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))
	if qdCount != 1 {
		return nil, "", fmt.Errorf("DoH DNS expected QDCOUNT=1, got %d", qdCount)
	}
	qname, nextQ, err := readDNSName(pkt, 12)
	if err != nil || nextQ+4 > len(pkt) {
		return nil, "", fmt.Errorf("malformed DoH DNS question")
	}
	if strings.ToLower(strings.TrimSuffix(qname, ".")) != wantDomain {
		return nil, "", fmt.Errorf("DoH DNS question name mismatch: got %q want %q", qname, wantDomain)
	}
	if qType := binary.BigEndian.Uint16(pkt[nextQ : nextQ+2]); qType != 1 {
		return nil, "", fmt.Errorf("DoH DNS question QTYPE mismatch: %d", qType)
	}
	if qClass := binary.BigEndian.Uint16(pkt[nextQ+2 : nextQ+4]); qClass != 1 {
		return nil, "", fmt.Errorf("DoH DNS question QCLASS mismatch: %d", qClass)
	}

	if rcode := int(flags & 0x0f); rcode != 0 {
		return nil, "", fmt.Errorf("DoH DNS rcode %d for %s", rcode, wantDomain)
	}

	aRecords := make(map[string][]netip.Addr)
	cnameRecords := make(map[string]string)
	off := nextQ + 4
	for i := 0; i < anCount; i++ {
		ownerRaw, next, err := readDNSName(pkt, off)
		if err != nil || next+10 > len(pkt) {
			return nil, "", fmt.Errorf("malformed DoH DNS answer")
		}
		owner := strings.ToLower(strings.TrimSuffix(ownerRaw, "."))
		rrType := binary.BigEndian.Uint16(pkt[next : next+2])
		rrClass := binary.BigEndian.Uint16(pkt[next+2 : next+4])
		rdLen := int(binary.BigEndian.Uint16(pkt[next+8 : next+10]))
		rdataOff := next + 10
		if rdataOff+rdLen > len(pkt) {
			return nil, "", fmt.Errorf("truncated DoH DNS rdata")
		}
		if rrClass == 1 {
			switch rrType {
			case 1: // A
				if rdLen == 4 {
					addr := netip.AddrFrom4([4]byte{pkt[rdataOff], pkt[rdataOff+1], pkt[rdataOff+2], pkt[rdataOff+3]})
					aRecords[owner] = append(aRecords[owner], addr)
				}
			case 5: // CNAME
				if cnameRecords[owner] == "" {
					if targetRaw, _, cerr := readDNSName(pkt, rdataOff); cerr == nil {
						target := strings.ToLower(strings.TrimSuffix(targetRaw, "."))
						if target != "" {
							cnameRecords[owner] = target
						}
					}
				}
			}
		}
		off = rdataOff + rdLen
	}

	cur := wantDomain
	visited := map[string]bool{cur: true}
	for step := 0; step < 8; step++ {
		if addrs := aRecords[cur]; len(addrs) > 0 {
			return addrs, "", nil
		}
		nextOwner := cnameRecords[cur]
		if nextOwner == "" || visited[nextOwner] {
			break
		}
		visited[nextOwner] = true
		cur = nextOwner
	}
	if cur != wantDomain {
		return nil, cur, nil
	}
	return nil, "", nil
}

func readDNSName(pkt []byte, off int) (string, int, error) {
	var labels []string
	cur := off
	nextOff := -1
	jumps := 0
	for steps := 0; steps <= len(pkt); steps++ {
		if cur >= len(pkt) {
			return "", 0, fmt.Errorf("truncated DNS name")
		}
		l := pkt[cur]
		if l == 0 {
			if nextOff == -1 {
				nextOff = cur + 1
			}
			return strings.Join(labels, "."), nextOff, nil
		}
		if l&0xc0 == 0xc0 {
			if cur+2 > len(pkt) {
				return "", 0, fmt.Errorf("truncated DNS pointer")
			}
			if nextOff == -1 {
				nextOff = cur + 2
			}
			ptr := int(binary.BigEndian.Uint16(pkt[cur:cur+2]) & 0x3fff)
			if ptr >= len(pkt) {
				return "", 0, fmt.Errorf("out-of-range DNS pointer")
			}
			jumps++
			if jumps > 16 {
				return "", 0, fmt.Errorf("DNS pointer loop")
			}
			cur = ptr
			continue
		}
		if l&0xc0 != 0 {
			return "", 0, fmt.Errorf("invalid DNS label header")
		}
		end := cur + 1 + int(l)
		if end > len(pkt) {
			return "", 0, fmt.Errorf("truncated DNS label")
		}
		labels = append(labels, string(pkt[cur+1:end]))
		cur = end
	}
	return "", 0, fmt.Errorf("DNS name loop")
}

// enrichResources: имена из pinned (может быть nil) локально не резолвятся.
func (s *Service) enrichResources(ctx context.Context, extracted []extractedHost, pinned *sync.Map) []Resource {
	resources := make([]Resource, len(extracted))
	if len(extracted) == 0 {
		return resources
	}

	pinnedIP := func(host string) string {
		if pinned == nil {
			return ""
		}
		ip, _ := pinned.Load(host)
		str, _ := ip.(string)
		return str
	}

	domains := make([]string, 0, len(extracted))
	for _, item := range extracted {
		if pinnedIP(item.Host) == "" {
			domains = append(domains, item.Host)
		}
	}

	var routeMap map[string]RouteInfo
	if s.routeChecker != nil && len(domains) > 0 {
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
				IP:      pinnedIP(item.Host),
			}
			res.RouteUnknown = res.IP != ""

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
