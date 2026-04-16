package api

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox"
)

// ClashProxy forwards /api/singbox/clash/* to the sing-box embedded Clash API.
// Supports both plain HTTP (GET/DELETE/POST) and WebSocket upgrade (for
// /traffic, /connections, /logs endpoints).
type ClashProxy struct {
	op *singbox.Operator
}

// NewClashProxy creates a ClashProxy backed by the given sing-box Operator.
func NewClashProxy(op *singbox.Operator) *ClashProxy {
	return &ClashProxy{op: op}
}

const clashPrefix = "/api/singbox/clash"

var clashHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
}

// ServeHTTP routes HTTP and WebSocket upgrade requests to the Clash upstream.
func (p *ClashProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upstreamPath := strings.TrimPrefix(r.URL.Path, clashPrefix)
	if upstreamPath == "" {
		upstreamPath = "/"
	}
	addr := p.op.Clash().Address()

	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		p.proxyWebSocket(w, r, addr, upstreamPath)
		return
	}
	p.proxyHTTP(w, r, addr, upstreamPath)
}

func (p *ClashProxy) proxyHTTP(w http.ResponseWriter, r *http.Request, addr, path string) {
	target := &url.URL{Scheme: "http", Host: addr, Path: path, RawQuery: r.URL.RawQuery}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, vv := range r.Header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	resp, err := clashHTTPClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *ClashProxy) proxyWebSocket(w http.ResponseWriter, r *http.Request, addr, path string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufrw, err := hj.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	upstream, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
		return
	}
	defer upstream.Close()

	// Replay the original handshake to the upstream with the rewritten path.
	r.URL.Path = path
	r.Host = addr
	if err := r.Write(upstream); err != nil {
		_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
		return
	}

	errc := make(chan error, 2)
	go func() { _, err := io.Copy(upstream, bufrw); errc <- err }()
	go func() { _, err := io.Copy(clientConn, upstream); errc <- err }()
	<-errc
	// Close both sides so the other goroutine unblocks.
	upstream.Close()
	clientConn.Close()
	<-errc
}
