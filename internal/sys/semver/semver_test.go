package semver

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// equal
		{"1.0.0", "1.0.0", 0},
		{"2.3.11", "2.3.11", 0},
		// missing components treated as 0
		{"1.2", "1.2.0", 0},
		{"1.2.0", "1.2", 0},
		{"", "0.0.0", 0},
		{"1", "1.0.0", 0},
		{"1.2.3", "1.2.3.0", 0},
		// cross-digit carry-over (real-world release tags)
		{"2.3.10", "2.3.11", -1},
		{"2.3.11", "2.3.10", 1},
		{"2.7.10", "2.7.3", 1},
		{"10.0.0", "9.99.99", 1},
		// simple less/greater
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.99.99", "2.0.0", -1},
		{"0.0.1", "0.0.2", -1},
		{"1.2.3.1", "1.2.3", 1},
		{"2.4.0", "2.3.99", 1},
		// non-numeric component parses as 0 (per contract)
		{"1.x.3", "1.0.3", 0},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
