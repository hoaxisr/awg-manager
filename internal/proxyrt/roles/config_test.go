package roles

import "testing"

// Дефолт потоков зависит от архитектуры: на mips полосу отнимает CPU (замеры
// KN-1010 в комментарии DefaultWorkers), на arm64 стена дальше. Границы
// проверяются на точных значениях арок, а не на «не ноль».
func TestDefaultWorkers(t *testing.T) {
	cases := []struct {
		goarch string
		want   int
	}{
		{"mips", 9},
		{"mipsle", 9},
		{"mips64", 9},
		{"mips64le", 9},
		{"arm64", 27},
		{"amd64", 27},
	}
	for _, c := range cases {
		got := DefaultWorkers(c.goarch)
		if got != c.want {
			t.Errorf("DefaultWorkers(%q) = %d, want %d", c.goarch, got, c.want)
		}
		// Клиент округляет -n вниз до кратного девяти и поднимает до девяти
		// минимум: некратный дефолт молча урезался бы в процессе.
		if got < 9 || got%9 != 0 {
			t.Errorf("DefaultWorkers(%q) = %d: не кратно девяти либо меньше девяти", c.goarch, got)
		}
	}
}
