package tgwebproxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestMux_RoutingAndHostRewrite(t *testing.T) {
	var recHost1, recHost2 string

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recHost1 = r.Host
		w.Header().Set("X-Backend", "1")
		_, _ = w.Write([]byte("backend1 ok"))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recHost2 = r.Host
		w.Header().Set("X-Backend", "2")
		_, _ = w.Write([]byte("backend2 ok"))
	}))
	defer backend2.Close()

	u1, _ := url.Parse(backend1.URL)
	port1, _ := strconv.Atoi(u1.Port())

	u2, _ := url.Parse(backend2.URL)
	port2, _ := strconv.Atoi(u2.Port())

	secret := "ccf3c7a5a1112dcfa6fcb5365fe74028"
	host1 := "xy.example.com"
	host2 := "edge.example.com"

	token1 := DeriveBridgeToken(host1, secret)
	token2 := DeriveBridgeToken(host2, secret)

	if token1 == "" || token2 == "" || token1 == token2 {
		t.Fatalf("tokens invalid: t1=%q, t2=%q", token1, token2)
	}

	endpoints := []WebProxyEndpoint{
		{
			ID:         "ep1",
			Hostname:   host1,
			ListenPort: port1,
			AdminPort:  8086,
			IsCDN:      false,
			Enabled:    true,
		},
		{
			ID:         "ep2",
			Hostname:   host2,
			ListenPort: port2,
			AdminPort:  8089,
			IsCDN:      true,
			Enabled:    true,
		},
	}

	mux := NewMux()
	mux.Update(endpoints, secret)

	// Test 1: Route by bridge token for host1
	req1 := httptest.NewRequest(http.MethodGet, "/?bridge="+token1, nil)
	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, req1)
	if rec1.Header().Get("X-Backend") != "1" {
		t.Fatalf("expected backend 1, got %q", rec1.Header().Get("X-Backend"))
	}
	if recHost1 != host1 {
		t.Fatalf("expected host %q at backend 1, got %q", host1, recHost1)
	}

	// Test 2: Route by bridge token for host2
	req2 := httptest.NewRequest(http.MethodGet, "/?bridge="+token2, nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected backend 2, got %q", rec2.Header().Get("X-Backend"))
	}
	if recHost2 != host2 {
		t.Fatalf("expected host %q at backend 2, got %q", host2, recHost2)
	}

	// Test 3: Route by CDN header (even if Host header says host1, as CDN edge rewrites it)
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/session?b=token", nil)
	req3.Host = host1
	req3.Header.Set("X-Cdn2-Request-Id", "trace-12345")
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected CDN request to route to backend 2, got %q", rec3.Header().Get("X-Backend"))
	}
	if recHost2 != host2 {
		t.Fatalf("expected host %q at backend 2, got %q", host2, recHost2)
	}

	// Test 4: Route direct cloud request by Host
	req4 := httptest.NewRequest(http.MethodGet, "/api/v1/session?b=token", nil)
	req4.Host = host1
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, req4)
	if rec4.Header().Get("X-Backend") != "1" {
		t.Fatalf("expected host1 request to route to backend 1, got %q", rec4.Header().Get("X-Backend"))
	}
	if recHost1 != host1 {
		t.Fatalf("expected host %q at backend 1, got %q", host1, recHost1)
	}
}

func TestMux_Lifecycle(t *testing.T) {
	mux := NewMux()
	// Start on loopback high port
	port := 38445
	err := mux.Start(port, nil, "ccf3c7a5a1112dcfa6fcb5365fe74028")
	if err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if !mux.IsRunning() {
		t.Fatalf("expected running")
	}

	// Connect to verify listening
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = body
	}

	if err := mux.Stop(); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	if mux.IsRunning() {
		t.Fatalf("expected stopped")
	}
}

func TestMux_SessionResilienceAndClientAffinity(t *testing.T) {
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("b") == "edge-boot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Backend", "1")
		w.Header().Set("X-Session-Token", "sess-backend-1")
		_, _ = w.Write([]byte("backend1 ok"))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "2")
		w.Header().Set("X-Session-Token", "sess-backend-2")
		_, _ = w.Write([]byte("backend2 ok"))
	}))
	defer backend2.Close()

	u1, _ := url.Parse(backend1.URL)
	port1, _ := strconv.Atoi(u1.Port())

	u2, _ := url.Parse(backend2.URL)
	port2, _ := strconv.Atoi(u2.Port())

	secret := "ccf3c7a5a1112dcfa6fcb5365fe74028"
	host1 := "origin.example.test"
	host2 := "edge.example.test"

	token2 := DeriveBridgeToken(host2, secret)

	endpoints := []WebProxyEndpoint{
		{ID: "ep1", Hostname: host1, ListenPort: port1, IsCDN: true, Enabled: true},
		{ID: "ep2", Hostname: host2, ListenPort: port2, IsCDN: true, Enabled: true},
	}

	mux := NewMux()
	mux.Update(endpoints, secret)

	// 1. Client loads bridge for ep2
	reqBridge := httptest.NewRequest(http.MethodGet, "/?bridge="+token2, nil)
	reqBridge.Header.Set("X-Real-IP", "203.0.113.5")
	recBridge := httptest.NewRecorder()
	mux.ServeHTTP(recBridge, reqBridge)
	if recBridge.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected bridge to route to backend 2, got %q", recBridge.Header().Get("X-Backend"))
	}

	// 2. Client calls session with Host rewritten to host1 (as Selectel CDN does when using KeenDNS origin)
	reqSession := httptest.NewRequest(http.MethodGet, "/api/v1/session?b=edge-boot&h=nonce", nil)
	reqSession.Host = host1
	reqSession.Header.Set("X-Real-IP", "203.0.113.5")
	reqSession.Header.Set("X-Cdn2-Request-Id", "trace-xyz")
	recSession := httptest.NewRecorder()
	mux.ServeHTTP(recSession, reqSession)
	if recSession.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected session to route to backend 2 via affinity, got %q", recSession.Header().Get("X-Backend"))
	}
	if recSession.Header().Get("X-Session-Token") != "sess-backend-2" {
		t.Fatalf("expected sess-backend-2, got %q", recSession.Header().Get("X-Session-Token"))
	}

	// 3. Subsequent poll with session token routes to backend 2 by session
	reqPoll := httptest.NewRequest(http.MethodGet, "/api/v1/down?s=sess-backend-2&c=0", nil)
	reqPoll.Host = host1
	reqPoll.Header.Set("X-Real-IP", "198.51.100.99") // different IP!
	recPoll := httptest.NewRecorder()
	mux.ServeHTTP(recPoll, reqPoll)
	if recPoll.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected poll to route to backend 2 via session token, got %q", recPoll.Header().Get("X-Backend"))
	}

	// 4. Fallback resilience: client without bridge history sends session with edge-boot
	// Backend 1 returns 404, Mux falls back to backend 2 which returns 200 OK!
	reqFallback := httptest.NewRequest(http.MethodGet, "/api/v1/session?b=edge-boot&h=nonce", nil)
	reqFallback.Host = host1
	reqFallback.Header.Set("X-Real-IP", "198.51.100.77") // unknown IP
	recFallback := httptest.NewRecorder()
	mux.ServeHTTP(recFallback, reqFallback)
	if recFallback.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected fallback to route to backend 2, got %q", recFallback.Header().Get("X-Backend"))
	}
	if recFallback.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from fallback, got %d", recFallback.Code)
	}
}

func TestMux_CDNWebSocketUpgradeWithRewrittenHost(t *testing.T) {
	var wsUpgradedBackend string
	var wsReceivedHost string
	var wsReceivedProtocol string

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend1"))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Backend", "2")
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			wsUpgradedBackend = "2"
			wsReceivedHost = r.Host
			wsReceivedProtocol = r.Header.Get("Sec-WebSocket-Protocol")
			w.Header().Set("Upgrade", "websocket")
			w.Header().Set("Connection", "Upgrade")
			w.Header().Set("Sec-WebSocket-Accept", "dummy-accept")
			w.WriteHeader(http.StatusSwitchingProtocols)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/session") {
			w.Header().Set("X-Session-Token", "sess-cdn-ws-token-123")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"session":"sess-cdn-ws-token-123"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend2.Close()

	u1, _ := url.Parse(backend1.URL)
	port1, _ := strconv.Atoi(u1.Port())

	u2, _ := url.Parse(backend2.URL)
	port2, _ := strconv.Atoi(u2.Port())

	secret := "ccf3c7a5a1112dcfa6fcb5365fe74028"
	hostOrigin := "origin.example.test"
	hostCDN := "edge.example.test"

	endpoints := []WebProxyEndpoint{
		{
			ID:         "ep-origin",
			Hostname:   hostOrigin,
			ListenPort: port1,
			AdminPort:  8086,
			IsCDN:      false,
			Enabled:    true,
		},
		{
			ID:         "ep-cdn",
			Hostname:   hostCDN,
			ListenPort: port2,
			AdminPort:  18086,
			IsCDN:      true,
			Enabled:    true,
		},
	}

	mux := NewMux()
	mux.Update(endpoints, secret)

	muxServer := httptest.NewServer(mux)
	defer muxServer.Close()

	// Step 1: CDN request creates a session.
	// Notice: CDN has rewritten Host to hostOrigin, but sent Selectel CDN marker headers.
	reqSession, err := http.NewRequest(http.MethodGet, muxServer.URL+"/api/v1/session?b=boot", nil)
	if err != nil {
		t.Fatalf("failed to create session request: %v", err)
	}
	reqSession.Host = hostOrigin
	reqSession.Header.Set("X-CDN-Request-Id", "9acece999d7fa86404d60457a4b0b468")
	reqSession.Header.Set("X-Real-IP", "198.51.100.55")

	respSession, err := http.DefaultClient.Do(reqSession)
	if err != nil {
		t.Fatalf("session request failed: %v", err)
	}
	defer respSession.Body.Close()

	if respSession.Header.Get("X-Backend") != "2" {
		t.Fatalf("expected session creation via CDN to hit backend 2 (ep-cdn), got %q", respSession.Header.Get("X-Backend"))
	}
	if respSession.Header.Get("X-Session-Token") != "sess-cdn-ws-token-123" {
		t.Fatalf("expected session token sess-cdn-ws-token-123, got %q", respSession.Header.Get("X-Session-Token"))
	}
	if respSession.Header.Get("Connection") == "close" {
		t.Fatalf("Connection: close should NOT be forced on HTTP response to preserve keepalive")
	}

	// Step 2: Client initiates WebSocket upgrade using the session token in Sec-WebSocket-Protocol.
	// We use real TCP dial to allow HTTP Hijacker for protocol switching.
	conn, err := net.Dial("tcp", muxServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to dial mux server: %v", err)
	}
	defer conn.Close()

	wsReqRaw := fmt.Sprintf(
		"GET /api/v1/socket HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Protocol: tproxy-session.sess-cdn-ws-token-123\r\n"+
			"X-CDN-Request-Id: 9acece999d7fa86404d60457a4b0b469\r\n"+
			"X-Real-IP: 198.51.100.55\r\n\r\n",
		hostOrigin,
	)

	if _, err := conn.Write([]byte(wsReqRaw)); err != nil {
		t.Fatalf("failed to write WS upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	respWS, err := http.ReadResponse(reader, reqSession)
	if err != nil {
		t.Fatalf("failed to read WS upgrade response: %v", err)
	}
	defer respWS.Body.Close()

	if wsUpgradedBackend != "2" {
		t.Fatalf("expected WebSocket upgrade to reach backend 2, got %q", wsUpgradedBackend)
	}
	if wsReceivedHost != hostCDN {
		t.Fatalf("expected Mux to rewrite Host to CDN backend hostname %q, got %q", hostCDN, wsReceivedHost)
	}
	if !strings.Contains(wsReceivedProtocol, "tproxy-session.sess-cdn-ws-token-123") {
		t.Fatalf("expected Sec-WebSocket-Protocol to contain session token, got %q", wsReceivedProtocol)
	}
	if respWS.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", respWS.StatusCode)
	}
	if respWS.Header.Get("Connection") == "close" {
		t.Fatalf("Connection: close must never be set on WebSocket upgrade response")
	}
}

func TestMux_PostSessionFallbackPreservesBodyAndRoutesBySession(t *testing.T) {
	helloFrame := []byte{0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x01}

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if r.URL.Path == "/api/v1/session" && r.Header.Get("Authorization") == "Bearer edge-boot" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Backend", "1")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend1.Close()

	var backend2GotBody []byte
	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/v1/session" {
			backend2GotBody = body
			if len(body) != len(helloFrame) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("X-Backend", "2")
			w.Header().Set("X-Session-Token", "sess-https-edge-2")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte{0x11, 0, 0, 0, 0, 0, 0, 0})
			return
		}
		w.Header().Set("X-Backend", "2")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer backend2.Close()

	u1, _ := url.Parse(backend1.URL)
	port1, _ := strconv.Atoi(u1.Port())
	u2, _ := url.Parse(backend2.URL)
	port2, _ := strconv.Atoi(u2.Port())

	secret := "ccf3c7a5a1112dcfa6fcb5365fe74028"
	hostOrigin := "origin.example.test"
	hostCDN := "edge.example.test"

	endpoints := []WebProxyEndpoint{
		{ID: "xy", Hostname: hostOrigin, ListenPort: port1, IsCDN: false, Enabled: true},
		{ID: "edge", Hostname: hostCDN, ListenPort: port2, IsCDN: true, Enabled: true},
	}

	mux := NewMux()
	mux.Update(endpoints, secret)

	// 1. POST /api/v1/session without CDN headers hits backend 1 first (by Host), gets 404,
	// falls back to backend 2 with intact 9-byte HELLO body, and succeeds!
	reqSess := httptest.NewRequest(http.MethodPost, "/api/v1/session", strings.NewReader(string(helloFrame)))
	reqSess.Host = hostOrigin
	reqSess.Header.Set("Authorization", "Bearer edge-boot")
	reqSess.Header.Set("Content-Type", "application/octet-stream")
	recSess := httptest.NewRecorder()
	mux.ServeHTTP(recSess, reqSess)

	if recSess.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from POST /api/v1/session fallback, got %d", recSess.Code)
	}
	if recSess.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected backend 2, got %q", recSess.Header().Get("X-Backend"))
	}
	if string(backend2GotBody) != string(helloFrame) {
		t.Fatalf("expected backend 2 to receive intact 9-byte HELLO frame, got %x", backend2GotBody)
	}

	// 2. Subsequent POST /api/v1/down with Bearer session token routes directly to backend 2
	reqDown := httptest.NewRequest(http.MethodPost, "/api/v1/down", nil)
	reqDown.Host = hostOrigin
	reqDown.Header.Set("Authorization", "Bearer sess-https-edge-2")
	recDown := httptest.NewRecorder()
	mux.ServeHTTP(recDown, reqDown)

	if recDown.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content from POST /api/v1/down, got %d", recDown.Code)
	}
	if recDown.Header().Get("X-Backend") != "2" {
		t.Fatalf("expected POST /api/v1/down to route to backend 2, got %q", recDown.Header().Get("X-Backend"))
	}
}
