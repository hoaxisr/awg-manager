package kmod

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// Модель вне поставки: bundled сносится, и без этой записи в журнале роутер
// молча остаётся на том модуле, что уже стоит (или вовсе без него).
func TestSelectBundledModuleWarnsOnUnknownModel(t *testing.T) {
	dir := t.TempDir()
	setModulesDir(t, dir)
	if err := os.MkdirAll(BundledDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(BundledDir(), "amneziawg-KN-1810.ko"), []byte("ko"), 0644); err != nil {
		t.Fatal(err)
	}

	var warned []string
	l := &Loader{model: "KN-1613", Warn: func(m string) { warned = append(warned, m) }}
	l.selectBundledModule()

	if len(warned) != 1 || !strings.Contains(warned[0], "KN-1613") {
		t.Errorf("ожидали одну запись с моделью, получили %v", warned)
	}
	if _, err := os.Stat(BundledDir()); !os.IsNotExist(err) {
		t.Error("bundled должен быть снесён")
	}
}

// Совпадение по алиасу — не повод для предупреждения.
func TestSelectBundledModuleSilentOnAliasHit(t *testing.T) {
	dir := t.TempDir()
	setModulesDir(t, dir)
	if err := os.MkdirAll(BundledDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(BundledDir(), "amneziawg-KN-1810.ko"), []byte("ko"), 0644); err != nil {
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
	old := ModulesDir
	ModulesDir = dir
	t.Cleanup(func() { ModulesDir = old })
}

// stubRunCmd подменяет lsmod/insmod: lsmod пуст, insmod отказывает — тест
// видит, дошло ли дело до insmod, не трогая модули хоста.
func stubRunCmd(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	old := runCmd
	runCmd = func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		calls = append(calls, name)
		if name == "insmod" {
			return &exec.Result{}, errors.New("stub insmod")
		}
		return &exec.Result{}, nil
	}
	t.Cleanup(func() { runCmd = old })
	return &calls
}

// #953: модуль с меткой другой модели (перенос /opt, бэкап прежних версий)
// не грузится — insmod принял бы чужую конфигурацию ядра молча. Без метки —
// установка прежней версии или модель вне поставки: грузится, как раньше,
// иначе у них отвалился бы рабочий kernel-режим. Модель неизвестна — сверять
// не с чем.
func TestEnsureModule_RefusesOnlyModuleOfAnotherModel(t *testing.T) {
	cases := []struct {
		name, model, marker string
		wantInsmod          bool
	}{
		{"метка чужой модели", "KN-4210", "KN-1811", false},
		{"метки нет — прежняя установка", "KN-4210", "", true},
		{"метка своей модели", "KN-4210", "KN-4210", true},
		{"модель неизвестна", "", "KN-1811", true},
		{"перенос внутри группы модуля", "KN-1010", "KN-1810", true},
		{"перенос в другую группу", "KN-1010", "KN-1811", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			setModulesDir(t, dir)
			calls := stubRunCmd(t)
			ko := filepath.Join(dir, "amneziawg.ko")
			if err := os.WriteFile(ko, []byte("ko"), 0644); err != nil {
				t.Fatal(err)
			}
			if tc.marker != "" {
				if err := writeModel(tc.marker); err != nil {
					t.Fatal(err)
				}
			}
			l := &Loader{model: tc.model, modulePath: ko}
			err := l.EnsureModule(context.Background())
			insmod := strings.Contains(strings.Join(*calls, " "), "insmod")
			if insmod != tc.wantInsmod {
				t.Errorf("insmod вызван = %v, want %v (err: %v)", insmod, tc.wantInsmod, err)
			}
			if !tc.wantInsmod && (err == nil || !strings.Contains(err.Error(), "refusing")) {
				t.Errorf("ожидался отказ с причиной, got %v", err)
			}
		})
	}
}

// Модель вне поставки: модуль, что уже стоит без метки, при обновлении пакета
// получает метку этой модели; чужую метку обновление не перетирает.
func TestSelectBundledModule_AdoptsUnmarkedModuleWhenNoMatch(t *testing.T) {
	for _, tc := range []struct{ name, marker, want string }{
		{"без метки — усыновляется", "", "KN-1310"},
		{"чужая метка остаётся", "KN-1811", "KN-1811"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			setModulesDir(t, dir)
			ko := filepath.Join(dir, "amneziawg.ko")
			if err := os.WriteFile(ko, []byte("ko"), 0644); err != nil {
				t.Fatal(err)
			}
			if tc.marker != "" {
				if err := writeModel(tc.marker); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(BundledDir(), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(BundledDir(), "amneziawg-KN-1810.ko"), []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
			l := &Loader{model: "KN-1310", modulePath: ko}
			l.selectBundledModule()
			if got := readModel(); got != tc.want {
				t.Errorf("метка = %q, want %q", got, tc.want)
			}
		})
	}
}

// Метка — реальная модель роутера, а не цель алиаса: KN-4210 грузит файл
// KN-1812, и сверка в EnsureModule идёт с моделью роутера.
func TestSelectBundledModule_WritesRouterModelMarker(t *testing.T) {
	dir := t.TempDir()
	setModulesDir(t, dir)
	if err := os.MkdirAll(BundledDir(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(BundledDir(), "amneziawg-KN-1812.ko"), []byte("ko"), 0644); err != nil {
		t.Fatal(err)
	}
	l := &Loader{model: "KN-4210"}
	l.selectBundledModule()
	if got := readModel(); got != "KN-4210" {
		t.Errorf("метка = %q, want KN-4210", got)
	}
}

// Копия модуля не удалась — прежняя (чужая) метка остаётся при прежнем
// модуле, и EnsureModule его не грузит (#953). Метка удачно скопированного
// модуля, которую не удалось записать, снимается: чужая при своём модуле
// запретила бы загрузку.
func TestSelectBundledModule_MarkerFollowsSuccessfulCopy(t *testing.T) {
	t.Run("копия не удалась", func(t *testing.T) {
		dir := t.TempDir()
		setModulesDir(t, dir)
		calls := stubRunCmd(t)
		ko := filepath.Join(dir, "amneziawg.ko")
		if err := os.WriteFile(ko, []byte("foreign"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := writeModel("KN-1811"); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(BundledDir(), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(BundledDir(), "amneziawg-KN-1812.ko"), []byte("ko"), 0644); err != nil {
			t.Fatal(err)
		}
		// Копия не встанет: на её временном имени — каталог.
		if err := os.MkdirAll(ko+".tmp/x", 0755); err != nil {
			t.Fatal(err)
		}
		l := &Loader{model: "KN-4210", modulePath: ko}
		_ = l.EnsureModule(context.Background())
		if got := readModel(); got != "KN-1811" {
			t.Errorf("метка = %q, want прежняя KN-1811", got)
		}
		if strings.Contains(strings.Join(*calls, " "), "insmod") {
			t.Error("чужой модуль отправлен в insmod")
		}
	})
	t.Run("метка не записалась", func(t *testing.T) {
		dir := t.TempDir()
		setModulesDir(t, dir)
		if err := writeModel("KN-1811"); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(BundledDir(), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(BundledDir(), "amneziawg-KN-1812.ko"), []byte("ko"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, modelFile+".tmp", "x"), 0755); err != nil {
			t.Fatal(err)
		}
		l := &Loader{model: "KN-4210"}
		l.selectBundledModule()
		if got := readModel(); got != "" {
			t.Errorf("при своём модуле осталась метка %q", got)
		}
	})
}
