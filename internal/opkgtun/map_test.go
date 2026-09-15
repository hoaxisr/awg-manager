package opkgtun

import "testing"

// Потолок — факт прошивки, а не наше решение: на mips 16 создаётся, а 17
// отвергается («index 17 is too large for "OpkgTun" interface», стенд
// 25.08.2026). Поднимать границу mips нельзя без нового замера на железе.
func TestCeilingByArch(t *testing.T) {
	cases := []struct {
		arch string
		want int
	}{
		{"mips", 16},
		{"mipsle", 16},
		{"mips64", 16},
		{"mips64le", 16},
		{"arm", 49},
		{"arm64", 49},
		{"amd64", 49},
	}
	for _, c := range cases {
		if got := Ceiling(c.arch); got != c.want {
			t.Fatalf("Ceiling(%q) = %d, ожидали %d", c.arch, got, c.want)
		}
	}
}
