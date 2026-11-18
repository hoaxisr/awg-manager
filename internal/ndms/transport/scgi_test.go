package transport

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeNdmSCGI — ndm на unix-сокете: разбирает netstring, отвечает в CGI-форме
// так, как ответил стенд 5.1.9 (18.11.2026): без AUTH_ALLOWED=1 или без
// NORMALIZED_URI=/rci… — 403 0x1867.
func fakeNdmSCGI(t *testing.T) (path string, got chan map[string]string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "scgi") // t.TempDir() бывает длиннее лимита sun_path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path = filepath.Join(dir, "ndm.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan map[string]string, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			br := bufio.NewReader(c)
			lenStr, _ := br.ReadString(':')
			n, _ := strconv.Atoi(strings.TrimSuffix(lenStr, ":"))
			raw := make([]byte, n+1)
			io.ReadFull(br, raw)
			parts := strings.Split(string(raw[:n]), "\x00")
			h := map[string]string{"_first": parts[0]}
			for i := 0; i+1 < len(parts); i += 2 {
				h[parts[i]] = parts[i+1]
			}
			cl, _ := strconv.Atoi(h["CONTENT_LENGTH"])
			body := make([]byte, cl)
			io.ReadFull(br, body)
			h["_body"] = string(body)
			got <- h
			if h["AUTH_ALLOWED"] == "1" && strings.HasPrefix(h["NORMALIZED_URI"], "/rci") {
				io.WriteString(c, "Status: 200 OK\r\nContent-Type: application/json\r\nContent-Length: 6\r\n\r\n{\"a\":1")
			} else {
				io.WriteString(c, "Status: 403 Forbidden\r\nX-Detail: 0x1867, invalid query string\r\n\r\n")
			}
			c.Close()
		}
	}()
	return path, got
}

func TestSCGITransport_RequestAndResponse(t *testing.T) {
	path, got := fakeNdmSCGI(t)
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:79/rci/", bytes.NewBufferString(`[{"show":{"version":{}}}]`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "awgm/test")

	resp, err := scgiTransport{path: path}.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != `{"a":1` || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("resp %d %q %v", resp.StatusCode, b, resp.Header)
	}
	h := <-got
	want := map[string]string{
		"_first":         "CONTENT_LENGTH",
		"CONTENT_LENGTH": "25",
		"_body":          `[{"show":{"version":{}}}]`,
		"REQUEST_METHOD": "POST",
		"NORMALIZED_URI": "/rci/",
		"HTTP_HOST":      "127.0.0.1:79",
		"REMOTE_IDENT":   "awgm/test",
		"AUTH_ALLOWED":   "1",
	}
	for k, v := range want {
		if h[k] != v {
			t.Errorf("%s = %q, want %q", k, h[k], v)
		}
	}
}

func TestLoopbackFallback_SwitchesOn0x1867AndReplaysBody(t *testing.T) {
	path, got := fakeNdmSCGI(t)
	var httpHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		w.Header().Set("X-Detail", "0x1867, invalid query string")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	rt := &loopbackFallback{base: srv.Client().Transport, scgi: scgiTransport{path: path}}
	c := &http.Client{Transport: rt}
	for i := range 2 {
		resp, err := c.Post(srv.URL+"/rci/", "application/json", strings.NewReader(`[1]`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("call %d: status %d", i, resp.StatusCode)
		}
		if h := <-got; h["_body"] != `[1]` {
			t.Fatalf("call %d: body %q не переигран", i, h["_body"])
		}
	}
	if n := httpHits.Load(); n != 1 {
		t.Fatalf("HTTP hits = %d, want 1 (после 0x1867 — только сокет)", n)
	}
}

func TestLoopbackFallback_Other403StaysOnHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Detail", "0x2312, not identified")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	rt := &loopbackFallback{base: srv.Client().Transport, scgi: scgiTransport{path: "/nonexistent"}}
	resp, err := (&http.Client{Transport: rt}).Get(srv.URL + "/rci/show/version")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || rt.useSCGI.Load() {
		t.Fatalf("status %d, useSCGI %v", resp.StatusCode, rt.useSCGI.Load())
	}
}
