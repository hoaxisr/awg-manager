package updater

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	osexec "os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

const (
	defaultEntwareRepoURL = "http://repo.hoaxisr.ru"
	repoTimeout           = 30 * time.Second
	downloadTimeout       = 5 * time.Minute
	downloadDir           = "/opt/tmp"
	pkgName               = "awg-manager"
)

const (
	channelStable  = "stable"
	channelDevelop = "develop"
)

// entwareRepoURL is a variable so tests can override it with httptest server URL.
var entwareRepoURL = defaultEntwareRepoURL

// channelBaseURL возвращает базовый URL репозитория для канала. develop
// отдаётся из подкаталога /develop того же сервера.
func channelBaseURL(channel string) string {
	if channel == channelDevelop {
		return entwareRepoURL + "/develop"
	}
	return entwareRepoURL
}

// versionComparator выбирает сравнялку версий по каналу: develop учитывает
// build-revision (+rN), stable — нет (как было).
func versionComparator(channel string) func(a, b string) int {
	if channel == channelDevelop {
		return semver.CompareWithRevision
	}
	return semver.Compare
}

// Check queries the entware repo's Packages.gz for the latest awg-manager
// version and returns update info including the .ipk download URL if a newer
// version is available. Uses the stable channel.
func Check(ctx context.Context, currentVersion string) *UpdateInfo {
	return checkWithDownloader(ctx, currentVersion, channelStable, newDefaultDownloader())
}

func checkWithDownloader(ctx context.Context, currentVersion, channel string, dl Downloader) *UpdateInfo {
	info := &UpdateInfo{
		CurrentVersion: currentVersion,
		CheckedAt:      time.Now(),
	}

	cmp := versionComparator(channel)
	base := channelBaseURL(channel)
	archDir := archSuffixToRepoDir(archSuffix())
	pkgsURL := fmt.Sprintf("%s/%s/Packages.gz", base, archDir)

	pkg, err := fetchLatestPackageWithDownloader(ctx, dl, pkgsURL, pkgName, cmp)
	if err != nil {
		info.Error = err.Error()
		info.ErrorCode = string(downloader.RouteErrorCode(err))
		return info
	}

	if cmp(currentVersion, pkg.Version) >= 0 {
		return info
	}

	info.Available = true
	info.LatestVersion = pkg.Version
	info.DownloadURL = fmt.Sprintf("%s/%s/%s", base, archDir, pkg.Filename)
	return info
}

// Upgrade downloads the IPK from downloadURL and launches opkg install in a
// detached process.
func Upgrade(ctx context.Context, downloadURL string) error {
	return upgradeWithDownloader(ctx, downloadURL, newDefaultDownloader())
}

var startDetachedUpgrade = func(ipkPath string) error {
	cmd := osexec.Command("sh", "-c", fmt.Sprintf("sleep 2 && opkg install %s && rm -f %s", ipkPath, ipkPath))
	setUpgradeDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func upgradeWithDownloader(ctx context.Context, downloadURL string, dl Downloader) error {
	if dl == nil {
		dl = newDefaultDownloader()
	}
	filename, err := ipkFilenameFromURL(downloadURL)
	if err != nil {
		return err
	}
	ipkPath := downloadDir + "/" + filename

	_, err = dl.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose:      "awgm-update-ipk",
			URL:          downloadURL,
			Method:       http.MethodGet,
			Timeout:      downloadTimeout,
			MaxBodyBytes: ipkMaxBytes,
		},
		DestPath:     ipkPath,
		MaxFileBytes: ipkMaxBytes,
		Mode:         0o644,
		Atomic:       true,
	})
	if err != nil {
		return fmt.Errorf("download IPK: %w", err)
	}
	if err := startDetachedUpgrade(ipkPath); err != nil {
		os.Remove(ipkPath)
		return err
	}
	return nil
}

func ipkFilenameFromURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid download URL: %w", err)
	}
	if strings.Contains(u.Path, "..") || strings.Contains(u.EscapedPath(), "..") || strings.Contains(strings.ToLower(u.EscapedPath()), "%2e") {
		return "", fmt.Errorf("invalid download URL path: %q", raw)
	}
	name := path.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		return "", fmt.Errorf("invalid download URL path: %q", raw)
	}
	if !isSafeIPKFilename(name) {
		return "", fmt.Errorf("invalid package filename %q", name)
	}
	return name, nil
}

var safeIPKFilenameRe = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

func isSafeIPKFilename(name string) bool {
	if name == "" || name == "." || name == "/" {
		return false
	}
	if !strings.HasPrefix(name, pkgName+"_") {
		return false
	}
	if !strings.HasSuffix(strings.ToLower(name), ".ipk") {
		return false
	}
	return safeIPKFilenameRe.MatchString(name)
}
