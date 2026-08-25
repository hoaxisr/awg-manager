package install

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

// installedVersionRecord — содержимое <dataDir>/wdtt-version.json и
// <dataDir>/freeturn-version.json: что и когда поставила последняя установка.
// Форма — объединение двух старых записей; ClientVersion/ServerVersion пишет
// только wdtt (его клиент и сервер выпускаются раздельно).
type installedVersionRecord struct {
	Version     string    `json:"version"`
	ClientVer   string    `json:"clientVersion,omitempty"`
	ServerVer   string    `json:"serverVersion,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
	ClientPath  string    `json:"clientPath"`
	ServerPath  string    `json:"serverPath,omitempty"`
}

// readInstalledVersion — версия из version-файла. Любая беда с файлом (нет,
// битый, чужой) читается как «версия неизвестна»: она лишь дополняет сверку
// бинарей с пином, а не заменяет её.
func (sub *subsys) readInstalledVersion() string {
	if sub.versionPath == "" {
		return ""
	}
	data, err := os.ReadFile(sub.versionPath)
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
	if sub.name != SubsystemWdtt {
		return ""
	}
	// Файлы wdtt до составной метки: версии клиента и сервера лежали раздельно.
	if rec.ClientVer != "" && rec.ServerVer != "" {
		return rec.ClientVer + "+server-" + rec.ServerVer
	}
	return rec.ClientVer
}

func (sub *subsys) writeInstalledVersion(version string) error {
	if sub.versionPath == "" {
		return nil
	}
	rec := installedVersionRecord{
		Version:     version,
		InstalledAt: time.Now().UTC(),
		ClientPath:  sub.clientBin,
		ServerPath:  sub.serverBin,
	}
	if sub.name == SubsystemWdtt && sub.specs != nil {
		rec.ClientVer = sub.specs.Client.Version
		rec.ServerVer = sub.specs.Server.Version
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(sub.versionPath), 0755); err != nil {
		return err
	}
	tmp := sub.versionPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, sub.versionPath)
}

// binariesMatchSpecs — совпадают ли бинари на диске с SHA256 из пина сборки.
//
// Единственный источник бинарей — зеркало по пину; в IPK они не кладутся.
// Раньше факта «файл на месте» хватало, чтобы объявить его пином: протухший
// бинарь считался установленным, обновление не предлагалось, а старт падал на
// флаге, которого тот бинарь не знает (flag.Parse → exit 2).
//
// Результат кешируется по mtime+size: сверка читает десятки мегабайт, а
// зовётся из Status(), который фронт опрашивает постоянно. Ключ меняется при
// любой перезаписи бинаря, так что установка инвалидирует кеш сама.
func (sub *subsys) binariesMatchSpecs() bool {
	if sub.specs == nil {
		return false
	}
	specs := *sub.specs
	if specs.Client.SHA256 == "" {
		return false
	}
	if specs.serverSupported() && specs.Server.SHA256 == "" {
		return false
	}
	key := specs.Client.SHA256 + "|" + specs.Server.SHA256 + "|" +
		fileFingerprint(sub.clientBin) + "|" + fileFingerprint(sub.serverBin)

	sub.matchMu.Lock()
	defer sub.matchMu.Unlock()
	if key == sub.matchKey {
		return sub.matchVal
	}
	match := sha256Equals(sub.clientBin, specs.Client.SHA256)
	if match && specs.serverSupported() {
		match = sha256Equals(sub.serverBin, specs.Server.SHA256)
	}
	sub.matchKey, sub.matchVal = key, match
	return match
}

func sha256Equals(path, want string) bool {
	got, err := sha256File(path)
	return err == nil && strings.EqualFold(got, want)
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

// binaryPresent — по пути лежит исполняемый обычный файл. awg-manager не
// возит бинари прокси в IPK, поэтому панели нужно отличать «не установлен» от
// настоящей ошибки запуска.
func binaryPresent(path string) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Mode().Perm()&0111 != 0
}

// ensureExecutable ставит 0755 обычному файлу, которому не хватает бита
// исполнения (бинарь, приехавший мимо установщика).
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
