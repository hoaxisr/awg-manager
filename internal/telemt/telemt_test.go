package telemt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeArch(t *testing.T) {
	cases := []struct {
		in        string
		want      string
		supported bool
	}{
		{"aarch64-3.10", "aarch64", true},
		{"arm64", "aarch64", true},
		{"mipsel-3.4", "mipsel", false},
		{"mipsle", "mipsel", false},
		{"mips-3.4", "mips", false},
		{"x86_64", "x86_64", false},
	}

	for _, c := range cases {
		got := NormalizeArch(c.in)
		if got != c.want {
			t.Errorf("NormalizeArch(%q) = %q; want %q", c.in, got, c.want)
		}
		if sup := IsArchSupported(c.in); sup != c.supported {
			t.Errorf("IsArchSupported(%q) = %v; want %v", c.in, sup, c.supported)
		}
	}
}

func TestGenerateTOML(t *testing.T) {
	cfg := Config{
		Enabled:        true,
		Port:           8443,
		ListenIP:       "0.0.0.0",
		Secret:         "0123456789abcdef0123456789abcdef",
		TLSDomain:      "yandex.ru",
		UpstreamDevice: "direct",
	}

	toml := GenerateTOML(cfg)
	if !strings.Contains(toml, "port = 8443") {
		t.Errorf("expected port 8443 in toml, got: %s", toml)
	}
	if !strings.Contains(toml, `ip = "0.0.0.0"`) {
		t.Errorf("expected 0.0.0.0 listener in toml, got: %s", toml)
	}
	if !strings.Contains(toml, `tls_domain = "yandex.ru"`) {
		t.Errorf("expected yandex.ru in toml, got: %s", toml)
	}
	if !strings.Contains(toml, `user1 = "0123456789abcdef0123456789abcdef"`) {
		t.Errorf("expected user1 secret in toml, got: %s", toml)
	}
}

func TestGenerateTgLink(t *testing.T) {
	link := GenerateTgLink("192.168.1.1", 8443, "0123456789abcdef0123456789abcdef", "yandex.ru")
	if !strings.HasPrefix(link, "tg://proxy?") {
		t.Fatalf("expected tg://proxy? prefix, got: %s", link)
	}
	if !strings.Contains(link, "server=192.168.1.1") {
		t.Errorf("expected server in link, got: %s", link)
	}
	if !strings.Contains(link, "port=8443") {
		t.Errorf("expected port in link, got: %s", link)
	}
	// Secret should have 'ee' prefix + 32 hex chars + hex(yandex.ru)
	// hex("yandex.ru") = 79616e6465782e7275
	expectedSecret := "ee0123456789abcdef0123456789abcdef79616e6465782e7275"
	if !strings.Contains(link, "secret="+expectedSecret) {
		t.Errorf("expected secret %s in link, got: %s", expectedSecret, link)
	}
}

func TestExtractTarGzBinary(t *testing.T) {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	content := []byte("#!/bin/sh\necho 'telemt test'\n")
	hdr := &tar.Header{
		Name:     "telemt",
		Mode:     0755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write content: %v", err)
	}
	_ = tw.Close()
	_ = gzw.Close()

	tmpDir := t.TempDir()
	outBin := filepath.Join(tmpDir, "telemt")

	if err := extractTarGzBinary(&buf, "telemt", outBin); err != nil {
		t.Fatalf("extractTarGzBinary failed: %v", err)
	}

	read, err := os.ReadFile(outBin)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(read) != string(content) {
		t.Fatalf("content mismatch: %q vs %q", string(read), string(content))
	}
}

func TestService_GetStatus_Mock(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "telemt")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho 'telemt 3.5.7'\n"), 0755); err != nil {
		t.Fatalf("write mock binary: %v", err)
	}

	svc := New(tmpDir, "aarch64")
	svc.binPath = binPath

	status := svc.GetStatus(context.Background())
	if !status.Installed {
		t.Fatalf("expected installed=true")
	}
	if !status.ArchSupported {
		t.Fatalf("expected archSupported=true for aarch64")
	}
	if status.LatestVersion != PinnedTelemtVersion {
		t.Fatalf("expected latestVersion=%s, got %s", PinnedTelemtVersion, status.LatestVersion)
	}
}
