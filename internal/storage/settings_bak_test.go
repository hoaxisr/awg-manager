package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Битый settings.json карантинится в .corrupt, значения восстанавливаются из .bak
// (hardlink, сделанный прошлой записью), уведомление backup-restore записано.
func TestLoad_RestoresFromBakWhenCorrupt(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *Settings) error { s.UsageLevel = "expert"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *Settings) error { s.UsageLevel = "expert"; s.ApiKey = "k-1"; return nil }); err != nil {
		t.Fatal(err) // вторая запись — .bak содержит первую версию с expert
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Буфер pendingNotices к этому моменту НЕ пуст (quarantine-уведомления соседних тестов
	// пакета, notices.go:50-59 выгружает его при подключении sink) — сперва осушить.
	SetNoticeSink(func(Notice) {})
	var notices []Notice
	SetNoticeSink(func(n Notice) { notices = append(notices, n) })
	t.Cleanup(func() { SetNoticeSink(nil) })

	fresh := NewSettingsStore(dir)
	got, err := fresh.Load()
	if err != nil {
		t.Fatalf("Load обязан восстановиться из .bak: %v", err)
	}
	if got.UsageLevel != "expert" {
		t.Fatalf("восстановлено %+v, want usageLevel=expert из .bak", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json.corrupt")); err != nil {
		t.Fatal("битый файл обязан уйти в .corrupt")
	}
	if len(notices) == 0 || notices[0].Action != "backup-restore" ||
		!strings.Contains(notices[0].Message, "quarantined to "+filepath.Join(dir, "settings.json.corrupt")) {
		t.Fatalf("notices = %+v", notices)
	}
	// Восстановленное сразу переписано на диск валидным JSON: свежий стор
	// поднимает именно восстановленное значение, а не дефолты (без
	// пересохранения settings.json остался бы в карантине, и Load отдал бы
	// дефолтную запись, на которой прежняя проверка была зелёной всегда).
	again, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if again.UsageLevel != "expert" {
		t.Fatalf("после восстановления на диске %+v, want usageLevel=expert", again)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json обязан быть переписан: %v", err)
	}
	var onDisk Settings
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatalf("settings.json не валидный JSON: %v (%s)", err, raw)
	}
	if onDisk.UsageLevel != "expert" {
		t.Fatalf("в файле %+v, want usageLevel=expert", onDisk)
	}
}

// Отказ карантина (место .corrupt занято непустым каталогом — os.Rename файла поверх
// него отказывает и под root) больше не выдаётся за «quarantined to»: битый файл
// остаётся на месте, восстановление из .bak всё равно идёт, уведомление говорит правду.
func TestLoad_CorruptQuarantineRenameFailsIsReported(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *Settings) error { s.UsageLevel = "expert"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *Settings) error { s.ApiKey = "k-1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "settings.json.corrupt", "busy"), 0o755); err != nil {
		t.Fatal(err)
	}
	SetNoticeSink(func(Notice) {})
	var notices []Notice
	SetNoticeSink(func(n Notice) { notices = append(notices, n) })
	t.Cleanup(func() { SetNoticeSink(nil) })

	got, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatalf("Load обязан восстановиться из .bak и при отказе карантина: %v", err)
	}
	if got.UsageLevel != "expert" {
		t.Fatalf("восстановлено %+v", got)
	}
	if len(notices) == 0 || !strings.Contains(notices[0].Message, "corrupt file left in place") ||
		strings.Contains(notices[0].Message, "quarantined to") {
		t.Fatalf("уведомление врёт о карантине: %+v", notices)
	}
}
