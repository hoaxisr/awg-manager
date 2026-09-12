package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/obfuscator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/sys/kmod"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// F168: флаг `-data-dir` соблюдался наполовину — демон в песочнице всё равно
// писал .conf туннелей, конфиги релея и модули в боевой /opt/etc/awg-manager.
func TestApplyDataDir_RedirectsDerivedPaths(t *testing.T) {
	oldConf, oldObf, oldMod := tunnel.ConfDir, obfuscator.ConfDir, kmod.ModulesDir
	t.Cleanup(func() {
		tunnel.ConfDir, obfuscator.ConfDir, kmod.ModulesDir = oldConf, oldObf, oldMod
	})

	dir := t.TempDir()
	applyDataDir(dir)

	for name, got := range map[string]string{
		"tunnel.ConfDir":     tunnel.ConfDir,
		"obfuscator.ConfDir": obfuscator.ConfDir,
		"kmod.ModulesDir":    kmod.ModulesDir,
	} {
		if !strings.HasPrefix(got, dir) {
			t.Errorf("%s остался вне каталога данных: %q", name, got)
		}
	}
	if want := filepath.Join(dir, "obfuscator"); obfuscator.ConfDir != want {
		t.Errorf("obfuscator.ConfDir = %q, ждали %q", obfuscator.ConfDir, want)
	}
}

// Пути роутера тоже уезжают в каталог данных: до правки демон в песочнице
// писал туда же, куда боевой, — ctclean.sh и правила netfilter.
func TestApplyDataDir_RedirectsRouterPaths(t *testing.T) {
	dir := t.TempDir()
	applyDataDir(dir)

	for name, got := range router.DataDirPaths() {
		if !strings.HasPrefix(got, dir) {
			t.Errorf("%s остался вне каталога данных: %q", name, got)
		}
	}
}

// Пустой флаг ничего не переставляет: боевые значения остаются как есть.
func TestApplyDataDir_EmptyKeepsDefaults(t *testing.T) {
	before := tunnel.ConfDir
	applyDataDir("")
	if tunnel.ConfDir != before {
		t.Errorf("пустой каталог данных переписал путь: %q", tunnel.ConfDir)
	}
}
