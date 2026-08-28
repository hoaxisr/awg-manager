package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestScanner(t *testing.T) *Scanner {
	t.Helper()
	dir := t.TempDir()
	return &Scanner{InitDir: dir}
}

// Защита от удаления обходилась перезаписью: сохранить пустой скрипт под тем
// же именем — тот же результат, что и удалить.
func TestSaveScript_RefusesManagedService(t *testing.T) {
	sc := newTestScanner(t)
	if _, err := sc.SaveScript("S99awg-manager", "#!/bin/sh\nexit 0\n"); err == nil {
		t.Fatal("перезапись S99awg-manager разрешена")
	}
	if _, err := sc.SaveScript("S80ttyd", "#!/bin/sh\nexit 0\n"); err == nil {
		t.Fatal("перезапись S80ttyd разрешена")
	}
	if _, err := sc.SaveScript("S90myservice", "#!/bin/sh\nexit 0\n"); err != nil {
		t.Fatalf("обычная служба должна сохраняться: %v", err)
	}
}

// Остановить службу, отдающую эту же панель, значит потерять доступ к ней.
func TestRunAction_RefusesStoppingSelf(t *testing.T) {
	sc := newTestScanner(t)
	script := filepath.Join(sc.InitDir, "S99awg-manager")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RunAction("S99awg-manager", "stop"); err == nil {
		t.Fatal("остановка собственной службы разрешена")
	} else if !strings.Contains(err.Error(), "cannot stop") {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
}

func TestDeleteScript_RefusesManagedService(t *testing.T) {
	sc := newTestScanner(t)
	for _, name := range []string{"S99awg-manager", "S80ttyd", "S99sing-box"} {
		script := filepath.Join(sc.InitDir, name)
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := sc.DeleteScript(name); err == nil {
			t.Errorf("удаление %s разрешено", name)
		}
	}
}
