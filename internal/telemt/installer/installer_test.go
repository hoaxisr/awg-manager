package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeArch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"aarch64-3.10", "aarch64"},
		{"arm64", "aarch64"},
		{"mipsel-3.4", "mipsel"},
		{"mipsle", "mipsel"},
		{"mips-3.4", "mips"},
		{"x86_64", "x86_64"},
		{"amd64", "x86_64"},
		{"armv7", "armv7"},
	}

	for _, c := range cases {
		got := normalizeArch(c.in)
		if got != c.want {
			t.Errorf("normalizeArch(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		cur  string
		lat  string
		want bool
	}{
		{"3.5.5", "3.5.8", true},
		{"3.5.8", "3.5.8", false},
		{"3.5.9", "3.5.8", false},
		{"v3.5.0", "3.5.8", true},
		{"3.5.8", "v3.6.0", true},
		{"", "3.5.8", false},
		{"3.5.8", "", false},
	}

	for _, c := range cases {
		got := compareVersions(c.cur, c.lat)
		if got != c.want {
			t.Errorf("compareVersions(%q, %q) = %v; want %v", c.cur, c.lat, got, c.want)
		}
	}
}

func TestExtractTarGzBinary(t *testing.T) {
	// Create mock in-memory tar.gz
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	content := []byte("#!/bin/sh\necho telemt mock\n")
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
		t.Fatalf("extracted content mismatch: %q vs %q", string(read), string(content))
	}
}

func TestTelemtInstaller_GetStatus_Mock(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "telemt")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\necho 'telemt 3.5.8'\n"), 0755); err != nil {
		t.Fatalf("write mock binary: %v", err)
	}

	inst := New("aarch64")
	inst.targetPath = binPath

	status := inst.GetStatus(context.Background())
	if !status.Installed {
		t.Fatalf("expected installed=true")
	}
	if status.Arch != "aarch64" {
		t.Fatalf("expected arch=aarch64, got %s", status.Arch)
	}
	if status.LatestVersion != PinnedTelemtVersion {
		t.Fatalf("expected latestVersion=%s, got %s", PinnedTelemtVersion, status.LatestVersion)
	}
}

func TestEmbeddedBinaries_PinsComplete(t *testing.T) {
	for _, arch := range []string{"aarch64", "x86_64", "mipsel"} {
		spec, ok := EmbeddedBinaries[arch]
		if !ok {
			t.Errorf("missing EmbeddedBinaries for arch %q", arch)
			continue
		}
		if spec.Version == "" {
			t.Errorf("empty version for %q", arch)
		}
		if spec.SHA256 == "" {
			t.Errorf("empty SHA256 for %q", arch)
		}
		if spec.ArchiveSize <= 0 {
			t.Errorf("invalid archive size for %q: %d", arch, spec.ArchiveSize)
		}
	}
}

func TestCheckLatestRelease_Pinned(t *testing.T) {
	inst := New("aarch64")
	ver, url, err := inst.CheckLatestRelease(context.Background())
	if err != nil {
		t.Fatalf("CheckLatestRelease failed: %v", err)
	}
	if ver != PinnedTelemtVersion {
		t.Fatalf("expected %s, got %s", PinnedTelemtVersion, ver)
	}
	if url != EmbeddedBinaries["aarch64"].URL {
		t.Fatalf("expected %s, got %s", EmbeddedBinaries["aarch64"].URL, url)
	}
}
