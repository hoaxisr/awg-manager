package kmod

import (
	"os"
	"path/filepath"
	"testing"
)

// prebuiltKmodDir — каталог с модулями относительно этого пакета.
const prebuiltKmodDir = "../../../prebuilt/kmod"

// Алиасы разрешаются в один шаг (см. selectBundledModule), поэтому цепочка
// "модель → алиас → алиас" молча не найдёт файл.
func TestModelAliasHasNoChains(t *testing.T) {
	for model, target := range modelAlias {
		if _, isAlias := modelAlias[target]; isAlias {
			t.Errorf("%s ссылается на %s, который сам является алиасом", model, target)
		}
	}
}

// Каждая цель алиаса должна иметь файл в prebuilt/kmod, иначе модель остаётся
// без модуля: selectBundledModule ничего не скопирует и промолчит.
func TestModelAliasTargetsExist(t *testing.T) {
	if _, err := os.Stat(prebuiltKmodDir); err != nil {
		t.Skip("prebuilt/kmod недоступен")
	}
	for model, target := range modelAlias {
		path := filepath.Join(prebuiltKmodDir, "amneziawg-"+target+".ko")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s ссылается на %s, но файла %s нет", model, target, filepath.Base(path))
		}
	}
}
