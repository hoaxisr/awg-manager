package wdtt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

type BinarySpec struct {
	Version string
	URL     string
	SHA256  string
	Size    int64 // bytes; download hard-cap = Size + slack
}

const PinnedVersion = "1.0.0-1"

// releaseBase — прод-доставка с зеркала (паритет с
// internal/freeturn/install.go и internal/singbox/installer/embedded.go —
// GitHub из RU у части пользователей недоступен, репо приватный).
// Канонический источник сборки: https://github.com/hoaxisr/wdtt-client
// релиз v1.0.0-1.
const releaseBase = "http://repo.hoaxisr.ru/wt/" + PinnedVersion + "/"

// EmbeddedBinaries maps the awg-manager build arch (detectArch(): e.g.
// "mipsel-3.4") to the pinned wdtt client asset. У wdtt только client
// (server-бинаря нет). SHA256/Size — из checksums.txt релиза
// hoaxisr/wdtt-client v1.0.0-1.
var EmbeddedBinaries = map[string]BinarySpec{
	"aarch64-3.10": {
		Version: PinnedVersion, URL: releaseBase + "wt-client-linux-arm64",
		SHA256: "47e72c6491d61cb0ba30aa522b444ca30e2851f9d77361a2520cb92601e6972b", Size: 11337890,
	},
	"mipsel-3.4": {
		Version: PinnedVersion, URL: releaseBase + "wt-client-linux-mipsle-softfloat",
		SHA256: "7cd2c6b0dfee1bbfb64dba415d8c20318a05dccb8babc59d18d77d843c7163f7", Size: 13172929,
	},
	"mips-3.4": {
		Version: PinnedVersion, URL: releaseBase + "wt-client-linux-mips-softfloat",
		SHA256: "f62be339ae86ead7f97439fbe919fd17d0321bfd7265fb8ca2ad484709c5392d", Size: 13172929,
	},
}

const downloadSlack = 1 << 20

type Downloader interface {
	DownloadFile(ctx context.Context, url, destPath string, maxBytes int64) error
}

type installedVersionRecord struct {
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installedAt"`
	ClientPath  string    `json:"clientPath"`
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
	return rec.Version
}

func (s *Service) writeInstalledVersion(version string) error {
	if s.versionPath == "" {
		return nil
	}
	rec := installedVersionRecord{
		Version:     version,
		InstalledAt: time.Now().UTC(),
		ClientPath:  s.clientBin,
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
	if installedVersion == "" {
		return installedVersion, true
	}
	if semver.Compare(installedVersion, installVersion) < 0 {
		return installedVersion, true
	}
	return installedVersion, false
}

func (s *Service) installOne(ctx context.Context, binPath string, spec BinarySpec) error {
	if binPath == "" {
		return fmt.Errorf("путь бинаря не задан")
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
		return fmt.Errorf("контрольная сумма не совпала (получено %s, ожидалось %s)", got, spec.SHA256)
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
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
