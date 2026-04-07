package updater

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// gzipBytes is a test helper that gzips a string and returns the bytes.
func gzipBytes(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestParsePackagesGz_SingleBlock(t *testing.T) {
	body := `Package: awg-manager
Version: 2.7.3
Architecture: aarch64-3.10
Filename: awg-manager_2.7.3_aarch64-3.10-kn.ipk
Size: 4354993
`
	pkg, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, body)), "awg-manager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkg.Version != "2.7.3" {
		t.Errorf("Version = %q, want 2.7.3", pkg.Version)
	}
	if pkg.Filename != "awg-manager_2.7.3_aarch64-3.10-kn.ipk" {
		t.Errorf("Filename = %q, want awg-manager_2.7.3_aarch64-3.10-kn.ipk", pkg.Filename)
	}
}

func TestParsePackagesGz_MultipleBlocksHighestWins(t *testing.T) {
	body := `Package: awg-manager
Version: 2.6.5
Filename: awg-manager_2.6.5_aarch64-3.10-kn.ipk

Package: awg-manager
Version: 2.7.3
Filename: awg-manager_2.7.3_aarch64-3.10-kn.ipk

Package: awg-manager
Version: 2.7.10
Filename: awg-manager_2.7.10_aarch64-3.10-kn.ipk
`
	pkg, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, body)), "awg-manager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkg.Version != "2.7.10" {
		t.Errorf("Version = %q, want 2.7.10 (numeric, not lexical, comparison)", pkg.Version)
	}
	if pkg.Filename != "awg-manager_2.7.10_aarch64-3.10-kn.ipk" {
		t.Errorf("Filename = %q", pkg.Filename)
	}
}

func TestParsePackagesGz_IgnoresOtherPackages(t *testing.T) {
	body := `Package: curl
Version: 8.0.1
Filename: curl_8.0.1_aarch64-3.10.ipk

Package: awg-manager
Version: 2.7.3
Filename: awg-manager_2.7.3_aarch64-3.10-kn.ipk

Package: iptables
Version: 1.8.7
Filename: iptables_1.8.7_aarch64-3.10.ipk
`
	pkg, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, body)), "awg-manager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pkg.Version != "2.7.3" {
		t.Errorf("Version = %q, want 2.7.3", pkg.Version)
	}
}

func TestParsePackagesGz_NoMatchingPackage(t *testing.T) {
	body := `Package: curl
Version: 8.0.1
Filename: curl_8.0.1_aarch64-3.10.ipk
`
	_, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, body)), "awg-manager")
	if err == nil {
		t.Fatal("expected error for missing package")
	}
	if !strings.Contains(err.Error(), "awg-manager") {
		t.Errorf("error = %q, want it to mention 'awg-manager'", err.Error())
	}
}

func TestParsePackagesGz_MissingVersion(t *testing.T) {
	body := `Package: awg-manager
Filename: awg-manager_2.7.3_aarch64-3.10-kn.ipk
`
	_, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, body)), "awg-manager")
	if err == nil {
		t.Fatal("expected error for block missing Version field")
	}
}

func TestParsePackagesGz_MissingFilename(t *testing.T) {
	body := `Package: awg-manager
Version: 2.7.3
`
	_, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, body)), "awg-manager")
	if err == nil {
		t.Fatal("expected error for block missing Filename field")
	}
}

func TestParsePackagesGz_EmptyBody(t *testing.T) {
	_, err := parsePackagesGz(bytes.NewReader(gzipBytes(t, "")), "awg-manager")
	if err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestParsePackagesGz_MalformedGzip(t *testing.T) {
	_, err := parsePackagesGz(bytes.NewReader([]byte("not-a-gzip-stream")), "awg-manager")
	if err == nil {
		t.Fatal("expected gunzip error")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.3.10", "2.3.11", -1},
		{"2.3.11", "2.3.10", 1},
		{"2.3.11", "2.3.11", 0},
		{"2.4.0", "2.3.99", 1},
		{"1.0.0", "2.0.0", -1},
		{"2.3", "2.3.0", 0},
		{"2.3.0", "2.3", 0},
		{"2.3", "2.3.1", -1},
		{"10.0.0", "9.99.99", 1},
		{"0.0.1", "0.0.2", -1},
		{"2.7.10", "2.7.3", 1},
	}
	for _, tt := range tests {
		got := compareVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestArchRepoDir(t *testing.T) {
	tests := []struct {
		suffix string
		want   string
	}{
		{"aarch64-3.10", "aarch64-k3.10"},
		{"mipsel-3.4", "mipsel-k3.4"},
		{"mips-3.4", "mips-k3.4"},
	}
	for _, tt := range tests {
		got := archSuffixToRepoDir(tt.suffix)
		if got != tt.want {
			t.Errorf("archSuffixToRepoDir(%q) = %q, want %q", tt.suffix, got, tt.want)
		}
	}
}
