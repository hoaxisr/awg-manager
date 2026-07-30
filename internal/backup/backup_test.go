package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportRestoreRoundtrip(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "awg-manager")
	if err := os.MkdirAll(filepath.Join(dataDir, "tunnels"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte(`{"version":32}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "tunnels", "awg1.json"), []byte(`{"id":"awg1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "run", "wdtt.pid"), []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Export(dataDir, "2.16.0", &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}

	target := filepath.Join(root, "restored")
	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "settings.json")); err != nil {
		t.Fatalf("settings.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "tunnels", "awg1.json")); err != nil {
		t.Fatalf("tunnel missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "run", "wdtt.pid")); !os.IsNotExist(err) {
		t.Fatalf("run/ should be skipped, got err=%v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, "restored.pre-restore-*"))
	if len(matches) != 0 {
		t.Fatalf("fresh restore should not create pre-restore dir, got %v", matches)
	}

	// Second restore replaces existing dir and keeps pre-restore snapshot.
	if err := Restore(target, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("Restore again: %v", err)
	}
	matches, _ = filepath.Glob(filepath.Join(root, "restored.pre-restore-*"))
	if len(matches) != 1 {
		t.Fatalf("expected one pre-restore dir after overwrite, got %v", matches)
	}
}

func TestValidateStagingRejectsBadArchive(t *testing.T) {
	dir := t.TempDir()
	if err := validateStaging(dir); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Fatalf("expected settings.json error, got %v", err)
	}
}

func TestExtractRejectsPathTraversal(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()

	if err := extractArchive(bytes.NewReader(buf.Bytes()), dest); err == nil {
		t.Fatal("expected path traversal error")
	}
}
