package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func newStrictStore(t *testing.T) (*AWGTunnelStore, string) {
	t.Helper()
	dir := t.TempDir()
	return NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks")), dir
}

func TestListStrictReturnsAll(t *testing.T) {
	s, _ := newStrictStore(t)
	for _, id := range []string{"a", "wdttraw-de"} {
		if err := s.Save(&AWGTunnel{ID: id, Type: "awg"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListStrict()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("записей %d, ждали 2", len(got))
	}
}

func TestListStrictFailsOnCorruptJSONWithoutQuarantine(t *testing.T) {
	s, dir := newStrictStore(t)
	if err := s.Save(&AWGTunnel{ID: "ok", Type: "awg"}); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(bad, []byte("{нет"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListStrict(); err == nil {
		t.Fatal("битый файл обязан валить ListStrict целиком")
	}
	// Побочных действий нет: файл на месте, карантина не случилось.
	if _, err := os.Stat(bad); err != nil {
		t.Fatalf("ListStrict не имеет права трогать файл: %v", err)
	}
	if _, err := os.Stat(bad + ".corrupt"); err == nil {
		t.Fatal("ListStrict не имеет права карантинить")
	}
}

func TestListStrictFailsOnUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("под root chmod 000 не запрещает чтение")
	}
	s, dir := newStrictStore(t)
	p := filepath.Join(dir, "locked.json")
	if err := os.WriteFile(p, []byte(`{"id":"locked","type":"awg"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListStrict(); err == nil {
		t.Fatal("нечитаемый файл обязан валить ListStrict целиком")
	}
}

func TestListStrictEmptyDirIsEmptyNotError(t *testing.T) {
	s, _ := newStrictStore(t)
	got, err := s.ListStrict()
	if err != nil || len(got) != 0 {
		t.Fatalf("пустой каталог: %v, %d", err, len(got))
	}
}

// Каталога ещё нет — записей не создавали: это «пусто», а не отказ. Отдельно
// от TestListStrictEmptyDirIsEmptyNotError: там каталог существует и ветка
// os.IsNotExist не проходится вовсе.
func TestListStrictMissingDirIsEmptyNotError(t *testing.T) {
	s := NewAWGTunnelStoreWithLockDir(filepath.Join(t.TempDir(), "нет-такого"), t.TempDir())
	got, err := s.ListStrict()
	if err != nil || len(got) != 0 {
		t.Fatalf("отсутствующий каталог: %v, %d", err, len(got))
	}
}

// Строгость не должна распространяться на то, что записью не является:
// подкаталог (у стора свой каталог локов внутри) и чужой файл обязаны
// пропускаться. Иначе один посторонний файл в каталоге запирал бы уборку
// зеркальных записей навсегда.
func TestListStrictSkipsNonRecords(t *testing.T) {
	s, dir := newStrictStore(t)
	if err := s.Save(&AWGTunnel{ID: "ok", Type: "awg"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListStrict()
	if err != nil {
		t.Fatalf("посторонние имена в каталоге не должны валить перечисление: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("записей %d, ждали 1", len(got))
	}
}
