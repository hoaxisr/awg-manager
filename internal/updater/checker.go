package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
		info.Error = fmt.Sprintf("entware repo: %s", err)
		return info
	}

	if cmp(currentVersion, pkg.Version) >= 0 {
		return info
	}

	info.Available = true
	info.LatestVersion = pkg.Version
	info.DownloadURL = fmt.Sprintf("%s/%s/%s", base, archDir, pkg.Filename)
	info.SHA256 = pkg.SHA256
	return info
}

// upgradeLogPath captures the detached opkg output. The old daemon is
// stopped by the package prerm mid-install, so this file is the ONLY
// diagnostic left if opkg fails and the router ends up without awg-manager.
const upgradeLogPath = "/opt/tmp/awg-manager-upgrade.log"

// installedBinPath — куда пакет кладёт бинарь; проверяется после opkg install.
const installedBinPath = "/opt/bin/awg-manager"

// buildUpgradeScript собирает detached-скрипт установки: opkg + верификация
// бинаря (не пустой + ELF-магия) + исход в syslog роутера через logger —
// единственный канал, который виден пользователю и без демона, и без нашего
// веба (#571: сбойная запись на накопитель оставила 0-байтный бинарь, а вся
// обвязка молча отчиталась об успехе). IPK удаляется только после успешной
// проверки — иначе остаётся для ручного opkg install.
func buildUpgradeScript(ipkPath, binPath string) string {
	return fmt.Sprintf(`sleep 2; opkg install %[1]s; rc=$?
if [ "$rc" = "0" ] && [ -s %[2]s ] && dd if=%[2]s bs=4 count=1 2>/dev/null | grep -q ELF; then
	rm -f %[1]s
	logger -t awg-manager "upgrade: opkg install OK, binary verified"
else
	logger -t awg-manager -p daemon.err "upgrade FAILED: opkg rc=$rc or corrupt binary %[2]s; ipk kept: %[1]s"
fi`, ipkPath, binPath)
}

var startDetachedUpgrade = func(ipkPath string) error {
	cmd := osexec.Command("sh", "-c", buildUpgradeScript(ipkPath, installedBinPath))
	if logf, err := os.OpenFile(upgradeLogPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644); err == nil {
		cmd.Stdout = logf
		cmd.Stderr = logf
		defer logf.Close() // child keeps its own fd after Start
	}
	setUpgradeDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

func upgradeWithDownloader(ctx context.Context, downloadURL, wantSHA256 string, dl Downloader) error {
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
	if err := verifyFileSHA256(ipkPath, wantSHA256); err != nil {
		os.Remove(ipkPath)
		return err
	}
	if err := startDetachedUpgrade(ipkPath); err != nil {
		os.Remove(ipkPath)
		return err
	}
	return nil
}

// verifyFileSHA256 checks path against the hex digest want. Empty want is
// accepted (older repo indexes without SHA256sum) — but when the index does
// provide a digest, a mismatch aborts the root opkg install of the file.
func verifyFileSHA256(path, want string) error {
	if want == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify IPK: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("verify IPK: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("IPK checksum mismatch: got %s, want %s", got, want)
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
