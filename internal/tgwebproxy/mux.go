package tgwebproxy

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DeriveBridgeToken generates the HMAC-SHA256 URL-safe token for Telegram Web Proxy bridge validation.
func DeriveBridgeToken(host, secretHex string) string {
	secretBytes, err := hex.DecodeString(secretHex)
	if err != nil || len(secretBytes) == 0 {
		return ""
	}
	context := []byte("tdesktop-web-proxy-bridge-v1\n" + host)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write(context)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// DeriveFileBridgeToken generates the version-separated HMAC-SHA256 URL-safe token for file-https-v1 bridge validation.
func DeriveFileBridgeToken(host, secretHex string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	s := strings.ToLower(strings.TrimSpace(secretHex))
	if len(s) == 34 && (strings.HasPrefix(s, "dd") || strings.HasPrefix(s, "ee")) {
		s = s[2:]
	} else if len(s) > 34 && strings.HasPrefix(s, "ee") {
		s = s[2:34]
	}
	secretBytes, err := hex.DecodeString(s)
	if err != nil || len(secretBytes) == 0 {
		return ""
	}
	context := []byte("telegram-web-proxy-bridge-file-https-v1\n" + h)
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write(context)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type muxEndpoint struct {
	ID       string
	Hostname string
	Port     int
	Token    string
	Tokens   []string
	IsCDN    bool
	proxy    *httputil.ReverseProxy
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

func (ep *muxEndpoint) matchesToken(t string) bool {
	if t == "" {
		return false
	}
	if ep.Token != "" && ep.Token == t {
		return true
	}
	for _, tok := range ep.Tokens {
		if tok != "" && tok == t {
			return true
		}
	}
	return false
}

type sessionEntry struct {
	endpointID string
	expiresAt  time.Time
}

type clientEntry struct {
	endpointID string
	expiresAt  time.Time
}

type sessionCache struct {
	mu       sync.RWMutex
	sessions map[string]sessionEntry
	clients  map[string]clientEntry
}

func newSessionCache() *sessionCache {
	return &sessionCache{
		sessions: make(map[string]sessionEntry),
		clients:  make(map[string]clientEntry),
	}
}

func (sc *sessionCache) recordSession(token, endpointID string) {
	if token == "" || endpointID == "" {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.sessions[token] = sessionEntry{
		endpointID: endpointID,
		expiresAt:  time.Now().Add(24 * time.Hour),
	}
}

func (sc *sessionCache) lookupSession(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	sc.mu.RLock()
	entry, ok := sc.sessions[token]
	sc.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.endpointID, true
}

func (sc *sessionCache) recordClient(ip, endpointID string) {
	if ip == "" || endpointID == "" {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.clients[ip] = clientEntry{
		endpointID: endpointID,
		expiresAt:  time.Now().Add(30 * time.Minute),
	}
}

func (sc *sessionCache) lookupClient(ip string) (string, bool) {
	if ip == "" {
		return "", false
	}
	sc.mu.RLock()
	entry, ok := sc.clients[ip]
	sc.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.endpointID, true
}

func extractClientIP(r *http.Request) string {
	for _, h := range []string{"X-Real-IP", "CF-Connecting-IP", "X-Forwarded-For"} {
		v := r.Header.Get(h)
		if v != "" {
			parts := strings.Split(v, ",")
			first := strings.TrimSpace(parts[0])
			if ip := net.ParseIP(first); ip != nil {
				return first
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func extractSessionToken(r *http.Request) string {
	if s := r.URL.Query().Get("s"); s != "" {
		return s
	}
	if b := r.URL.Query().Get("b"); b != "" {
		return b
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if proto := r.Header.Get("Sec-WebSocket-Protocol"); proto != "" {
		for _, part := range strings.Split(proto, ",") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "tproxy-v1.") {
				return strings.TrimPrefix(part, "tproxy-v1.")
			}
			if strings.HasPrefix(part, "tproxy-session.") {
				return strings.TrimPrefix(part, "tproxy-session.")
			}
			if strings.HasPrefix(part, "tproxy-lane-v1.") {
				sub := strings.TrimPrefix(part, "tproxy-lane-v1.")
				if idx := strings.Index(sub, "."); idx != -1 {
					return sub[:idx]
				}
				return sub
			}
		}
	}
	return ""
}

// Mux is an in-process, high-performance multiplexer for Telegram Web Proxy backends.
// It listens on 127.0.0.1:8085 and routes requests to the correct tproxy-server backend based on
// HMAC bridge tokens, active sessions, client affinities, and Host headers.
type Mux struct {
	mu         sync.RWMutex
	endpoints  []muxEndpoint
	cache      *sessionCache
	server     *http.Server
	listener   net.Listener
	listenPort int
	running    bool
}

// NewMux creates a new Mux instance.
func NewMux() *Mux {
	return &Mux{
		cache: newSessionCache(),
	}
}

func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

type muxBufferPool struct {
	pool sync.Pool
}

func newMuxBufferPool() *muxBufferPool {
	return &muxBufferPool{
		pool: sync.Pool{
			New: func() any {
				buf := make([]byte, 64*1024)
				return &buf
			},
		},
	}
}

func (p *muxBufferPool) Get() []byte {
	return *(p.pool.Get().(*[]byte))
}

func (p *muxBufferPool) Put(b []byte) {
	if cap(b) >= 64*1024 {
		b = b[:64*1024]
		p.pool.Put(&b)
	}
}

var sharedMuxBufPool = newMuxBufferPool()

func (m *Mux) buildEndpoints(listenPort int, eps []WebProxyEndpoint, secretHex string) []muxEndpoint {
	var secrets []string
	if secretHex != "" {
		secrets = []string{secretHex}
	}
	return m.buildEndpointsWithSecrets(listenPort, eps, secrets)
}

func (m *Mux) buildEndpointsWithSecrets(muxListenPort int, eps []WebProxyEndpoint, secrets []string) []muxEndpoint {
	if muxListenPort <= 0 {
		muxListenPort = DefaultListenPort
	}
	var built []muxEndpoint
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   50,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}

	for _, ep := range eps {
		if !ep.Enabled {
			continue
		}
		port := ep.ListenPort
		if port <= 0 {
			port = 8084
		}
		targetURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		host := ep.Hostname
		var tokens []string
		for _, sec := range secrets {
			if tok := DeriveBridgeToken(host, sec); tok != "" {
				tokens = append(tokens, tok)
			}
			if fTok := DeriveFileBridgeToken(host, sec); fTok != "" {
				tokens = append(tokens, fTok)
			}
		}
		var primaryToken string
		if len(tokens) > 0 {
			primaryToken = tokens[0]
		}
		epID := ep.ID

		rp := &httputil.ReverseProxy{
			Transport:  transport,
			BufferPool: sharedMuxBufPool,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(targetURL)
				pr.Out.Host = host
				pr.Out.Header.Set("Host", host)
				pr.Out.Header.Add("X-Awgm-Proxy-Hop", fmt.Sprintf("mux:%d", muxListenPort))

				// tproxy-server strictly requires X-Forwarded-For to be a single IP without commas.
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
				} else if remoteHost, _, err := net.SplitHostPort(pr.In.RemoteAddr); err == nil {
					pr.Out.Header.Set("X-Forwarded-For", remoteHost)
				}
			},
			ModifyResponse: func(resp *http.Response) error {
				if sToken := resp.Header.Get("X-Session-Token"); sToken != "" {
					m.cache.recordSession(sToken, epID)
				}
				return nil
			},
		}

		built = append(built, muxEndpoint{
			ID:       epID,
			Hostname: host,
			Port:     port,
			Token:    primaryToken,
			Tokens:   tokens,
			IsCDN:    ep.IsCDN,
			proxy:    rp,
		})
	}
	return built
}

// Update updates the routing endpoints and secret.
func (m *Mux) Update(eps []WebProxyEndpoint, secretHex string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpoints = m.buildEndpoints(m.listenPort, eps, secretHex)
}

// UpdateWithSecrets updates the routing endpoints and multiple user secrets.
func (m *Mux) UpdateWithSecrets(eps []WebProxyEndpoint, secrets []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endpoints = m.buildEndpointsWithSecrets(m.listenPort, eps, secrets)
}

func (m *Mux) matchEndpoint(r *http.Request) *muxEndpoint {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.endpoints) == 0 {
		return nil
	}

	clientIP := extractClientIP(r)

	// 1. Check bridge token in query parameter (?bridge=<token> or ?b=<token>)
	bridgeToken := r.URL.Query().Get("bridge")
	if bridgeToken == "" {
		bridgeToken = r.URL.Query().Get("b")
	}
	if bridgeToken != "" {
		for i := range m.endpoints {
			if m.endpoints[i].matchesToken(bridgeToken) {
				if clientIP != "" {
					m.cache.recordClient(clientIP, m.endpoints[i].ID)
				}
				return &m.endpoints[i]
			}
		}
	}

	// 2. Check active session token (?s=<token>, Bearer, or Sec-WebSocket-Protocol)
	if sToken := extractSessionToken(r); sToken != "" {
		if epID, ok := m.cache.lookupSession(sToken); ok {
			for i := range m.endpoints {
				if m.endpoints[i].ID == epID {
					return &m.endpoints[i]
				}
			}
		}
	}

	// 3. Check CDN-specific markers and Host
	isCDNReq := r.Header.Get("X-Cdn-Request-Id") != "" ||
		r.Header.Get("X-CDN-Request-Id") != "" ||
		r.Header.Get("X-Cdn2-Request-Id") != "" ||
		r.Header.Get("X-Real-Ip") != "" ||
		r.Header.Get("X-Real-IP") != "" ||
		strings.Contains(r.Host, ".selcdn.net")

	reqHost := normalizeHost(r.Host)
	if fwdHost := normalizeHost(r.Header.Get("X-Forwarded-Host")); fwdHost != "" {
		reqHost = fwdHost
	}

	// 4. For CDN requests where Host might be rewritten to the origin, check client IP affinity
	if isCDNReq && clientIP != "" && (strings.HasPrefix(r.URL.Path, "/api/v1/") || strings.HasPrefix(r.URL.Path, "/bridge/v1/")) {
		if epID, ok := m.cache.lookupClient(clientIP); ok {
			for i := range m.endpoints {
				if m.endpoints[i].ID == epID {
					return &m.endpoints[i]
				}
			}
		}
	}

	if isCDNReq {
		for i := range m.endpoints {
			if m.endpoints[i].IsCDN && (normalizeHost(m.endpoints[i].Hostname) == reqHost) {
				return &m.endpoints[i]
			}
		}
		for i := range m.endpoints {
			if m.endpoints[i].IsCDN {
				return &m.endpoints[i]
			}
		}
	}

	// 5. Check Host and X-Forwarded-Host
	for i := range m.endpoints {
		if normalizeHost(m.endpoints[i].Hostname) == reqHost {
			return &m.endpoints[i]
		}
	}

	// 6. Fallback: return the first configured endpoint
	return &m.endpoints[0]
}

func copyRecordedResponse(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vv := range rec.Header() {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	muxPort := m.ListenPort()
	if muxPort <= 0 {
		muxPort = DefaultListenPort
	}
	if hasLoopHop(r, fmt.Sprintf("mux:%d", muxPort), 6) {
		http.Error(w, http.StatusText(http.StatusLoopDetected), http.StatusLoopDetected)
		return
	}
	target := m.matchEndpoint(r)
	if target == nil || target.proxy == nil {
		http.Error(w, "Telegram Web Proxy backend unavailable", http.StatusServiceUnavailable)
		return
	}

	// Special resilience for session creation: GET /api/v1/session?b=... or POST /api/v1/session or GET /bridge/v1/session.bin
	isSessionCreate := (strings.HasPrefix(r.URL.Path, "/api/v1/session") || strings.HasPrefix(r.URL.Path, "/bridge/v1/session.bin")) &&
		r.URL.Query().Get("s") == "" &&
		r.URL.Query().Get("action") == "" &&
		(r.Method == http.MethodGet || r.Method == http.MethodPost)

	if isSessionCreate {
		m.mu.RLock()
		endpoints := m.endpoints
		m.mu.RUnlock()

		if len(endpoints) > 1 {
			var bodyBytes []byte
			if r.Body != nil && r.Method == http.MethodPost {
				bodyBytes, _ = io.ReadAll(io.LimitReader(r.Body, 64*1024))
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				r.ContentLength = int64(len(bodyBytes))
			}

			rec := httptest.NewRecorder()
			target.proxy.ServeHTTP(rec, r)

			if rec.Code == http.StatusOK {
				if clientIP := extractClientIP(r); clientIP != "" {
					m.cache.recordClient(clientIP, target.ID)
				}
				copyRecordedResponse(w, rec)
				return
			}

			// If preferred endpoint returned 404, try alternative endpoints
			if rec.Code == http.StatusNotFound {
				for i := range endpoints {
					if endpoints[i].ID == target.ID || endpoints[i].proxy == nil {
						continue
					}
					if bodyBytes != nil {
						r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
						r.ContentLength = int64(len(bodyBytes))
					}
					recAlt := httptest.NewRecorder()
					endpoints[i].proxy.ServeHTTP(recAlt, r)
					if recAlt.Code == http.StatusOK {
						if clientIP := extractClientIP(r); clientIP != "" {
							m.cache.recordClient(clientIP, endpoints[i].ID)
						}
						copyRecordedResponse(w, recAlt)
						return
					}
				}
			}

			// None succeeded: write original response
			copyRecordedResponse(w, rec)
			return
		}
	}

	target.proxy.ServeHTTP(w, r)
}

// Start starts the Mux HTTP listener on listenPort (default 8085).
func (m *Mux) Start(listenPort int, eps []WebProxyEndpoint, secretHex string) error {
	var secrets []string
	if secretHex != "" {
		secrets = []string{secretHex}
	}
	return m.StartWithSecrets(listenPort, eps, secrets)
}

// StartWithSecrets starts the Mux HTTP listener with multiple user secrets.
// When already running on the same port, it updates endpoints in place; when listenPort
// changes, it binds the new listener first and only closes the previous listener on success.
func (m *Mux) StartWithSecrets(listenPort int, eps []WebProxyEndpoint, secrets []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if listenPort <= 0 {
		listenPort = DefaultListenPort
	}

	if m.running && m.listener != nil && m.listenPort == listenPort {
		m.endpoints = m.buildEndpointsWithSecrets(listenPort, eps, secrets)
		return nil
	}

	addr := fmt.Sprintf("127.0.0.1:%d", listenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mux listen on %s failed: %w", addr, err)
	}

	srv := &http.Server{
		Handler: m,
		// A relay request can remain active for the lifetime of a media upload,
		// download, or a carrier long-poll.  ReadTimeout and WriteTimeout are
		// absolute deadlines in net/http, so setting them here used to cut a
		// healthy transfer after 120 seconds (and IdleTimeout after 60 seconds).
		// Bound only header parsing; protocol-level tproxy limits handle idle
		// sessions and carrier retries.
		ReadHeaderTimeout: 15 * time.Second,
	}

	oldSrv := m.server
	oldLn := m.listener

	m.endpoints = m.buildEndpointsWithSecrets(listenPort, eps, secrets)
	m.listener = ln
	m.server = srv
	m.listenPort = listenPort
	m.running = true

	go func() {
		_ = srv.Serve(ln)
	}()

	if oldSrv != nil {
		_ = oldSrv.Close()
	} else if oldLn != nil {
		_ = oldLn.Close()
	}

	return nil
}

// Stop stops the Mux listener and closes active connections.
func (m *Mux) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.running {
		return nil
	}

	var err error
	if m.server != nil {
		err = m.server.Close()
	} else if m.listener != nil {
		err = m.listener.Close()
	}

	m.running = false
	m.listenPort = 0
	m.server = nil
	m.listener = nil
	return err
}

// IsRunning reports whether the Mux listener is running.
func (m *Mux) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// ListenPort returns the active TCP port bound by the Mux listener (or 0 if stopped).
func (m *Mux) ListenPort() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.running {
		return 0
	}
	return m.listenPort
}
