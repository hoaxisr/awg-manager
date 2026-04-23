package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

const (
	defaultEntwareRepoURL = "http://repo.hoaxisr.ru"
	repoTimeout           = 30 * time.Second
	downloadTimeout       = 5 * time.Minute
	downloadDir           = "/opt/tmp"
	pkgName               = "awg-manager"
)

// entwareRepoURL is a variable so tests can override it with httptest server URL.
var entwareRepoURL = defaultEntwareRepoURL

// Check queries the entware repo's Packages.gz for the latest awg-manager
// version and returns update info including the .ipk download URL if a newer
// version is available.
func Check(ctx context.Context, currentVersion string) *UpdateInfo {
	info := &UpdateInfo{
		CurrentVersion: currentVersion,
		CheckedAt:      time.Now(),
	}

	archDir := archSuffixToRepoDir(archSuffix())
	pkgsURL := fmt.Sprintf("%s/%s/Packages.gz", entwareRepoURL, archDir)

	pkg, err := fetchLatestPackage(ctx, pkgsURL, pkgName)
	if err != nil {
		info.Error = fmt.Sprintf("entware repo: %s", err)
		return info
	}

	if semver.Compare(currentVersion, pkg.Version) >= 0 {
		return info
	}

	info.Available = true
	info.LatestVersion = pkg.Version
	info.DownloadURL = fmt.Sprintf("%s/%s/%s", entwareRepoURL, archDir, pkg.Filename)
	return info
}

// fetchLatestPackage downloads pkgsURL and returns the highest-version entry
// for pkgName from the gzipped Packages index.
func fetchLatestPackage(ctx context.Context, pkgsURL, pkgName string) (PackageEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, repoTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pkgsURL, nil)
	if err != nil {
		return PackageEntry{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return PackageEntry{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return PackageEntry{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return parsePackagesGz(resp.Body, pkgName)
}

// Upgrade downloads the IPK from downloadURL and launches opkg install in a
// detached process.
func Upgrade(_ context.Context, downloadURL string) error {
	filename := path.Base(downloadURL)
	ipkPath := downloadDir + "/" + filename

	if err := downloadFile(downloadURL, ipkPath); err != nil {
		return fmt.Errorf("download IPK: %w", err)
	}

	cmd := osexec.Command("sh", "-c", fmt.Sprintf("sleep 2 && opkg install %s && rm -f %s", ipkPath, ipkPath))
	setUpgradeDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		os.Remove(ipkPath)
		return err
	}
	go cmd.Wait() // reap zombie if upgrade fails and we survive
	return nil
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
