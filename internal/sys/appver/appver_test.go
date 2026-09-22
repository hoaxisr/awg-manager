package appver

import "testing"

func TestSet(t *testing.T) {
	t.Cleanup(func() { ua = "awgm/dev" })

	tests := []struct {
		version, arch, want string
	}{
		{"2.19.7", "mipsel-3.4", "awgm/2.19.7 (mipsel-3.4)"},
		{"2.19.7", "", "awgm/2.19.7"},
	}
	for _, tc := range tests {
		Set(tc.version, tc.arch)
		if UA() != tc.want {
			t.Errorf("Set(%q, %q) = %q, want %q", tc.version, tc.arch, UA(), tc.want)
		}
	}
}
