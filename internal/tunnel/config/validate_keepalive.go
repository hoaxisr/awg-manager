package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ValidateKeepalive проверяет PersistentKeepalive: одиночное значение или
// диапазон "min-max" в границах u16, как их разбирают `awg setconf` и
// amneziawg-go. Пустая строка допустима — при генерации подставится дефолт.
func ValidateKeepalive(k storage.Keepalive) error {
	if k == "" {
		return nil
	}
	loStr, hiStr, isRange := strings.Cut(string(k), "-")
	lo, err := strconv.ParseUint(strings.TrimSpace(loStr), 10, 16)
	if err != nil {
		return fmt.Errorf("PersistentKeepalive: %q не число и не диапазон min-max", k)
	}
	if !isRange {
		return nil
	}
	hi, err := strconv.ParseUint(strings.TrimSpace(hiStr), 10, 16)
	if err != nil {
		return fmt.Errorf("PersistentKeepalive: %q не число и не диапазон min-max", k)
	}
	if hi < lo {
		return fmt.Errorf("PersistentKeepalive: верхняя граница %d меньше нижней %d", hi, lo)
	}
	return nil
}

// ValidateKeepaliveForBackend вдобавок запрещает диапазон на NativeWG: туда
// keepalive уходит числом через ASC/NDMS, и диапазонов прошивка не понимает.
func ValidateKeepaliveForBackend(k storage.Keepalive, backend string) error {
	if err := ValidateKeepalive(k); err != nil {
		return err
	}
	if backend == "nativewg" && k.IsRange() {
		return fmt.Errorf("PersistentKeepalive: диапазон %q работает только в режиме kernel с модулем AmneziaWG 3.0, NativeWG принимает одно число", k)
	}
	return nil
}
