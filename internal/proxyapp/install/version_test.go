package install

import "testing"

func TestCompareFreeturnVersion_Revision(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.8.0-2", "1.8.0-3", -1}, // баг, который чинит фикс: semver.Compare даёт 0
		{"1.8.0-3", "1.8.0-2", 1},
		{"1.8.0-3", "1.8.0-3", 0},
		{"1.8.0", "1.8.0-1", -1},  // нет суффикса → ревизия 0
		{"1.8.1-1", "1.8.0-9", 1}, // разные базы решает semver, ревизия не важна
	}
	for _, c := range cases {
		if got := compareFreeturnVersion(c.a, c.b); got != c.want {
			t.Errorf("compareFreeturnVersion(%q,%q)=%d, want %d", c.a, c.b, got, c.want)
		}
	}
}
