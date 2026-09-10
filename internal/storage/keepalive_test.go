package storage

import "testing"

// Effective — единственное правило, по которому keepalive доезжает до
// прошивки: диапазон AWG 3.0 схлопывается в нижнюю границу, а всё, чего
// прошивке не понять, не отправляется вовсе. Тот же список повторяет
// frontend/src/lib/utils/keepalive.ts — расхождение врёт в карточке.
func TestKeepaliveEffective(t *testing.T) {
	for _, tc := range []struct {
		in   Keepalive
		want int
		ok   bool
	}{
		{"25", 25, true},
		{"25-35", 25, true},
		{" 25 - 35 ", 25, true},
		{"65535", 65535, true},
		{"", 0, false},
		{"0", 0, false},
		{" 0 ", 0, false},
		// Нижняя граница 0 — тот же выключенный keepalive, что и "0".
		// ValidateKeepalive такое отвергает, но в записи оно ещё может лежать:
		// keepalive не валидирует ни импорт, ни файлы прошлых версий.
		{"0-80", 0, false},
		// Вне u16: `awg setconf` и NDMS такое не примут, слать нечего.
		{"65536", 0, false},
		{"70000-80000", 0, false},
		{"abc", 0, false},
		{"-5", 0, false},
		// U+FEFF (BOM): strings.TrimSpace пробелом его не считает, а JS trim()
		// снимает. Обе стороны обязаны отвечать одинаково, иначе карточка
		// покажет значение, которое бэкенд отвергнет целиком.
		{"\ufeff25", 0, false},
		{"25\ufeff", 0, false},
		// Формат отсекается ValidateKeepalive до записи; если такое всё же
		// попалось, берём нижнюю границу — она читается однозначно.
		{"22-", 22, true},
	} {
		got, ok := tc.in.Effective()
		if got != tc.want || ok != tc.ok {
			t.Errorf("Keepalive(%q).Effective() = (%d, %v), ждали (%d, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
