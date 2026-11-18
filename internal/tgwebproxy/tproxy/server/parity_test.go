package server

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/frame"
)

// A request that does not hold a valid capability must not be able to tell
// whether the site runs the relay: for every method, an API-shaped request
// and a static-miss-shaped request must produce identical status, headers and
// body, and `/` with any non-capability query must be exactly `GET /`.
func TestProbingParityAcrossAPIAndStaticPaths(t *testing.T) {
	backend := startEchoBackend(t)
	application, index := newTestServer(t, backend)
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	type shape struct {
		status  int
		headers string
		body    []byte
	}
	fingerprint := func(t *testing.T, method, target string, decorate func(*http.Request)) shape {
		t.Helper()
		var body []byte
		if method == http.MethodPost || method == http.MethodDelete {
			body = []byte("payload")
		}
		request := request(t, method, hosted.URL+target, body, "")
		if decorate != nil {
			decorate(request)
		}
		response := perform(t, hosted.Client(), request)
		responseBody := readResponse(t, response)
		names := make([]string, 0, len(response.Header))
		for name := range response.Header {
			if name == "Date" {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		var headers strings.Builder
		for _, name := range names {
			fmt.Fprintf(&headers, "%s: %s\n", name, strings.Join(response.Header[name], ", "))
		}
		return shape{response.StatusCode, headers.String(), responseBody}
	}

	decorations := map[string]func(*http.Request){
		"plain": nil,
		"origin": func(r *http.Request) {
			r.Header.Set("Origin", "https://"+testHost)
		},
		"cookie": func(r *http.Request) {
			r.Header.Set("Cookie", "session=1")
		},
		"upgrade": func(r *http.Request) {
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			r.Header.Set("Sec-WebSocket-Version", "13")
			r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			r.Header.Set("Sec-WebSocket-Protocol", "tproxy-v1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		},
		"encoding": func(r *http.Request) {
			r.Header.Set("Accept-Encoding", "gzip, br")
		},
		"bearer": func(r *http.Request) {
			r.Header.Set("Origin", "https://"+testHost)
			r.Header.Set("Authorization", "Bearer AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
			r.Header.Set("Content-Type", "application/octet-stream")
		},
	}
	apiPaths := []string{"/api/v1/session", "/api/v1/up", "/api/v1/down", "/api/v1/ws", "/api/v1/nope"}
	missPaths := []string{"/nonexistent", "/api/nope"}
	rootQueries := []string{"", "?x=1", "?bridge=short", "?bridge=" + strings.Repeat("A", 43), "?bridge=" + strings.Repeat("A", 43) + "&x=1"}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions, http.MethodDelete} {
		for name, decorate := range decorations {
			reference := fingerprint(t, method, "/nonexistent", decorate)
			if reference.status != http.StatusNotFound {
				t.Fatalf("%s /nonexistent (%s) is %d, want 404", method, name, reference.status)
			}
			for _, target := range append(apiPaths, missPaths...) {
				got := fingerprint(t, method, target, decorate)
				if got.status != reference.status || got.headers != reference.headers || !bytes.Equal(got.body, reference.body) {
					t.Fatalf("%s %s (%s) differs from a static miss:\n%d\n%s\nvs\n%d\n%s", method, target, name, got.status, got.headers, reference.status, reference.headers)
				}
			}
			rootReference := fingerprint(t, method, "/", decorate)
			if method == http.MethodGet || method == http.MethodHead {
				if rootReference.status != http.StatusOK {
					t.Fatalf("%s / (%s) is %d, want 200", method, name, rootReference.status)
				}
				if method == http.MethodGet && !bytes.Equal(rootReference.body, index) {
					t.Fatalf("GET / (%s) did not return the index", name)
				}
			} else if rootReference.status != reference.status || rootReference.headers != reference.headers || !bytes.Equal(rootReference.body, reference.body) {
				t.Fatalf("%s / (%s) differs from %s /nonexistent", method, name, method)
			}
			for _, query := range rootQueries {
				got := fingerprint(t, method, "/"+query, decorate)
				want := rootReference
				if got.status != want.status || got.headers != want.headers || !bytes.Equal(got.body, want.body) {
					t.Fatalf("%s /%s (%s) differs from %s /:\n%d\n%s\nvs\n%d\n%s", method, query, name, method, got.status, got.headers, want.status, want.headers)
				}
			}
		}
	}

	// Old configurations retain their deployed static aliases.
	about := fingerprint(t, http.MethodGet, "/about", nil)
	if about.status != http.StatusOK || !bytes.Contains(about.body, []byte("About")) {
		t.Fatalf("/about did not resolve to about.html: %d", about.status)
	}
	favicon := fingerprint(t, http.MethodGet, "/favicon.ico", nil)
	if favicon.status != http.StatusOK || !strings.Contains(favicon.headers, "Content-Type: image/svg+xml") {
		t.Fatalf("/favicon.ico did not resolve to favicon.svg: %d\n%s", favicon.status, favicon.headers)
	}
	root := fingerprint(t, http.MethodGet, "/", nil)
	if !strings.Contains(about.headers, "Accept-Ranges: bytes") || !strings.Contains(about.headers, "Etag:") {
		t.Fatal("static entries omitted standard range or conditional headers")
	}
	conditional := request(t, http.MethodGet, hosted.URL+"/about", nil, "")
	conditional.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	notModified := perform(t, hosted.Client(), conditional)
	_ = readResponse(t, notModified)
	if notModified.StatusCode != http.StatusNotModified {
		t.Fatalf("If-Modified-Since was not honoured: %d", notModified.StatusCode)
	}
	traversal := fingerprint(t, http.MethodGet, "/../index.html", nil)
	if traversal.status != http.StatusNotFound && traversal.status != http.StatusOK {
		t.Fatalf("path traversal returned %d", traversal.status)
	}
	if strings.Contains(root.headers, "Content-Security-Policy:") {
		t.Fatal("public response unexpectedly imposes a shared CSP")
	}
	withPort := request(t, http.MethodGet, hosted.URL+"/", nil, "")
	withPort.Host = testHost + ":443"
	portResponse := perform(t, hosted.Client(), withPort)
	if body := readResponse(t, portResponse); portResponse.StatusCode != http.StatusOK || !bytes.Equal(body, index) {
		t.Fatalf("Host with an explicit :443 was rejected: %d", portResponse.StatusCode)
	}
}

// A failed authenticated bridge request must never enter public-site routing.
func TestBridgeLimitFailsLocally(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Limits.MaxBootstrapsGlobal = 1
		value.Limits.NewBootstrapsBurst = 1
		value.Limits.NewBootstrapsPerMinute = 1
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	capability := config.CapabilityString(config.DeriveCapability(testHost, "", secret))
	first := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge="+url.QueryEscape(capability), nil, ""))
	if body := readResponse(t, first); first.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("tproxy-init")) {
		t.Fatalf("first bridge request failed: %d", first.StatusCode)
	}
	second := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge="+url.QueryEscape(capability), nil, ""))
	body := readResponse(t, second)
	if second.StatusCode != http.StatusNotFound || bytes.Contains(body, []byte("tproxy-init")) {
		t.Fatalf("rate-limited bridge request did not fail locally: %d", second.StatusCode)
	}
	if second.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("authenticated bridge failure is cacheable: %q", second.Header.Get("Cache-Control"))
	}

}

func TestSessionCreateAuthenticatesBeforeReadingBody(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newTestServer(t, backend)
	application.bodyReadDeadline = 500 * time.Millisecond
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	misplaced, err := application.manager.IssueBootstrap(&application.config.Profiles[0], "198.51.100.7")
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer writer.Close()
	unknown, err := http.NewRequest(http.MethodPost, hosted.URL+"/wrong-path", reader)
	if err != nil {
		t.Fatal(err)
	}
	unknown.Host = testHost
	unknown.ContentLength = -1
	unknown.Header.Set("X-Forwarded-For", "198.51.100.7")
	unknown.Header.Set("Origin", "https://"+testHost)
	unknown.Header.Set("Content-Type", "application/octet-stream")
	unknown.Header.Set("Authorization", "Bearer "+misplaced)
	done := make(chan *http.Response, 1)
	go func() {
		response, err := hosted.Client().Do(unknown)
		if err != nil {
			done <- nil
			return
		}
		done <- response
	}()
	// The relay rejects the misplaced authentic bearer without reading the
	// body. The read deadline bounds how long net/http may spend discarding
	// the never-completing body before the 404 leaves.
	select {
	case response := <-done:
		if response == nil {
			t.Fatal("request with a never-completing body failed instead of being rejected")
		}
		_ = readResponse(t, response)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("misplaced authentic bearer with an unfinished body got %d, want 404", response.StatusCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("misplaced authentic bearer was allowed to hold the connection while its body never completes")
	}
	_ = writer.Close()

	// A known bootstrap retains the tiny session-create body cap.
	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	capability := config.CapabilityString(config.DeriveCapability(testHost, "", secret))
	bridge := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge="+url.QueryEscape(capability), nil, ""))
	bridgeBody := readResponse(t, bridge)
	start := bytes.Index(bridgeBody, []byte(`bootstrap="`))
	if start < 0 {
		t.Fatal("no bootstrap in the bridge page")
	}
	bootstrap := string(bridgeBody[start+len(`bootstrap="`) : start+len(`bootstrap="`)+43])
	oversized := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/session", bootstrap, bytes.Repeat([]byte{1}, maxCreateBodyBytes+1))
	rejected := perform(t, hosted.Client(), oversized)
	_ = readResponse(t, rejected)
	if rejected.StatusCode != http.StatusNotFound {
		t.Fatalf("oversized create body got %d, want 404", rejected.StatusCode)
	}
	hello := frame.Encode(frame.Hello, 0, []byte{1})
	created := perform(t, hosted.Client(), apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/session", bootstrap, hello))
	_ = readResponse(t, created)
	if created.StatusCode != http.StatusOK {
		t.Fatalf("session creation after the oversized attempt failed: %d", created.StatusCode)
	}
}

func TestConcurrentUplinkIsRetryableAndDownlinkSupersedes(t *testing.T) {
	backend := startEchoBackend(t)
	application, _ := newConfiguredTestServer(t, backend, func(value *config.Config) {
		value.Timeouts.LongPoll = config.Duration(5 * time.Second)
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()

	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	capability := config.CapabilityString(config.DeriveCapability(testHost, "", secret))
	bridge := perform(t, hosted.Client(), request(t, http.MethodGet, hosted.URL+"/?bridge="+url.QueryEscape(capability), nil, ""))
	bridgeBody := readResponse(t, bridge)
	start := bytes.Index(bridgeBody, []byte(`bootstrap="`))
	bootstrap := string(bridgeBody[start+len(`bootstrap="`) : start+len(`bootstrap="`)+43])
	created := perform(t, hosted.Client(), apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/session", bootstrap, frame.Encode(frame.Hello, 0, []byte{1})))
	_ = readResponse(t, created)
	token := created.Header.Get("X-Session-Token")
	if created.StatusCode != http.StatusOK || token == "" {
		t.Fatalf("session creation failed: %d", created.StatusCode)
	}

	type polled struct {
		status int
		cursor string
		body   []byte
	}
	poll := func(cursor string) chan polled {
		result := make(chan polled, 1)
		go func() {
			request := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/down", token, nil)
			request.Header.Set("X-Down-Cursor", cursor)
			response, err := hosted.Client().Do(request)
			if err != nil {
				result <- polled{status: -1}
				return
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			result <- polled{response.StatusCode, response.Header.Get("X-Down-Cursor"), body}
		}()
		return result
	}
	first := poll("0")
	time.Sleep(200 * time.Millisecond)
	second := poll("0")
	select {
	case superseded := <-first:
		if superseded.status != http.StatusNoContent || superseded.cursor != "0" {
			t.Fatalf("superseded poll got %d cursor %q, want 204 with cursor 0", superseded.status, superseded.cursor)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("superseded poll was not released")
	}
	streamID := uint32(5)
	uplink := append(frame.Encode(frame.Open, streamID, nil), frame.Encode(frame.Data, streamID, []byte("ping"))...)
	up := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/up", token, uplink)
	up.Header.Set("X-Up-Seq", "1")
	upResponse := perform(t, hosted.Client(), up)
	_ = readResponse(t, upResponse)
	if upResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("uplink failed: %d", upResponse.StatusCode)
	}
	select {
	case winner := <-second:
		if winner.status != http.StatusOK || len(winner.body) == 0 {
			t.Fatalf("newest poll got %d with %d bytes, want the echoed data", winner.status, len(winner.body))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("newest poll did not receive downlink data")
	}

	// A racing /up (retry while the previous parse is still in flight) is a
	// retryable 503, not the fatal local 404.
	value, err := application.manager.Get(token)
	if err != nil {
		t.Fatal(err)
	}
	value.SetUpActiveForTest(true)
	racing := apiRequest(t, http.MethodPost, hosted.URL+"/api/v1/up", token, frame.Encode(frame.Data, streamID, []byte("more")))
	racing.Header.Set("X-Up-Seq", "2")
	racingResponse := perform(t, hosted.Client(), racing)
	_ = readResponse(t, racingResponse)
	value.SetUpActiveForTest(false)
	if racingResponse.StatusCode != http.StatusServiceUnavailable || racingResponse.Header.Get("Retry-After") != "1" {
		t.Fatalf("racing uplink got %d (Retry-After %q), want 503 with Retry-After 1", racingResponse.StatusCode, racingResponse.Header.Get("Retry-After"))
	}
	closing := perform(t, hosted.Client(), apiRequest(t, http.MethodDelete, hosted.URL+"/api/v1/session", token, nil))
	_ = readResponse(t, closing)
	if closing.StatusCode != http.StatusNoContent {
		t.Fatalf("session close failed: %d", closing.StatusCode)
	}
}

func TestStaticSiteLoadsWholeTree(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(directory, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "assets", "app.css"), []byte("body{}"), 0600); err != nil {
		t.Fatal(err)
	}
	site, err := loadStaticSite(directory)
	if err != nil {
		t.Fatal(err)
	}
	if site.resolve("/assets/app.css", true) == nil || site.resolve("/assets/../index.html", true) != nil || site.resolve("/index", true) == nil || site.resolve("/", true) != nil {
		t.Fatal("static resolution does not follow the front-proxy rules")
	}
	if site.notFound != site.index {
		t.Fatal("missing 404.html must fall back to the index")
	}
}
