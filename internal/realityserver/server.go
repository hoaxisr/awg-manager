package realityserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xtls/reality"
)

const (
	TransportTCP   = "tcp"
	TransportXHTTP = "xhttp"

	DefaultXHTTPPath = "/rx"
)

// Config describes the desired REALITY inbound.
type Config struct {
	// ListenAddr is the TCP bind address, e.g. "0.0.0.0:2053".
	ListenAddr string
	// Transport is "tcp" (default) or "xhttp".
	Transport string
	// Dest is the camouflage target "host:port" (must speak TLS 1.3 + X25519).
	Dest string
	// ServerNames are the SNI values accepted from clients (normally the dest host).
	ServerNames []string
	// PrivateKey is the X25519 private key (Xray base64url format).
	PrivateKey string
	// ShortIDs are the accepted client short ids (hex).
	ShortIDs []string
	// MaxTimeDiff limits client clock skew (0 = disabled, Xray default).
	MaxTimeDiff time.Duration
	// XHTTPPath is the XHTTP path prefix. For Transport == "tcp" it is the
	// internal path the raw stream is handed to awgm-xhttpd under.
	XHTTPPath string
	// Upstream is the awgm-xhttpd address ("127.0.0.1:port"); the daemon must
	// serve XHTTPPath and trust loopback for X-Forwarded-For.
	Upstream string
}

// Normalize fills defaults and validates the configuration.
func (c Config) Normalize() (Config, error) {
	c.ListenAddr = strings.TrimSpace(c.ListenAddr)
	host, portStr, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return c, fmt.Errorf("realityserver: invalid listen address %q: %w", c.ListenAddr, err)
	}
	// Port 0 (ephemeral) is accepted for tests; the service layer validates real ports.
	if p, err := strconv.Atoi(portStr); err != nil || p < 0 || p > 65535 {
		return c, fmt.Errorf("realityserver: invalid listen port %q", portStr)
	}
	if host == "" {
		c.ListenAddr = net.JoinHostPort("0.0.0.0", portStr)
	}

	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	switch c.Transport {
	case "", "raw", TransportTCP:
		c.Transport = TransportTCP
	case TransportXHTTP:
	default:
		return c, fmt.Errorf("realityserver: unsupported transport %q", c.Transport)
	}

	dest, err := NormalizeDest(c.Dest)
	if err != nil {
		return c, err
	}
	c.Dest = dest

	var names []string
	seen := map[string]bool{}
	for _, n := range c.ServerNames {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	if len(names) == 0 {
		h, _, _ := net.SplitHostPort(c.Dest)
		if net.ParseIP(h) != nil {
			return c, errors.New("realityserver: server_names required when dest is an IP address")
		}
		names = []string{strings.ToLower(h)}
	}
	c.ServerNames = names

	if _, err := DecodeKey(c.PrivateKey); err != nil {
		return c, err
	}

	var sids []string
	seenSID := map[string]bool{}
	for _, s := range c.ShortIDs {
		s = strings.ToLower(strings.TrimSpace(s))
		if _, err := ParseShortID(s); err != nil {
			return c, err
		}
		if seenSID[s] {
			continue
		}
		seenSID[s] = true
		sids = append(sids, s)
	}
	if len(sids) == 0 {
		return c, errors.New("realityserver: at least one short id is required")
	}
	c.ShortIDs = sids

	p := "/" + strings.Trim(strings.TrimSpace(c.XHTTPPath), "/")
	if p == "/" || c.Transport == TransportTCP {
		p = DefaultXHTTPPath
	}
	c.XHTTPPath = p
	c.Upstream = strings.TrimSpace(c.Upstream)
	if _, _, err := net.SplitHostPort(c.Upstream); err != nil {
		return c, fmt.Errorf("realityserver: invalid upstream %q: %w", c.Upstream, err)
	}
	return c, nil
}

// NormalizeDest validates "host[:port]" and defaults the port to 443.
func NormalizeDest(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimSuffix(d, "/")
	if d == "" {
		return "", errors.New("realityserver: dest is required")
	}
	host, port, err := net.SplitHostPort(d)
	if err != nil {
		host, port = d, "443"
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.ContainsAny(host, " /\\") {
		return "", fmt.Errorf("realityserver: invalid dest host %q", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return "", fmt.Errorf("realityserver: invalid dest port %q", port)
	}
	return net.JoinHostPort(host, port), nil
}

// listenerKey captures every parameter that requires rebinding the listener.
func (c Config) listenerKey() string {
	names := append([]string(nil), c.ServerNames...)
	sort.Strings(names)
	sids := append([]string(nil), c.ShortIDs...)
	sort.Strings(sids)
	return strings.Join([]string{
		c.ListenAddr, c.Transport, c.Dest, strings.Join(names, ","),
		c.PrivateKey, strings.Join(sids, ","), c.XHTTPPath, c.MaxTimeDiff.String(), c.Upstream,
	}, "|")
}

func (c Config) realityConfig() (*reality.Config, error) {
	priv, err := DecodeKey(c.PrivateKey)
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(c.ServerNames))
	for _, n := range c.ServerNames {
		names[n] = true
	}
	sids := make(map[[8]byte]bool, len(c.ShortIDs))
	for _, s := range c.ShortIDs {
		id, err := ParseShortID(s)
		if err != nil {
			return nil, err
		}
		sids[id] = true
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	return &reality.Config{
		DialContext:            dialer.DialContext,
		Type:                   "tcp",
		Dest:                   c.Dest,
		ServerNames:            names,
		PrivateKey:             priv,
		ShortIds:               sids,
		MaxTimeDiff:            c.MaxTimeDiff,
		SessionTicketsDisabled: true,
	}, nil
}

// Status is a runtime snapshot.
type Status struct {
	Running     bool   `json:"running"`
	ListenAddr  string `json:"listen_addr,omitempty"`
	Transport   string `json:"transport,omitempty"`
	ActiveConns int    `json:"active_conns"`
	StartedAt   string `json:"started_at,omitempty"`
}

// Server is the REALITY inbound runtime. The zero value is not usable; use New.
type Server struct {
	mu        sync.Mutex
	cfg       Config
	key       string
	running   bool
	tcpLn     net.Listener
	httpSrv   *http.Server
	upstream  *http.Transport
	tracker   *connTracker
	ctx       context.Context
	cancel    context.CancelFunc
	startedAt time.Time

	// listen is overridable in tests.
	listen func(network, addr string) (net.Listener, error)
}

// maxConcurrentConns caps open connections (handshaking + established).
const maxConcurrentConns = 512

// New creates an idle REALITY server.
func New() *Server {
	return &Server{listen: net.Listen}
}

// Apply starts the server or reconciles it with cfg. Clients and egress live
// in awgm-xhttpd, so only listener-level changes matter here: they rebind the
// port, anything else keeps existing connections.
func (s *Server) Apply(cfg Config) error {
	norm, err := cfg.Normalize()
	if err != nil {
		return err
	}
	key := norm.listenerKey()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running && s.key == key {
		s.cfg = norm
		return nil
	}
	s.stopLocked()
	return s.startLocked(norm, key)
}

func (s *Server) startLocked(cfg Config, key string) error {
	rcfg, err := cfg.realityConfig()
	if err != nil {
		return err
	}
	listen := s.listen
	if listen == nil {
		listen = net.Listen
	}
	tcpLn, err := listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("realityserver: listen %s: %w", cfg.ListenAddr, err)
	}

	tracker := newConnTracker()
	// Порт открыт с WAN, а лимиты сессий есть только у C-демона: без потолка
	// поток подключений съел бы память роутера горутинами и пробами dest.
	// ponytail: общий потолок; лимит на IP — если упрёмся в одного клиента.
	limited := newLimitListener(tcpLn, maxConcurrentConns)
	rln := &trackingListener{Listener: reality.NewListener(limited, rcfg), tracker: tracker}
	ctx, cancel := context.WithCancel(context.Background())
	tr := newUpstreamTransport()

	var httpSrv *http.Server
	switch cfg.Transport {
	case TransportXHTTP:
		// Клиенты под REALITY говорят h2 без ALPN-согласования на нашей стороне
		// (TLS держит reality.Conn), поэтому h2c с prior knowledge плюс h1.
		var protos http.Protocols
		protos.SetHTTP1(true)
		protos.SetUnencryptedHTTP2(true)
		httpSrv = &http.Server{
			Handler:           newXHTTPProxy(cfg.Upstream, cfg.XHTTPPath, tr),
			ErrorLog:          quietLog,
			Protocols:         &protos,
			ReadHeaderTimeout: 15 * time.Second,
			MaxHeaderBytes:    16 << 10,
			IdleTimeout:       120 * time.Second,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		}
		go func() { _ = httpSrv.Serve(rln) }()
	default:
		go acceptRaw(ctx, rln, cfg.Upstream, cfg.XHTTPPath, tr)
	}

	s.cfg = cfg
	s.key = key
	s.tcpLn = tcpLn
	s.httpSrv = httpSrv
	s.upstream = tr
	s.tracker = tracker
	s.ctx = ctx
	s.cancel = cancel
	s.running = true
	s.startedAt = time.Now()
	return nil
}

func acceptRaw(ctx context.Context, ln net.Listener, upstream, path string, tr http.RoundTripper) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go pipeRawVLESS(ctx, c, upstream, path, tr)
	}
}

// Stop shuts the listener down and closes all REALITY connections.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Server) stopLocked() {
	if !s.running {
		return
	}
	s.running = false
	if s.cancel != nil {
		s.cancel()
	}
	if s.tcpLn != nil {
		_ = s.tcpLn.Close()
	}
	if s.httpSrv != nil {
		_ = s.httpSrv.Close()
	}
	if s.tracker != nil {
		s.tracker.closeAll()
	}
	if s.upstream != nil {
		s.upstream.CloseIdleConnections() // простаивающие keep-alive держат воркеры C
	}
	s.tcpLn = nil
	s.httpSrv = nil
	s.upstream = nil
	s.tracker = nil
	s.key = ""
}

// IsRunning reports whether the listener is active.
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Status returns a runtime snapshot.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Running: s.running}
	if s.running {
		st.ListenAddr = s.cfg.ListenAddr
		st.Transport = s.cfg.Transport
		st.StartedAt = s.startedAt.UTC().Format(time.RFC3339)
		if s.tracker != nil {
			st.ActiveConns = s.tracker.count()
		}
	}
	return st
}

// Addr returns the bound address (useful when listening on port 0 in tests).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tcpLn == nil {
		return ""
	}
	return s.tcpLn.Addr().String()
}

// trackingListener registers every accepted (authenticated) REALITY connection
// so Stop can force-close them, including HTTP/2 connections owned by net/http.
type trackingListener struct {
	net.Listener
	tracker *connTracker
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.tracker.add(c)
	return &trackedConn{Conn: c, tracker: l.tracker}, nil
}

type trackedConn struct {
	net.Conn
	tracker *connTracker
	once    sync.Once
}

func (c *trackedConn) Close() error {
	c.once.Do(func() { c.tracker.remove(c.Conn) })
	return c.Conn.Close()
}
