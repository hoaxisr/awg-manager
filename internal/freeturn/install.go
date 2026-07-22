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
const PinnedVersion = "1.8.0"

const releaseBase = "https://github.com/samosvalishe/free-turn-proxy/releases/download/v" + PinnedVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch (detectArch(): e.g.
// "mipsel-3.4") to the upstream release assets. SHA256 from the v1.8.0
// checksums.txt.
var EmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "client-linux-arm64", SHA256: "69c991b1fd0d8110d47c98bf597956eb6be4dd4330c9c1581490beec94686306", Size: 14680226},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "server-linux-arm64", SHA256: "2c1189b41a3aa1c33048ec5daf823be529a52513a3ae347b84ed2445528d79cf", Size: 6160546},
	},
	"mipsel-3.4": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "client-linux-mipsle-softfloat", SHA256: "2b4011b0d40fb7e99025ce509c8048b35a0dbb684c482f654881059998d1d05c", Size: 16580801},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "server-linux-mipsle-softfloat", SHA256: "b323bff9fe3297de5998f8802f9cc551036cc3d4adabc685d3779f4c0a744014", Size: 7012545},
	},
	"mips-3.4": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "client-linux-mips-softfloat", SHA256: "c757d3dcec4bfa4eed3cd0b1ab6d443c2a48746758fb48c069587d7cf41214df", Size: 16580801},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "server-linux-mips-softfloat", SHA256: "d1c4cd4ea4477eb7f013cfe9f1017b2718b9e95d0b594a81a4fd1e10b39af195", Size: 7012545},
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
