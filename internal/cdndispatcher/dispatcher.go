package cdndispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
)

// Dispatcher coordinates HTTP traffic from the router's origin proxy port (default :9009)
// to either Xray (for VPN path e.g. /cdn-bridge) or Telegram WEB Proxy (for /?bridge= and /api/v1).
type Dispatcher struct {
	mu       sync.Mutex
	dataDir  string
	cfg      Config
	snapshot atomic.Pointer[routeSnapshot]
	server   *http.Server
	listener net.Listener
	exited   chan struct{}
	running  bool
}

// CDNRouteRule defines a domain routing rule for CDN Ingress.
type CDNRouteRule struct {
	ID             string `json:"id"`
	Domain         string `json:"domain"`                    // FQDN (e.g. cdn.domain.ru)
	Description    string `json:"description"`               // Human-readable label (e.g. "Primary CDN")
	RouteXray      bool   `json:"route_xray"`                // Proxy /cdn-bridge/* to Xray
	RouteTelegram  bool   `json:"route_telegram"`            // Proxy /, /api/*, /bridge/* to Telegram WebProxy
	TelegramTarget string `json:"telegram_target,omitempty"` // Optional host-specific Telegram backend override (e.g. "http://127.0.0.1:9099")
	Enabled        bool   `json:"enabled"`
}

type Config struct {
	Enabled                bool           `json:"enabled"`
	ListenAddr             string         `json:"listen_addr"`                        // e.g. ":9009"
	XrayTarget             string         `json:"xray_target"`                        // e.g. "http://127.0.0.1:9008"
	TgTarget               string         `json:"tg_target"`                          // e.g. "http://127.0.0.1:8085"
	Routes                 []CDNRouteRule `json:"routes,omitempty"`                   // Structured domain routing rules
	DefaultAction          string         `json:"default_action,omitempty"`           // "404" | "reject"
	PublicHostname         string         `json:"public_hostname,omitempty"`          // Shared Public domain for CDN ingress (default empty)
	XrayPublicHost         string         `json:"xray_public_host,omitempty"`         // Dedicated Xray CDN host if distinct
	TgPublicHost           string         `json:"tg_public_host,omitempty"`           // Dedicated Telegram CDN host if distinct
	XrayPathPrefix         string         `json:"xray_path_prefix"`                   // e.g. "/cdn-bridge"
	XrayAdditionalPrefixes []string       `json:"xray_additional_prefixes,omitempty"` // Additional Xray path prefixes from multi-endpoint configs
}

type routeSnapshot struct {
	listenAddr       string
	xrayProxy        http.Handler
	tgProxy          http.Handler
	ruleTgProxies    map[string]http.Handler
	xrayPathPrefix   string
	xrayPathPrefixes []string
	defaultAction    string
	routeMap         map[string]CDNRouteRule
	hasRules         bool
	publicHostname   string
	xrayPublicHost   string
	tgPublicHost     string
}

func extractAddrPort(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	if strings.HasPrefix(addr, ":") {
		return strings.TrimPrefix(addr, ":")
	}
	return ""
}

func isLoopbackPortConflict(listenAddr, targetURL string) bool {
	lp := extractAddrPort(listenAddr)
	if lp == "" || strings.TrimSpace(targetURL) == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil || u.Host == "" {
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port != lp {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// ValidateConfig verifies dispatcher targets do not create a loopback forwarding self-loop.
func ValidateConfig(cfg Config) error {
	listenAddr := strings.TrimSpace(cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = ":9009"
	}
	if cfg.XrayTarget != "" {
		if _, err := url.Parse(cfg.XrayTarget); err != nil {
			return fmt.Errorf("invalid xray target %q: %w", cfg.XrayTarget, err)
		}
		if isLoopbackPortConflict(listenAddr, cfg.XrayTarget) {
			return fmt.Errorf("dispatcher xray target %q self-references listen address %q", cfg.XrayTarget, listenAddr)
		}
	}
	if cfg.TgTarget != "" {
		if _, err := url.Parse(cfg.TgTarget); err != nil {
			return fmt.Errorf("invalid tg target %q: %w", cfg.TgTarget, err)
		}
		if isLoopbackPortConflict(listenAddr, cfg.TgTarget) {
			return fmt.Errorf("dispatcher tg target %q self-references listen address %q", cfg.TgTarget, listenAddr)
		}
	}
	for _, r := range cfg.Routes {
		if t := strings.TrimSpace(r.TelegramTarget); t != "" {
			if _, err := url.Parse(t); err != nil {
				return fmt.Errorf("invalid route telegram target %q: %w", t, err)
			}
			if isLoopbackPortConflict(listenAddr, t) {
				return fmt.Errorf("dispatcher route %q telegram target %q self-references listen address %q", r.Domain, t, listenAddr)
			}
		}
	}
	return nil
}

func hasLoopHop(r *http.Request, selfHop string, maxHops int) bool {
	if r == nil {
		return false
	}
	count := 0
	for _, h := range r.Header.Values("X-Awgm-Proxy-Hop") {
		for _, part := range strings.Split(h, ",") {
			token := strings.TrimSpace(part)
			if token == "" {
				continue
			}
			count++
			if token == selfHop || count >= maxHops {
				return true
			}
		}
	}
	return false
}

func normalizeHost(rawHost string) string {
	rawHost = strings.TrimSpace(rawHost)
	if rawHost == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(rawHost); err == nil {
		rawHost = h
	}
	rawHost = strings.TrimPrefix(rawHost, "[")
	rawHost = strings.TrimSuffix(rawHost, "]")
	return strings.ToLower(strings.TrimSpace(rawHost))
}

func isTelegramEndpoint(path string) bool {
	if path == "/" || path == "" {
		return true
	}
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		return true
	}
	if path == "/tg" || strings.HasPrefix(path, "/tg/") {
		return true
	}
	if path == "/bridge" || strings.HasPrefix(path, "/bridge/") {
		return true
	}
	if path == "/metrics" || path == "/health" {
		return true
	}
	return false
}

func New(cfg Config) *Dispatcher {
	return NewWithDataDir("/opt/etc/awg-manager", cfg)
}

func NewWithDataDir(dataDir string, cfg Config) *Dispatcher {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":9009"
	}
	if cfg.XrayTarget == "" {
		cfg.XrayTarget = "http://127.0.0.1:9008"
	}
	if cfg.TgTarget == "" {
		cfg.TgTarget = "http://127.0.0.1:8085"
	}
	if cfg.XrayPathPrefix == "" {
		cfg.XrayPathPrefix = "/cdn-bridge"
	}

	d := &Dispatcher{
		dataDir: dataDir,
		cfg:     cfg,
	}
	if dataDir == "/opt/etc/awg-manager" {
		legacyNginxOnce.Do(func() { go RemoveLegacyNginxTuning() })
	}

	snap, _ := d.buildSnapshot(cfg)
	if snap != nil {
		d.snapshot.Store(snap)
	}

	return d
}

func (d *Dispatcher) SetDataDir(dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dataDir = dir
}

// NormalizePathPrefix returns a clean, canonical path prefix with leading slash and no trailing slash (e.g. "/cdn-bridge").
func NormalizePathPrefix(prefix string) string {
	norm := "/" + strings.Trim(strings.TrimSpace(prefix), "/")
	if norm == "/" {
		return "/cdn-bridge"
	}
	return norm
}

type proxyBufferPool struct {
	pool sync.Pool
}

func newProxyBufferPool() *proxyBufferPool {
	return &proxyBufferPool{
		pool: sync.Pool{
			New: func() any {
				buf := make([]byte, 64*1024)
				return &buf
			},
		},
	}
}

func (p *proxyBufferPool) Get() []byte {
	return *(p.pool.Get().(*[]byte))
}

func (p *proxyBufferPool) Put(b []byte) {
	if cap(b) >= 64*1024 {
		b = b[:64*1024]
		p.pool.Put(&b)
	}
}

var sharedProxyBufPool = newProxyBufferPool()

// tailFlushWriter flushes streaming XHTTP GET responses immediately while tracking
// activity for the stream reaper.
type tailFlushWriter struct {
	w          http.ResponseWriter
	flusher    http.Flusher
	onActivity func()
	mu         sync.Mutex
}

func isKeenDNSLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		// Check if X-Forwarded-Host or Host is a non-KeenDNS external CDN
		inHost := normalizeHost(r.Header.Get("X-Forwarded-Host"))
		if inHost == "" {
			inHost = normalizeHost(r.Host)
		}
		if strings.Contains(inHost, ".crazedns.") || strings.Contains(inHost, ".keenetic.") || strings.Contains(inHost, ".netcraze.") || inHost == "127.0.0.1" || inHost == "localhost" {
			return true
		}
	}
	return false
}

func newTailFlushWriter(w http.ResponseWriter, _ bool, onActivity func()) *tailFlushWriter {
	f, _ := w.(http.Flusher)
	return &tailFlushWriter{
		w:          w,
		flusher:    f,
		onActivity: onActivity,
	}
}

func (t *tailFlushWriter) Header() http.Header {
	return t.w.Header()
}

func (t *tailFlushWriter) WriteHeader(statusCode int) {
	t.w.WriteHeader(statusCode)
}

func (t *tailFlushWriter) Unwrap() http.ResponseWriter {
	return t.w
}

func (t *tailFlushWriter) Write(b []byte) (int, error) {
	if t.onActivity != nil && len(b) > 0 {
		t.onActivity()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// ReverseProxy sets FlushInterval=-1 and invokes Flush() after every copy
	// iteration; flushing again inside Write doubles flush/syscall work on MIPS.
	return t.w.Write(b)
}

func (t *tailFlushWriter) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.flusher != nil {
		t.flusher.Flush()
	}
}

func (t *tailFlushWriter) Close() {}

const (
	maxActiveXrayGetStreams  = 512
	maxKeenDNSXrayGetStreams = 56
)

type xrayGetEntry struct {
	id         uint64
	sid        string
	keenDNS    bool
	createdAt  time.Time
	lastActive atomic.Int64
	hasUplink  atomic.Bool
	cancel     context.CancelFunc
}

type xrayStreamTracker struct {
	mu          sync.Mutex
	nextID      uint64
	activeByID  map[uint64]*xrayGetEntry
	activeBySID map[string]*xrayGetEntry
	recentPosts map[string]time.Time
	reapRunning bool
	stopCh      chan struct{}
}

var globalXrayTracker = newXrayStreamTracker()

func newXrayStreamTracker() *xrayStreamTracker {
	return &xrayStreamTracker{
		activeByID:  make(map[uint64]*xrayGetEntry),
		activeBySID: make(map[string]*xrayGetEntry),
		recentPosts: make(map[string]time.Time),
	}
}

func (tr *xrayStreamTracker) stopAll() {
	var toCancel []context.CancelFunc
	tr.mu.Lock()
	for id, e := range tr.activeByID {
		delete(tr.activeByID, id)
		toCancel = append(toCancel, e.cancel)
	}
	tr.activeBySID = make(map[string]*xrayGetEntry)
	tr.recentPosts = make(map[string]time.Time)
	if tr.reapRunning && tr.stopCh != nil {
		close(tr.stopCh)
		tr.stopCh = nil
		tr.reapRunning = false
	}
	tr.mu.Unlock()
	for _, fn := range toCancel {
		fn()
	}
}

func extractXraySID(r *http.Request, prefixes []string) (sid string, isGetStream bool) {
	if r.Header.Get("X-FP-Version") != "" || r.Header.Get("X-FP-Auth") != "" {
		return "", false
	}
	q := r.URL.Query()
	sid = q.Get("sid")
	if sid == "" {
		sid = q.Get("session_id")
	}
	if sid == "" {
		sid = r.Header.Get("X-Session-ID")
	}
	if sid == "" {
		for _, prefix := range prefixes {
			if prefix != "" && strings.HasPrefix(r.URL.Path, prefix+"/") {
				rem := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix+"/"), "/")
				if rem != "" {
					parts := strings.Split(rem, "/")
					if len(parts) > 0 && (parts[0] == "stream.ts" || parts[0] == "stream.m3u8") {
						parts = parts[1:]
					}
					if len(parts) >= 2 && sid == "" {
						sid = parts[len(parts)-2]
					} else if len(parts) == 1 && sid == "" {
						sid = parts[0]
					}
				}
				break
			}
		}
	}
	seq := q.Get("page")
	if seq == "" {
		seq = q.Get("seq")
	}
	if seq == "" {
		seq = r.Header.Get("X-Seq")
	}
	isGetStream = (r.Method == http.MethodGet && seq == "")
	return sid, isGetStream
}

func (tr *xrayStreamTracker) registerGet(sid string, keenDNS bool, cancel context.CancelFunc) *xrayGetEntry {
	now := time.Now()
	nowNano := now.UnixNano()

	var toCancel []context.CancelFunc

	tr.mu.Lock()
	// If an older GET stream with the exact same SID exists, replace it
	if sid != "" {
		if old, ok := tr.activeBySID[sid]; ok {
			delete(tr.activeByID, old.id)
			delete(tr.activeBySID, sid)
			toCancel = append(toCancel, old.cancel)
		}
	}

	countMatch := func(onlyKeenDNS bool) int {
		if !onlyKeenDNS {
			return len(tr.activeByID)
		}
		c := 0
		for _, e := range tr.activeByID {
			if e.keenDNS {
				c++
			}
		}
		return c
	}

	limit := maxActiveXrayGetStreams
	if keenDNS {
		limit = maxKeenDNSXrayGetStreams
	}

	for countMatch(keenDNS) >= limit {
		var victim *xrayGetEntry
		for _, e := range tr.activeByID {
			if keenDNS && !e.keenDNS {
				continue
			}
			if !e.hasUplink.Load() {
				if victim == nil || e.createdAt.Before(victim.createdAt) {
					victim = e
				}
			}
		}
		if victim == nil {
			for _, e := range tr.activeByID {
				if keenDNS && !e.keenDNS {
					continue
				}
				if victim == nil || e.lastActive.Load() < victim.lastActive.Load() {
					victim = e
				}
			}
		}
		if victim == nil {
			break
		}
		delete(tr.activeByID, victim.id)
		if victim.sid != "" && tr.activeBySID[victim.sid] == victim {
			delete(tr.activeBySID, victim.sid)
		}
		toCancel = append(toCancel, victim.cancel)
	}

	tr.nextID++
	entry := &xrayGetEntry{
		id:        tr.nextID,
		sid:       sid,
		keenDNS:   keenDNS,
		createdAt: now,
		cancel:    cancel,
	}
	entry.lastActive.Store(nowNano)

	if sid != "" {
		if postTime, ok := tr.recentPosts[sid]; ok && now.Sub(postTime) < 15*time.Second {
			entry.hasUplink.Store(true)
		}
		tr.activeBySID[sid] = entry
	}
	tr.activeByID[entry.id] = entry
	if !tr.reapRunning {
		tr.reapRunning = true
		stopCh := make(chan struct{})
		tr.stopCh = stopCh
		go tr.reapLoop(stopCh)
	}
	tr.mu.Unlock()

	for _, fn := range toCancel {
		fn()
	}
	return entry
}

func (tr *xrayStreamTracker) unregisterGet(entry *xrayGetEntry) {
	if entry == nil {
		return
	}
	tr.mu.Lock()
	delete(tr.activeByID, entry.id)
	if entry.sid != "" && tr.activeBySID[entry.sid] == entry {
		delete(tr.activeBySID, entry.sid)
	}
	if len(tr.activeByID) == 0 && tr.reapRunning && tr.stopCh != nil {
		close(tr.stopCh)
		tr.stopCh = nil
		tr.reapRunning = false
	}
	tr.mu.Unlock()
}

func (tr *xrayStreamTracker) recordUplink(sid string) {
	if sid == "" {
		return
	}
	now := time.Now()
	nowNano := now.UnixNano()
	tr.mu.Lock()
	tr.recentPosts[sid] = now
	if entry, ok := tr.activeBySID[sid]; ok {
		entry.hasUplink.Store(true)
		entry.lastActive.Store(nowNano)
	}
	tr.mu.Unlock()
}

func (tr *xrayStreamTracker) reapLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case now := <-ticker.C:
			var toCancel []context.CancelFunc
			tr.mu.Lock()
			totalCount := len(tr.activeByID)
			keenCount := 0
			for _, e := range tr.activeByID {
				if e.keenDNS {
					keenCount++
				}
			}

			for id, e := range tr.activeByID {
				handshakeLimit := 8 * time.Second
				idleLimit := 35 * time.Second
				if totalCount >= 40 {
					idleLimit = 18 * time.Second
				} else if totalCount >= 24 {
					idleLimit = 25 * time.Second
				}
				if e.keenDNS {
					handshakeLimit = 6 * time.Second
					if idleLimit > 30*time.Second {
						idleLimit = 30 * time.Second
					}
					if keenCount >= 40 {
						handshakeLimit = 4500 * time.Millisecond
						idleLimit = 15 * time.Second
					} else if keenCount >= 28 {
						handshakeLimit = 5 * time.Second
						if idleLimit > 20*time.Second {
							idleLimit = 20 * time.Second
						}
					}
				}
				age := now.Sub(e.createdAt)
				idle := time.Duration(now.UnixNano() - e.lastActive.Load())
				if (!e.hasUplink.Load() && age > handshakeLimit) || idle > idleLimit {
					delete(tr.activeByID, id)
					if e.sid != "" && tr.activeBySID[e.sid] == e {
						delete(tr.activeBySID, e.sid)
					}
					toCancel = append(toCancel, e.cancel)
				}
			}

			if len(tr.recentPosts) > 256 {
				for k, t := range tr.recentPosts {
					if now.Sub(t) > 20*time.Second {
						delete(tr.recentPosts, k)
					}
				}
			}
			shouldExit := len(tr.activeByID) == 0
			if shouldExit && tr.stopCh == stopCh {
				tr.reapRunning = false
				tr.stopCh = nil
			}
			tr.mu.Unlock()

			for _, fn := range toCancel {
				fn()
			}
			if shouldExit {
				return
			}
		}
	}
}

func (d *Dispatcher) wrapXrayStreamHandler(prefixes []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow HTTP/1.1 full-duplex streaming (stream-one / stream-up) so ReverseProxy
		// streams response bytes immediately without waiting for the request body EOF.
		_ = http.NewResponseController(w).EnableFullDuplex()
		sid, isGetStream := extractXraySID(r, prefixes)
		if isGetStream {
			keenDNS := isKeenDNSLoopbackRequest(r)
			ctx, cancel := context.WithCancel(r.Context())
			entry := globalXrayTracker.registerGet(sid, keenDNS, cancel)
			defer func() {
				globalXrayTracker.unregisterGet(entry)
				cancel()
			}()
			tf := newTailFlushWriter(w, keenDNS, func() {
				entry.hasUplink.Store(true)
				entry.lastActive.Store(time.Now().UnixNano())
			})
			defer tf.Close()
			next.ServeHTTP(tf, r.WithContext(ctx))
			return
		}
		if sid != "" {
			globalXrayTracker.recordUplink(sid)
		}
		next.ServeHTTP(w, r)
	})
}

func (d *Dispatcher) buildSnapshot(cfg Config) (*routeSnapshot, error) {
	normPrefix := NormalizePathPrefix(cfg.XrayPathPrefix)
	xrayPrefixes := []string{normPrefix}
	seenXrayPrefixes := map[string]bool{normPrefix: true}
	for _, rawP := range cfg.XrayAdditionalPrefixes {
		if strings.TrimSpace(rawP) == "" {
			continue
		}
		np := NormalizePathPrefix(rawP)
		if np != "" && !seenXrayPrefixes[np] {
			seenXrayPrefixes[np] = true
			xrayPrefixes = append(xrayPrefixes, np)
		}
	}

	normPublic := normalizeHost(cfg.PublicHostname)
	normXray := normalizeHost(cfg.XrayPublicHost)
	normTg := normalizeHost(cfg.TgPublicHost)

	if normXray == "" && normPublic != "" {
		normXray = normPublic
	}
	if normTg == "" && normPublic != "" {
		normTg = normPublic
	}
	if normTg == "" && d.dataDir != "" {
		tproxyCfgPath := filepath.Join(d.dataDir, "tproxy", "config.json")
		if data, err := os.ReadFile(tproxyCfgPath); err == nil {
			var tp struct {
				PublicHostname string `json:"public_hostname"`
			}
			if json.Unmarshal(data, &tp) == nil && tp.PublicHostname != "" {
				normTg = normalizeHost(tp.PublicHostname)
			}
		}
	}

	listenAddr := strings.TrimSpace(cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = ":9009"
	}
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}

	snap := &routeSnapshot{
		listenAddr:       listenAddr,
		xrayPathPrefix:   normPrefix,
		xrayPathPrefixes: xrayPrefixes,
		publicHostname:   normPublic,
		xrayPublicHost:   normXray,
		tgPublicHost:     normTg,
		routeMap:         make(map[string]CDNRouteRule),
		ruleTgProxies:    make(map[string]http.Handler),
		defaultAction:    "404",
	}

	if strings.EqualFold(strings.TrimSpace(cfg.DefaultAction), "reject") {
		snap.defaultAction = "reject"
	}

	if len(cfg.Routes) > 0 {
		for _, r := range cfg.Routes {
			norm := normalizeHost(r.Domain)
			if norm != "" {
				snap.routeMap[norm] = r
			}
		}
		snap.hasRules = len(snap.routeMap) > 0
	}

	proxyTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
		ReadBufferSize:        64 * 1024,
		WriteBufferSize:       64 * 1024,
	}

	if cfg.XrayTarget != "" {
		xrayURL, err := url.Parse(cfg.XrayTarget)
		if err != nil {
			return nil, fmt.Errorf("invalid xray target: %w", err)
		}
		snap.xrayProxy = d.wrapXrayStreamHandler(xrayPrefixes, &httputil.ReverseProxy{
			Transport:     proxyTransport,
			FlushInterval: -1,
			BufferPool:    sharedProxyBufPool,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(xrayURL)
				pr.Out.Host = pr.In.Host
				pr.Out.Header.Add("X-Awgm-Proxy-Hop", "dispatcher:"+listenAddr)

				clientIP := pr.In.Header.Get("CF-Connecting-IP")
				if clientIP == "" {
					clientIP = pr.In.Header.Get("X-Real-IP")
				}
				if clientIP == "" {
					clientIP = pr.In.Header.Get("X-Forwarded-For")
				}
				if clientIP != "" {
					parts := strings.Split(clientIP, ",")
					firstIP := strings.TrimSpace(parts[0])
					if ip := net.ParseIP(firstIP); ip != nil {
						pr.Out.Header.Set("X-Forwarded-For", firstIP)
					}
				} else if pr.In.RemoteAddr != "" {
					host, _, _ := net.SplitHostPort(pr.In.RemoteAddr)
					if host != "" {
						pr.Out.Header.Set("X-Forwarded-For", host)
					}
				}

				isFP := strings.TrimSpace(pr.In.Header.Get("X-FP-Version")) != "" || strings.TrimSpace(pr.In.Header.Get("X-FP-Auth")) != ""
				if !isFP {
					// 1. Ensure trailing slash if request path is exactly prefix (/cdn-bridge -> /cdn-bridge/)
					// Xray's splithttp handler strictly expects path prefix with trailing slash.
					// FiniteProto v2 requests bind the exact request path in X-FP-Auth HMAC, so preserve their path untouched.
					for _, p := range xrayPrefixes {
						if pr.Out.URL.Path == p {
							pr.Out.URL.Path = p + "/"
							break
						}
					}

					// 2. Fix: When requests arrive via CDN or KeenDNS relay, the Referer header
					// (which Xray SplitHTTP uses to carry x_padding) is often stripped by edge servers
					// or local reverse proxies. If neither Referer nor x_padding query parameter is present,
					// inject a valid padding Referer header so Xray server's padding validation succeeds.
					if pr.Out.Header.Get("Referer") == "" && pr.Out.URL.Query().Get("x_padding") == "" && pr.Out.URL.Query().Get("_dc") == "" {
						dummyPadding := strings.Repeat("X", 300)
						targetHost := normXray
						if targetHost == "" {
							targetHost = pr.Out.Host
						}
						pr.Out.Header.Set("Referer", "https://"+targetHost+normPrefix+"/?_dc="+dummyPadding+"&x_padding="+dummyPadding)
					}
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				// ServeHTTP already sets X-CDN-Route on w.Header(); remove any upstream
				// copy so ReverseProxy does not emit duplicate X-Cdn-Route headers.
				resp.Header.Del("X-CDN-Route")
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0, s-maxage=0, no-transform")
				resp.Header.Set("Pragma", "no-cache")
				resp.Header.Set("Expires", "0")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				w.Header().Set("X-CDN-Route", "xray")
				http.Error(w, "Xray service unavailable", http.StatusServiceUnavailable)
			},
		})
	}

	newTgProxy := func(target string) (http.Handler, error) {
		tgURL, err := url.Parse(target)
		if err != nil {
			return nil, fmt.Errorf("invalid tg target %q: %w", target, err)
		}
		return &httputil.ReverseProxy{
			Transport:  proxyTransport,
			BufferPool: sharedProxyBufPool,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(tgURL)
				pr.Out.Header.Add("X-Awgm-Proxy-Hop", "dispatcher:"+listenAddr)
				// Preserve the incoming Host / X-Forwarded-Host for the Go Multiplexer (:8085)
				inHost := pr.In.Header.Get("X-Forwarded-Host")
				if inHost == "" {
					inHost = pr.In.Host
				}
				inHost = normalizeHost(inHost)
				if inHost != "" && !strings.HasPrefix(inHost, "127.") && inHost != "localhost" {
					pr.Out.Host = inHost
					pr.Out.Header.Set("Host", inHost)
					pr.Out.Header.Set("X-Forwarded-Host", inHost)
				} else if normTg != "" {
					pr.Out.Host = normTg
					pr.Out.Header.Set("Host", normTg)
				} else if cfg.PublicHostname != "" {
					pr.Out.Host = cfg.PublicHostname
					pr.Out.Header.Set("Host", cfg.PublicHostname)
				} else {
					pr.Out.Host = pr.In.Host
				}
				clientIP := pr.In.Header.Get("CF-Connecting-IP")
				if clientIP == "" {
					clientIP = pr.In.Header.Get("X-Real-IP")
				}
				if clientIP == "" {
					clientIP = pr.In.Header.Get("X-Forwarded-For")
				}
				if clientIP != "" {
					parts := strings.Split(clientIP, ",")
					firstIP := strings.TrimSpace(parts[0])
					if ip := net.ParseIP(firstIP); ip != nil {
						pr.Out.Header.Set("X-Forwarded-For", firstIP)
					}
				} else if pr.In.RemoteAddr != "" {
					host, _, _ := net.SplitHostPort(pr.In.RemoteAddr)
					if host != "" {
						pr.Out.Header.Set("X-Forwarded-For", host)
					}
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				// ServeHTTP already sets X-CDN-Route on w.Header(); remove any upstream
				// copy so ReverseProxy does not emit duplicate X-Cdn-Route headers.
				resp.Header.Del("X-CDN-Route")
				resp.Header.Set("X-Accel-Buffering", "no")
				resp.Header.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0, s-maxage=0, no-transform")
				resp.Header.Set("Pragma", "no-cache")
				resp.Header.Set("Expires", "0")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				w.Header().Set("X-CDN-Route", "tgwebproxy")
				http.Error(w, "Telegram service unavailable", http.StatusServiceUnavailable)
			},
		}, nil
	}

	if cfg.TgTarget != "" {
		p, err := newTgProxy(cfg.TgTarget)
		if err != nil {
			return nil, err
		}
		snap.tgProxy = p
	}

	for normHost, r := range snap.routeMap {
		if target := strings.TrimSpace(r.TelegramTarget); target != "" {
			p, err := newTgProxy(target)
			if err != nil {
				return nil, err
			}
			snap.ruleTgProxies[normHost] = p
		}
	}

	return snap, nil
}

func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Loopback-only health check endpoint
	if r.URL.Path == "/.cdndisp/health" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
		return
	}

	snap := d.snapshot.Load()
	if snap == nil {
		http.Error(w, "Dispatcher not configured", http.StatusServiceUnavailable)
		return
	}
	if hasLoopHop(r, "dispatcher:"+snap.listenAddr, 6) {
		http.Error(w, http.StatusText(http.StatusLoopDetected), http.StatusLoopDetected)
		return
	}

	path := r.URL.Path
	prefix := snap.xrayPathPrefix
	matchesXray := path == prefix || strings.HasPrefix(path, prefix+"/")
	if !matchesXray && len(snap.xrayPathPrefixes) > 1 {
		for _, p := range snap.xrayPathPrefixes {
			if path == p || strings.HasPrefix(path, p+"/") {
				matchesXray = true
				break
			}
		}
	}
	isTelegram := isTelegramEndpoint(path)

	reqHost := normalizeHost(r.Host)
	if fwdHost := normalizeHost(r.Header.Get("X-Forwarded-Host")); fwdHost != "" {
		reqHost = fwdHost
	}
	xrayHost := snap.xrayPublicHost
	tgHost := snap.tgPublicHost

	reject := func() {
		if snap.defaultAction == "reject" {
			w.Header().Set("Connection", "close")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	}

	isLoopback := reqHost == "127.0.0.1" || reqHost == "localhost" || strings.HasPrefix(reqHost, "127.")

	// Local loopback requests (e.g. KeenDNS relay on 127.0.0.1) can route directly by path:
	if isLoopback {
		go EnsureKeeneticPreserveHost()
		if matchesXray {
			if snap.xrayProxy != nil {
				w.Header().Set("X-CDN-Route", "xray")
				snap.xrayProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "xray")
			http.Error(w, "Xray target unavailable", http.StatusServiceUnavailable)
			return
		}
		if isTelegram {
			if snap.tgProxy != nil {
				w.Header().Set("X-CDN-Route", "tgwebproxy")
				snap.tgProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "tgwebproxy")
			http.Error(w, "Telegram target unavailable", http.StatusServiceUnavailable)
			return
		}
		reject()
		return
	}

	// 1. Structured routing rules (Lock-Free hash table):
	if snap.hasRules {
		rule, found := snap.routeMap[reqHost]
		if !found || !rule.Enabled {
			reject()
			return
		}

		if matchesXray && rule.RouteXray {
			if snap.xrayProxy != nil {
				w.Header().Set("X-CDN-Route", "xray")
				snap.xrayProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "xray")
			http.Error(w, "Xray target unavailable", http.StatusServiceUnavailable)
			return
		}

		if isTelegram && rule.RouteTelegram {
			targetProxy := snap.tgProxy
			if customProxy, ok := snap.ruleTgProxies[reqHost]; ok && customProxy != nil {
				targetProxy = customProxy
			}
			if targetProxy != nil {
				w.Header().Set("X-CDN-Route", "tgwebproxy")
				targetProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "tgwebproxy")
			http.Error(w, "Telegram target unavailable", http.StatusServiceUnavailable)
			return
		}

		// Host matched but route for this path is disabled
		reject()
		return
	}

	// 2. Legacy fallback routing when no structured rules configured:
	if xrayHost == "" && tgHost == "" {
		if matchesXray {
			if snap.xrayProxy != nil {
				w.Header().Set("X-CDN-Route", "xray")
				snap.xrayProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "xray")
			http.Error(w, "Xray target unavailable", http.StatusServiceUnavailable)
			return
		}
		if isTelegram {
			if snap.tgProxy != nil {
				w.Header().Set("X-CDN-Route", "tgwebproxy")
				snap.tgProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "tgwebproxy")
			http.Error(w, "Telegram target unavailable", http.StatusServiceUnavailable)
			return
		}
		reject()
		return
	}

	isHostMatched := func(pattern, host string) bool {
		if pattern == "" {
			return false
		}
		for _, p := range strings.Split(pattern, ",") {
			if normalizeHost(p) == host {
				return true
			}
		}
		return false
	}
	isXrayMatch := isHostMatched(xrayHost, reqHost)
	isTgMatch := isHostMatched(tgHost, reqHost)

	// Distinct host routing:
	// 1. Host == XrayPublicHost && !isTgMatch (Xray-only host)
	if isXrayMatch && !isTgMatch {
		if matchesXray {
			if snap.xrayProxy != nil {
				w.Header().Set("X-CDN-Route", "xray")
				snap.xrayProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "xray")
			http.Error(w, "Xray target unavailable", http.StatusServiceUnavailable)
			return
		}
		reject()
		return
	}

	// 2. Host == TgPublicHost && !isXrayMatch (Telegram-only host)
	if isTgMatch && !isXrayMatch {
		if isTelegram {
			if snap.tgProxy != nil {
				w.Header().Set("X-CDN-Route", "tgwebproxy")
				snap.tgProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "tgwebproxy")
			http.Error(w, "Telegram target unavailable", http.StatusServiceUnavailable)
			return
		}
		reject()
		return
	}

	// 3. Shared host (Host == XrayPublicHost && Host == TgPublicHost)
	if isXrayMatch && isTgMatch {
		if matchesXray {
			if snap.xrayProxy != nil {
				w.Header().Set("X-CDN-Route", "xray")
				snap.xrayProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "xray")
			http.Error(w, "Xray target unavailable", http.StatusServiceUnavailable)
			return
		}
		if isTelegram {
			if snap.tgProxy != nil {
				w.Header().Set("X-CDN-Route", "tgwebproxy")
				snap.tgProxy.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-CDN-Route", "tgwebproxy")
			http.Error(w, "Telegram target unavailable", http.StatusServiceUnavailable)
			return
		}
		reject()
		return
	}

	// 4. Unknown host
	reject()
}

func (d *Dispatcher) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.startLocked()
}

func (d *Dispatcher) startLocked() error {
	if d.running {
		return nil
	}

	snap, err := d.buildSnapshot(d.cfg)
	if err != nil {
		return err
	}
	d.snapshot.Store(snap)

	ln, err := net.Listen("tcp", d.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("cdn dispatcher bind %s: %w", d.cfg.ListenAddr, err)
	}

	server := newDispatcherServer(d)

	exited := make(chan struct{})
	d.server = server
	d.listener = ln
	d.exited = exited
	d.running = true

	go func() {
		defer close(exited)
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[CDN-Dispatcher] Serve error: %v", err)
		}
		d.mu.Lock()
		if d.server == server {
			d.running = false
		}
		d.mu.Unlock()
	}()

	go EnsureKeeneticPreserveHost()

	return nil
}

func (d *Dispatcher) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopLocked()
}

func (d *Dispatcher) stopLocked() error {
	if !d.running || d.server == nil {
		return nil
	}

	server := d.server
	exited := d.exited
	d.running = false
	d.server = nil
	d.listener = nil

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := server.Shutdown(ctx)
	select {
	case <-exited:
	case <-ctx.Done():
		_ = server.Close()
	}
	globalXrayTracker.stopAll()
	return err
}

type applyMode int

const (
	modePatch applyMode = iota
	modeReplace
)

func (d *Dispatcher) applyConfigLocked(cfg Config, mode applyMode) error {
	var newCfg Config
	if mode == modePatch {
		newCfg = d.cfg
		if cfg.ListenAddr != "" {
			newCfg.ListenAddr = cfg.ListenAddr
		}
		if cfg.XrayTarget != "" {
			newCfg.XrayTarget = cfg.XrayTarget
		}
		if cfg.TgTarget != "" {
			newCfg.TgTarget = cfg.TgTarget
		}
		if cfg.PublicHostname != "" {
			newCfg.PublicHostname = cfg.PublicHostname
		}
		if cfg.XrayPublicHost != "" {
			newCfg.XrayPublicHost = cfg.XrayPublicHost
		}
		if cfg.TgPublicHost != "" {
			newCfg.TgPublicHost = cfg.TgPublicHost
		}
		if cfg.XrayPathPrefix != "" {
			newCfg.XrayPathPrefix = cfg.XrayPathPrefix
		}
		if cfg.Routes != nil {
			newCfg.Routes = cfg.Routes
		}
		if cfg.DefaultAction != "" {
			newCfg.DefaultAction = cfg.DefaultAction
		}
		newCfg.Enabled = cfg.Enabled
	} else {
		newCfg = cfg
	}

	newSnap, err := d.buildSnapshot(newCfg)
	if err != nil {
		return err
	}

	if !d.running {
		d.cfg = newCfg
		d.snapshot.Store(newSnap)
		return nil
	}

	// Two-phase listener switch if address changed
	if newCfg.ListenAddr != d.cfg.ListenAddr {
		newLn, err := net.Listen("tcp", newCfg.ListenAddr)
		if err != nil {
			return fmt.Errorf("cdn dispatcher reconfigure bind %s: %w", newCfg.ListenAddr, err)
		}

		oldServer := d.server
		oldExited := d.exited

		newServer := newDispatcherServer(d)
		newExited := make(chan struct{})

		d.snapshot.Store(newSnap)
		d.cfg = newCfg
		d.server = newServer
		d.listener = newLn
		d.exited = newExited

		go func() {
			defer close(newExited)
			if err := newServer.Serve(newLn); err != nil && err != http.ErrServerClosed {
				log.Printf("[CDN-Dispatcher] Serve error: %v", err)
			}
			d.mu.Lock()
			if d.server == newServer {
				d.running = false
			}
			d.mu.Unlock()
		}()

		// Drain old server asynchronously
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = oldServer.Shutdown(ctx)
			select {
			case <-oldExited:
			case <-ctx.Done():
				_ = oldServer.Close()
			}
		}()

		return nil
	}

	// Address didn't change: atomic snapshot swap
	d.cfg = newCfg
	d.snapshot.Store(newSnap)
	go EnsureKeeneticPreserveHost()
	return nil
}

func (d *Dispatcher) Reconfigure(cfg Config) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.applyConfigLocked(cfg, modePatch)
}

// ApplyConfig performs full-state replacement of the dispatcher configuration, allowing fields like PublicHostname to be cleared.
func (d *Dispatcher) ApplyConfig(cfg Config) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.applyConfigLocked(cfg, modeReplace)
}

func (d *Dispatcher) UpdateConfig(cfg Config) {
	_ = d.Reconfigure(cfg)
}

// StartConfigured starts the dispatcher runtime (satisfies BootLifecycle).
func (d *Dispatcher) StartConfigured() error {
	return d.Start()
}

// ShutdownRuntime stops the dispatcher runtime (satisfies BootLifecycle).
func (d *Dispatcher) ShutdownRuntime(ctx context.Context) error {
	return d.Stop()
}

func (d *Dispatcher) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

func (d *Dispatcher) GetConfig() Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cfg
}

var (
	preserveHostMu        sync.Mutex
	lastPreserveHostCheck time.Time
)

func runNdmc(cmdStr string) error {
	if p, err := exec.LookPath("ndmc"); err == nil {
		return exec.Command(p, "-c", cmdStr).Run()
	}
	if _, err := os.Stat("/bin/ndmc"); err == nil {
		return exec.Command("/bin/ndmc", "-c", cmdStr).Run()
	}
	if p, err := exec.LookPath("ndmq"); err == nil {
		return exec.Command(p, "-p", cmdStr, "-x").Run()
	}
	return errors.New("ndmc not found")
}

// EnsureKeeneticPreserveHost checks Keenetic's local RCI for any "ip http proxy" entries
// that target either this dispatcher (port 9009) or the Telegram proxy multiplexer (port 8085),
// and invokes ndmc/ndmq to enable "preserve-host" so incoming requests retain their Host header.
// nginx роутера (его глобальные настройки, HTTP/2, лимиты) мы не трогаем:
// это системный веб-сервер, смотрящий в интернет.
func EnsureKeeneticPreserveHost() {
	preserveHostMu.Lock()
	if time.Since(lastPreserveHostCheck) < 5*time.Second {
		preserveHostMu.Unlock()
		return
	}
	lastPreserveHostCheck = time.Now()
	preserveHostMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:79/rci/show/ip/http/proxy", nil)
	if err != nil {
		return
	}
	resp, err := (&http.Client{Transport: transport.SharedTransport()}).Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var proxies map[string]struct {
		Upstream string `json:"upstream"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&proxies); err != nil {
		return
	}

	for name, p := range proxies {
		if strings.HasSuffix(p.Upstream, ":9009") || strings.HasSuffix(p.Upstream, ":8085") {
			_ = runNdmc(fmt.Sprintf("ip http proxy %s preserve-host", name))
		}
	}
}

// CleanupSystemHooks снимает правку nginx роутера, которую ставили сборки
// #1239 (хук 65-awgm-nginx-unbuffer.sh), при полном сбросе диспетчера.
func (d *Dispatcher) CleanupSystemHooks() {
	d.mu.Lock()
	dataDir := d.dataDir
	d.mu.Unlock()
	if dataDir != "/opt/etc/awg-manager" && dataDir != "" {
		return
	}
	RemoveLegacyNginxTuning()
}

// legacyNginxOnce — уборка правки nginx на старте демона, один раз.
var legacyNginxOnce sync.Once

// Пути правки nginx из сборок #1239. Переменные — шов для тестов.
var (
	legacyNginxHookPath = "/opt/etc/ndm/netfilter.d/65-awgm-nginx-unbuffer.sh"
	legacyNginxOptConf  = "/opt/etc/awg-manager/comp_off.conf"
	legacyNginxCompOff  = "/etc/nginx/comp_off.conf"
	legacyNginxConf     = "/tmp/nginx/nginx.conf"
	runNginxCmd         = func(args ...string) error { return exec.Command(args[0], args[1:]...).Run() }
)

// legacyHTTP2Listen — ровно то, что дописывал хук к слушателям :443 и к
// unix-сокетам ndns/ndmp; обратная замена возвращает строку прошивки.
var legacyHTTP2Listen = regexp.MustCompile(`(listen [^;]*) ssl http2( proxy_protocol)?;`)

// RemoveLegacyNginxTuning снимает правку nginx роутера из сборок #1239: хук на
// каждое событие netfilter включал HTTP/2 на ВСЕХ слушателях :443 (включая
// внешний адрес), вырезал limit_req/limit_conn у блоков наших прокси и
// подменял глобальный comp_off.conf. Идемпотентна; ничего не делает, если
// правки не было. HTTP/2 откатываем только при следах нашего хука — если
// прошивка когда-нибудь включит его сама, мы его не трогаем. Вырезанные
// limit_* вернёт следующая пересборка конфига прошивкой. nginx не
// останавливаем (остановка роняет веб-управление, F771) — только reload.
func RemoveLegacyNginxTuning() {
	if runtime.GOOS != "linux" {
		return
	}
	_, hookErr := os.Stat(legacyNginxHookPath)
	_, optErr := os.Stat(legacyNginxOptConf)
	if hookErr != nil && optErr != nil {
		return
	}
	_ = os.Remove(legacyNginxHookPath)
	reload := false
	if optErr == nil {
		_ = runNginxCmd("umount", legacyNginxCompOff)
		_ = os.Remove(legacyNginxOptConf)
		reload = true
	}
	if hookErr == nil {
		if conf, err := os.ReadFile(legacyNginxConf); err == nil {
			fixed := legacyHTTP2Listen.ReplaceAll(conf, []byte("$1 ssl$2;"))
			if string(fixed) != string(conf) {
				tmp := legacyNginxConf + ".awgm"
				if os.WriteFile(tmp, fixed, 0o644) == nil && runNginxCmd("nginx", "-t", "-c", tmp) == nil && os.Rename(tmp, legacyNginxConf) == nil {
					reload = true
				}
				_ = os.Remove(tmp)
			}
		}
	}
	if reload {
		_ = runNginxCmd("nginx", "-s", "reload")
	}
}

// newDispatcherServer: адрес слушания не сужаем — в режиме origin внешний CDN
// забирает трафик с <адрес роутера>:9009 напрямую. ReadTimeout/WriteTimeout не
// ставим: через диспетчер идут долгие потоки XHTTP и WebSocket. Срок на
// заголовки закрывает медленный slowloris.
func newDispatcherServer(h http.Handler) *http.Server {
	return &http.Server{Handler: h, ReadHeaderTimeout: dispatcherReadHeaderTimeout}
}

const dispatcherReadHeaderTimeout = 10 * time.Second
