package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
)

type publicObservation struct {
	Method  string
	URI     string
	Host    string
	Header  http.Header
	Body    string
	Trailer http.Header
}

func observingPublicHandler(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:       requestSubprotocols(r),
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		kind, body, err := connection.Read(ctx)
		if err == nil {
			_ = connection.Write(ctx, kind, body)
		}
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body error", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=7")
	w.Header().Add("Set-Cookie", "site_session=public; Secure; HttpOnly")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	if r.Method != http.MethodHead {
		w.Header().Set("Trailer", "X-Site-Trailer")
	}
	_ = json.NewEncoder(w).Encode(publicObservation{
		r.Method, r.RequestURI, r.Host, r.Header, string(body), r.Trailer,
	})
	w.Header().Set("X-Site-Trailer", "complete")
}

func TestPublicRequestsPreserveApplicationSemantics(t *testing.T) {
	public := httptest.NewServer(http.HandlerFunc(observingPublicHandler))
	defer public.Close()
	application, _ := newConfiguredTestServer(t, "127.0.0.1:1", func(value *config.Config) {
		value.PublicDir = ""
		value.PublicUpstream = public.URL
	})
	defer application.Shutdown()
	hosted := httptest.NewServer(application.Handler())
	defer hosted.Close()
	comparePublicRequests(t, public.URL, hosted.URL, hosted.Client(), false)
	testPublicWebSockets(t, hosted.URL, nil)
}

func comparePublicRequests(t *testing.T, control, relay string, client *http.Client, behindCaddy bool) {
	t.Helper()
	for _, method := range []string{"GET", "HEAD", "POST", "OPTIONS", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/", "/random", "/api/v1/session", "/api/v1/up", "/api/v1/down", "/api/v1/ws"} {
			for _, query := range []string{"", "?bridge=" + strings.Repeat("A", 43), "?a=1;two&bad=%zz&bridge=wrong"} {
				t.Run(method+path+query, func(t *testing.T) {
					var observations [2]publicObservation
					var responses [2]*http.Response
					for i, base := range []string{control, relay} {
						r := request(t, method, base+path+query, []byte(strings.Repeat("recognizable upload ", 100)), "application/octet-stream")
						r.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
						r.Header.Add("Authorization", "Bearer malformed")
						r.Header.Set("Cookie", "site=1")
						r.Header.Set("Origin", "https://other.example")
						r.Header.Set("Sec-WebSocket-Protocol", "chat, tproxy-v1."+strings.Repeat("A", 43))
						r.Header.Set("X-Up-Seq", "1")
						r.Header.Set("X-Down-Cursor", "0")
						r.Header.Set("Range", "bytes=2-7")
						r.Header.Set("If-Range", `"site"`)
						r.Header.Set("If-None-Match", `"site"`)
						r.Header.Set("If-Modified-Since", "Wed, 01 Jan 2020 00:00:00 GMT")
						if method != "GET" && method != "HEAD" {
							r.ContentLength = -1
							r.Trailer = http.Header{"X-Upload-Trailer": {"original"}}
						}
						if !behindCaddy {
							r.Host = "alternate.example:8443"
						}
						responses[i] = perform(t, client, r)
						body := readResponse(t, responses[i])
						if method != "HEAD" {
							if err := json.Unmarshal(body, &observations[i]); err != nil {
								t.Fatal(err)
							}
						}
						responses[i].Header.Del("Date")
					}
					if !reflect.DeepEqual(observations[0], observations[1]) {
						observations[0].Body = "omitted"
						observations[1].Body = "omitted"
						t.Fatalf("application received different metadata:\n%+v\n%+v", observations[0], observations[1])
					}
					if responses[0].StatusCode != responses[1].StatusCode ||
						!reflect.DeepEqual(responses[0].Header, responses[1].Header) ||
						!reflect.DeepEqual(responses[0].Trailer, responses[1].Trailer) {
						t.Fatalf("response differs: %d %v vs %d %v", responses[0].StatusCode, responses[0].Header, responses[1].StatusCode, responses[1].Header)
					}
				})
			}
		}
	}
}

func testPublicWebSockets(t *testing.T, base string, httpClient *http.Client) {
	t.Helper()
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	for _, path := range []string{"/random", "/api/v1/session", "/api/v1/up", "/api/v1/down", "/api/v1/ws"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		connection, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+path, &websocket.DialOptions{
			HTTPClient:   httpClient,
			Host:         testHost,
			Subprotocols: []string{"chat", "tproxy-v1." + strings.Repeat("A", 43)},
		})
		if err != nil {
			cancel()
			t.Fatalf("public websocket %s: %v", path, err)
		}
		if response.StatusCode != http.StatusSwitchingProtocols {
			cancel()
			_ = connection.CloseNow()
			t.Fatal("public websocket not upgraded")
		}
		if err := connection.Write(ctx, websocket.MessageText, []byte("public websocket")); err != nil {
			cancel()
			_ = connection.CloseNow()
			t.Fatal(err)
		}
		_, body, err := connection.Read(ctx)
		cancel()
		_ = connection.CloseNow()
		if err != nil || string(body) != "public websocket" {
			t.Fatalf("public websocket echo: %v", err)
		}
	}
}

func TestStaticConditionalRequestsRangesAndExactRoutes(t *testing.T) {
	application, _ := newTestServer(t, "127.0.0.1:1")
	defer application.Shutdown()
	root := application.config.PublicDir
	old := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "about.html"), old, old); err != nil {
		t.Fatal(err)
	}
	site, err := loadStaticSite(root)
	if err != nil {
		t.Fatal(err)
	}
	application.site = site
	application.config.StaticRoutes = "exact"
	get := func(path string, headers http.Header) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+testHost+path, nil)
		r.Header = headers
		w := httptest.NewRecorder()
		application.Handler().ServeHTTP(w, r)
		return w
	}
	about := get("/about.html", http.Header{})
	index := get("/", http.Header{})
	if about.Header().Get("Last-Modified") != old.Format(http.TimeFormat) || index.Header().Get("Last-Modified") == about.Header().Get("Last-Modified") {
		t.Fatal("modification times are not per-file")
	}
	if about.Header().Get("Cache-Control") != "" || about.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("static site still imposes shared policy")
	}
	conditional := get("/about.html", http.Header{"If-None-Match": {about.Header().Get("Etag")}})
	if conditional.Code != http.StatusNotModified {
		t.Fatal("ETag not honored")
	}
	ranged := get("/about.html", http.Header{"Range": {"bytes=2-7"}, "If-Range": {about.Header().Get("Etag")}})
	if ranged.Code != http.StatusPartialContent || !bytes.Equal(ranged.Body.Bytes(), about.Body.Bytes()[2:8]) {
		t.Fatal("range not honored")
	}
	stale := get("/about.html", http.Header{"Range": {"bytes=2-7"}, "If-Range": {`"old"`}})
	if stale.Code != http.StatusOK || !bytes.Equal(stale.Body.Bytes(), about.Body.Bytes()) {
		t.Fatal("If-Range not honored")
	}
	for _, path := range []string{"/about", "/favicon.ico"} {
		if get(path, http.Header{}).Code != http.StatusNotFound {
			t.Fatal("exact mode resolved an implicit alias")
		}
	}
}
