package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// SnapshotDir — снимки перед обновлением, ВНУТРИ каталога данных: уходят
	// вместе с ним при opkg remove и rm -rf, в отличие от прежних копий
	// <dataDir>.pre-restore-* рядом (#953). Модулей ядра и бинаря sing-box в
	// снимке нет — их не несёт и обычный бэкап (hardwareBound).
	SnapshotDir = "update-snapshots"
	// SnapshotKeep — сколько последних снимков хранится; старшие удаляются
	// после записи нового. Только что записанный остаётся всегда.
	SnapshotKeep = 3
	// SnapshotTTL — срок жизни снимка: откат нужен, пока последствия
	// обновления свежие, а копить архивы с ключами незачем. Неделя, а не
	// сутки: автообновление в 05:00 субботы замечают и в понедельник.
	SnapshotTTL = 7 * 24 * time.Hour
	// MinSnapshotSpare — нижняя граница запаса места после снимка (см.
	// TakeUpdateSnapshot); обычно запас считается от размера пакета.
	MinSnapshotSpare = 8 << 20
	snapshotPrefix   = "before-update-"
	snapshotStamp    = "20060102-150405"
)

// ErrSnapshotNotFound — снимка с таким id нет (или id не похож на снимок).
var ErrSnapshotNotFound = errors.New("снимок не найден")

// Суффикс -N — второй снимок с той же меткой времени: часы роутера до NTP
// могут выдать уже занятую секунду, и прежний снимок перезаписывать нельзя.
var snapshotName = regexp.MustCompile(`^before-update-(\d{8}-\d{6})(-\d+)?\.tar\.gz$`)

// Snapshot описывает снимок каталога данных, снятый перед обновлением.
type Snapshot struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	AppVersion string    `json:"appVersion,omitempty"`
	Size       int64     `json:"size"`
}

var (
	snapshotMu sync.Mutex
	// availableBytesFunc подменяется в тестах.
	availableBytesFunc = availableBytes
)

// TakeUpdateSnapshot сохраняет архив каталога данных (тот же, что отдаёт
// экспорт) в <dataDir>/update-snapshots и оставляет SnapshotKeep последних,
// причём только что записанный — всегда: порядок задаёт время в имени, и
// при отстающих часах новый снимок иначе оказался бы «самым старым».
//
// spare — сколько места должно остаться ПОСЛЕ снимка (не меньше
// MinSnapshotSpare): следом opkg распаковывает пакет на тот же раздел. Места
// мало — снимок не пишется вовсе, а не забивает раздел до отказа установки.
// Недописанный файл удаляется.
//
// Снимок берёт лок восстановления: архив, снятый посреди Restore, мог бы
// застать каталог без settings.json и всё равно выглядеть удачным.
func TakeUpdateSnapshot(dataDir, appVersion string, now time.Time, spare int64) (Snapshot, error) {
	if err := CheckDataDir(dataDir); err != nil {
		return Snapshot{}, err
	}
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	restoreMu.Lock()
	defer restoreMu.Unlock()
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	dir := filepath.Join(dataDir, SnapshotDir)
	// 0700/0600: в архиве приватные ключи туннелей открытым текстом.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Snapshot{}, err
	}
	if leftovers, err := filepath.Glob(filepath.Join(dir, "*.tmp")); err == nil {
		for _, p := range leftovers {
			_ = os.Remove(p)
		}
	}

	need, err := exportSize(dataDir)
	if err != nil {
		return Snapshot{}, err
	}
	spare = max(spare, MinSnapshotSpare)
	if avail, ok := availableBytesFunc(dir); ok && avail < need+spare {
		return Snapshot{}, fmt.Errorf("мало места: снимку нужно до %d МБ и ещё %d МБ под установку пакета, свободно %d МБ",
			need>>20, spare>>20, avail>>20)
	}

	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	id := freeSnapshotID(dir, now)
	final := filepath.Join(dir, id)
	tmp := final + ".tmp"
	if err := writeSnapshot(dataDir, appVersion, tmp); err != nil {
		_ = os.Remove(tmp)
		return Snapshot{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return Snapshot{}, err
	}
	pruneSnapshots(dir, id, SnapshotKeep)

	snap := Snapshot{ID: id, CreatedAt: now.Truncate(time.Second), AppVersion: strings.TrimSpace(appVersion)}
	if info, err := os.Stat(final); err == nil {
		snap.Size = info.Size()
	}
	return snap, nil
}

func writeSnapshot(dataDir, appVersion, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := Export(dataDir, appVersion, f); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// exportSize — сколько несжатых байт положил бы в архив Export: оценка
// сверху, сжатый снимок меньше.
func exportSize(dataDir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dataDir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dataDir, p)
		if err != nil || rel == "." {
			return err
		}
		if shouldSkip(filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Файл, исчезнувший между чтением каталога и stat, оценку не валит.
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// freeSnapshotID — имя для снимка с меткой now, не занятое прежним снимком.
func freeSnapshotID(dir string, now time.Time) string {
	base := snapshotPrefix + now.Format(snapshotStamp)
	id := base + ".tar.gz"
	for n := 2; ; n++ {
		if _, err := os.Lstat(filepath.Join(dir, id)); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d.tar.gz", base, n)
	}
}

// snapshotTime — метка времени из имени снимка.
func snapshotTime(id string) (time.Time, bool) {
	m := snapshotName.FindStringSubmatch(id)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(snapshotStamp, m[1])
	return t, err == nil
}

// snapshotIDs возвращает имена снимков в dir, новые первыми (метка времени
// в имени сортируется как строка).
func snapshotIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.Type().IsRegular() && snapshotName.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

// pruneSnapshots оставляет fresh и keep-1 самых новых из остальных.
func pruneSnapshots(dir, fresh string, keep int) {
	ids, err := snapshotIDs(dir)
	if err != nil {
		return
	}
	kept := 1
	for _, id := range ids {
		if id == fresh {
			continue
		}
		if kept < keep {
			kept++
			continue
		}
		_ = os.Remove(filepath.Join(dir, id))
	}
}

// PruneExpiredSnapshots удаляет снимки старше SnapshotTTL и возвращает,
// сколько удалено. Самый новый снимок по сроку не удаляется: это последняя
// точка отката, а его возраст по часам роутера ненадёжен — снятый до NTP,
// он выглядел бы старым сразу после синхронизации. Снимок «из будущего»
// (часы отстают сейчас) тоже не трогается.
func PruneExpiredSnapshots(dataDir string, now time.Time) int {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	dir := filepath.Join(filepath.Clean(strings.TrimSpace(dataDir)), SnapshotDir)
	ids, err := snapshotIDs(dir)
	if err != nil || len(ids) < 2 {
		return 0
	}
	n := 0
	for _, id := range ids[1:] {
		t, ok := snapshotTime(id)
		if !ok || t.After(now) || now.Sub(t) < SnapshotTTL {
			continue
		}
		if os.Remove(filepath.Join(dir, id)) == nil {
			n++
		}
	}
	return n
}

// ListSnapshots возвращает снимки, новые первыми. Нечитаемый манифест не
// прячет снимок: время берётся из имени, версия остаётся пустой.
func ListSnapshots(dataDir string) ([]Snapshot, error) {
	dir := filepath.Join(filepath.Clean(strings.TrimSpace(dataDir)), SnapshotDir)
	ids, err := snapshotIDs(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		p := filepath.Join(dir, id)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		snap := Snapshot{ID: id, Size: info.Size()}
		if t, ok := snapshotTime(id); ok {
			snap.CreatedAt = t
		}
		if f, err := os.Open(p); err == nil {
			if m, err := readManifest(f); err == nil {
				snap.AppVersion = m.AppVersion
			}
			f.Close()
		}
		out = append(out, snap)
	}
	return out, nil
}

// snapshotPath проверяет id и возвращает путь к снимку. Id — только имя
// по шаблону, поэтому выйти им за пределы каталога снимков нельзя.
func snapshotPath(dataDir, id string) (string, error) {
	if !snapshotName.MatchString(id) {
		return "", ErrSnapshotNotFound
	}
	p := filepath.Join(filepath.Clean(strings.TrimSpace(dataDir)), SnapshotDir, id)
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrSnapshotNotFound
	}
	return p, nil
}

// OpenSnapshot открывает снимок на чтение; закрывает вызывающий.
func OpenSnapshot(dataDir, id string) (*os.File, error) {
	p, err := snapshotPath(dataDir, id)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// DeleteSnapshot удаляет снимок.
func DeleteSnapshot(dataDir, id string) error {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()
	p, err := snapshotPath(dataDir, id)
	if err != nil {
		return err
	}
	return os.Remove(p)
}
