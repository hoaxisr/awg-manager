package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Два писателя в ОДНОМ процессе не должны мешать друг другу.
//
// Прежнее имя временного файла `<путь>.tmp.<pid>.<UnixNano>` уникальным не
// было: на ревью Task 4 воспроизвелись 3 отказа записи на 200 итераций —
// писатели получали одну наносекунду, и второй os.Rename падал с ENOENT,
// потому что первый уже унёс общий временный файл. Цена промаха разная: для
// настроек это сорванная правка, для файла секрета устройства — потерянный
// ключ подписки на ПЕРВОЙ записи.
//
// Тест краснеет на возврате имени, построенного из времени: итераций взято
// с запасом от воспроизведения.
func TestAtomicWrite_ConcurrentWritersSameFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	const iterations = 300
	var wg sync.WaitGroup
	errs := make(chan error, iterations*2)

	for i := 0; i < iterations; i++ {
		wg.Add(2)
		for w := 0; w < 2; w++ {
			go func(w int) {
				defer wg.Done()
				if err := AtomicWrite(path, []byte(fmt.Sprintf(`{"writer":%d}`, w))); err != nil {
					errs <- err
				}
			}(w)
		}
	}
	wg.Wait()
	close(errs)

	var failed []error
	for err := range errs {
		failed = append(failed, err)
	}
	if len(failed) > 0 {
		t.Fatalf("отказов записи %d из %d, первый: %v", len(failed), iterations*2, failed[0])
	}

	// Файл на месте и целый: гонка не должна оставлять его пустым.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("итоговый файл: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("итоговый файл пуст")
	}
}

// Разные файлы в одном каталоге — тот же вопрос: временные имена не должны
// пересекаться между целями.
func TestAtomicWrite_ConcurrentWritersDifferentFiles(t *testing.T) {
	dir := t.TempDir()

	const writers = 32
	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := filepath.Join(dir, fmt.Sprintf("state-%d.json", i))
			for n := 0; n < 20; n++ {
				if err := AtomicWrite(p, []byte(`{"n":1}`)); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("отказ записи: %v", err)
	}
}

// Временные файлы за собой не остаются: иначе каталог состояния на флеше
// роутера засорялся бы при каждом отказе.
func TestAtomicWrite_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for i := 0; i < 20; i++ {
		if err := AtomicWrite(path, []byte(`{"a":1}`)); err != nil {
			t.Fatalf("запись: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("чтение каталога: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Fatalf("в каталоге остался посторонний файл: %s", e.Name())
		}
	}
}

// Режим цели ставится явно: os.CreateTemp всегда создаёт 0600, и без chmod
// обычные файлы состояния молча стали бы строже, а секретные — не строже.
func TestAtomicWrite_HonoursRequestedPermissions(t *testing.T) {
	dir := t.TempDir()
	for _, perm := range []os.FileMode{FilePermission, SecretFilePermission} {
		path := filepath.Join(dir, fmt.Sprintf("state-%o.json", perm))
		if err := AtomicWritePerm(path, []byte(`{"a":1}`), perm); err != nil {
			t.Fatalf("запись %o: %v", perm, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %o: %v", perm, err)
		}
		if got := info.Mode().Perm(); got != perm {
			t.Errorf("права %#o, ожидались %#o", got, perm)
		}
	}
}
