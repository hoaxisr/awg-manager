package freeturn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
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
	installedVersion = s.effectiveInstalledVersion()
	if !s.binariesOperational() {
		if installVersion == "" {
			return installedVersion, false
		}
		return installedVersion, true
	}
	if installVersion == "" {
		return installedVersion, false
	}
	if installedVersion == "" {
		return installVersion, false
	}
	if compareFreeturnVersion(installedVersion, installVersion) < 0 {
		return installedVersion, true
	}
	return installedVersion, false
}

func (s *Service) binariesOperational() bool {
	ensureExecutable(s.clientBin)
	ensureExecutable(s.serverBin)
	if binaryPresent(s.clientBin) && binaryPresent(s.serverBin) {
		return true
	}
	return s.installSpecs != nil && s.binariesMatchSpecs(*s.installSpecs)
}

// effectiveInstalledVersion prefers the build pin when on-disk binaries match
// its SHA256 — the version file can be stale after an IPK upgrade.
func (s *Service) effectiveInstalledVersion() string {
	if s.installSpecs != nil && s.binariesMatchSpecs(*s.installSpecs) {
		return s.installSpecs.Client.Version
	}
	return s.readInstalledVersion()
}

// binariesMatchSpecs сверяет SHA256 обоих бинарей с пином сборки.
//
// Результат кешируется по mtime+size файлов: сверка читает ~21 МБ, а
// вызывается она из installStatusFields — дважды на каждый статус, который
// UI опрашивает раз в 2 секунды. На mipsel это заметный CPU и постоянное
// чтение флеша. Ключ меняется при любой перезаписи бинаря, так что
// установка новой версии инвалидирует кеш сама.
func (s *Service) binariesMatchSpecs(specs ArchSpecs) bool {
	if specs.Client.SHA256 == "" || specs.Server.SHA256 == "" {
		return false
	}
	key := specs.Client.SHA256 + "|" + specs.Server.SHA256 + "|" +
		fileFingerprint(s.clientBin) + "|" + fileFingerprint(s.serverBin)

	s.matchMu.Lock()
	defer s.matchMu.Unlock()
	if key == s.matchKey {
		return s.matchVal
	}

	match := false
	clientHash, cErr := sha256File(s.clientBin)
	serverHash, sErr := sha256File(s.serverBin)
	if cErr == nil && sErr == nil {
		match = strings.EqualFold(clientHash, specs.Client.SHA256) &&
			strings.EqualFold(serverHash, specs.Server.SHA256)
	}
	s.matchKey, s.matchVal = key, match
	return match
}

// fileFingerprint — дешёвая замена содержимому файла: mtime+size.
// Пустая строка для отсутствующего файла (такой ключ тоже валиден:
// «оба файла отсутствуют» — стабильное состояние).
func fileFingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10) + "_" + strconv.FormatInt(fi.Size(), 10)
}

// EnsureBundledInstall fixes permissions on shipped binaries (IPK tar on
// Windows may record them as 0644) and syncs freeturn-version.json from
// on-disk SHA256 when bundled binaries are present.
func (s *Service) EnsureBundledInstall() {
	ensureExecutable(s.clientBin)
	ensureExecutable(s.serverBin)
	s.syncInstalledVersionFromBinaries()
}

func (s *Service) syncInstalledVersionFromBinaries() {
	if !s.binariesOperational() {
		return
	}
	ver := s.effectiveInstalledVersion()
	if ver == "" {
		return
	}
	if s.readInstalledVersion() == ver {
		return
	}
	if err := s.writeInstalledVersion(ver); err != nil && s.appLog != nil {
		s.appLog.Warn("install", "version-file", err.Error())
	}
}

// ensureExecutable chmods path 0755 when a regular file exists but is not executable.
func ensureExecutable(path string) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return
	}
	if st.Mode().Perm()&0111 != 0 {
		return
	}
	_ = os.Chmod(path, st.Mode().Perm()|0755)
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
