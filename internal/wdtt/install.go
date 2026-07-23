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
	Version    string
	URL        string
	SHA256     string
	Size       int64
	LocalAsset string
}

const PinnedVersion = "1.0.0-awg.1"

var EmbeddedBinaries = map[string]BinarySpec{
	"aarch64-3.10": {
		Version: PinnedVersion, LocalAsset: "client-linux-arm64",
		SHA256: "ea50ef14313f9f28aac40912d0387e2d9baec0cdb6a28581489befc74fd79d8a", Size: 11337890,
	},
	"mipsel-3.4": {
		Version: PinnedVersion, LocalAsset: "client-linux-mipsle-softfloat",
		SHA256: "", Size: 0,
	},
	"mips-3.4": {
		Version: PinnedVersion, LocalAsset: "client-linux-mips-softfloat",
		SHA256: "", Size: 0,
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
	if spec.LocalAsset != "" {
		if src, ok := findLocalWdttAsset(spec.LocalAsset); ok {
			return copyWdttBinary(src, binPath)
		}
	}
	if spec.URL == "" {
		return fmt.Errorf("локальный prebuilt не найден и URL загрузки не задан")
	}
	tmp := binPath + ".tmp"
	_ = os.Remove(tmp)
	maxBytes := spec.Size + downloadSlack
	if maxBytes <= downloadSlack {
		maxBytes = 32 << 20
	}
	if err := s.downloader.DownloadFile(ctx, spec.URL, tmp, maxBytes); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("загрузка %s: %w", spec.URL, err)
	}
	if spec.SHA256 != "" {
		got, err := sha256File(tmp)
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if !strings.EqualFold(got, spec.SHA256) {
			_ = os.Remove(tmp)
			return fmt.Errorf("контрольная сумма не совпала")
		}
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

func findLocalWdttAsset(name string) (string, bool) {
	candidates := []string{
		filepath.Join("prebuilt", "wdtt", name),
		filepath.Join("/opt/etc/awg-manager/wdtt", name),
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "wdtt", name))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

func copyWdttBinary(src, dest string) error {
	tmp := dest + ".tmp"
	_ = os.Remove(tmp)
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
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
