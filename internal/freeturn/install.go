package freeturn

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/childproc"
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
const PinnedVersion = "2.0.1-1"

// releaseBase — прод-доставка с зеркала (паритет с
// internal/singbox/installer/embedded.go — GitHub из RU у части пользователей
// недоступен). Канонический источник сборки:
// https://github.com/hoaxisr/free-turn-proxy релиз v<PinnedVersion>.
const releaseBase = "http://repo.hoaxisr.ru/ft/" + PinnedVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch (detectArch(): e.g.
// "mipsel-3.4") to pinned freeturn assets. SHA256/Size — из checksums.txt
// релиза hoaxisr/free-turn-proxy v<PinnedVersion>.
var EmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-client-linux-arm64", SHA256: "41a3ed5e5983702b68dbfe2735c01491af2fa4bc02151ba8dc8fae818208d4e4", Size: 14811298},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-server-linux-arm64", SHA256: "f2c87f556fc392aafdd95131ece322093a2f6cd5b39c01e1b61c56faf1e59891", Size: 6160546},
	},
	"mipsel-3.4": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-client-linux-mipsle-softfloat", SHA256: "dbe3e9fcc6525c75db6853383e0b5719af4b19613367e92ddf7ff3e26db0f1de", Size: 16711873},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-server-linux-mipsle-softfloat", SHA256: "f89de49f9b2da2ead27cf7e0a5076de8d286f13d827ad90ebff510a362c18a43", Size: 7012545},
	},
	"mips-3.4": {
		Client: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-client-linux-mips-softfloat", SHA256: "d1a568a2db0385b3b52be30563d677c3089483b45fb6883d640b3f0c8d56952b", Size: 16711873},
		Server: BinarySpec{Version: PinnedVersion, URL: releaseBase + "ft-server-linux-mips-softfloat", SHA256: "040e6ce0640a1dea8afb1dd9e40b326ed22a199d0bc94ed7202f551acf663361", Size: 7012545},
	},
}

// SetInstallSpecs wires the pinned specs for this router's arch. Not called
// (nil specs) = install unavailable, UI keeps the manual-install hint.
func (s *Service) SetInstallSpecs(specs ArchSpecs) {
	s.installSpecs = &specs
}

// SetDownloader wires the shared download service adapter.
func (s *Service) SetDownloader(dl childproc.Downloader) {
	s.downloader = dl
}

// resolveInstallSpecs returns the build-pinned specs and their version.
// Установка всегда ставит закреплённую в сборку версию; отдельного
// удалённого канала обновлений нет — новый freeturn приходит с новой
// сборкой awg-manager (см. EmbeddedBinaries / PinnedVersion).
func (s *Service) resolveInstallSpecs() (ArchSpecs, string) {
	if s.installSpecs == nil {
		return ArchSpecs{}, ""
	}
	pinned := *s.installSpecs
	return pinned, pinned.Client.Version
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
	err := childproc.Install(ctx, s.downloader, binPath, spec.URL, spec.SHA256, spec.Size)
	var ce *childproc.ChecksumError
	if errors.As(err, &ce) {
		s.appLog.Warn("install", spec.URL, fmt.Sprintf("sha256 mismatch: got %s, want %s", ce.Got, ce.Want))
	}
	return err
}
