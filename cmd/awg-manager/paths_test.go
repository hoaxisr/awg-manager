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
	restorePaths(t)

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
	restorePaths(t)

	dir := t.TempDir()
	applyDataDir(dir)

	for name, got := range router.DataDirPaths() {
		if !strings.HasPrefix(got, dir) {
			t.Errorf("%s остался вне каталога данных: %q", name, got)
		}
	}
	// Хук ndm лежит вне каталога данных, но его тело ссылается на эти файлы:
	// песочница обязана увести и его, иначе перепишет боевой скрипт ссылками
	// на временный каталог.
	if got := router.NetfilterHookPath(); !strings.HasPrefix(got, dir) {
		t.Errorf("хук ndm остался боевым при небоевом каталоге данных: %q", got)
	}
}

// Боевой каталог оставляет хук ndm на месте: иначе демон перестал бы ставить
// его туда, откуда его читает роутер.
func TestApplyDataDir_ProductionKeepsHook(t *testing.T) {
	restorePaths(t)

	applyDataDir(defaultDataDir)

	if got := router.NetfilterHookPath(); got != "/opt/etc/ndm/netfilter.d/50-awgm-tproxy.sh" {
		t.Errorf("боевой хук уехал: %q", got)
	}
}

// restorePaths возвращает все глобальные каталоги после теста: иначе
// следующий тест пакета получит пути в удалённый TempDir.
func restorePaths(t *testing.T) {
	t.Helper()
	conf, obf, mod, hook := tunnel.ConfDir, obfuscator.ConfDir, kmod.ModulesDir, router.NetfilterHookPath()
	routerPaths := router.DataDirPaths()
	t.Cleanup(func() {
		tunnel.ConfDir, obfuscator.ConfDir, kmod.ModulesDir = conf, obf, mod
		router.SetNetfilterHookPath(hook)
		router.RestoreDataDirPaths(routerPaths)
	})
}
