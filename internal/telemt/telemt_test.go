package telemt

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"runtime"
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
		{"x86_64", "x86_64", true},
		{"amd64", "x86_64", true},
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
	link := GenerateTgLink(ModeDirect, "192.168.1.1", 8443, "0123456789abcdef0123456789abcdef", "yandex.ru", "")
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

func TestGenerateTOML_Web(t *testing.T) {
	cfg := Config{
		Enabled:    true,
		Mode:       ModeWeb,
		Port:       8443,
		ListenIP:   "0.0.0.0",
		Secret:     "0123456789abcdef0123456789abcdef",
		WebHost:    "proxy.example.com",
		WebCarrier: "https-lanes",
		WebDecoy:   "http://127.0.0.1:80",
	}

	toml := GenerateTOML(cfg)
	if !strings.Contains(toml, `transport = "web"`) {
		t.Errorf("expected transport = web in toml, got: %s", toml)
	}
	if !strings.Contains(toml, `host = "proxy.example.com"`) {
		t.Errorf("expected host in toml, got: %s", toml)
	}
	if !strings.Contains(toml, `carrier = "https-lanes"`) {
		t.Errorf("expected carrier in toml, got: %s", toml)
	}
}

func TestGenerateTgLink_Web(t *testing.T) {
	link := GenerateTgLink(ModeWeb, "192.168.1.1", 8443, "0123456789abcdef0123456789abcdef", "", "proxy.example.com")
	if !strings.HasPrefix(link, "tg://webproxy?") {
		t.Fatalf("expected tg://webproxy? prefix, got: %s", link)
	}
	if !strings.Contains(link, "server=proxy.example.com") {
		t.Errorf("expected server in link, got: %s", link)
	}
	if !strings.Contains(link, "port=8443") {
		t.Errorf("expected port in link, got: %s", link)
	}
	if !strings.Contains(link, "secret=0123456789abcdef0123456789abcdef") {
		t.Errorf("expected secret in link, got: %s", link)
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
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script fixture on windows")
	}

	tmpDir := t.TempDir()
	svc := New(tmpDir, "aarch64")

	if err := os.WriteFile(svc.binPath, []byte("#!/bin/sh\necho 'telemt 3.5.7'\n"), 0755); err != nil {
		t.Fatalf("write mock binary: %v", err)
	}

	status := svc.GetStatus(context.Background())
	if !status.Installed {
		t.Fatalf("expected installed=true")
	}
	if !status.ArchSupported {
		t.Fatalf("expected archSupported=true for aarch64")
	}
	if status.Version != "3.5.7" {
		t.Fatalf("expected version=3.5.7, got %s", status.Version)
	}
	if status.LatestVersion != PinnedTelemtVersion {
		t.Fatalf("expected latestVersion=%s, got %s", PinnedTelemtVersion, status.LatestVersion)
	}
	if svc.cachedVersion != "3.5.7" {
		t.Fatalf("expected cachedVersion=3.5.7, got %s", svc.cachedVersion)
	}

	// Verify version caching: replace binary content with something broken
	if err := os.WriteFile(svc.binPath, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatalf("rewrite mock binary: %v", err)
	}
	// Even though the binary now fails, GetStatus uses the cached version
	status2 := svc.GetStatus(context.Background())
	if status2.Version != "3.5.7" {
		t.Fatalf("expected cached version 3.5.7, got %s", status2.Version)
	}

	// Invalidation: Uninstall clears the cached version
	if err := svc.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall error: %v", err)
	}
	if svc.cachedVersion != "" {
		t.Fatalf("expected cachedVersion to be cleared after uninstall, got %s", svc.cachedVersion)
	}
}

func TestService_Lifecycle_Enabled(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, "x86_64")

	// Initially disabled
	if svc.GetConfig().Enabled {
		t.Fatalf("expected initially disabled")
	}

	// Calling Start when uninstalled returns ErrNotInstalled and doesn't enable
	err := svc.Start(context.Background())
	if err == nil {
		t.Fatalf("expected start error when uninstalled")
	}
	if svc.GetConfig().Enabled {
		t.Fatalf("expected still disabled after failed start")
	}

	// Calling Stop sets Enabled=false
	svc.config.Enabled = true
	_ = svc.saveSettingsLocked()
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("stop error: %v", err)
	}
	if svc.GetConfig().Enabled {
		t.Fatalf("expected disabled after stop")
	}
}

func TestValidatePort(t *testing.T) {
	cases := []struct {
		port    int
		want    int
		wantErr bool
	}{
		{0, DefaultPort, false},
		{8443, 8443, false},
		{1080, 1080, false},
		{-1, 0, true},
		{65536, 0, true},
		{22, 0, true},
		{2222, 0, true},
		{1099, 0, true},
		{51820, 0, true},
	}
	for _, c := range cases {
		got, err := ValidatePort(c.port)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidatePort(%d) err = %v, wantErr = %v", c.port, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ValidatePort(%d) = %d, want %d", c.port, got, c.want)
		}
	}
}

func TestNormalizeTLSDomain(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"yandex.ru", "yandex.ru"},
		{"  yandex.ru  ", "yandex.ru"},
		{"https://yandex.ru", "yandex.ru"},
		{"http://yandex.ru:8443", "yandex.ru"},
		{"https://yandex.ru/test/path?foo=bar#hash", "yandex.ru"},
		{"yandex.ru:8443", "yandex.ru"},
		{"", DefaultTLSDomain},
		{"   ", DefaultTLSDomain},
	}
	for _, c := range cases {
		got := NormalizeTLSDomain(c.in)
		if got != c.want {
			t.Errorf("NormalizeTLSDomain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestService_Close_DoesNotDisable(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, "x86_64")

	// Set Enabled = true
	svc.config.Enabled = true
	_ = svc.saveSettingsLocked()

	// Calling Close() does NOT set Enabled = false
	if err := svc.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}
	if !svc.GetConfig().Enabled {
		t.Fatalf("expected service to remain enabled after Close()")
	}

	// Calling Stop() DOES set Enabled = false
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("stop error: %v", err)
	}
	if svc.GetConfig().Enabled {
		t.Fatalf("expected service to be disabled after Stop()")
	}
}

func TestService_StartIfEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, "x86_64")

	// Disabled and uninstalled -> returns nil without error
	if err := svc.StartIfEnabled(context.Background()); err != nil {
		t.Fatalf("unexpected error for disabled StartIfEnabled: %v", err)
	}

	// Enabled but uninstalled -> IsInstalled is false, so returns nil without error
	svc.config.Enabled = true
	if err := svc.StartIfEnabled(context.Background()); err != nil {
		t.Fatalf("unexpected error for uninstalled StartIfEnabled: %v", err)
	}
}

func TestSaveConfig_SecretGeneration(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, "x86_64")

	cfg := Config{
		Enabled:   false,
		Port:      8443,
		ListenIP:  "0.0.0.0",
		Secret:    "", // Empty secret
		TLSDomain: "https://example.com/foo",
	}

	if err := svc.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	saved := svc.GetConfig()
	if saved.Secret == "" {
		t.Fatalf("expected secret to be automatically generated")
	}
	if len(saved.Secret) != 32 {
		t.Fatalf("expected 32-char hex secret, got length %d: %s", len(saved.Secret), saved.Secret)
	}
	if saved.TLSDomain != "example.com" {
		t.Fatalf("expected normalized tlsDomain example.com, got %s", saved.TLSDomain)
	}
}

func TestSaveConfig_ReservedPort(t *testing.T) {
	tmpDir := t.TempDir()
	svc := New(tmpDir, "x86_64")

	for _, port := range []int{22, 2222, 1099, 51820} {
		cfg := Config{
			Port: port,
		}
		err := svc.SaveConfig(context.Background(), cfg)
		if err == nil {
			t.Fatalf("expected error for reserved port %d, got nil", port)
		}
		if !strings.Contains(err.Error(), "reserved by system") {
			t.Fatalf("expected 'reserved by system' error for port %d, got %v", port, err)
		}
	}
}
