package obfuscator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func packageTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestPackageURL(t *testing.T) {
	cases := map[string]string{
		"https://p.example/api/install/abc":                "https://p.example/api/install/abc/package.tar.gz",
		"https://p.example/api/install/abc/":                "https://p.example/api/install/abc/package.tar.gz",
		"https://p.example/api/install/abc/package.tar.gz": "https://p.example/api/install/abc/package.tar.gz",
	}
	for in, want := range cases {
		if got, err := PackageURL(in); err != nil || got != want {
			t.Errorf("%s -> %s (%v), want %s", in, got, err, want)
		}
	}
	if _, err := PackageURL("ftp://x/api/install/a"); err == nil {
		t.Fatal("scheme")
	}
	if _, err := PackageURL("https://p.example/clients"); err == nil {
		t.Fatal("not an install link")
	}
}

func TestFetchPhobosConf_SelfSignedTLS(t *testing.T) {
	pkg := packageTarGz(t, map[string]string{
		"phobos-router/wg-obfuscator.conf":        "[main]\n",
		"phobos-router/wg-socks5-obfuscator.conf": "[main]\n",
		"phobos-router/README.txt":                "x",
		"phobos-router/bin/wg-obfuscator-mipsel":  "ELF",
		"phobos-router/router.conf":               phobosConf,
	})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/package.tar.gz") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(pkg)
	}))
	defer srv.Close()
	conf, err := FetchPhobosConf(context.Background(), srv.URL+"/api/install/tok")
	if err != nil || conf != phobosConf {
		t.Fatalf("err=%v eq=%v", err, conf == phobosConf)
	}
}

func TestFetchPhobosConf_NoClientConf(t *testing.T) {
	pkg := packageTarGz(t, map[string]string{"p/wg-obfuscator.conf": "[main]\n"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pkg) }))
	defer srv.Close()
	if _, err := FetchPhobosConf(context.Background(), srv.URL+"/api/install/tok"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchPhobosConf_TooBig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "99999999")
		_, _ = w.Write(bytes.Repeat([]byte{0}, 1024))
	}))
	defer srv.Close()
	if _, err := FetchPhobosConf(context.Background(), srv.URL+"/api/install/tok"); err == nil {
		t.Fatal("expected size error")
	}
	// Без Content-Length: gzip-поток больше лимита → та же внятная ошибка.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(bytes.Repeat([]byte("x"), MaxPackageBytes+4096))
		_ = gz.Close()
	}))
	defer srv2.Close()
	_, err := FetchPhobosConf(context.Background(), srv2.URL+"/api/install/tok")
	if err == nil || !strings.Contains(err.Error(), "слишком большой") {
		t.Fatalf("want size error, got %v", err)
	}
}
