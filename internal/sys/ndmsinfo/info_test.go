package ndmsinfo

import "testing"

func TestSupportsASC(t *testing.T) {
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
			got := supportsASC(tt.release)
			if got != tt.want {
				t.Errorf("supportsASC(%q) = %v, want %v", tt.release, got, tt.want)
			}
		})
	}
}
