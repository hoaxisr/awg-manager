package opkg

import (
	"strings"
	"testing"
)

// Поиск уходит в exec тем же путём, что install: аргумент, начинающийся с
// дефиса, opkg прочитает как флаг.
func TestSearch_RejectsFlagLikeQuery(t *testing.T) {
	c := NewClient()
	for _, q := range []string{"-f /tmp/my.conf", "--force-reinstall", "/tmp/evil.ipk"} {
		_, err := c.Search(q)
		if err == nil {
			t.Errorf("запрос %q принят", q)
			continue
		}
		// Отказ должен быть от валидации, а не от «opkg не установлен»:
		// иначе тест зеленеет на машине без Entware по чужой причине.
		if !strings.Contains(err.Error(), "invalid package name") {
			t.Errorf("запрос %q отклонён не валидацией: %v", q, err)
		}
	}
}

func TestValidatePkgNames(t *testing.T) {
	if err := validatePkgNames([]string{"curl", "libopenssl1.1", "zoneinfo-core"}); err != nil {
		t.Errorf("нормальные имена отвергнуты: %v", err)
	}
	for _, bad := range []string{"--force-depends", "-f", "/tmp/x.ipk", "pkg;rm -rf /", ""} {
		if err := validatePkgNames([]string{bad}); err == nil {
			t.Errorf("имя %q принято", bad)
		}
	}
}
