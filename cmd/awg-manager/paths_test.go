package main

import (
	"os"
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

	// Полные пути: «внутри каталога» прошло бы и для раскладки, в которой
	// .conf туннелей уехали бы в подкаталог и осиротели.
	for name, pair := range map[string][2]string{
		"tunnel.ConfDir":     {tunnel.ConfDir, dir},
		"obfuscator.ConfDir": {obfuscator.ConfDir, filepath.Join(dir, "obfuscator")},
		"kmod.ModulesDir":    {kmod.ModulesDir, filepath.Join(dir, "modules")},
		"kmod.BundledDir":    {kmod.BundledDir(), filepath.Join(dir, "modules", "bundled")},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, ждали %q", name, pair[0], pair[1])
		}
	}
}

// Пути роутера тоже уезжают в каталог данных: до правки демон в песочнице
// писал туда же, куда боевой, — ctclean.sh и правила netfilter.
func TestApplyDataDir_RedirectsRouterPaths(t *testing.T) {
	restorePaths(t)

	dir := t.TempDir()
	applyDataDir(dir)

	// Сверяем ПОЛНЫЕ пути, а не префикс: copy-paste, отправивший два разных
	// набора правил в один файл, префикс проходит, а на роутере nat и mangle
	// затирают друг друга.
	sub := filepath.Join(dir, "singbox")
	want := map[string]string{
		"netfilterRulesPath":       filepath.Join(sub, "router-netfilter.rules"),
		"netfilterBlackholePath":   filepath.Join(sub, "router-blackhole.rules"),
		"netfilterMangleRulesPath": filepath.Join(sub, "router-netfilter-mangle.rules"),
		"netfilterNatRulesPath":    filepath.Join(sub, "router-netfilter-nat.rules"),
		"netfilterCtCleanPath":     filepath.Join(sub, "awgm-ctclean.sh"),
		"bypassSavePath":           filepath.Join(sub, "bypass.ipset"),
	}
	got := router.DataDirPaths()
	if len(got) != len(want) {
		t.Fatalf("инвентарь путей разошёлся: %d против %d", len(got), len(want))
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, ждали %q", name, got[name], w)
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

// Каталог раздаётся ДО путей, которые им пользуются: --cleanup удаляет конфиги
// релея по obfuscator.ConfDir, и вызов, съехавший ниже по main, стёр бы боевые
// файлы при запуске в песочнице. Проверяем по исходнику: порядок здесь и есть
// поведение, а другого способа его зафиксировать у main() нет.
func TestApplyDataDir_RunsBeforeCleanupAndService(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	apply := strings.Index(text, "applyDataDir(*dataDir)")
	if apply < 0 {
		t.Fatal("вызов applyDataDir пропал из main")
	}
	for _, later := range []string{"runCleanup(*dataDir)", "runService(*serviceAction, *dataDir)"} {
		if i := strings.Index(text, later); i < 0 {
			t.Errorf("не найден вызов %s", later)
		} else if i < apply {
			t.Errorf("%s стоит ДО applyDataDir — каталог данных ещё не роздан", later)
		}
	}
}
