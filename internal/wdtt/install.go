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

const PinnedClientVersion = "1.4.5-awgm"
const PinnedServerVersion = "1.4.5-awgm"

// releaseBase — прод-доставка клиента с зеркала (паритет с freeturn).
const releaseBase = "http://repo.hoaxisr.ru/wt/" + PinnedClientVersion + "/"

// serverReleaseBase — прод-доставка сервера с зеркала.
const serverReleaseBase = "http://repo.hoaxisr.ru/wt/server/" + PinnedServerVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch to pinned wdtt assets.
var EmbeddedBinaries = map[string]ArchSpecs{
	"aarch64-3.10": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-arm64",
			SHA256: "a95d93dea1b9ed9dca20c40ca428f8a3c1eca75d1c4b2ea3c12a072009942e38", Size: 15401122,
		},
		Server: BinarySpec{
			Version: PinnedServerVersion, URL: serverReleaseBase + "wdtt-server-linux-arm64",
			SHA256: "8fbd9391a350a1ac95cb1f1cc8de556f9c97d1d08d74eaf6fafffed4c398cea9", Size: 8126626,
		},
	},
	// mipsel/mips — только клиент: апстримовый pkg/paneldb тянет
	// modernc.org/sqlite → modernc.org/libc, где нет этих архитектур,
	// поэтому wdtt-server под них не собирается (contrib/wdtt-server-patch/BUILD.md).
	"mipsel-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mipsle-softfloat",
			SHA256: "2b92ae0b636d4b4f7239bccb1da295bae5b7ae37f66ac4162c35e493253b0376", Size: 17563841,
		},
	},
	"mips-3.4": {
		Client: BinarySpec{
			Version: PinnedClientVersion, URL: releaseBase + "wt-client-linux-mips-softfloat",
			SHA256: "924ad71f363aec4472ea683c6282d67c1f38d070ac853a75fad6657f3d84d421", Size: 17563841,
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
	// Бинарь на диске не тот, что в пине (протухший бандл, ручная подмена):
	// версия из wdtt-version.json ему не принадлежит, поэтому не выдаём её за
	// установленную и зовём поставить пин.
	if !s.binariesMatchSpecs() {
		return "", true
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
