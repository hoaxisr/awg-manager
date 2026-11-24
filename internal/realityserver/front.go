package realityserver

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Go здесь — только TLS-фронт REALITY. VLESS (заголовок, клиенты, выход в
// сеть, лимиты) обслуживает C-демон awgm-xhttpd на loopback: XHTTP уходит к
// нему реверс-прокси (h2 → h1, дуплекс), сырой VLESS по TCP — одним
// дуплексным POST stream-one: у stream-one тело и ответ несут ровно те же
// байты, что сырой VLESS-поток. Адрес клиента C получает в X-Forwarded-For
// (loopback у демона в TrustedProxies).

// newUpstreamTransport — h1 к C на loopback: без h2, сжатия и прокси из env.
func newUpstreamTransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
	}
}

// quietLog: обрыв потока клиентом — штатное событие, а не строка в stderr.
var quietLog = log.New(io.Discard, "", 0)

// newXHTTPProxy проксирует в C-демон XHTTP-запросы клиентов REALITY — только
// под prefix: прочие пути демона (служебный /.xhttpd/, чужие CDN-префиксы,
// FiniteProto) с WAN-порта входа недостижимы.
func newXHTTPProxy(upstream, prefix string, tr http.RoundTripper) http.Handler {
	target := &url.URL{Scheme: "http", Host: upstream}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = pr.In.Host
			// Rewrite заранее срезает присланные клиентом X-Forwarded-*, так что
			// XFF — ровно адрес пира. CF-Connecting-IP у C приоритетнее XFF
			// (vless_xhttp.c resolve_effective_client_ip) — снимаем сами.
			pr.SetXForwarded()
			pr.Out.Header.Del("CF-Connecting-IP")
			pr.Out.Header.Del("X-Real-IP")
		},
		FlushInterval: -1,
		Transport:     tr,
		ErrorLog:      quietLog,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ".." (и %2e%2e — URL.Path уже раскодирован) вывел бы за prefix.
		if p := r.URL.Path; strings.Contains(p, "..") || (p != prefix && !strings.HasPrefix(p, prefix+"/")) {
			http.NotFound(w, r)
			return
		}
		// На h2 дуплекс есть всегда; на HTTP/1.x его надо включить явно.
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// pipeRawVLESS отдаёт сырой VLESS-поток conn в C как POST stream-one.
func pipeRawVLESS(ctx context.Context, conn net.Conn, upstream, path string, tr http.RoundTripper) {
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+upstream+path+"/", io.NopCloser(conn))
	if err != nil {
		return
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/grpc")
	if host, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
		req.Header.Set("X-Forwarded-For", host)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	_, _ = io.Copy(conn, resp.Body)
}

// connTracker держит принятые соединения, чтобы Stop закрыл и те, что
// принадлежат net/http.
type connTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newConnTracker() *connTracker { return &connTracker{conns: map[net.Conn]struct{}{}} }

func (t *connTracker) add(c net.Conn) {
	t.mu.Lock()
	t.conns[c] = struct{}{}
	t.mu.Unlock()
}

func (t *connTracker) remove(c net.Conn) {
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
}

func (t *connTracker) closeAll() {
	t.mu.Lock()
	conns := t.conns
	t.conns = map[net.Conn]struct{}{}
	t.mu.Unlock()
	for c := range conns {
		_ = c.Close()
	}
}

func (t *connTracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.conns)
}
