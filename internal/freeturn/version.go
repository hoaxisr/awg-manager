package freeturn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

// installedVersionRecord is persisted after a successful one-click install.
type installedVersionRecord struct {
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installedAt"`
	ClientPath  string    `json:"clientPath"`
	ServerPath  string    `json:"serverPath"`
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
		ServerPath:  s.serverBin,
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
	clientOK := binaryPresent(s.clientBin)
	serverOK := binaryPresent(s.serverBin)
	if !clientOK || !serverOK {
		return installedVersion, true
	}
	if installedVersion == "" {
		return installedVersion, true
	}
	if compareFreeturnVersion(installedVersion, installVersion) < 0 {
		return installedVersion, true
	}
	return installedVersion, false
}

// compareFreeturnVersion сравнивает версии вида "1.8.0-N", где N — номер
// пересборки awg-manager. semver трактует "-N" как pre-release-суффикс и
// отбрасывает его (Compare("1.8.0-2","1.8.0-3")==0), поэтому при равных
// semver-базах решает целое после последнего "-". Возвращает -1/0/1.
func compareFreeturnVersion(a, b string) int {
	if c := semver.Compare(a, b); c != 0 {
		return c
	}
	ra, rb := revisionSuffix(a), revisionSuffix(b)
	switch {
	case ra < rb:
		return -1
	case ra > rb:
		return 1
	default:
		return 0
	}
}

// revisionSuffix возвращает целое после последнего "-" ("1.8.0-3" → 3),
// или 0 если суффикса нет либо он не числовой.
func revisionSuffix(v string) int {
	i := strings.LastIndexByte(v, '-')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v[i+1:]))
	if err != nil {
		return 0
	}
	return n
}
