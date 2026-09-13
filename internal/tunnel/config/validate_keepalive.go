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

// ValidateKeepaliveSubmitted проверяет ПРИСЛАННОЕ значение и вдобавок к формату
// запрещает диапазон с нулевой нижней границей: 0 означает «keepalive
// выключен», диапазон — «случайное значение из отрезка на каждый взвод
// таймера», вместе они бессмысленны, и именно такое значение разводило пути
// (запись хранила диапазон, а прошивке не уходило ничего).
//
// Отдельно от ValidateKeepalive, потому что на слитую запись этот запрет
// накладывать нельзя: туннель, сохранённый с "0-80" до запрета, перестал бы
// правиться вообще — включая ту самую правку, которой keepalive и чинят.
// Тот же довод у валидаторов настроек в internal/api/settings_derive.go.
func ValidateKeepaliveSubmitted(k storage.Keepalive) error {
	if err := ValidateKeepalive(k); err != nil {
		return err
	}
	loStr, _, isRange := strings.Cut(string(k), "-")
	if !isRange {
		return nil
	}
	if lo, _ := strconv.ParseUint(strings.TrimSpace(loStr), 10, 16); lo == 0 { // формат уже проверен
		return fmt.Errorf("PersistentKeepalive: %q — нижняя граница 0 означает выключенный keepalive, диапазоном её задать нельзя", k)
	}
	return nil
}
