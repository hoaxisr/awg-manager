package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/bridge"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/frame"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/session"
)

const (
	webSocketProtocolPrefix     = "tproxy-v1."
	webSocketLaneProtocolPrefix = "tproxy-lane-v1."
)

// A create body is a single HELLO frame (8-byte header + 1-byte payload), so a
// tiny cap is plenty and keeps an unauthenticated POST /session from streaming
// megabytes before Create rejects it.
const maxCreateBodyBytes = 64

// A request body must not be able to hold a relay goroutine and a
// Caddy->relay connection open indefinitely — neither while a handler reads
// it nor while net/http discards an unread body before answering. Long polls
// carry no body. Public requests use the ordinary gateway timeout policy.
const bodyReadDeadline = 30 * time.Second

type Server struct {
	config           config.Config
	base             string
	manager          *session.Manager
	site             *staticSite
	publicUpstream   http.Handler
	publicTransport  *http.Transport
	bodyReadDeadline time.Duration
}

func New(value config.Config) (*Server, error) {
	tokenKey, err := config.ReadTokenKey(value.TokenKeyFile)
	if err != nil {
		return nil, err
	}
	if err := session.ValidateBudget(value); err != nil {
		return nil, err
	}
	var site *staticSite
	var publicUpstream http.Handler
	var publicTransport *http.Transport
	if value.PublicDir != "" {
		var err error
		site, err = loadStaticSite(value.PublicDir)
		if err != nil {
			return nil, err
		}
	} else if value.PublicUpstream != "" {
		target, err := url.Parse(value.PublicUpstream)
		if err != nil {
			return nil, err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableCompression = true
		publicTransport = transport
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(request *httputil.ProxyRequest) {
				request.Out.URL.Scheme = target.Scheme
				request.Out.URL.Host = target.Host
				request.Out.Host = request.In.Host
				request.Out.URL.RawQuery = request.In.URL.RawQuery
				request.Out.Trailer = request.In.Trailer
				for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Awgm-Proxy-Hop"} {
					if values, ok := request.In.Header[name]; ok {
						request.Out.Header[name] = append([]string(nil), values...)
					}
				}
			},
		}
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		}
		publicUpstream = proxy
	}
	return &Server{
		config:           value,
		base:             value.Base(),
		manager:          session.NewManager(value, tokenKey),
		site:             site,
		publicUpstream:   publicUpstream,
		publicTransport:  publicTransport,
		bodyReadDeadline: bodyReadDeadline,
	}, nil
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/readyz", s.serveReady)
	mux.HandleFunc("/metrics", s.serveMetrics)
	if s.config.EnablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}

func (s *Server) UpdateProfiles(profiles []config.Profile) error {
	return s.manager.UpdateProfiles(profiles)
}

func (s *Server) Shutdown() {
	s.manager.Shutdown()
	if s.publicTransport != nil {
		s.publicTransport.CloseIdleConnections()
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hasInternalSecret(r) {
		s.servePublic(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.ContentLength != 0 {
		s.setReadDeadline(w)
	}
	if r.Host != s.config.PublicHostname && r.Host != s.config.PublicHostname+":443" {
		s.serveNotFound(w, r)
		return
	}
	if suffix, ok := s.transportSuffix(r.URL.Path); ok && r.URL.EscapedPath() == r.URL.Path {
		s.serveAPI(w, r, suffix)
		return
	}
	profile := s.bridgeProfile(r)
	if profile != nil {
		s.serveBridge(w, r, profile)
		return
	}
	s.serveNotFound(w, r)
}

func (s *Server) serveBridge(w http.ResponseWriter, r *http.Request, profile *config.Profile) {
	clientIP, err := s.clientIP(r)
	if err != nil {
		s.serveNotFound(w, r)
		return
	}
	token, err := s.manager.IssueBootstrap(profile, clientIP)
	if err != nil {
		s.serveNotFound(w, r)
		return
	}
	page, err := bridge.Render(
		s.config.PublicHostname,
		s.config.BasePath,
		token,
		string(profile.CarrierMode.WithDefault()),
		s.config.Limits.CarrierBatchBytes)
	if err != nil {
		s.serveNotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", page.CSP)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-DNS-Prefetch-Control", "off")
	w.Header().Set("Permissions-Policy", bridge.PermissionsPolicy)
	w.Header().Set("Content-Length", strconv.Itoa(len(page.Body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page.Body)
}

func (s *Server) serveAPI(w http.ResponseWriter, r *http.Request, suffix string) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery ||
		len(r.Header.Values("Authorization")) > 1 ||
		len(r.Header.Values("Sec-WebSocket-Protocol")) > 1 ||
		(r.Header.Get("Cookie") != "" && suffix != "api/v1/ws") {
		s.serveNotFound(w, r)
		return
	}
	clientIP, err := s.clientIP(r)
	if err != nil {
		s.serveNotFound(w, r)
		return
	}
	if suffix == "api/v1/ws" {
		s.serveWebSocket(w, r, clientIP)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		s.serveNotFound(w, r)
		return
	}
	switch suffix {
	case "api/v1/session":
		s.serveSession(w, r, token, clientIP)
	case "api/v1/up":
		s.serveUp(w, r, token, clientIP)
	case "api/v1/down":
		s.serveDown(w, r, token, clientIP)
	default:
		s.serveNotFound(w, r)
	}
}

func (s *Server) serveSession(
	w http.ResponseWriter,
	r *http.Request,
	token string,
	clientIP string,
) {
	if r.Method == http.MethodDelete {
		if !emptyBody(r) || r.Header.Get("Content-Type") != "" {
			s.serveNotFound(w, r)
			return
		}
		if err := s.manager.CloseToken(token); err != nil {
			s.serveNotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost || !binaryContentType(r.Header.Get("Content-Type")) {
		s.serveNotFound(w, r)
		return
	}
	if !s.manager.HasBootstrap(token) {
		s.serveNotFound(w, r)
		return
	}
	body, err := readBody(w, r, maxCreateBodyBytes)
	if err != nil {
		s.serveNotFound(w, r)
		return
	}
	result, err := s.manager.Create(token, clientIP, body)
	if err != nil {
		if errors.Is(err, session.ErrLimit) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.serveNotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Session-Token", result.Token)
	w.Header().Set("X-Carrier-Mode", string(result.Session.CarrierMode()))
	w.Header().Set("X-Down-Cursor", "0")
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Welcome)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result.Welcome)
}

func (s *Server) serveUp(w http.ResponseWriter, r *http.Request, token, clientIP string) {
	if r.Method != http.MethodPost || !binaryContentType(r.Header.Get("Content-Type")) {
		s.serveNotFound(w, r)
		return
	}
	sequence, ok := canonicalUint(r.Header.Get("X-Up-Seq"))
	if !ok || sequence == 0 {
		s.serveNotFound(w, r)
		return
	}
	value, err := s.manager.Get(token)
	if err != nil || !s.manager.ObserveClientIP(value, clientIP) {
		s.serveNotFound(w, r)
		return
	}
	body, err := readBody(w, r, s.config.Limits.MaxBodyBytes)
	if err != nil {
		s.serveNotFound(w, r)
		return
	}
	var ack uint64
	switch value.CarrierMode() {
	case config.CarrierHTTPS:
		if r.Header.Get("X-Lane-ID") != "" {
			s.serveNotFound(w, r)
			return
		}
		ack, err = value.ProcessUp(sequence, body)
	case config.CarrierHTTPSLanes:
		lane, ok := canonicalUint(r.Header.Get("X-Lane-ID"))
		if !ok || lane > frame.MaxStreamID {
			s.serveNotFound(w, r)
			return
		}
		ack, err = value.ProcessUpLane(uint32(lane), sequence, body)
	default:
		s.serveNotFound(w, r)
		return
	}
	if err != nil {
		if errors.Is(err, session.ErrBackpressure) ||
			errors.Is(err, session.ErrConcurrent) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.serveNotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Up-Ack", strconv.FormatUint(ack, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serveDown(w http.ResponseWriter, r *http.Request, token, clientIP string) {
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "" {
		s.serveNotFound(w, r)
		return
	}
	cursor, ok := canonicalUint(r.Header.Get("X-Down-Cursor"))
	if !ok {
		s.serveNotFound(w, r)
		return
	}
	value, err := s.manager.Get(token)
	if err != nil || !s.manager.ObserveClientIP(value, clientIP) {
		s.serveNotFound(w, r)
		return
	}
	if !emptyBody(r) {
		s.serveNotFound(w, r)
		return
	}
	var body []byte
	var next uint64
	var laneClosed bool
	switch value.CarrierMode() {
	case config.CarrierHTTPS:
		if r.Header.Get("X-Lane-ID") != "" {
			s.serveNotFound(w, r)
			return
		}
		body, next, err = value.Poll(r.Context(), cursor)
	case config.CarrierHTTPSLanes:
		lane, ok := canonicalUint(r.Header.Get("X-Lane-ID"))
		if !ok || lane > frame.MaxStreamID {
			s.serveNotFound(w, r)
			return
		}
		body, next, laneClosed, err = value.PollLane(
			r.Context(),
			uint32(lane),
			cursor)
	default:
		s.serveNotFound(w, r)
		return
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, session.ErrConcurrent) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		s.serveNotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Down-Cursor", strconv.FormatUint(next, 10))
	if laneClosed {
		w.Header().Set("X-Lane-Closed", "1")
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) serveWebSocket(w http.ResponseWriter, r *http.Request, clientIP string) {
	if r.Header.Get("Sec-Websocket-Key") != "" {
		if r.Header.Get("Upgrade") == "" {
			r.Header.Set("Upgrade", "websocket")
		}
		if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
			r.Header.Set("Connection", "Upgrade")
		}
	}
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
		s.serveNotFound(w, r)
		return
	}
	protocols := requestSubprotocols(r)
	if len(protocols) != 1 {
		s.serveNotFound(w, r)
		return
	}
	token, laneID, lanes, ok := webSocketCredentials(protocols[0])
	if !ok {
		s.serveNotFound(w, r)
		return
	}
	if _, ok := bearerToken("Bearer " + token); !ok {
		s.serveNotFound(w, r)
		return
	}
	value, err := s.manager.Get(token)
	if err != nil || !s.manager.ObserveClientIP(value, clientIP) {
		s.serveNotFound(w, r)
		return
	}
	if (lanes && value.CarrierMode() != config.CarrierWebSocketLanes) || (!lanes && value.CarrierMode() != config.CarrierWebSocket) {
		s.serveNotFound(w, r)
		return
	}
	if !emptyBody(r) {
		s.serveNotFound(w, r)
		return
	}
	acquired := false
	if lanes {
		acquired = value.AcquireWebSocketLane(laneID)
	} else {
		acquired = value.AcquireWebSocket()
	}
	if !acquired {
		s.serveNotFound(w, r)
		return
	}
	// A dead peer that never sends is otherwise only noticed by the listener's
	// TCP keep-alive minutes later: bound silence to two long-poll periods,
	// refreshed by every message and every pong, and let the writer ping the
	// peer whenever a poll period passes without downlink data.
	idle := 2 * s.config.Timeouts.LongPoll.Value()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	idleReset := make(chan struct{}, 1)
	triggerIdleReset := func() {
		select {
		case idleReset <- struct{}{}:
		default:
		}
	}

	connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       protocols,
		InsecureSkipVerify: true,
		OnPongReceived: func(context.Context, []byte) {
			triggerIdleReset()
		},
	})
	if err != nil {
		if lanes {
			value.ReleaseWebSocketLane(laneID)
		} else {
			value.Close()
		}
		return
	}
	if lanes {
		defer value.ReleaseWebSocketLane(laneID)
	} else {
		defer value.Close()
	}
	defer connection.CloseNow()
	connection.SetReadLimit(int64(s.config.Limits.MaxBodyBytes))

	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-idleReset:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			case <-timer.C:
				cancel()
				return
			}
		}
	}()

	finished := make(chan error, 2)
	go s.readWebSocket(ctx, connection, value, laneID, lanes, triggerIdleReset, finished)
	go s.writeWebSocket(ctx, connection, value, laneID, lanes, finished)
	<-finished
	cancel()
	_ = connection.CloseNow()
}

func requestSubprotocols(r *http.Request) []string {
	var out []string
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func webSocketCredentials(protocol string) (string, uint32, bool, bool) {
	if strings.HasPrefix(protocol, webSocketProtocolPrefix) {
		token := strings.TrimPrefix(protocol, webSocketProtocolPrefix)
		return token, 0, false, token != ""
	}
	if !strings.HasPrefix(protocol, webSocketLaneProtocolPrefix) {
		return "", 0, false, false
	}
	tokenAndLane := strings.TrimPrefix(protocol, webSocketLaneProtocolPrefix)
	token, laneText, ok := strings.Cut(tokenAndLane, ".")
	if !ok || token == "" {
		return "", 0, false, false
	}
	lane, ok := canonicalUint(laneText)
	if !ok || lane == 0 || lane > frame.MaxStreamID {
		return "", 0, false, false
	}
	return token, uint32(lane), true, true
}

func (s *Server) readWebSocket(
	ctx context.Context,
	connection *websocket.Conn,
	value *session.Session,
	laneID uint32,
	lanes bool,
	triggerIdleReset func(),
	finished chan<- error) {
	sequence := uint64(1)
	for {
		messageType, body, err := connection.Read(ctx)
		if err != nil {
			finished <- err
			return
		}
		triggerIdleReset()
		if messageType != websocket.MessageBinary || len(body) == 0 {
			finished <- session.ErrProtocol
			return
		}
		deadline := time.NewTimer(30 * time.Second)
		for {
			var ack uint64
			var processErr error
			if lanes {
				ack, processErr = value.ProcessUpLane(laneID, sequence, body)
			} else {
				ack, processErr = value.ProcessUp(sequence, body)
			}
			if processErr == nil && ack == sequence {
				if !deadline.Stop() {
					<-deadline.C
				}
				sequence++
				break
			}
			if !errors.Is(processErr, session.ErrBackpressure) {
				deadline.Stop()
				finished <- processErr
				return
			}
			select {
			case <-ctx.Done():
				deadline.Stop()
				finished <- ctx.Err()
				return
			case <-deadline.C:
				finished <- session.ErrBackpressure
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

func (s *Server) writeWebSocket(
	ctx context.Context,
	connection *websocket.Conn,
	value *session.Session,
	laneID uint32,
	lanes bool,
	finished chan<- error) {
	cursor := uint64(0)
	for {
		var body []byte
		var next uint64
		var laneClosed bool
		var err error
		if lanes {
			body, next, laneClosed, err = value.PollLane(
				ctx,
				laneID,
				cursor)
		} else {
			body, next, err = value.Poll(ctx, cursor)
		}
		if err != nil {
			finished <- err
			return
		}
		if laneClosed {
			finished <- nil
			return
		}
		if len(body) == 0 {
			pingCtx, pingCancel := context.WithTimeout(ctx, 30*time.Second)
			err := connection.Ping(pingCtx)
			pingCancel()
			if err != nil {
				finished <- err
				return
			}
			continue
		}
		writeCtx, writeCancel := context.WithTimeout(ctx, 30*time.Second)
		err = connection.Write(writeCtx, websocket.MessageBinary, body)
		writeCancel()
		if err != nil {
			finished <- err
			return
		}
		cursor = next
	}
}

func (s *Server) clientIP(r *http.Request) (string, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			ipStr := strings.TrimSpace(parts[0])
			if parsed := net.ParseIP(ipStr); parsed != nil {
				return parsed.String(), nil
			}
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		if parsed := net.ParseIP(realIP); parsed != nil {
			return parsed.String(), nil
		}
	}
	peer := net.ParseIP(host)
	if peer != nil {
		return peer.String(), nil
	}
	return "127.0.0.1", nil
}

// The request reaches the site with its original path, base prefix included. A
// stripped prefix would expose the whole site a second time under it, which is a
// far louder signature than the 404 it would replace.
const builtinFallbackHTML = "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><title>Service</title></head><body><h1>Welcome</h1></body></html>\n"

func (s *Server) servePublic(w http.ResponseWriter, r *http.Request) {
	if s.publicUpstream != nil {
		hops := r.Header.Values("X-Awgm-Proxy-Hop")
		selfHop := "tproxy:" + s.config.Listen
		if len(hops) >= 6 {
			http.Error(w, http.StatusText(http.StatusLoopDetected), http.StatusLoopDetected)
			return
		}
		for _, h := range hops {
			for _, part := range strings.Split(h, ",") {
				if strings.TrimSpace(part) == selfHop {
					http.Error(w, http.StatusText(http.StatusLoopDetected), http.StatusLoopDetected)
					return
				}
			}
		}
		s.publicUpstream.ServeHTTP(w, r)
		return
	}
	if s.site == nil {
		if s.config.PublicFallback == "builtin" && (r.Method == http.MethodGet || r.Method == http.MethodHead) && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Length", strconv.Itoa(len(builtinFallbackHTML)))
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = w.Write([]byte(builtinFallbackHTML))
			}
			return
		}
		s.serveNotFound(w, r)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if r.URL.Path == "/" {
			s.serveEntry(w, r, s.site.index, http.StatusOK)
			return
		}
		if entry := s.site.resolve(r.URL.Path, s.config.StaticRoutes == "legacy"); entry != nil {
			s.serveEntry(w, r, entry, http.StatusOK)
			return
		}
	}
	s.serveEntry(w, r, s.site.notFound, http.StatusNotFound)
}

func (s *Server) serveNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.NotFound(w, r)
}

// transportSuffix names the carrier endpoint a request path selects, relative to
// the configured base. A path outside the base, or one naming anything else, is
// not carrier traffic and stays with the bridge or public handlers.
func (s *Server) transportSuffix(requestPath string) (string, bool) {
	if !strings.HasPrefix(requestPath, s.base) {
		return "", false
	}
	switch suffix := requestPath[len(s.base):]; suffix {
	case "api/v1/session", "api/v1/up", "api/v1/down", "api/v1/ws":
		return suffix, true
	}
	return "", false
}

func (s *Server) serveReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	for _, profile := range s.manager.Profiles() {
		connection, err := net.DialTimeout("tcp", profile.Backend, s.config.Timeouts.BackendDial.Value())
		if err != nil {
			http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = connection.Close()
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ready\n")
}

func (s *Server) serveMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	metrics := s.manager.Metrics()
	capacity := s.manager.Capacity()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w,
		"tproxy_sessions_live %d\n"+
			"tproxy_streams_live %d\n"+
			"tproxy_backend_dials_in_flight %d\n"+
			"tproxy_pending_bytes %d\n"+
			"tproxy_pending_items %d\n"+
			"tproxy_sessions_created_total %d\n"+
			"tproxy_sessions_closed_total %d\n"+
			"tproxy_streams_opened_total %d\n"+
			"tproxy_streams_rejected_total %d\n"+
			"tproxy_backend_dial_failures_total %d\n"+
			"tproxy_bytes_up_total %d\n"+
			"tproxy_bytes_down_total %d\n"+
			"tproxy_limit_hits_total %d\n",
		capacity.Sessions, capacity.Streams, capacity.BackendDialsInFlight,
		capacity.PendingBytes, capacity.PendingItems,
		metrics.SessionsCreated, metrics.SessionsClosed,
		metrics.StreamsOpened, metrics.StreamsRejected,
		metrics.BackendDialFailures, metrics.BytesUp, metrics.BytesDown,
		metrics.LimitHits)
}

func (s *Server) setReadDeadline(w http.ResponseWriter) {
	if controller := http.NewResponseController(w); controller != nil {
		_ = controller.SetReadDeadline(time.Now().Add(s.bodyReadDeadline))
	}
}

func readBody(w http.ResponseWriter, r *http.Request, limit int) ([]byte, error) {
	reader := http.MaxBytesReader(w, r.Body, int64(limit))
	defer reader.Close()
	result, err := io.ReadAll(reader)
	if err != nil || len(result) == 0 {
		return nil, errors.New("invalid body")
	}
	return result, nil
}

func emptyBody(r *http.Request) bool {
	if r.ContentLength > 0 {
		return false
	}
	if r.Body == nil {
		return true
	}
	var single [1]byte
	read, _ := r.Body.Read(single[:])
	return read == 0
}

func binaryContentType(value string) bool {
	mediaType, parameters, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/octet-stream" && len(parameters) == 0
}

func bearerToken(value string) (string, bool) {
	if !strings.HasPrefix(value, "Bearer ") || strings.Count(value, " ") != 1 {
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return token, err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == token
}

func canonicalUint(value string) (uint64, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') || strings.HasPrefix(value, "+") {
		return 0, false
	}
	result, err := strconv.ParseUint(value, 10, 64)
	return result, err == nil && strconv.FormatUint(result, 10) == value
}
