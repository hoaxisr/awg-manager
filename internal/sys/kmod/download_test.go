package kmod

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionedURLFormat(t *testing.T) {
	tests := []struct {
		version string
		model   string
		want    string
	}{
		{"1.0.2", "KN-1012", VersionedBaseURL + "/v1.0.2/amneziawg-KN-1012.ko"},
		{"1.0.3", "KN-1012", VersionedBaseURL + "/v1.0.3/amneziawg-KN-1012.ko"},
		{"", "KN-1012", BaseURL + "/amneziawg-KN-1012.ko"},
		{"latest", "KN-1012", BaseURL + "/amneziawg-KN-1012.ko"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("version=%q model=%s", tt.version, tt.model), func(t *testing.T) {
			var got string
			if tt.version == "" || tt.version == "latest" {
				got = fmt.Sprintf("%s/amneziawg-%s.ko", BaseURL, tt.model)
			} else {
				got = fmt.Sprintf("%s/v%s/amneziawg-%s.ko", VersionedBaseURL, tt.version, tt.model)
			}
			if got != tt.want {
				t.Errorf("URL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsKnownVersion(t *testing.T) {
	if !IsKnownVersion("1.0.3") {
		t.Error("1.0.3 should be known")
	}
	if !IsKnownVersion("1.0.2") {
		t.Error("1.0.2 should be known")
	}
	if IsKnownVersion("0.0.1") {
		t.Error("0.0.1 should not be known")
	}
	if IsKnownVersion("") {
		t.Error("empty should not be known")
	}
}

func TestNeedsUpdate(t *testing.T) {
	// Without any version file, needsUpdate should return true
	origDir := ModulesDir

	tmpDir := t.TempDir()
	ModulesDir = tmpDir // temporarily override
	defer func() { ModulesDir = origDir }()

	// No version file → needs update
	if !needsUpdate("") {
		t.Error("expected needsUpdate=true when no version file")
	}

	// Write version matching target → no update
	_ = os.WriteFile(filepath.Join(tmpDir, versionFile), []byte("1.0.3"), 0644)
	if needsUpdate("1.0.3") {
		t.Error("expected needsUpdate=false when version matches target")
	}

	// Write version not matching target → needs update
	if !needsUpdate("1.0.2") {
		t.Error("expected needsUpdate=true when version differs from target")
	}

	// Empty target → compare against ExpectedKmodVersion
	if needsUpdate("") {
		// on-disk is "1.0.3" which equals ExpectedKmodVersion
		t.Error("expected needsUpdate=false when target is empty and on-disk matches expected")
	}
}

// TestVersionedURLsReachable checks that GitHub actually serves files at versioned URLs.
// This test makes real HTTP requests — skip with -short.
func TestVersionedURLsReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	model := "KN-1012" // aarch64 model (Giga/Hero)
	urls := map[string]string{
		"latest": fmt.Sprintf("%s/amneziawg-%s.ko", BaseURL, model),
		"1.0.3":  fmt.Sprintf("%s/v1.0.3/amneziawg-%s.ko", VersionedBaseURL, model),
		"1.0.2":  fmt.Sprintf("%s/v1.0.2/amneziawg-%s.ko", VersionedBaseURL, model),
	}

	client := &http.Client{
		// Don't follow redirects — just check the initial response
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for version, url := range urls {
		t.Run(version, func(t *testing.T) {
			resp, err := client.Get(url)
			if err != nil {
				t.Fatalf("GET %s: %v", url, err)
			}
			resp.Body.Close()

			// GitHub returns 302 redirect to blob storage
			if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusOK {
				t.Errorf("GET %s: status %d, want 200 or 302", url, resp.StatusCode)
			}
		})
	}
}

// TestDownloadVersion verifies that downloadVersion actually downloads a file.
// This test makes real HTTP requests and writes to a temp dir — skip with -short.
func TestDownloadVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	origDir := ModulesDir
	tmpDir := t.TempDir()
	ModulesDir = tmpDir
	defer func() { ModulesDir = origDir }()

	ctx := context.Background()
	model := "KN-1012"

	// Test downloading specific version
	if err := downloadVersion(ctx, model, "1.0.2"); err != nil {
		t.Fatalf("downloadVersion(1.0.2): %v", err)
	}

	// Verify file exists
	koPath := filepath.Join(tmpDir, "amneziawg.ko")
	info, err := os.Stat(koPath)
	if err != nil {
		t.Fatalf("module file not found after download: %v", err)
	}
	if info.Size() == 0 {
		t.Error("downloaded module file is empty")
	}

	// Verify version was recorded
	ver := readVersion()
	if ver != "1.0.2" {
		t.Errorf("recorded version = %q, want %q", ver, "1.0.2")
	}

	// Test downloading latest
	if err := downloadVersion(ctx, model, ""); err != nil {
		t.Fatalf("downloadVersion(latest): %v", err)
	}
	ver = readVersion()
	if ver != ExpectedKmodVersion {
		t.Errorf("recorded version after latest = %q, want %q", ver, ExpectedKmodVersion)
	}
}
