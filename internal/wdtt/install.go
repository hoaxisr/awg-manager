package wdtt

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

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

const PinnedClientVersion = "1.4.4-awgm"
const PinnedServerVersion = "1.4.4-awgm"

// releaseBase — прод-доставка клиента с зеркала (паритет с freeturn).
// После scripts/build-wdtt-client.sh обновить SHA256/Size ниже и залить на repo.hoaxisr.ru.
const releaseBase = "http://repo.hoaxisr.ru/wt/" + PinnedClientVersion + "/"

// serverReleaseBase — patched wdtt-server (-no-nat, -wg-iface) for Keenetic/awg-manager.
// После scripts/build-wdtt-server.sh обновить SHA256/Size и залить бинарь на зеркало.
const serverReleaseBase = "http://repo.hoaxisr.ru/wt/server/" + PinnedServerVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch to pinned wdtt assets.
var EmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-arm64",
			SHA256: "6f82bfd0b5851b1c61398d80ea4665575ba570c5dd641997194b25aef17f6e83", Size: 15401122,
		},
		Server: BinarySpec{
			Version: PinnedServerVersion, URL: serverReleaseBase + "wdtt-server-linux-arm64",
			SHA256: "b639505b9952485bc16e9e3d43d6503975a878b0b18aba3fa5269953b61fd000", Size: 8126626,
		},
	},
	// mipsel/mips — только клиент: апстримовый pkg/paneldb тянет
	// modernc.org/sqlite → modernc.org/libc, где нет этих архитектур,
	// поэтому wdtt-server под них не собирается (contrib/wdtt-server-patch/BUILD.md).
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "0af429515d65f7f844c3d24f0ec052c6b27cb65f6a9ef70e6ebdeb9f39782b7d", Size: 17563841,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "8b4d2d838c696f91b771507c2992ba62d9b1f2993fad32ef396d5b53b976906e", Size: 17563841,
		},
	},
}

// serverSupported — есть ли для этой арки собираемый wdtt-server.
func (s *ArchSpecs) serverSupported() bool { return s != nil && s.Server.URL != "" }

type installedVersionRecord struct {
	Version     string    `json:"version"`
	ClientVer   string    `json:"clientVersion,omitempty"`
	ServerVer   string    `json:"serverVersion,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	ClientPath  string    `json:"clientPath"`
	ServerPath  string    `json:"serverPath,omitempty"`
}

func installVersionLabel(specs ArchSpecs) string {
	if !specs.serverSupported() {
		return specs.Client.Version
	}
	return specs.Client.Version + "+server-" + specs.Server.Version
}

func (s *Service) readInstalledVersion() string {
	if s.versionPath == "" {
		return ""
	}
	data, err := os.ReadFile(s.versionPath)
	if err != nil {
		return ""
	}
	var rec installedVersionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ""
	}
	if rec.Version != "" {
		return rec.Version
	}
	if rec.ClientVer != "" && rec.ServerVer != "" {
		return rec.ClientVer + "+server-" + rec.ServerVer
	}
	return rec.ClientVer
}

func (s *Service) writeInstalledVersion(version string) error {
	if s.versionPath == "" {
		return nil
	}
	rec := installedVersionRecord{
		Version:     version,
		ClientVer:   PinnedClientVersion,
		ServerVer:   PinnedServerVersion,
		InstalledAt: time.Now().UTC(),
		ClientPath:  s.clientBin,
		ServerPath:  s.serverBin,
	}
	if s.installSpecs != nil {
		rec.ClientVer = s.installSpecs.Client.Version
		rec.ServerVer = s.installSpecs.Server.Version
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.versionPath), 0755); err != nil {
		return err
	}
	tmp := s.versionPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.versionPath)
}

func (s *Service) installStatusFields(installVersion string) (installedVersion string, updateAvailable bool) {
	if installVersion == "" {
		return s.readInstalledVersion(), false
	}
	installedVersion = s.readInstalledVersion()
	if !binaryPresent(s.clientBin) {
		return installedVersion, true
	}
	if s.installSpecs.serverSupported() && !binaryPresent(s.serverBin) {
		return installedVersion, true
	}
	if installedVersion == "" {
		return installedVersion, true
	}
	if installedVersion != installVersion {
		return installedVersion, true
	}
	return installedVersion, false
}

func (s *Service) installOne(ctx context.Context, binPath string, spec BinarySpec) error {
	if binPath == "" {
		return fmt.Errorf("путь бинаря не задан")
	}
	return childproc.Install(ctx, s.downloader, binPath, spec.URL, spec.SHA256, spec.Size)
}
