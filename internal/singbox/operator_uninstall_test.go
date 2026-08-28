package singbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// seedInstallation раскладывает то, что оставляет после себя установка и
// работа движка: бинарь, слоты config.d, кэш FakeIP и pid.
func seedInstallation(t *testing.T, dir string) (binary, cache string) {
	t.Helper()
	binary = filepath.Join(dir, "sing-box")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	confDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"00-base.json", "10-tunnels.json", "20-router.json"} {
		if err := os.WriteFile(filepath.Join(confDir, slot), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cache = filepath.Join(dir, "cache.db")
	if err := os.WriteFile(cache, []byte("fakeip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sing-box.pid"), []byte("4242"), 0o644); err != nil {
		t.Fatal(err)
	}
	return binary, cache
}

func TestOperator_Uninstall_RemovesBinaryAndConfig(t *testing.T) {
	dir := t.TempDir()
	binary, cache := seedInstallation(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	for _, path := range []string{binary, filepath.Join(dir, "config.d"), cache, filepath.Join(dir, "sing-box.pid")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("остался после удаления: %s (err=%v)", path, err)
		}
	}
}

// Удаление идемпотентно: повторный вызов на пустом каталоге — не ошибка.
func TestOperator_Uninstall_Idempotent(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("первый вызов: %v", err)
	}
	if err := op.Uninstall(context.Background()); err != nil {
		t.Errorf("повторный вызов: %v", err)
	}
}

// Каталог движка сносится целиком — вместе со всем, что в нём лежало.
func TestOperator_Uninstall_RemovesEngineDirectory(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "20-router.json.bak"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("каталог движка остался: %v", err)
	}
}

// Установка после удаления должна быть возможна: статус обязан честно
// сказать «не установлен».
func TestOperator_Uninstall_StatusReportsMissing(t *testing.T) {
	dir := t.TempDir()
	binary, _ := seedInstallation(t, dir)
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})

	if err := op.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if s := op.GetStatus(context.Background()); s.Installed {
		t.Errorf("статус после удаления: installed=%v", s.Installed)
	}
}
