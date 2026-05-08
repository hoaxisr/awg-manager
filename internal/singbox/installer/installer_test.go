package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestInstaller_Download_Success(t *testing.T) {
	body := []byte("fake sing-box binary contents")
	sum := sha256.Sum256(body)
	hexSum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: hexSum}, nil)

	tmp, err := inst.Download(context.Background())
	if err != nil {
		t.Fatalf("Download err: %v", err)
	}
	if tmp != target+".tmp" {
		t.Errorf("tmp path = %q, want %s.tmp", tmp, target)
	}
	got, _ := os.ReadFile(tmp)
	if string(got) != string(body) {
		t.Errorf("content mismatch")
	}
}

func TestInstaller_Download_SHAMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered content"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: "0000"}, nil)

	if _, err := inst.Download(context.Background()); err == nil {
		t.Fatal("expected SHA mismatch error, got nil")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked after SHA mismatch: %v", err)
	}
}

func TestInstaller_Download_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: "0000"}, nil)

	if _, err := inst.Download(context.Background()); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked after HTTP error")
	}
}

func TestInstaller_Activate_AtomicMove(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, []byte("binary"), 0644); err != nil {
		t.Fatal(err)
	}

	inst := New(target, "test-arch", BinarySpec{}, nil)
	if err := inst.Activate(tmp); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	st, err := os.Stat(target)
	if err != nil {
		t.Fatalf("target missing after Activate: %v", err)
	}
	if st.Mode().Perm()&0100 == 0 {
		t.Errorf("executable bit not set: %v", st.Mode())
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("tmp file still exists after Activate")
	}
}

func TestInstaller_CurrentVersion_NotInstalled(t *testing.T) {
	dir := t.TempDir()
	inst := New(filepath.Join(dir, "sing-box"), "x", BinarySpec{}, nil)
	if v := inst.CurrentVersion(context.Background()); v != "" {
		t.Errorf("CurrentVersion on missing binary = %q, want empty", v)
	}
}

func TestInstaller_CurrentVersion_AcceptsMixedCaseBanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable script fixture is POSIX-only")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	script := "#!/bin/sh\necho 'SingBox Version 1.13.11'\n"
	if err := os.WriteFile(target, []byte(script), 0755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	inst := New(target, "x", BinarySpec{}, nil)
	if v := inst.CurrentVersion(context.Background()); v != "1.13.11" {
		t.Errorf("CurrentVersion() = %q, want 1.13.11", v)
	}
}

func TestInstaller_BinaryPathAndRequiredVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.2.3", URL: "u", SHA256: "s"}, nil)
	if got := inst.BinaryPath(); got != target {
		t.Errorf("BinaryPath() = %q, want %q", got, target)
	}
	if got := inst.RequiredVersion(); got != "1.2.3" {
		t.Errorf("RequiredVersion() = %q, want 1.2.3", got)
	}
}

func TestInstaller_Download_ContextCancellation(t *testing.T) {
	// Slow handler — writes one byte then blocks until the request context
	// is cancelled. Mimics a stalled download.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("X"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	dir := t.TempDir()
	target := filepath.Join(dir, "sing-box")
	inst := New(target, "test-arch", BinarySpec{Version: "1.13.11", URL: srv.URL, SHA256: "0000"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	if _, err := inst.Download(ctx); err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file leaked after context cancellation: %v", err)
	}
}
