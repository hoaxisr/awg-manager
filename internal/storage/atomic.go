package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const FilePermission = 0644
const DirPermission = 0755

// AtomicWrite writes data to path atomically using temp file + rename.
func AtomicWrite(path string, data []byte) error {
	return AtomicWritePerm(path, data, FilePermission)
}

// AtomicWritePerm is like AtomicWrite but with custom file permissions.
//
// The temp file and the containing directory are fsync'ed before/after the
// rename: on routers state lives on flash with ext4 delayed allocation, and
// without the syncs a power loss inside the writeback window can leave a
// zero-length file under the final name.
func AtomicWritePerm(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)

	if err := os.MkdirAll(dir, DirPermission); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmpPath := fmt.Sprintf("%s.tmp.%d.%d", path, os.Getpid(), time.Now().UnixNano())

	if err := writeFileSync(tmpPath, data, perm); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp to target: %w", err)
	}

	syncDir(dir)
	return nil
}

func writeFileSync(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir fsyncs a directory so a preceding rename survives power loss.
// Best-effort: some filesystems reject directory fsync.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}

// QuarantineCorrupt уносит повреждённый файл состояния в <path>.corrupt и
// сообщает об этом в журнал приложения. Зовут его хранилища, которые иначе
// молча сбросились бы к умолчаниям, а на следующем сохранении затёрли бы
// повреждённый файл окончательно.
//
// После переименования к файлу больше никто не обращается: читатели ищут
// .json. Копия остаётся на диске не ради того, чтобы пользователь чинил её
// руками — до файла на роутере он не пойдёт, — а чтобы данные можно было
// достать, если он попросит помощи.
func QuarantineCorrupt(path string, parseErr error) {
	quarantine := path + ".corrupt"
	if err := os.Rename(path, quarantine); err != nil {
		fmt.Fprintf(os.Stderr, "storage: %s is corrupt (%v); quarantine failed: %v\n", path, parseErr, err)
		// Переименовать не вышло — файл остаётся на месте и будет прочитан
		// снова, то есть сообщение повторится на каждом чтении. Говорим об
		// этом прямо: сам он не починится.
		recordNotice("quarantine", filepath.Base(path), fmt.Sprintf(
			"Файл %s повреждён (%v) и не поддаётся автоматическому исправлению: %v. Настройки из него недоступны.",
			filepath.Base(path), parseErr, err))
		return
	}
	fmt.Fprintf(os.Stderr, "storage: %s is corrupt (%v); moved to %s, continuing with defaults\n", path, parseErr, quarantine)
	// Текст адресован человеку в журнале приложения, а не инженеру в консоли:
	// звать «восстановить вручную» бессмысленно — до файла на роутере никто не
	// пойдёт. Говорим прямо, что запись потеряна и что делать дальше; сам файл
	// уже переименован, то есть читать его больше никто не будет.
	recordNotice("quarantine", filepath.Base(path), fmt.Sprintf(
		"Файл %s повреждён и больше не используется (%v). Настройки из него потеряны — создайте их заново. Повреждённая копия сохранена рядом как %s.",
		filepath.Base(path), parseErr, filepath.Base(quarantine)))
}
