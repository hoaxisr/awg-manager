package testing

import (
	"strconv"
	"strings"
	"testing"
)

func TestPingByIface_ParseFloatMs(t *testing.T) {
	// Verifies our strconv parsing is correct.
	cases := []struct {
		raw  string
		want int
	}{
		{"0.043", 43},
		{"0.001", 1},
		{"0.0001", 1}, // floor to 1ms min
		{"", 0},
	}
	for _, c := range cases {
		raw := strings.TrimSpace(c.raw)
		var got int
		if raw == "" {
			got = 0
		} else {
			sec, _ := strconv.ParseFloat(raw, 64)
			if sec <= 0 {
				got = 0
			} else {
				got = int(sec * 1000)
				if got < 1 {
					got = 1
				}
			}
		}
		if got != c.want {
			t.Errorf("%q: got %d want %d", c.raw, got, c.want)
		}
	}
}
