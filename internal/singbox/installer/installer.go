// Package installer manages the sing-box binary lifecycle outside the
// Entware opkg pipeline. Downloads come from our entware-repo server
// over HTTP (integrity guarded by SHA256 baked into this build of
// awg-manager), and land at an absolute path in our config directory
// so they cannot be confused with a user-installed sing-box on PATH.
package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
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
	isUBIFS  func(path string) bool          // overridable for tests; defaults to routerinfo.IsUBIFS

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

// ErrNoSpace — бинарь не помещается на диск: отказ гейта или ENOSPC при
// записи (сжатие ubifs оказалось хуже ubifsCompressFactor).
var ErrNoSpace = errors.New("недостаточно места на диске")

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

// ubifsCompressFactor — во сколько раз ubifs (внутренняя память Keenetic)
// сжимает бинарь sing-box. Замер владельца — 2.52× (#964); 2 взято с запасом
// на разброс между сборками. statfs на ubifs отдаёт свободное место без учёта
// сжатия, поэтому гейт и потолок скачивания пересчитывают размер бинаря.
const ubifsCompressFactor = 2

// downloadByteLimit returns the max download size: free disk space minus a
// reserve margin, times ratio (network bytes per on-disk byte: 1, or
// ubifsCompressFactor on ubifs), or the static fallback when free space is
// unknown. Clamped to be non-negative.
func downloadByteLimit(freeBytes int64, freeOK bool, fallback, reserve, ratio int64) int64 {
	if !freeOK {
		return fallback
	}
	avail := freeBytes - reserve
	if avail < 0 {
		avail = 0
	}
	return avail * ratio
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
		isUBIFS:    routerinfo.IsUBIFS,
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

// MatchesPinnedBytes reports whether the bytes on disk are the pinned
// build: SHA256 equal, or a UPX-packed copy of the pinned version (#868).
// The SHA256 check is intentional: custom sing-box rebuilds can keep the
// same upstream version while fixing target-specific binary contents.
// Users on small flash compress the binary themselves; its SHA can never
// match, and offering an update they have no room for is noise.
// currentVersion is the version resolved by the operator (sidecar, Clash
// API or probe); pass "" when unknown — then only the SHA counts.
func (i *Installer) MatchesPinnedBytes(currentVersion string) bool {
	sha, err := i.binarySHA256()
	if err == nil && strings.EqualFold(sha, i.spec.SHA256) {
		return true
	}
	return currentVersion != "" && currentVersion == i.spec.Version && isUPXPacked(i.binaryPath)
}

// isUPXPacked reports whether path carries the UPX loader magic. UPX puts
// its l_info record right after the ELF program headers, so the first
// page is enough — no need to scan the whole ~80MB file. Deliberately a
// plain substring match: it only relaxes the SHA check for a binary that
// already reports the pinned version, so a false positive costs nothing.
func isUPXPacked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := io.ReadFull(f, buf)
	return bytes.Contains(buf[:n], []byte("UPX!"))
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

	free, freeOK := i.FreeBytes()
	maxBytes := downloadByteLimit(free, freeOK, binaryMaxBytes, diskReserveBytes, i.compressRatio())

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
		if errors.Is(err, syscall.ENOSPC) {
			return "", fmt.Errorf("download %s: %w: %w", i.spec.URL, ErrNoSpace, err)
		}
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
	if err := storage.CommitFile(tmpPath, i.binaryPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename %s -> %s: %w", tmpPath, i.binaryPath, err)
	}
	i.appLog.Info("activate", i.binaryPath, "managed binary in place")
	return nil
}

// HasManagedBinary — управляемый бинарь лежит на месте. Вопрос «мигрировать
// ли» решается по файлу, а не по ответу `<бинарь> version`: пустой ответ (UPX
// не распаковался при нехватке RAM, таймаут) — не отсутствие бинаря, и
// миграция поверх живого бинаря снесла бы рабочую установку (F134).
func (i *Installer) HasManagedBinary() bool { return isExecutable(i.binaryPath) }

// FreeBytes wraps the freeDisk probe so callers (Operator.GetStatus) don't
// recompute install-dir. Returns (0, false) when probe unavailable.
func (i *Installer) FreeBytes() (int64, bool) {
	if i.freeDisk == nil {
		return 0, false
	}
	return i.freeDisk(i.probeDir())
}

// DiscardStaleTmp удаляет <binary>.tmp, брошенный оборванным скачиванием
// (демон убит, питание): он съедает место, и гейт отказывал бы навсегда —
// сам Download чистит tmp только после гейта. Звать под installBusy.
func (i *Installer) DiscardStaleTmp() {
	_ = os.Remove(i.binaryPath + ".tmp")
}

// probeDir — ближайший существующий каталог на пути к бинарю. Uninstall
// сносит каталог sing-box целиком, и statfs по нему вернул бы ENOENT —
// гейт места молча отключился бы до следующей установки.
func (i *Installer) probeDir() string {
	dir := filepath.Dir(i.binaryPath)
	for {
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
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

// compressRatio — сколько байт бинаря из сети ложится в один байт на диске
// каталога установки.
func (i *Installer) compressRatio() int64 {
	if i.isUBIFS != nil && i.isUBIFS(i.probeDir()) {
		return ubifsCompressFactor
	}
	return 1
}

// RequiredDiskBytes — сколько свободного места (как его видит statfs) нужно
// гейту установки: бинарь на диске + safetyMargin + diskReserveBytes.
// Ровно то число, с которым сравнивает EvaluateInstallState.
func (i *Installer) RequiredDiskBytes() int64 {
	return i.spec.Size/i.compressRatio() + safetyMargin + diskReserveBytes
}

// EvaluateInstallState reports the install state vs pinned spec, gated by
// free disk. Pure file ops: isExecutable + cached binarySHA256 (no
// `sing-box version` subprocess), so safe to call on every status-poll.
// currentVersion — see MatchesPinnedBytes; "" when the caller only needs
// the MissingNoSpace answer.
//
// Returns Installed/Missing/MissingNoSpace/OutdatedNoSpace. Free-disk unknown
// OR spec.Size==0 → gate skipped (Missing instead of NoSpace).
//
// Note: возвращает Missing и для clean install, и для outdated-with-space —
// EvaluateInstallState отвечает только на «можно ли проходить gate»;
// различение install vs update делается через UpdateAvailable в GetStatus.
func (i *Installer) EvaluateInstallState(currentVersion string) InstallState {
	installed := isExecutable(i.binaryPath)
	if installed && i.MatchesPinnedBytes(currentVersion) {
		return InstallStateInstalled
	}

	freeBytes, ok := i.FreeBytes()
	noSpace := ok && i.spec.Size > 0 && freeBytes < i.RequiredDiskBytes()

	if !installed {
		if noSpace {
			return InstallStateMissingNoSpace
		}
		return InstallStateMissing
	}
	// installed && !matches → upgrade
	if noSpace {
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
