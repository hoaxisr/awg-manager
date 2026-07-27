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

const PinnedClientVersion = "1.0.0-1"
const PinnedServerVersion = "0.1.5-awgm"

// releaseBase — прод-доставка клиента с зеркала (паритет с freeturn).
const releaseBase = "http://repo.hoaxisr.ru/wt/" + PinnedClientVersion + "/"

// serverReleaseBase — patched wdtt-server (-no-nat) for Keenetic/awg-manager.
// Fallback: бинарь из IPK (/opt/bin/wdtt-server) или prebuilt/wdtt в репозитории.
const serverReleaseBase = "http://repo.hoaxisr.ru/wt/server/" + PinnedServerVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch to pinned wdtt assets.
var EmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-arm64",
			SHA256: "47e72c6491d61cb0ba30aa522b444ca30e2851f9d77361a2520cb92601e6972b", Size: 11337890,
		},
		Server: BinarySpec{
			Version: PinnedServerVersion, URL: serverReleaseBase + "wdtt-server-linux-arm64",
			SHA256: "ac651abdd0475cb384aebedf77a19883e99fc3f117226adb6e6b02df4185236", Size: 12320930,
		},
	},
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "7cd2c6b0dfee1bbfb64dba415d8c20318a05dccb8babc59d18d77d843c7163f7", Size: 13172929,
		},
		Server: BinarySpec{
			Version: PinnedServerVersion, URL: serverReleaseBase + "wdtt-server-entware-mipsel-softfloat",
			SHA256: "04cc241a27ecc8de1aa59a0034541c22b0c3f22bc7ad37ac17cf46521b24b1ef", Size: 8913053,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "f62be339ae86ead7f97439fbe919fd17d0321bfd7265fb8ca2ad484709c5392d", Size: 13172929,
		},
		Server: BinarySpec{
			Version: PinnedServerVersion, URL: serverReleaseBase + "wdtt-server-entware-mips-softfloat",
			SHA256: "6f55ddfa11aac98b8f17af7908e9440a5e76c3524a9ed0a16ab1d166a80ef951", Size: 8913053,
		},
	},
}

type installedVersionRecord struct {
	Version     string    `json:"version"`
	ClientVer   string    `json:"clientVersion,omitempty"`
	ServerVer   string    `json:"serverVersion,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	ClientPath  string    `json:"clientPath"`
	ServerPath  string    `json:"serverPath,omitempty"`
}

func installVersionLabel(specs ArchSpecs) string {
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
	if !binaryPresent(s.clientBin) || !binaryPresent(s.serverBin) {
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
