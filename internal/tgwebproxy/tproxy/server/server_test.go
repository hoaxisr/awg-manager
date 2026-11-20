package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/frame"
)

const testHost = "proxy.example.com"

func TestWebSocketCredentials(t *testing.T) {
	token := strings.Repeat("A", 43)
	tests := []struct {
		protocol  string
		wantToken string
		wantLane  uint32
		wantLanes bool
		wantOK    bool
	}{
		{webSocketProtocolPrefix + token, token, 0, false, true},
		{webSocketLaneProtocolPrefix + token + ".17", token, 17, true, true},
		{webSocketLaneProtocolPrefix + token + ".0", "", 0, false, false},
		{webSocketLaneProtocolPrefix + token + ".017", "", 0, false, false},
		{webSocketLaneProtocolPrefix + token + ".16777216", "", 0, false, false},
		{webSocketLaneProtocolPrefix + token + ".17.extra", "", 0, false, false},
	}
	for _, test := range tests {
		gotToken, gotLane, gotLanes, gotOK := webSocketCredentials(test.protocol)
		if gotToken != test.wantToken ||
			gotLane != test.wantLane ||
			gotLanes != test.wantLanes ||
			gotOK != test.wantOK {
			t.Fatalf(
				"credentials for %q were (%q, %d, %v, %v)",
				test.protocol,
				gotToken,
				gotLane,
				gotLanes,
				gotOK)
		}
	}
}

func TestPublicFallbackAndCarrierRoundTrip(t *testing.T) {
	backend := startEchoBackend(t)
	application, index := newTestServer(t, backend)
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	wrong := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge=wrong&extra=1", nil, ""))
	if wrong.StatusCode != http.StatusOK || !bytes.Equal(readResponse(t, wrong), index) {
		t.Fatal("wrong bridge query did not return the public index")
	}

	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	capability := config.CapabilityString(config.DeriveCapability(testHost, "", secret))
	for _, query := range []string{
		"bridge=" + capability + "&extra=1",
		"bridge=" + capability + "&bridge=" + capability,
		"bridge=%" + hex.EncodeToString([]byte{capability[0]}) + capability[1:],
	} {
		fallback := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?"+query, nil, ""))
		if fallback.StatusCode != http.StatusNotFound || bytes.Equal(readResponse(t, fallback), index) {
			t.Fatal("augmented or duplicated valid bridge query did not return the public index")
		}
	}
	bridgeResponse := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge="+url.QueryEscape(capability), nil, ""))
	bridgeBody := readResponse(t, bridgeResponse)
	if bridgeResponse.StatusCode != http.StatusOK || !bytes.Contains(bridgeBody, []byte("tproxy-init")) {
		t.Fatalf("bridge response failed: status %d", bridgeResponse.StatusCode)
	}
	if bytes.Contains(bridgeBody, []byte(hex.EncodeToString(secret))) || bridgeResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("bridge response contained the profile secret or allowed caching")
	}
	csp := bridgeResponse.Header.Get("Content-Security-Policy")
	for _, directive := range []string{
		"frame-ancestors http://127.0.0.1:*",
		"worker-src 'none'",
		"media-src 'none'",
		"sandbox allow-same-origin allow-scripts",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("bridge is missing CSP directive %q", directive)
		}
	}
	if bridgeResponse.Header.Get("X-DNS-Prefetch-Control") != "off" ||
		!strings.Contains(bridgeResponse.Header.Get("Permissions-Policy"), "autoplay=()") ||
		!strings.Contains(bridgeResponse.Header.Get("Permissions-Policy"), "clipboard-read=()") {
		t.Fatal("bridge is missing hardened browser capability headers")
	}
	for _, header := range []string{
		"Cross-Origin-Embedder-Policy",
		"Cross-Origin-Opener-Policy",
		"Set-Cookie",
		"X-Frame-Options",
	} {
		if bridgeResponse.Header.Get(header) != "" {
			t.Fatalf("bridge response must not send %s", header)
		}
	}
	match := regexp.MustCompile(`bootstrap="([A-Za-z0-9_-]{43})"`).FindSubmatch(bridgeBody)
	if len(match) != 2 {
		t.Fatal("could not locate embedded bootstrap token")
	}
	bootstrap := string(match[1])

	hello := frame.Encode(frame.Hello, 0, []byte{1})
	withCookie := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/session", bootstrap, hello)
	withCookie.Header.Set("Cookie", "unexpected=value")
	cookieResponse := perform(t, hosted.Client(), withCookie)
	_ = readResponse(t, cookieResponse)
	if cookieResponse.StatusCode != http.StatusNotFound {
		t.Fatal("carrier API accepted a cookie-bearing request")
	}
	createRequest := apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		hello)
	createRequest.Header.Set("X-Forwarded-For", "198.51.100.8")
	created := perform(t, hosted.Client(), createRequest)
	welcome := readResponse(t, created)
	if created.StatusCode != http.StatusOK || !bytes.Equal(welcome, frame.Encode(frame.Welcome, 0, nil)) {
		t.Fatalf("session creation failed: status %d", created.StatusCode)
	}
	sessionToken := created.Header.Get("X-Session-Token")
	if sessionToken == "" {
		t.Fatal("missing session token")
	}

	retryRequest := apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		hello)
	retryRequest.Header.Set("X-Forwarded-For", "198.51.100.9")
	repeated := perform(t, hosted.Client(), retryRequest)
	_ = readResponse(t, repeated)
	if repeated.StatusCode != http.StatusOK || repeated.Header.Get("X-Session-Token") != sessionToken {
		t.Fatal("bootstrap retry was not idempotent")
	}

	streamID := uint32(17)
	secondStreamID := uint32(18)
	uplink := append(frame.Encode(frame.Open, streamID, nil), frame.Encode(frame.Data, streamID, []byte("round trip"))...)
	uplink = append(uplink, frame.Encode(frame.Open, secondStreamID, nil)...)
	uplink = append(uplink, frame.Encode(frame.Data, secondStreamID, []byte("second stream"))...)
	upRequest := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/up", sessionToken, uplink)
	upRequest.Header.Set("X-Up-Seq", "1")
	up := perform(t, hosted.Client(), upRequest)
	_ = readResponse(t, up)
	if up.StatusCode != http.StatusNoContent || up.Header.Get("X-Up-Ack") != "1" {
		t.Fatalf("uplink failed: status %d", up.StatusCode)
	}

	firstBody, replayBase, firstCursor := pollForData(t, hosted.Client(), hosted.URL, sessionToken, "0", map[uint32][]byte{
		streamID:       []byte("round trip"),
		secondStreamID: []byte("second stream"),
	})
	replayRequest := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/down", sessionToken, nil)
	replayRequest.Header.Set("X-Down-Cursor", replayBase)
	replayed := perform(t, hosted.Client(), replayRequest)
	replayedBody := readResponse(t, replayed)
	if replayed.StatusCode != http.StatusOK || replayed.Header.Get("X-Down-Cursor") != firstCursor || !bytes.Equal(replayedBody, firstBody) {
		t.Fatal("lost downlink response was not replayed byte-for-byte")
	}

	closeRequest := apiRequest(t, http.MethodDelete, hosted.URL+"/api/v1/session", sessionToken, nil)
	closed := perform(t, hosted.Client(), closeRequest)
	_ = readResponse(t, closed)
	if closed.StatusCode != http.StatusNoContent {
		t.Fatalf("session close failed: status %d", closed.StatusCode)
	}
	closedAgain := perform(t, hosted.Client(), apiRequest(t, http.MethodDelete, hosted.URL+"/api/v1/session", sessionToken, nil))
	_ = readResponse(t, closedAgain)
	if closedAgain.StatusCode != http.StatusNoContent {
		t.Fatalf("session close retry was not idempotent: status %d", closedAgain.StatusCode)
	}
}

func TestAPIRejectsUnknownBearerAsPublic404(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newTestServer(t, backend)
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	request := request(t, http.MethodPost, hosted.URL+"/api/v1/session", frame.Encode(frame.Hello, 0, []byte{1}), "application/octet-stream")
	request.Header.Set("Authorization", "Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	request.Header.Set("Origin", "https://wrong.example")
	response := perform(t, hosted.Client(), request)
	body := readResponse(t, response)
	if response.StatusCode != http.StatusNotFound || !bytes.Contains(body, []byte("Operator Site")) {
		t.Fatal("invalid API request did not receive the ordinary public 404")
	}
}

func TestSessionAuthenticationDoesNotDependOnOrigin(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newTestServer(t, backend)
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	bootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		"198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	hello := frame.Encode(frame.Hello, 0, []byte{1})
	createdRequest := apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		hello)
	createdRequest.Header.Del("Origin")
	created := perform(t, hosted.Client(), createdRequest)
	_ = readResponse(t, created)
	sessionToken := created.Header.Get("X-Session-Token")
	if created.StatusCode != http.StatusOK || sessionToken == "" {
		t.Fatalf("missing Origin rejected session creation: status %d", created.StatusCode)
	}

	retryRequest := apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		hello)
	retryRequest.Header.Set("Origin", "https://wrong.example")
	retried := perform(t, hosted.Client(), retryRequest)
	_ = readResponse(t, retried)
	if retried.StatusCode != http.StatusOK ||
		retried.Header.Get("X-Session-Token") != sessionToken {
		t.Fatal("arbitrary Origin changed authenticated session retry")
	}
}

func TestUplinkBackpressureIsRetryable(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Limits.MaxStreamsPerSession = 1
		value.Limits.MaxPendingPerSession = 5500
		value.Limits.MaxPendingItemsPerSession = 64
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	clientIP := "198.51.100.7"
	bootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		clientIP)
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.manager.Create(
		bootstrap,
		clientIP,
		frame.Encode(frame.Hello, 0, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}

	body := append(
		frame.Encode(frame.Open, 23, nil),
		frame.Encode(frame.Data, 23, bytes.Repeat([]byte{1}, 512))...)
	upRequest := apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/up",
		created.Token,
		body)
	upRequest.Header.Set("X-Up-Seq", "1")
	response := perform(t, hosted.Client(), upRequest)
	_ = readResponse(t, response)
	if response.StatusCode != http.StatusServiceUnavailable ||
		response.Header.Get("Retry-After") != "1" ||
		response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("backpressure response was not retryable: status=%d", response.StatusCode)
	}
	if _, err := application.manager.Get(created.Token); err != nil {
		t.Fatal("backpressure invalidated the session token")
	}

	retry := apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/up",
		created.Token,
		frame.Encode(frame.Open, 23, nil))
	retry.Header.Set("X-Up-Seq", "1")
	retried := perform(t, hosted.Client(), retry)
	_ = readResponse(t, retried)
	if retried.StatusCode != http.StatusNoContent ||
		retried.Header.Get("X-Up-Ack") != "1" {
		t.Fatalf("uncommitted sequence was not retryable: status=%d", retried.StatusCode)
	}
}

func TestSessionCapacityOverloadIsRetryable(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Limits.MaxSessionsGlobal = 1
		value.Limits.NewSessionsBurst = 10
		value.Limits.NewSessionsPerMinute = 600
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	clientIP := "198.51.100.7"
	firstBootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		clientIP)
	if err != nil {
		t.Fatal(err)
	}
	first, err := application.manager.Create(
		firstBootstrap,
		clientIP,
		frame.Encode(frame.Hello, 0, []byte{1}))
	if err != nil {
		t.Fatal(err)
	}
	secondBootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		clientIP)
	if err != nil {
		t.Fatal(err)
	}
	hello := frame.Encode(frame.Hello, 0, []byte{1})
	overloaded := perform(t, hosted.Client(), apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		secondBootstrap,
		hello))
	_ = readResponse(t, overloaded)
	if overloaded.StatusCode != http.StatusServiceUnavailable ||
		overloaded.Header.Get("Retry-After") != "1" ||
		overloaded.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("session overload response was not retryable: status=%d", overloaded.StatusCode)
	}
	first.Session.Close()
	deadline := time.Now().Add(time.Second)
	for application.manager.Capacity().Sessions != 0 {
		if time.Now().After(deadline) {
			t.Fatal("closed session did not release capacity")
		}
		time.Sleep(time.Millisecond)
	}
	retried := perform(t, hosted.Client(), apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		secondBootstrap,
		hello))
	_ = readResponse(t, retried)
	if retried.StatusCode != http.StatusOK {
		t.Fatalf("session overload consumed the bootstrap: status=%d", retried.StatusCode)
	}
}

func TestHTTPSLanesRoundTripIndependently(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Profiles[0].CarrierMode = config.CarrierHTTPSLanes
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	bootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		"198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	created := perform(t, hosted.Client(), apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		frame.Encode(frame.Hello, 0, []byte{1})))
	_ = readResponse(t, created)
	if created.StatusCode != http.StatusOK || created.Header.Get("X-Carrier-Mode") != string(config.CarrierHTTPSLanes) {
		t.Fatalf("lane session creation failed: status=%d mode=%q", created.StatusCode, created.Header.Get("X-Carrier-Mode"))
	}
	token := created.Header.Get("X-Session-Token")
	for _, test := range []struct {
		id      uint32
		payload string
	}{{31, "first lane"}, {32, "second lane"}} {
		body := append(frame.Encode(frame.Open, test.id, nil), frame.Encode(frame.Data, test.id, []byte(test.payload))...)
		request := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/up", token, body)
		request.Header.Set("X-Up-Seq", "1")
		request.Header.Set("X-Lane-ID", strconv.FormatUint(uint64(test.id), 10))
		response := perform(t, hosted.Client(), request)
		_ = readResponse(t, response)
		if response.StatusCode != http.StatusNoContent || response.Header.Get("X-Up-Ack") != "1" {
			t.Fatalf("lane %d uplink failed: %d", test.id, response.StatusCode)
		}
	}

	for _, test := range []struct {
		id      uint32
		payload string
	}{{31, "first lane"}, {32, "second lane"}} {
		cursor := "0"
		var received []byte
		for attempt := 0; attempt != 8 && !bytes.Equal(received, []byte(test.payload)); attempt++ {
			request := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/down", token, nil)
			request.Header.Set("X-Down-Cursor", cursor)
			request.Header.Set("X-Lane-ID", strconv.FormatUint(uint64(test.id), 10))
			response := perform(t, hosted.Client(), request)
			body := readResponse(t, response)
			if response.StatusCode == http.StatusNoContent {
				continue
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("lane %d downlink failed: %d", test.id, response.StatusCode)
			}
			cursor = response.Header.Get("X-Down-Cursor")
			frames, parseErr := frame.ParseAll(body, frame.MaxPayload)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			for _, value := range frames {
				if value.StreamID != test.id {
					t.Fatalf("lane %d received stream %d", test.id, value.StreamID)
				}
				if value.Type == frame.Data {
					received = append(received, value.Payload...)
				}
			}
		}
		if !bytes.Equal(received, []byte(test.payload)) {
			t.Fatalf("lane %d did not echo its payload", test.id)
		}
	}
}

func TestWebSocketCarrierRoundTrip(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Profiles[0].CarrierMode = config.CarrierWebSocket
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	bootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		"198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	created := perform(t, hosted.Client(), apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		frame.Encode(frame.Hello, 0, []byte{1})))
	_ = readResponse(t, created)
	token := created.Header.Get("X-Session-Token")
	if created.StatusCode != http.StatusOK || created.Header.Get("X-Carrier-Mode") != string(config.CarrierWebSocket) || token == "" {
		t.Fatalf("websocket session creation failed: status=%d", created.StatusCode)
	}

	hostedAddress := strings.TrimPrefix(hosted.URL, "http://")
	wsClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, hostedAddress)
			},
		},
	}
	headers := http.Header{
		"Origin":          []string{"https://wrong.example"},
		"X-Forwarded-For": []string{"198.51.100.7"},
	}
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer wsCancel()
	connection, response, err := websocket.Dial(wsCtx, "ws://"+testHost+"/api/v1/ws", &websocket.DialOptions{
		HTTPClient:   wsClient,
		Host:         testHost,
		Subprotocols: []string{webSocketProtocolPrefix + token},
		HTTPHeader:   headers,
	})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connection.Subprotocol() != webSocketProtocolPrefix+token {
		t.Fatal("websocket did not authenticate its subprotocol")
	}
	streamID := uint32(41)
	payload := []byte("websocket round trip")
	body := append(frame.Encode(frame.Open, streamID, nil), frame.Encode(frame.Data, streamID, payload)...)
	if err := connection.Write(wsCtx, websocket.MessageBinary, body); err != nil {
		t.Fatal(err)
	}
	var received []byte
	for !bytes.Equal(received, payload) {
		messageType, body, err := connection.Read(wsCtx)
		if err != nil {
			t.Fatal(err)
		}
		if messageType != websocket.MessageBinary {
			t.Fatalf("unexpected websocket message type %d", messageType)
		}
		frames, err := frame.ParseAll(body, frame.MaxPayload)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range frames {
			if value.Type == frame.Data && value.StreamID == streamID {
				received = append(received, value.Payload...)
			}
		}
	}
}

func TestWebSocketLanesRemainIndependent(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Profiles[0].CarrierMode = config.CarrierWebSocketLanes
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	bootstrap, err := application.manager.IssueBootstrap(
		&application.config.Profiles[0],
		"198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	created := perform(t, hosted.Client(), apiRequest(
		t,
		http.MethodPost,
		hosted.URL+"/api/v1/session",
		bootstrap,
		frame.Encode(frame.Hello, 0, []byte{1})))
	_ = readResponse(t, created)
	token := created.Header.Get("X-Session-Token")
	if created.StatusCode != http.StatusOK ||
		created.Header.Get("X-Carrier-Mode") != string(config.CarrierWebSocketLanes) ||
		token == "" {
		t.Fatalf("websocket-lanes session creation failed: status=%d", created.StatusCode)
	}

	hostedAddress := strings.TrimPrefix(hosted.URL, "http://")
	laneClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, hostedAddress)
			},
		},
	}
	dialLane := func(laneID uint32) *websocket.Conn {
		t.Helper()
		protocol := webSocketLaneProtocolPrefix + token + "." +
			strconv.FormatUint(uint64(laneID), 10)
		headers := http.Header{
			"X-Forwarded-For": []string{"198.51.100.7"},
		}
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dialCancel()
		connection, response, err := websocket.Dial(dialCtx, "ws://"+testHost+"/api/v1/ws", &websocket.DialOptions{
			HTTPClient:   laneClient,
			Host:         testHost,
			Subprotocols: []string{protocol},
			HTTPHeader:   headers,
		})
		if err != nil {
			if response != nil {
				_ = response.Body.Close()
			}
			t.Fatal(err)
		}
		if connection.Subprotocol() != protocol {
			_ = connection.CloseNow()
			t.Fatal("websocket lane did not authenticate its subprotocol")
		}
		return connection
	}
	readPayload := func(connection *websocket.Conn, laneID uint32, expected []byte) {
		t.Helper()
		readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer readCancel()
		var received []byte
		for !bytes.Equal(received, expected) {
			messageType, body, err := connection.Read(readCtx)
			if err != nil {
				t.Fatal(err)
			}
			if messageType != websocket.MessageBinary {
				t.Fatalf("unexpected websocket message type %d", messageType)
			}
			frames, err := frame.ParseAll(body, frame.MaxPayload)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range frames {
				if value.StreamID != laneID {
					t.Fatalf("lane %d received stream %d", laneID, value.StreamID)
				}
				if value.Type == frame.Data {
					received = append(received, value.Payload...)
				}
			}
		}
	}

	firstID := uint32(51)
	secondID := uint32(52)
	first := dialLane(firstID)
	second := dialLane(secondID)
	defer second.CloseNow()
	firstPayload := []byte("first websocket lane")
	secondPayload := []byte("second websocket lane")
	opCtx, opCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer opCancel()
	if err := first.Write(
		opCtx,
		websocket.MessageBinary,
		append(
			frame.Encode(frame.Open, firstID, nil),
			frame.Encode(frame.Data, firstID, firstPayload)...)); err != nil {
		t.Fatal(err)
	}
	if err := second.Write(
		opCtx,
		websocket.MessageBinary,
		append(
			frame.Encode(frame.Open, secondID, nil),
			frame.Encode(frame.Data, secondID, secondPayload)...)); err != nil {
		t.Fatal(err)
	}
	readPayload(first, firstID, firstPayload)
	readPayload(second, secondID, secondPayload)
	if err := first.Write(
		opCtx,
		websocket.MessageBinary,
		frame.Encode(frame.Data, firstID+100, []byte("cross-lane"))); err != nil {
		t.Fatal(err)
	}
	for {
		if _, _, err := first.Read(opCtx); err != nil {
			break
		}
	}
	_ = first.CloseNow()

	followUp := []byte("still independent")
	if err := second.Write(
		opCtx,
		websocket.MessageBinary,
		frame.Encode(frame.Data, secondID, followUp)); err != nil {
		t.Fatal(err)
	}
	readPayload(second, secondID, followUp)
	if _, err := application.manager.Get(token); err != nil {
		t.Fatal("closing one websocket lane closed the parent session")
	}
}

func TestDynamicPublicUpstreamAndTransportCoexist(t *testing.T) {
	backend := startEchoBackend(t)
	var requests []string
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, strings.Join([]string{
			r.Method,
			r.URL.RequestURI(),
			r.Host,
			r.Header.Get("Authorization"),
			string(body),
		}, " "))
		w.Header().Set("X-Public-Application", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "dynamic site")
	}))
	defer public.Close()
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.PublicDir = ""
		value.PublicUpstream = public.URL
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	for _, target := range []string{
		"/article?id=7",
		"/?bridge=not-a-capability",
		"/api/v1/session",
	} {
		response := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+target, nil, ""))
		body := readResponse(t, response)
		if response.StatusCode != http.StatusTeapot ||
			response.Header.Get("X-Public-Application") != "yes" ||
			string(body) != "dynamic site" {
			t.Fatalf("%s was not delegated to the public application: %d %q", target, response.StatusCode, body)
		}
	}
	invalidCarrier := request(t, http.MethodPost, hosted.URL+"/api/v1/up", []byte("opaque carrier data"), "")
	invalidCarrier.Header.Set("Origin", "https://"+testHost)
	invalidCarrier.Header.Set("Authorization", "Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	invalidCarrier.Header.Set("Content-Type", "application/octet-stream")
	invalidCarrier.Header.Set("X-Up-Seq", "1")
	response := perform(t, hosted.Client(), invalidCarrier)
	_ = readResponse(t, response)
	if response.StatusCode != http.StatusTeapot {
		t.Fatalf("invalid carrier did not receive the application fallback: %d", response.StatusCode)
	}
	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	capability := config.CapabilityString(config.DeriveCapability(testHost, "", secret))
	response = perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge="+capability, nil, ""))
	body := readResponse(t, response)
	if response.StatusCode != http.StatusOK ||
		response.Header.Get("X-Public-Application") != "" ||
		!bytes.Contains(body, []byte("api/v1/session")) {
		t.Fatalf("valid bridge was delegated to the public application: %d", response.StatusCode)
	}
	if len(requests) != 4 {
		t.Fatalf("public application received %d requests, want 4: %v", len(requests), requests)
	}
	for i, got := range requests {
		if !strings.Contains(got, " "+testHost+" ") {
			t.Fatalf("public application did not receive the original Host: %q", got)
		}
		if i == len(requests)-1 &&
			(!strings.Contains(got, "Bearer") || !strings.Contains(got, "opaque carrier data")) {
			t.Fatalf("random credentials or public body were stripped: %q", got)
		}
	}
}

func TestAdminSurfaceIsSeparate(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newTestServer(t, backend)
	defer application.Shutdown()

	admin := httptest.NewRecorder()
	application.AdminHandler().ServeHTTP(admin, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/debug/pprof/", nil))
	if admin.Code != http.StatusNotFound {
		t.Fatal("profiling endpoint is enabled by default")
	}
	application.config.EnablePprof = true
	profiling := httptest.NewRecorder()
	application.AdminHandler().ServeHTTP(profiling, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/debug/pprof/", nil))
	if profiling.Code != http.StatusOK || !strings.Contains(profiling.Body.String(), "profile") {
		t.Fatal("explicitly enabled loopback profiling endpoint is unavailable")
	}
	public := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/debug/pprof/", nil)
	request.Host = testHost
	application.Handler().ServeHTTP(public, request)
	if public.Code != http.StatusNotFound {
		t.Fatal("profiling endpoint was available from the public handler")
	}
	metrics := httptest.NewRecorder()
	application.AdminHandler().ServeHTTP(metrics, httptest.NewRequest(
		http.MethodGet,
		"http://127.0.0.1/metrics",
		nil))
	for _, name := range []string{
		"tproxy_streams_live",
		"tproxy_backend_dials_in_flight",
		"tproxy_pending_bytes",
		"tproxy_streams_rejected_total",
		"tproxy_backend_dial_failures_total",
	} {
		if !strings.Contains(metrics.Body.String(), name+" ") {
			t.Fatalf("metrics output omitted %s", name)
		}
	}
}

func newTestServer(t *testing.T, backend string) (*Server, []byte) {
	return newConfiguredTestServer(t, backend, nil)
}

func newConfiguredTestServer(
	t *testing.T,
	backend string,
	configure func(*config.Config)) (*Server, []byte) {
	t.Helper()
	directory := t.TempDir()
	index := []byte("<!doctype html><title>Operator Site</title><p>ordinary site</p>")
	notFound := []byte("<!doctype html><title>Not found</title><p>Operator Site</p>")
	if err := os.WriteFile(filepath.Join(directory, "index.html"), index, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "404.html"), notFound, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "about.html"), []byte("<!doctype html><title>About</title>"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "favicon.svg"), []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"/>"), 0600); err != nil {
		t.Fatal(err)
	}
	value := config.Defaults()
	value.TokenKeyFile = filepath.Join(t.TempDir(), "token.key")
	if err := os.WriteFile(value.TokenKeyFile, bytes.Repeat([]byte{1}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	value.PublicHostname = testHost
	value.PublicDir = directory
	value.Timeouts.LongPoll = config.Duration(500 * time.Millisecond)
	value.Timeouts.BackendDial = config.Duration(time.Second)
	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	value.Profiles = []config.Profile{{
		Name:       "default",
		Backend:    backend,
		Capability: config.DeriveCapability(testHost, "", secret),
	}}
	if configure != nil {
		configure(&value)
	}
	application, err := New(value)
	if err != nil {
		t.Fatal(err)
	}
	return application, index
}

func startEchoBackend(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_, _ = io.Copy(connection, connection)
			}()
		}
	}()
	return listener.Addr().String()
}

func pollForData(t *testing.T, client *http.Client, baseURL, token, cursor string, expected map[uint32][]byte) ([]byte, string, string) {
	t.Helper()
	received := make(map[uint32][]byte)
	for attempt := 0; attempt != 8; attempt++ {
		poll := apiRequest(t, http.MethodPost, baseURL+"/api/v1/down", token, nil)
		poll.Header.Set("X-Down-Cursor", cursor)
		response := perform(t, client, poll)
		body := readResponse(t, response)
		if response.StatusCode == http.StatusNoContent {
			continue
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("downlink failed: status %d", response.StatusCode)
		}
		next := response.Header.Get("X-Down-Cursor")
		frames, err := frame.ParseAll(body, frame.MaxPayload)
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range frames {
			if _, exists := expected[value.StreamID]; value.Type == frame.Data && exists {
				received[value.StreamID] = append(received[value.StreamID], value.Payload...)
			}
		}
		complete := true
		for id, payload := range expected {
			if !bytes.Equal(received[id], payload) {
				complete = false
			}
		}
		if complete {
			return body, cursor, next
		}
		cursor = next
	}
	t.Fatal("echo data did not arrive")
	return nil, "", ""
}

func request(t *testing.T, method, target string, body []byte, contentType string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = testHost
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request
}

func apiRequest(t *testing.T, method, target, token string, body []byte) *http.Request {
	t.Helper()
	contentType := ""
	if body != nil {
		contentType = "application/octet-stream"
	}
	request := request(t, method, target, body, contentType)
	request.Header.Set("Origin", "https://"+testHost)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func perform(t *testing.T, client *http.Client, request *http.Request) *http.Response {
	t.Helper()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readResponse(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
