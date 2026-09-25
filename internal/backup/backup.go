package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	ManifestName  = "awg-manager-backup.json"
	FileType      = "awg-manager-full-backup"
	FileVersion   = 1
	maxArchiveLen = 512 << 20 // 512 MiB
)

// Manifest describes a full awg-manager data-dir backup archive.
type Manifest struct {
	Version    int       `json:"version"`
	Type       string    `json:"type"`
	ExportedAt time.Time `json:"exportedAt"`
	AppVersion string    `json:"appVersion,omitempty"`
}

// CheckDataDir validates dataDir before an export starts. Отдельно от Export,
// чтобы HTTP-обработчик мог ответить ошибкой ДО отправки заголовков и уже
// потом стримить архив, не собирая его целиком в памяти.
func CheckDataDir(dataDir string) error {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return fmt.Errorf("data-dir не задан")
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return fmt.Errorf("data-dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("data-dir не каталог")
	}
	return nil
}

// Export writes a gzip-compressed tar of dataDir to w. Runtime caches are skipped.
func Export(dataDir, appVersion string, w io.Writer) error {
	if err := CheckDataDir(dataDir); err != nil {
		return err
	}
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	manifest := Manifest{
		Version:    FileVersion,
		Type:       FileType,
		ExportedAt: time.Now().UTC(),
		AppVersion: strings.TrimSpace(appVersion),
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeTarBytes(tw, ManifestName, raw, manifest.ExportedAt); err != nil {
		return err
	}

	return filepath.WalkDir(dataDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dataDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if shouldSkip(relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			hdr := &tar.Header{
				Name:     relSlash + "/",
				Typeflag: tar.TypeDir,
				Mode:     0o755,
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(hdr)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		hdr := &tar.Header{
			Name:    relSlash,
			Mode:    int64(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	})
}

// restoreTmpDir — каталог распаковки архива ВНУТРИ каталога данных: всё
// временное живёт там же, где данные, и уходит вместе с ними при удалении
// каталога. В архив не попадает и при очистке не трогается (shouldSkip).
const restoreTmpDir = ".restore-tmp"

var restoreMu sync.Mutex

// Restore заменяет данные из архива r (gzip tar) на месте, не подменяя каталог:
//  1. архив распаковывается в <dataDir>/.restore-tmp и проверяется манифест —
//     при отказе текущие данные не тронуты;
//  2. из каталога данных удаляется всё, что несёт бэкап; то, чего он не несёт
//     (секрет устройства, модули ядра, бинарь sing-box, run/, кеши —
//     shouldSkip), остаётся на месте и не переносится;
//  3. содержимое архива переносится на место, временный каталог удаляется.
//
// Откатной копии нет: откат — бэкап, выгруженный пользователем через UI.
// Прежняя схема (подмена всего каталога и отложенная копия
// <dataDir>.pre-restore-* рядом) оставляла копию данных, которую не удалял ни
// opkg remove, ни rm -rf каталога, — с чужим модулем ядра внутри (#953).
// Оборванное на шаге 2–3 восстановление оставляет каталог частично
// заменённым; лечится повторным восстановлением того же файла.
func Restore(dataDir string, r io.Reader) error {
	// Временный каталог один на каталог данных — два одновременных
	// восстановления сносили бы распаковку друг друга.
	restoreMu.Lock()
	defer restoreMu.Unlock()
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return fmt.Errorf("data-dir не задан")
	}
	_, statErr := os.Stat(dataDir)
	created := os.IsNotExist(statErr)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	staging := filepath.Join(dataDir, restoreTmpDir)
	_ = os.RemoveAll(staging) // остаток прерванного восстановления
	defer os.RemoveAll(staging)

	// Отвергнутый архив не оставляет следов: каталог, созданный под него,
	// удаляется вместе с распакованным.
	reject := func(err error) error {
		if created {
			_ = os.RemoveAll(dataDir)
		}
		return err
	}
	if err := extractArchive(r, staging); err != nil {
		return reject(err)
	}
	if err := validateStaging(staging); err != nil {
		return reject(err)
	}
	// С этого места данные меняются: отказ оставляет их частично
	// заменёнными, и об этом надо сказать прямо — лечится повтором.
	if _, err := clearBackedUp(dataDir, ""); err != nil {
		return fmt.Errorf("данные заменены частично, повторите восстановление тем же файлом: %w", err)
	}
	if err := moveInto(staging, dataDir, ""); err != nil {
		return fmt.Errorf("данные заменены частично, повторите восстановление тем же файлом: %w", err)
	}
	if err := WritePostRestoreMarker(dataDir); err != nil {
		return fmt.Errorf("не удалось записать маркер post-restore: %w", err)
	}
	return nil
}

// clearBackedUp удаляет из root/rel всё, что несёт бэкап, и оставляет то, что
// он не несёт (shouldSkip). Возвращает, осталось ли что-то внутри: каталог,
// в котором лежит оставляемое (singbox/ с бинарём), удалять нельзя.
func clearBackedUp(root, rel string) (kept bool, err error) {
	entries, err := os.ReadDir(filepath.Join(root, rel))
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		r := path.Join(rel, e.Name())
		if r == restoreTmpDir || shouldSkip(r) {
			kept = true
			continue
		}
		p := filepath.Join(root, r)
		if e.IsDir() {
			k, err := clearBackedUp(root, r)
			if err != nil {
				return kept, err
			}
			if k {
				kept = true
				continue
			}
		}
		if err := os.RemoveAll(p); err != nil {
			return kept, err
		}
	}
	return kept, nil
}

// moveInto переносит содержимое src/rel в dst/rel. Каталоги, уже существующие
// в dst (в них лежит оставленное), сливаются; манифест архива не переносится.
func moveInto(src, dst, rel string) error {
	entries, err := os.ReadDir(filepath.Join(src, rel))
	if err != nil {
		return err
	}
	for _, e := range entries {
		r := path.Join(rel, e.Name())
		if r == ManifestName {
			continue
		}
		from, to := filepath.Join(src, r), filepath.Join(dst, r)
		if e.IsDir() {
			if info, err := os.Lstat(to); err == nil && info.IsDir() {
				if err := moveInto(src, dst, r); err != nil {
					return err
				}
				continue
			}
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return nil
}

// PruneRestoreLeftovers удаляет остатки восстановлений: рядом с dataDir —
// откатные копии `<name>.pre-restore-*` и временные `.awg-manager-restore-*`
// прежних версий, внутри — `.restore-tmp` оборванного Restore. Зовётся на
// старте демона. Без условий: оборванное восстановление — это файл бэкапа в
// руках пользователя, прежние данные он и так заменял, а новые — в этом файле;
// лечится повторным восстановлением. Возвращает число удалённых каталогов.
func PruneRestoreLeftovers(dataDir string) int {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	parent := filepath.Dir(dataDir)
	n := 0
	for _, pattern := range []string{
		filepath.Join(parent, filepath.Base(dataDir)+".pre-restore-*"),
		filepath.Join(parent, ".awg-manager-restore-*"),
		filepath.Join(dataDir, restoreTmpDir),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range matches {
			if os.RemoveAll(path) == nil {
				n++
			}
		}
	}
	return n
}

// hardwareBound — файлы, собранные под конкретное железо: модули ядра (под
// модель и SoC) и бинарь sing-box (под архитектуру). В архив не едут, а при
// восстановлении остаются свои: Restore их не трогает.
// Иначе бэкап с другого роутера сажает чужой модуль, который ядро принимает
// молча и который вешает роутер (#953: amneziawg KN-1811 на NC-4210).
var hardwareBound = []string{
	"modules",
	"singbox/sing-box",
	"singbox/sing-box.meta.json",
}

func isHardwareBound(rel string) bool {
	for _, p := range hardwareBound {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

func shouldSkip(rel string) bool {
	if rel == ManifestName {
		return true
	}
	if isHardwareBound(rel) {
		return true
	}
	// Секрет устройства привязан к установке и в бэкап не едет: архив
	// пользователь пересылает в поддержку и кладёт в облако. Префикс — про
	// .device-key.corrupt: файл негодной длины QuarantineCorrupt уносит
	// рядом под этим именем, и в нём лежит настоящий секрет.
	if rel == storage.DeviceKeyFile || strings.HasPrefix(rel, storage.DeviceKeyFile+".") {
		return true
	}
	// rci-token.tmp от прерванной атомарной записи отсеивает правило *.tmp ниже.
	if rel == storage.RCITokenFile {
		return true
	}
	if rel == "run" || strings.HasPrefix(rel, "run/") {
		return true
	}
	if rel == "locks" || strings.HasPrefix(rel, "locks/") {
		return true
	}
	switch rel {
	case "singbox/cache.db", "singbox/cache.db-shm", "singbox/cache.db-wal":
		return true
	}
	if strings.HasPrefix(rel, "singbox/cache.db") {
		return true
	}
	if strings.HasSuffix(rel, ".tmp") || strings.HasSuffix(rel, ".pid") {
		return true
	}
	if strings.HasSuffix(rel, ".lock") || strings.HasSuffix(rel, ".lock.d") {
		return true
	}
	if rel == restoreTmpDir || strings.HasPrefix(rel, restoreTmpDir+"/") {
		return true
	}
	if strings.Contains(rel, ".pre-restore-") || strings.HasPrefix(rel, ".awg-manager-restore-") {
		return true
	}
	return false
}

// skipFromArchive — shouldSkip для имени ИЗ архива: отсеивает ещё и всё, что
// лежит под отсеиваемым именем. На выгрузке это делает filepath.SkipDir, а
// здесь предикат смотрел только на имя целиком, и ".device-key/x" проходил
// мимо фильтра: в каталоге данных появлялся КАТАЛОГ ".device-key", после чего
// свой секрет туда уже не записать и не прочитать — ключ подписки терялся
// навсегда. Тем же путём проходили "settings.json.lock/x" и прочие имена,
// отсеиваемые по суффиксу.
func skipFromArchive(name string) bool {
	for {
		if shouldSkip(name) {
			return true
		}
		i := strings.LastIndex(name, "/")
		if i < 0 {
			return false
		}
		name = name[:i]
	}
}

func writeTarBytes(tw *tar.Writer, name string, data []byte, mod time.Time) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    0o644,
		Size:    int64(len(data)),
		ModTime: mod,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func extractArchive(r io.Reader, destDir string) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	gr, err := gzip.NewReader(io.LimitReader(r, maxArchiveLen+1))
	if err != nil {
		return fmt.Errorf("не gzip-архив: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ошибка чтения tar: %w", err)
		}
		if hdr == nil {
			continue
		}
		name := filepath.Clean(hdr.Name)
		name = filepath.ToSlash(name)
		if name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
			return fmt.Errorf("небезопасный путь в архиве: %q", hdr.Name)
		}
		// Отказ, а не нормализация: наш Export строит имена через filepath.Rel
		// и потому НИКОГДА не пишет ни абсолютных имён, ни имени корня — такое
		// имя признак подлога, а не совместимости со старыми архивами.
		// Нормализация («/.device-key» → «.device-key») стирала бы эту разницу.
		//
		// Ведущий слэш filepath.Clean не снимает: "/.device-key" остаётся с
		// ним, shouldSkip сравнивает с относительными именами и не срабатывает,
		// а filepath.Join(destDir, "/.device-key") кладёт файл ровно в каталог
		// данных — одним символом обходился весь фильтр распаковки (и секрет
		// устройства, и run/, и locks/). Имя корня (".") Export пропускает
		// явно, а при распаковке оно означало бы запись поверх самого каталога.
		if strings.HasPrefix(name, "/") || name == "." {
			return fmt.Errorf("недопустимое имя записи в архиве — это не резервная копия awg-manager")
		}
		// Симметрия с выгрузкой: чего мы принципиально не кладём в архив,
		// того из архива принципиально не достаём. Без этого подложенный
		// .device-key ложился бы в каталог данных (и правами из tar-заголовка),
		// то есть автор чужого архива знал бы секрет устройства и расшифровал
		// бы всё, что пользователь после восстановления зашифрует.
		//
		// Отсев здесь, а не проходом по распакованному каталогу: файл не
		// появляется на диске вообще, поэтому его не подхватит ни прерванное
		// на полпути восстановление, ни чтение staging кем-то ещё. Проход
		// после распаковки давал бы более слабое свойство — «полежал и был
		// убран» вместо «не был положен».
		//
		// Манифест — исключение: в архив его кладёт сам Export отдельной
		// записью (в каталоге данных его нет), а из staging его читает
		// validateStaging.
		if name != ManifestName && skipFromArchive(name) {
			continue
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		if !strings.HasPrefix(target, destDir+string(os.PathSeparator)) && target != destDir {
			return fmt.Errorf("небезопасный путь в архиве: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			total += hdr.Size
			if total > maxArchiveLen {
				return fmt.Errorf("архив слишком большой (лимит %d МБ)", maxArchiveLen>>20)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
		// Прочие типы (симлинки, hardlink'и, устройства) пропускаем молча: в
		// наших архивах их нет, а распаковывать их из чужого файла незачем.
	}
	return nil
}

func validateStaging(dir string) error {
	// Манифест обязателен, а тип и версия в нём — обязательно наши. Его
	// кладёт сам Export с первой версии фичи, поэтому архив без манифеста
	// (или с пустыми полями) — не старый бэкап, а чужой tar.gz либо попытка
	// уйти от этих же проверок. Читать его условно («есть — проверим, нет —
	// и ладно») означало бы, что достаточно выкинуть одну запись, чтобы
	// восстановление приняло что угодно с settings.json внутри.
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return fmt.Errorf("в архиве нет %s — это не резервная копия awg-manager", ManifestName)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("некорректный %s: %w", ManifestName, err)
	}
	if manifest.Type != FileType {
		return fmt.Errorf("неподдерживаемый тип архива: %q", manifest.Type)
	}
	if manifest.Version < 1 || manifest.Version > FileVersion {
		return fmt.Errorf("версия архива %d не поддерживается", manifest.Version)
	}
	// Именно обычный файл. Проверка «путь существует» проходила и для
	// КАТАЛОГА settings.json — его делает запись вида "settings.json/x" в
	// подложенном архиве, — а дальше SettingsStore.Load получал EISDIR,
	// который не IsNotExist, и панель не поднималась вовсе. Тот же класс,
	// что каталог ".device-key" из архива.
	info, err := os.Stat(filepath.Join(dir, "settings.json"))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("в архиве нет settings.json — это не резервная копия awg-manager")
	}
	return nil
}

// Filename returns a suggested download name for a backup export.
func Filename(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("awg-manager-backup-%s.tar.gz", now.Format("2006-01-02-150405"))
}

// PeekManifest reads manifest from an in-memory gzip tar prefix (for UI hints).
func PeekManifest(data []byte) (*Manifest, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("manifest not found")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Name != ManifestName {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	}
}
