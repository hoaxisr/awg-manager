package kmod

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// BaseURL is the base URL for downloading kernel modules (latest release).
	// Module files are named amneziawg-{model}.ko (e.g. amneziawg-KN-1010.ko).
	BaseURL = "https://github.com/hoaxisr/amneziawg-linux-kernel-module-keenetic/releases/latest/download"

	// VersionedBaseURL is the base URL for downloading specific kernel module versions.
	VersionedBaseURL = "https://github.com/hoaxisr/amneziawg-linux-kernel-module-keenetic/releases/download"

	// downloadTimeout is the HTTP client timeout for module downloads.
	downloadTimeout = 60 * time.Second

	// ExpectedKmodVersion is the kernel module version expected by this build.
	// Bump this when new .ko files are released to trigger re-download on upgrade.
	ExpectedKmodVersion = "1.0.3"

	// versionFile is the filename that stores the on-disk module version.
	versionFile = "amneziawg.version"
)

// KnownVersions is the list of available kernel module versions (newest first).
// Updated when new .ko releases are published alongside awg-manager.
var KnownVersions = []string{"1.0.3", "1.0.2"}

// RecommendedVersion is the version recommended for most users.
const RecommendedVersion = ExpectedKmodVersion

// DownloadStatus represents kernel module download state.
type DownloadStatus string

const (
	StatusNotNeeded   DownloadStatus = "not_needed"       // module already exists on disk
	StatusDownloading DownloadStatus = "downloading"      // download in progress
	StatusDownloaded  DownloadStatus = "downloaded"       // download completed successfully
	StatusFailed      DownloadStatus = "download_failed"  // download failed
	StatusUnsupported DownloadStatus = "unsupported"      // unknown model, can't download
)

func writeVersion(version string) error {
	path := filepath.Join(ModulesDir, versionFile)
	return os.WriteFile(path, []byte(version), 0644)
}

func readVersion() string {
	path := filepath.Join(ModulesDir, versionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func needsUpdate(targetVersion string) bool {
	if targetVersion == "" || targetVersion == "latest" {
		targetVersion = ExpectedKmodVersion
	}
	return readVersion() != targetVersion
}

// IsKnownVersion returns true if the version is in KnownVersions.
func IsKnownVersion(version string) bool {
	for _, v := range KnownVersions {
		if v == version {
			return true
		}
	}
	return false
}

// downloadVersion fetches a specific version of the kernel module.
// version "" or "latest" uses the latest release URL; otherwise downloads from a tagged release.
func downloadVersion(ctx context.Context, model, version string) error {
	var url string
	if version == "" || version == "latest" {
		url = fmt.Sprintf("%s/amneziawg-%s.ko", BaseURL, model)
		version = ExpectedKmodVersion
	} else {
		url = fmt.Sprintf("%s/v%s/amneziawg-%s.ko", VersionedBaseURL, version, model)
	}
	return downloadFromURL(ctx, url, version)
}

// download fetches the kernel module for the given model from GitHub releases (latest).
func download(ctx context.Context, model string) error {
	url := fmt.Sprintf("%s/amneziawg-%s.ko", BaseURL, model)
	return downloadFromURL(ctx, url, ExpectedKmodVersion)
}

// downloadFromURL fetches a kernel module from the given URL and records the version.
func downloadFromURL(ctx context.Context, url, version string) error {
	client := &http.Client{Timeout: downloadTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// Ensure modules directory exists
	if err := os.MkdirAll(ModulesDir, 0755); err != nil {
		return fmt.Errorf("create modules dir: %w", err)
	}

	targetPath := filepath.Join(ModulesDir, "amneziawg.ko")
	tmpPath := targetPath + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	_, err = io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write module: %w", err)
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", closeErr)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, targetPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename module: %w", err)
	}

	// Record version for future update checks (best-effort)
	_ = writeVersion(version)

	return nil
}
