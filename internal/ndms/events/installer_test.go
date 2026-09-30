package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstaller_CreatesAllFourHooks(t *testing.T) {
	root := t.TempDir()
	inst := &Installer{Root: root, Log: NopLogger()}

	if err := inst.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	for _, hook := range HookDirs {
		path := filepath.Join(root, hook+".d", "50-awg-manager.sh")
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s not created: %v", path, err)
			continue
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("%s perm: want 0755, got %o", path, info.Mode().Perm())
		}
		got, _ := os.ReadFile(path)
		if string(got) != hookScriptContent {
			t.Errorf("%s: содержимое не совпадает со встроенным скриптом", path)
		}
	}
}

func TestInstaller_Idempotent(t *testing.T) {
	root := t.TempDir()
	inst := &Installer{Root: root, Log: NopLogger()}

	if err := inst.Install(); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	path := filepath.Join(root, "iflayerchanged.d", "50-awg-manager.sh")
	stat1, _ := os.Stat(path)

	if err := inst.Install(); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	stat2, _ := os.Stat(path)
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("idempotent Install unexpectedly rewrote file")
	}
}

func TestInstaller_RewritesStaleContent(t *testing.T) {
	root := t.TempDir()
	hookDir := filepath.Join(root, "iflayerchanged.d")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(hookDir, "50-awg-manager.sh")
	if err := os.WriteFile(path, []byte("old content"), 0o755); err != nil {
		t.Fatalf("seed: %v", err)
	}

	inst := &Installer{Root: root, Log: NopLogger()}
	if err := inst.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) == "old content" {
		t.Errorf("stale content not rewritten")
	}
}

// Хук стоит в очереди NDMS: ни одного fork'а и ни одной сетевой утилиты
// (F571 — блокирующий скрипт задерживал хуки, и NDMS писал E на снятых
// интерфейсах).
func TestHookScript_NoForks(t *testing.T) {
	for _, bad := range []string{"$(", "`", "curl", "wget", "sed", "awk", "/opt/sbin/ip"} {
		if strings.Contains(hookScriptContent, bad) {
			t.Errorf("в хук-скрипте %q", bad)
		}
	}
}
