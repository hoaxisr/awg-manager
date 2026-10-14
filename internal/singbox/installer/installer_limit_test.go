package installer

import "testing"

func TestDownloadByteLimit(t *testing.T) {
	const (
		fallback = 128 << 20
		reserve  = 16 << 20
	)
	cases := []struct {
		name   string
		free   int64
		freeOK bool
		ratio  int64
		want   int64
	}{
		{"free unknown → fallback", 0, false, 1, fallback},
		{"plenty (external storage, GBs)", 4 << 30, true, 1, (4 << 30) - reserve},
		{"enough for a 72MB binary", 200 << 20, true, 1, (200 << 20) - reserve},
		{"tiny disk → below a 72MB binary", 40 << 20, true, 1, (40 << 20) - reserve},
		{"free below reserve → clamps to 0", 8 << 20, true, 1, 0},
		{"ubifs: сеть ×2 к месту на диске", 40 << 20, true, 2, ((40 << 20) - reserve) * 2},
		{"ubifs, free unknown → fallback", 0, false, 2, fallback},
	}
	for _, c := range cases {
		got := downloadByteLimit(c.free, c.freeOK, fallback, reserve, c.ratio)
		if got != c.want {
			t.Errorf("%s: downloadByteLimit(%d,%v) = %d, want %d", c.name, c.free, c.freeOK, got, c.want)
		}
	}
}
