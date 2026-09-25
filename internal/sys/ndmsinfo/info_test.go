package ndmsinfo

import "testing"

func TestIsAtLeast501A3(t *testing.T) {
	tests := []struct {
		release string
		want    bool
	}{
		{"4.02.01.0-0", false},
		{"5.00.A.1.0-0", false},
		{"5.01.A.1.0-0", false},
		{"5.01.A.3.0-0", true},
		{"5.01.A.4.0-0", true},
		{"5.01.A.5.0-0", true},
		{"5.01.B.0.0-1", true},
		{"5.01.B.1.0-0", true},
		{"5.01.03.0-0", true},
		{"5.02.A.1.0-0", true},
		{"6.00.A.1.0-0", true},
		{"", false},
		{"5", false},
		{"5.01", false},
	}
	for _, tt := range tests {
		t.Run(tt.release, func(t *testing.T) {
			got := isAtLeast501A3(tt.release)
			if got != tt.want {
				t.Errorf("isAtLeast501A3(%q) = %v, want %v", tt.release, got, tt.want)
			}
		})
	}
}

// Порог ASC 3.x — 5.02.A.11 (стенд 5.02.A.11.0-1, 25.09.2026).
func TestIsAtLeast502A11(t *testing.T) {
	tests := []struct {
		release string
		want    bool
	}{
		{"5.01.C.3.0-1", false},
		{"5.01.03.0-0", false},
		{"5.02.A.10.0-0", false},
		{"5.02.A.11.0-1", true},
		{"5.02.A.12.0-0", true},
		{"5.02.B.1.0-0", true},
		{"5.02.02.0-0", true},
		{"5.03.A.1.0-0", true},
		{"6.00.A.1.0-0", true},
		{"5.02", false},
	}
	for _, tt := range tests {
		if got := isAtLeast502A11(tt.release); got != tt.want {
			t.Errorf("isAtLeast502A11(%q) = %v, want %v", tt.release, got, tt.want)
		}
	}
}
