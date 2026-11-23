package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// F861: битый стор политик не оставляет uplink без стражей — файл
// откладывается, стор открывается пустым.
func TestOpenQuarantiningCorruptUplinkStore(t *testing.T) {
	dir := t.TempDir()
	settingsStore := storage.NewSettingsStore(dir)
	loggingService := logging.NewService(settingsStore)
	t.Cleanup(loggingService.Stop)
	a := &app{bootLog: logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup)}
	path := filepath.Join(dir, "uplink-policies.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openQuarantining(a, path, "политики начаты заново", uplink.OpenStore)
	if err != nil || store == nil {
		t.Fatalf("store not opened after quarantine: %v", err)
	}
	if len(store.List()) != 0 {
		t.Fatal("quarantined store must start empty")
	}
	moved, _ := filepath.Glob(path + ".corrupt-*")
	if len(moved) != 1 {
		t.Fatalf("corrupt file not kept aside: %v", moved)
	}
	if data, _ := os.ReadFile(moved[0]); string(data) != "{broken" {
		t.Fatal("quarantined copy altered")
	}
}

// Ревью F861: файл, который не читается (а не битый), в карантин не уходит.
func TestOpenQuarantiningKeepsUnreadableStore(t *testing.T) {
	dir := t.TempDir()
	loggingService := logging.NewService(storage.NewSettingsStore(dir))
	t.Cleanup(loggingService.Stop)
	a := &app{bootLog: logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup)}
	path := filepath.Join(dir, "uplink-policies.json")
	if err := os.Mkdir(path, 0o700); err != nil { // чтение каталога как файла — ошибка чтения
		t.Fatal(err)
	}
	if _, err := openQuarantining(a, path, "x", uplink.OpenStore); err == nil {
		t.Fatal("read error must be returned, not quarantined")
	}
	if moved, _ := filepath.Glob(path + ".corrupt-*"); len(moved) != 0 {
		t.Fatalf("unreadable store quarantined: %v", moved)
	}
}
