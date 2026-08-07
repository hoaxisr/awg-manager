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

const PinnedClientVersion = "1.4.2-awgm"
const PinnedServerVersion = "1.4.2-awgm"

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
			SHA256: "f5171ad5fda9a13fb19bf1b5c2b452e418a082be78d43edcbf4408db18f690a0", Size: 15335586,
		},
		Server: BinarySpec{
			Version: PinnedServerVersion, URL: serverReleaseBase + "wdtt-server-linux-arm64",
			SHA256: "ba150d7d28d69359ae4793f90a40c2883403db0570d1ba9bfe1c528cc2642e9c", Size: 8126626,
		},
	},
	// mipsel/mips — только клиент: апстримовый pkg/paneldb тянет
	// modernc.org/sqlite → modernc.org/libc, где нет этих архитектур,
	// поэтому wdtt-server под них не собирается (contrib/wdtt-server-patch/BUILD.md).
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "844eba00062025eee77dfe24d5240f1a679a7341eea45861a7147504706f23fc", Size: 17563841,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "0819ddf643ed681aac1eb649d34002fba0469802f73b4739b29d824d8ff2fbd1", Size: 17563841,
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
