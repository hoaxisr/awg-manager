package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandler(t *testing.T) {
	staticFS := fstest.MapFS{
		"index.html": {
			Data: []byte("<!doctype html><div id=\"app\"></div>"),
			Mode: fs.ModePerm,
		},
		"_app/immutable/assets/app.123.css": {
			Data: []byte("body{color:#111}"),
			Mode: fs.ModePerm,
		},
		"site.webmanifest": {
			Data: []byte(`{"name":"AWG Manager"}`),
			Mode: fs.ModePerm,
		},
	}
	handler := spaHandler(staticFS)

	tests := []struct {
		name        string
		path        string
		wantStatus  int
		wantBody    string
		wantCache   string
		contentType string
	}{
		{
			name:        "root serves index",
			path:        "/",
			wantStatus:  http.StatusOK,
			wantBody:    "<!doctype html>",
			wantCache:   "no-cache, no-store, must-revalidate",
			contentType: "text/html; charset=utf-8",
		},
		{
			name:        "nested route falls back to index",
			path:        "/tunnels/abc",
			wantStatus:  http.StatusOK,
			wantBody:    "<!doctype html>",
			wantCache:   "no-cache, no-store, must-revalidate",
			contentType: "text/html; charset=utf-8",
		},
		{
			name:        "asset is served",
			path:        "/site.webmanifest",
			wantStatus:  http.StatusOK,
			wantBody:    "AWG Manager",
			wantCache:   "no-cache, no-store, must-revalidate",
			contentType: "application/manifest+json",
		},
		{
			name:        "immutable asset gets long cache",
			path:        "/_app/immutable/assets/app.123.css",
			wantStatus:  http.StatusOK,
			wantBody:    "body{color:#111}",
			wantCache:   "public, max-age=31536000, immutable",
			contentType: "text/css",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d", rec.Code, tc.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), tc.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Fatalf("Cache-Control: got %q want %q", got, tc.wantCache)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.contentType) {
				t.Fatalf("Content-Type: got %q want prefix %q", got, tc.contentType)
			}
		})
	}
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// Build-time gzip (frontend postbuild) drops the raw original for compressed
// assets, so the .gz twin is the only copy in the embedded FS.
func TestSPAHandlerGzip(t *testing.T) {
	jsContent := []byte("export const x = 1;" + strings.Repeat(" /* pad */", 100))
	htmlContent := []byte("<!doctype html><div id=\"app\"></div>")
	pngContent := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x00}, 200)...)
	staticFS := fstest.MapFS{
		"index.html.gz":                       {Data: gzipBytes(t, htmlContent), Mode: fs.ModePerm},
		"_app/immutable/chunks/app.123.js.gz": {Data: gzipBytes(t, jsContent), Mode: fs.ModePerm},
		"favicon.png":                         {Data: pngContent, Mode: fs.ModePerm},
	}
	handler := spaHandler(staticFS)

	t.Run("serves precompressed js to gzip client", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_app/immutable/chunks/app.123.js", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding: got %q want gzip", got)
		}
		if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
			t.Fatalf("Vary: got %q want to contain Accept-Encoding", got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/javascript") {
			t.Fatalf("Content-Type: got %q want application/javascript prefix", got)
		}
		gz, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		got, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("read gzip: %v", err)
		}
		if !bytes.Equal(got, jsContent) {
			t.Fatalf("decompressed body mismatch")
		}
	})

	t.Run("gunzips for non-gzip client", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/_app/immutable/chunks/app.123.js", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding: got %q want empty", got)
		}
		if !bytes.Equal(rec.Body.Bytes(), jsContent) {
			t.Fatalf("body: want gunzipped js content")
		}
	})

	t.Run("serves raw asset without a gz twin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/favicon.png", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("Content-Encoding: got %q want empty for png", got)
		}
		if !bytes.Equal(rec.Body.Bytes(), pngContent) {
			t.Fatalf("png body mismatch")
		}
	})

	t.Run("spa fallback resolves a gz-only index", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/some/client/route", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding: got %q want gzip", got)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Fatalf("Content-Type: got %q want text/html prefix", got)
		}
		gz, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		got, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("read gzip: %v", err)
		}
		if !bytes.Equal(got, htmlContent) {
			t.Fatalf("fallback body mismatch")
		}
	})
}
