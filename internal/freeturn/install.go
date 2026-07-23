package freeturn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BinarySpec is one binary's pinned download metadata. Version+SHA256 are
// baked into this build of awg-manager (same trust model as the sing-box
// installer): a compromised download source cannot serve a tampered binary
// that awg-manager would still install.
type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
	Size    int64 // bytes; download hard-cap = Size + slack
}

// ArchSpecs pairs the client+server binaries for one router architecture.
type ArchSpecs struct {
	Client BinarySpec
	Server BinarySpec
}

// PinnedVersion is the free-turn-proxy release this build installs.
// Bump procedure: update the constant, URLs, SHA256 (from the release's
// checksums.txt) and sizes below.
const PinnedVersion = "1.8.0-1"

// releaseBase — прод-доставка с зеркала (паритет с
// internal/singbox/installer/embedded.go — GitHub из RU у части пользователей
// недоступен). Канонический источник сборки:
// https://github.com/hoaxisr/free-turn-proxy релиз v1.8.0-1.
const releaseBase = "http://repo.hoaxisr.ru/ft/" + PinnedVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch (detectArch(): e.g.
// "mipsel-3.4") to pinned freeturn assets. SHA256/Size — из checksums.txt
// релиза hoaxisr/free-turn-proxy v1.8.0-1.
var EmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-client-linux-arm64", SHA256: "f0666e574932027882822c5a986808f2029c0de8a9989937b27fcda5dbbf0aeb", Size: 14680226},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-server-linux-arm64", SHA256: "4831fc971df03b78c21570f4b36a080e9fb2f1fb15bdc1f254aad19a8164747b", Size: 6160546},
	},
	"mipsel-3.4": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-client-linux-mipsle-softfloat", SHA256: "714ba97fbdcdc3567e19a888c54ce1a793de1d741f2b6a65bcfd9640aff48caa", Size: 16646337},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-server-linux-mipsle-softfloat", SHA256: "8d4ff24449b309a25dc220f206ddfb15ca21fa35eb13f4483c62b2e929d090c3", Size: 7012545},
	},
	"mips-3.4": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-client-linux-mips-softfloat", SHA256: "9c200eabc98e73740257b5ad448394777ba4f03e3c60e9e2f8de59f4d818005d", Size: 16646337},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-server-linux-mips-softfloat", SHA256: "c0cf856e829da8e9a095b5635d08ea2dd6d9d937acbc82f1e88d30bce8c1d05a", Size: 7012545},
	},
}

// downloadSlack tops up MaxFileBytes over the expected size so a legitimate
// asset a few bytes larger than pinned doesn't fail mid-transfer (it would
// still fail SHA256 afterwards — the slack only moves WHERE it fails).
const downloadSlack = 1 << 20

// Downloader is the narrow download contract; the adapter in cmd/awg-manager
// bridges it to the shared downloader.Service (timeouts, redirects, limits).
type Downloader interface {
	// DownloadFile fetches url into destPath (mode 0644, non-atomic —
	// caller activates via chmod+rename). maxBytes hard-caps the transfer.
	DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error
}

// SetInstallSpecs wires the pinned specs for this router's arch. Not called
// (nil specs) = install unavailable, UI keeps the manual-install hint.
func (s *Service) SetInstallSpecs(specs ArchSpecs) {
	s.installSpecs = &specs
}

// SetDownloader wires the shared download service adapter.
func (s *Service) SetDownloader(dl Downloader) {
	s.downloader = dl
}

// InstallInfo reports whether one-click install is available and which
// version it would install (always the build pin).
func (s *Service) InstallInfo() (version string, available bool) {
	if s.installSpecs == nil || s.downloader == nil {
		return "", false
	}
	_, ver := s.resolveInstallSpecs()
	return ver, true
}

// Installing reports whether an install is currently in flight (for status).
func (s *Service) Installing() bool {
	s.installMu.Lock()
	defer s.installMu.Unlock()
	return s.installing
}

// InstallBinaries downloads, verifies and activates BOTH freeturn binaries
// (client + server) at their configured paths. Verification is against the
// build-pinned SHA256; on any failure nothing is activated for that binary
// (tmp removed), but a binary already activated earlier in the call stays —
// each is independent. Serialized: a second concurrent call errors out.
func (s *Service) InstallBinaries(ctx context.Context) error {
	if s.installSpecs == nil || s.downloader == nil {
		return fmt.Errorf("установка недоступна: для этой архитектуры нет закреплённой сборки freeturn")
	}
	s.installMu.Lock()
	if s.installing {
		s.installMu.Unlock()
		return fmt.Errorf("установка уже выполняется")
	}
	s.installing = true
	s.installMu.Unlock()
	defer func() {
		s.installMu.Lock()
		s.installing = false
		s.installMu.Unlock()
	}()

	specs, installVer := s.resolveInstallSpecs()

	if err := s.installOne(ctx, s.clientBin, specs.Client); err != nil {
		return fmt.Errorf("клиент: %w", err)
	}
	if err := s.installOne(ctx, s.serverBin, specs.Server); err != nil {
		return fmt.Errorf("сервер: %w", err)
	}
	if err := s.writeInstalledVersion(installVer); err != nil {
		s.appLog.Warn("install", "version-file", err.Error())
	}
	s.appLog.Info("install", installVer,
		fmt.Sprintf("freeturn v%s установлен: %s, %s", installVer, s.clientBin, s.serverBin))
	return nil
}

func (s *Service) installOne(ctx context.Context, binPath string, spec BinarySpec) error {
	if binPath == "" {
		return fmt.Errorf("путь бинаря не сконфигурирован")
	}
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return err
	}
	tmp := binPath + ".tmp"
	_ = os.Remove(tmp)
	if err := s.downloader.DownloadFile(ctx, spec.URL, tmp, spec.Size+downloadSlack); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("загрузка %s: %w", spec.URL, err)
	}
	got, err := sha256File(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if !strings.EqualFold(got, spec.SHA256) {
		_ = os.Remove(tmp)
		s.appLog.Warn("install", spec.URL, fmt.Sprintf("sha256 mismatch: got %s, want %s", got, spec.SHA256))
		return fmt.Errorf("контрольная сумма не совпала (получено %s, ожидалось %s)", got, spec.SHA256)
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Atomic activation: same directory → same filesystem, rename не рвётся.
	if err := os.Rename(tmp, binPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
