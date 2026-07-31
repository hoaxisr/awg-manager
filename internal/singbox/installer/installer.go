// Package installer manages the sing-box binary lifecycle outside the
// Entware opkg pipeline. Downloads come from our entware-repo server
// over HTTP (integrity guarded by SHA256 baked into this build of
// awg-manager), and land at an absolute path in our config directory
// so they cannot be confused with a user-installed sing-box on PATH.
package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/httpdownload"
	"github.com/hoaxisr/awg-manager/internal/sys/routerinfo"
)

// DefaultBinaryPath is where the managed sing-box binary is placed on disk.
// Inside our own config directory so cleanup is trivial (rm -rf the dir).
const DefaultBinaryPath = "/opt/etc/awg-manager/singbox/sing-box"

// BinarySpec is one architecture's download metadata. Embedded in the
// build via embedded.go so a compromised server cannot serve a tampered
// binary that awg-manager would still trust.
type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
	Size    int64 // bytes (uncompressed); 0 → disk-gate skipped (backwards-compatible)
}

// Installer downloads, verifies, and activates sing-box binaries.
type Installer struct {
	binaryPath string
	arch       string
	spec       BinarySpec
	downloader Downloader
	appLog     *logging.ScopedLogger

	// opkgRemove is overridable for tests; production uses defaultOpkgRemove.
	// Hooked here (not in installer constructor) so Phase 3.3 (Migrate) can
	// inject its own.
	opkgRemove        func(context.Context) error
	opkgListInstalled func(context.Context) (string, error)

	freeDisk func(path string) (int64, bool) // overridable for tests; defaults to routerinfo.FreeBytes

	// SHA256 of the live binary, cached by (size, mtime). Generic SHA256 of a
	// ~80MB binary costs ~13s CPU on softfloat MIPS, and GetStatus needs the
	// checksum on every 30s poll — without the cache the router burns a full
	// core just serving /api/singbox/status. Activate() replaces the file via
	// rename, so a stat mismatch self-invalidates the cache.
	shaMu   sync.Mutex
	shaVal  string
	shaSize int64
	shaMod  time.Time
}

type Downloader interface {
	DownloadFile(ctx context.Context, req DownloadFileRequest) (DownloadFileResult, error)
}

// binaryMaxBytes is the fallback download ceiling used only when free disk
// space can't be queried. Normally the limit is dynamic — actual free space
// minus diskReserveBytes — so large (uncompressed) binaries download fine on
// routers with roomy /opt (e.g. entware on external storage) while tiny-disk
// routers are still protected. 128 MiB covers current uncompressed develop
// builds (~70-86 MB per arch) even on the statfs-unavailable fallback path.
const binaryMaxBytes = 128 << 20

// diskReserveBytes is kept free after the download so activation/other writes
// don't fill the filesystem completely.
const diskReserveBytes = 16 << 20

// downloadByteLimit returns the max download size: free disk space minus a
// reserve margin, or the static fallback when free space is unknown. Clamped
// to be non-negative.
func downloadByteLimit(freeBytes int64, freeOK bool, fallback, reserve int64) int64 {
	if !freeOK {
		return fallback
	}
	avail := freeBytes - reserve
	if avail < 0 {
		avail = 0
	}
	return avail
}

type DownloadFileRequest struct {
	URL          string
	DestPath     string
	Timeout      time.Duration
	MaxFileBytes int64
	Mode         os.FileMode
	Progress     httpdownload.ProgressFn
}

type DownloadFileResult struct {
	Path string
	Size int64
}

// New builds an Installer. arch maps into EmbeddedBinaries; spec is what
// this build is pinned to. appLogger may be nil (tests).
func New(binaryPath, arch string, spec BinarySpec, appLogger logging.AppLogger) *Installer {
	return &Installer{
		binaryPath: binaryPath,
		arch:       arch,
		spec:       spec,
		appLog:     logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBProcess),
		freeDisk:   routerinfo.FreeBytes,
	}
}

func (i *Installer) SetDownloader(dl Downloader) {
	i.downloader = dl
}

// BinaryPath is the absolute filesystem path where the managed binary
// lives once activated. Callers wire this into the Operator.
func (i *Installer) BinaryPath() string { return i.binaryPath }

// RequiredVersion is the version this awg-manager build is pinned to.
func (i *Installer) RequiredVersion() string { return i.spec.Version }

// RequiredSHA256 is the checksum this awg-manager build is pinned to.
func (i *Installer) RequiredSHA256() string { return i.spec.SHA256 }

// RequiredSize is the expected size of the pinned binary (bytes, uncompressed).
// Zero means unknown — disk-gate is skipped.
func (i *Installer) RequiredSize() int64 { return i.spec.Size }

// CurrentSHA256 returns the checksum of the installed managed binary,
// served from the (size, mtime) cache when the file is unchanged.
func (i *Installer) CurrentSHA256() (string, error) {
	return i.binarySHA256()
}

// binarySHA256 returns the cached checksum of binaryPath, rehashing only
// when the file's size or mtime differs from the cached snapshot. The
// mutex serializes concurrent polls so they don't hash the same file twice.
func (i *Installer) binarySHA256() (string, error) {
	i.shaMu.Lock()
	defer i.shaMu.Unlock()

	fi, err := os.Stat(i.binaryPath)
	if err != nil {
		return "", err
	}
	if i.shaVal != "" && fi.Size() == i.shaSize && fi.ModTime().Equal(i.shaMod) {
		return i.shaVal, nil
	}
	sha, err := sha256File(i.binaryPath)
	if err != nil {
		return "", err
	}
	i.shaVal = sha
	i.shaSize = fi.Size()
	i.shaMod = fi.ModTime()
	return sha, nil
}

// MatchesRequired reports whether the installed binary matches both the
// pinned version and pinned bytes. The SHA256 check is intentional: custom
// sing-box rebuilds can keep the same upstream version while fixing target-
// specific binary contents.
func (i *Installer) MatchesRequired(ctx context.Context) bool {
	if i.CurrentVersion(ctx) != i.RequiredVersion() {
		return false
	}
	currentSHA, err := i.CurrentSHA256()
	return err == nil && strings.EqualFold(currentSHA, i.RequiredSHA256())
}

// Download fetches the binary to <binaryPath>.tmp and verifies SHA256.
// On verification failure or HTTP error the tmp file is removed so the
// caller does not activate corrupted contents. Returns the tmp path on
// success.
// Download fetches the binary with optional throttled progress callbacks.
// onProgress may be nil when no UI feedback is needed (e.g. early-boot
// migrations). When provided, it receives cumulative byte counters and
// the expected total (0 if Content-Length absent).
func (i *Installer) Download(ctx context.Context, onProgress httpdownload.ProgressFn) (string, error) {
	tmp := i.binaryPath + ".tmp"
	if err := os.MkdirAll(filepath.Dir(i.binaryPath), 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(i.binaryPath), err)
	}
	_ = os.Remove(tmp)

	dl := i.downloader
	if dl == nil {
		return "", fmt.Errorf("downloader is not configured")
	}

	free, freeOK := routerinfo.FreeBytes(filepath.Dir(i.binaryPath))
	maxBytes := downloadByteLimit(free, freeOK, binaryMaxBytes, diskReserveBytes)

	res, err := dl.DownloadFile(ctx, DownloadFileRequest{
		URL:          i.spec.URL,
		DestPath:     tmp,
		Timeout:      5 * time.Minute,
		MaxFileBytes: maxBytes,
		Mode:         0o644,
		Progress:     onProgress,
	})
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("download %s: %w", i.spec.URL, err)
	}
	got, err := sha256File(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("sha256 %s: %w", tmp, err)
	}
	if !strings.EqualFold(got, i.spec.SHA256) {
		_ = os.Remove(tmp)
		i.appLog.Warn("download", i.spec.URL, fmt.Sprintf("sha256 mismatch: got %s, want %s", got, i.spec.SHA256))
		return "", fmt.Errorf("sha256 mismatch (downloaded %d bytes): got %s, want %s", res.Size, got, i.spec.SHA256)
	}
	i.appLog.Info("download", i.spec.URL, fmt.Sprintf("downloaded %d bytes, sha256 verified", res.Size))
	return tmp, nil
}

// Activate atomically replaces the live binary with the verified tmp,
// sets executable bit, and removes the tmp on failure.
//
// IMPORTANT: tmpPath MUST live on the same filesystem as binaryPath so
// os.Rename can succeed atomically. Cross-device rename returns EXDEV
// and the cleanup path here would discard the verified download —
// callers placing tmp elsewhere should copy+unlink instead. Download
// satisfies this by placing tmp at "<binaryPath>.tmp".
func (i *Installer) Activate(tmpPath string) error {
	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, i.binaryPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, i.binaryPath, err)
	}
	i.appLog.Info("activate", i.binaryPath, "managed binary in place")
	return nil
}

// CurrentVersion runs `<binaryPath> version` and returns the parsed
// version string, or "" if the binary is missing/unexecutable.
func (i *Installer) CurrentVersion(ctx context.Context) string {
	if _, err := os.Stat(i.binaryPath); err != nil {
		return ""
	}
	// Entware/UPX builds on Keenetic can take several seconds to emit
	// the version banner — keep the headroom so first-call probes
	// still succeed on slow targets.
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, i.binaryPath, "version").Output()
	if err != nil {
		return ""
	}
	versionRe := regexp.MustCompile(`(?i)\bsing-?box\b\s+version\b\s+([^\s]+)`)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if m := versionRe.FindStringSubmatch(line); len(m) == 2 {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// FreeBytes wraps the freeDisk probe so callers (Operator.GetStatus) don't
// recompute install-dir. Returns (0, false) when probe unavailable.
func (i *Installer) FreeBytes() (int64, bool) {
	if i.freeDisk == nil {
		return 0, false
	}
	return i.freeDisk(filepath.Dir(i.binaryPath))
}

// SetFreeDiskFn overrides the free-disk probe (tests only).
func (i *Installer) SetFreeDiskFn(fn func(string) (int64, bool)) {
	i.freeDisk = fn
}

// isExecutable reports whether path is a regular executable file.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// safetyMargin is added on top of spec.Size for the disk-gate, covering
// tmp download file, logs, and slack. ~5 MiB.
const safetyMargin = 5 << 20

// SafetyMargin is the disk-gate slack added to spec.Size (bytes).
const SafetyMargin = safetyMargin

// EvaluateInstallState reports the install state vs pinned spec, gated by
// free disk. Pure file ops: isExecutable + cached binarySHA256 (no
// `sing-box version` subprocess), so safe to call on every status-poll.
//
// Returns Installed/Missing/MissingNoSpace/OutdatedNoSpace. Free-disk unknown
// OR spec.Size==0 → gate skipped (Missing instead of NoSpace).
//
// Note: возвращает Missing и для clean install, и для outdated-with-space —
// EvaluateInstallState отвечает только на «можно ли проходить gate»;
// различение install vs update делается через UpdateAvailable в GetStatus.
func (i *Installer) EvaluateInstallState() InstallState {
	installed := isExecutable(i.binaryPath)

	matches := false
	if installed {
		sha, err := i.binarySHA256()
		matches = err == nil && strings.EqualFold(sha, i.spec.SHA256)
	}
	if matches {
		return InstallStateInstalled
	}

	required := i.spec.Size + safetyMargin
	freeBytes, ok := int64(0), false
	if i.freeDisk != nil {
		freeBytes, ok = i.freeDisk(filepath.Dir(i.binaryPath))
	}
	gate := ok && i.spec.Size > 0
	avail := freeBytes - diskReserveBytes

	if !installed {
		if gate && avail < required {
			return InstallStateMissingNoSpace
		}
		return InstallStateMissing
	}
	// installed && !matches → upgrade
	if gate && avail < required {
		return InstallStateOutdatedNoSpace
	}
	return InstallStateMissing
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
