package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	defaultGitHubAPIURL = "https://api.github.com/repos/hoaxisr/awg-manager/releases/latest"
	apiTimeout          = 30 * time.Second
	downloadTimeout     = 5 * time.Minute
	downloadDir         = "/opt/tmp"
)

// githubAPIURL is a variable so tests can override it with httptest server URL.
var githubAPIURL = defaultGitHubAPIURL

// githubRelease is the minimal GitHub API response structure.
type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

// githubAsset is a release asset from GitHub.
type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Check queries the GitHub Releases API for the latest version
// and returns update info including download URL if available.
func Check(ctx context.Context, currentVersion string) *UpdateInfo {
	info := &UpdateInfo{
		CurrentVersion: currentVersion,
		CheckedAt:      time.Now(),
	}

	release, warning, err := fetchLatestRelease(ctx)
	if err != nil {
		info.Error = fmt.Sprintf("GitHub API: %s", err)
		return info
	}
	if warning != "" {
		info.Warning = warning
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == "" {
		info.Error = "GitHub API: empty tag_name"
		return info
	}

	if compareVersions(currentVersion, latestVersion) < 0 {
		info.Available = true
		info.LatestVersion = latestVersion

		// Find the IPK asset for current architecture
		suffix := archSuffix()
		for _, asset := range release.Assets {
			if strings.Contains(asset.Name, suffix) && strings.HasSuffix(asset.Name, ".ipk") {
				info.DownloadURL = asset.BrowserDownloadURL
				break
			}
		}

		if info.DownloadURL == "" {
			info.Error = fmt.Sprintf("no IPK asset found for architecture %s", suffix)
			info.Available = false
		}
	}

	return info
}

// Upgrade downloads the IPK from downloadURL and launches opkg install in a detached process.
func Upgrade(_ context.Context, downloadURL string) error {
	filename := path.Base(downloadURL)
	ipkPath := downloadDir + "/" + filename

	if err := downloadFile(downloadURL, ipkPath); err != nil {
		return fmt.Errorf("download IPK: %w", err)
	}

	cmd := osexec.Command("sh", "-c", fmt.Sprintf("sleep 2 && opkg install %s && rm -f %s", ipkPath, ipkPath))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		os.Remove(ipkPath)
		return err
	}
	go cmd.Wait() // reap zombie if upgrade fails and we survive
	return nil
}

// fetchLatestRelease calls the GitHub API and returns the latest release.
// On TLS errors, it attempts to install ca-bundle and retries.
func fetchLatestRelease(ctx context.Context) (*githubRelease, string, error) {
	release, err := doFetchRelease(ctx)
	if err == nil {
		return release, "", nil
	}

	// TLS fallback: install ca-bundle and retry
	if isTLSError(err) {
		exec.RunWithOptions(ctx, "opkg", []string{"install", "ca-bundle"}, exec.Options{
			Timeout: 60 * time.Second,
		})
		// Update SSL_CERT_FILE for the current process
		for _, certPath := range []string{"/opt/etc/ssl/certs/ca-certificates.crt", "/opt/share/ca-certificates/ca-certificates.crt"} {
			if _, err := os.Stat(certPath); err == nil {
				os.Setenv("SSL_CERT_FILE", certPath)
				break
			}
		}

		release, retryErr := doFetchRelease(ctx)
		if retryErr == nil {
			return release, "Автоматически установлен пакет ca-bundle для поддержки HTTPS", nil
		}
		return nil, "", fmt.Errorf("TLS error (ca-bundle installed but still failing): %w", retryErr)
	}

	return nil, "", err
}

// doFetchRelease performs a single GitHub API request.
func doFetchRelease(ctx context.Context) (*githubRelease, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &release, nil
}

// downloadFile downloads a file from url to the given path.
func downloadFile(url, destPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(destPath)
		return err
	}
	return nil
}

// isTLSError checks if the error is TLS/certificate related.
func isTLSError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") || strings.Contains(msg, "tls:")
}

// compareVersions compares two semver-like version strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}
	return 0
}

// archSuffix returns the entware architecture string for the current platform.
func archSuffix() string {
	switch runtime.GOARCH {
	case "mipsle":
		return "mipsel-3.4"
	case "mips":
		return "mips-3.4"
	case "arm64":
		return "aarch64-3.10"
	default:
		return runtime.GOARCH
	}
}
