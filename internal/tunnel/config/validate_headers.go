package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ErrHeaderFormat — H-значение не разобрать (не число и не диапазон). Отличать
// его от пересечения нужно вызывающим: анализатор отдаёт разные коды ошибок.
var ErrHeaderFormat = errors.New("некорректное значение H")

// headerRange — H-значение как отрезок [lo, hi]: одиночное число — вырожденный
// отрезок, "min-max" — диапазон AWG 2.0.
type headerRange struct {
	lo, hi uint64
}

func parseHeaderRange(name, v string) (headerRange, error) {
	v = strings.TrimSpace(v)
	if lo, hi, ok := strings.Cut(v, "-"); ok {
		a, err1 := strconv.ParseUint(strings.TrimSpace(lo), 10, 32)
		b, err2 := strconv.ParseUint(strings.TrimSpace(hi), 10, 32)
		if err1 != nil || err2 != nil || a > b {
			return headerRange{}, fmt.Errorf("%w: %s = %q (ожидается число или диапазон min-max)", ErrHeaderFormat, name, v)
		}
		return headerRange{a, b}, nil
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return headerRange{}, fmt.Errorf("%w: %s = %q (ожидается число или диапазон min-max)", ErrHeaderFormat, name, v)
	}
	return headerRange{n, n}, nil
}

// ValidateHeaderRanges повторяет проверку модуля ядра: setconf отвергается,
// если отрезки H1–H4 пересекаются (`u32_range_overlap` в netlink.c,
// wg_set_device). Равные одиночные значения — частный случай пересечения.
// Незаданный H модуль берёт из текущего значения устройства — дефолта
// WireGuard 1/2/3/4 — и проверяет пересечение с ним, поэтому пустые H
// подставляются дефолтами, а не пропускаются.
func ValidateHeaderRanges(o *storage.AWGObfuscation) error {
	if o == nil {
		return nil
	}
	names := [4]string{"H1", "H2", "H3", "H4"}
	defaults := [4]string{"1", "2", "3", "4"}
	values := [4]string{o.H1, o.H2, o.H3, o.H4}
	var ranges [4]headerRange
	for i, v := range values {
		if strings.TrimSpace(v) == "" {
			values[i] = defaults[i]
		}
		r, err := parseHeaderRange(names[i], values[i])
		if err != nil {
			return err
		}
		ranges[i] = r
	}
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			a, b := ranges[i], ranges[j]
			if a.lo <= b.hi && b.lo <= a.hi {
				return fmt.Errorf("%s и %s пересекаются (%s, %s): модуль отвергает такой конфиг, значения H1-H4 не должны перекрываться (незаданный H = дефолт %s)",
					names[i], names[j], values[i], values[j], strings.Join(defaults[:], "/"))
			}
		}
	}
	return nil
}

// ValidateObfuscation — общий гейт импорта, create и update: правила AWG 3.0
// (ключ header protection, S1-S4 ≥ 12) и пересечение H1-H4. Анализатор
// вызывает обе проверки по отдельности ради разных кодов ошибок.
func ValidateObfuscation(o *storage.AWGObfuscation) error {
	if err := ValidateAWG3(o); err != nil {
		return err
	}
	return ValidateHeaderRanges(o)
}
