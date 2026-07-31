package kmod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Модель вне поставки: bundled сносится, и без этой записи в журнале роутер
// молча остаётся на том модуле, что уже стоит (или вовсе без него).
func TestSelectBundledModuleWarnsOnUnknownModel(t *testing.T) {
	dir := t.TempDir()
	setModulesDir(t, dir)
	if err := os.MkdirAll(BundledDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(BundledDir, "amneziawg-KN-1810.ko"), []byte("ko"), 0644); err != nil {
		t.Fatal(err)
	}

	var warned []string
	l := &Loader{model: "KN-1613", Warn: func(m string) { warned = append(warned, m) }}
	l.selectBundledModule()

	if len(warned) != 1 || !strings.Contains(warned[0], "KN-1613") {
		t.Errorf("ожидали одну запись с моделью, получили %v", warned)
	}
	if _, err := os.Stat(BundledDir); !os.IsNotExist(err) {
		t.Error("bundled должен быть снесён")
	}
}

// Совпадение по алиасу — не повод для предупреждения.
func TestSelectBundledModuleSilentOnAliasHit(t *testing.T) {
	dir := t.TempDir()
	setModulesDir(t, dir)
	if err := os.MkdirAll(BundledDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(BundledDir, "amneziawg-KN-1810.ko"), []byte("ko"), 0644); err != nil {
		t.Fatal(err)
	}

	var warned []string
	l := &Loader{model: "KN-2910", Warn: func(m string) { warned = append(warned, m) }}
	l.selectBundledModule()

	if len(warned) != 0 {
		t.Errorf("предупреждений быть не должно, получили %v", warned)
	}
	if _, err := os.Stat(filepath.Join(dir, "amneziawg.ko")); err != nil {
		t.Errorf("модуль алиаса не скопирован: %v", err)
	}
}

func setModulesDir(t *testing.T, dir string) {
	t.Helper()
	oldModules, oldBundled := ModulesDir, BundledDir
	ModulesDir, BundledDir = dir, filepath.Join(dir, "bundled")
	t.Cleanup(func() { ModulesDir, BundledDir = oldModules, oldBundled })
}
