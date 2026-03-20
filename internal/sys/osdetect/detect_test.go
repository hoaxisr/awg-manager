package osdetect

import "testing"

func TestParseRelease(t *testing.T) {
	tests := []struct {
		input string
		major int
		minor int
		patch int
		valid bool
	}{
		{"5.1.3", 5, 1, 3, true},
		{"5.0.14", 5, 0, 14, true},
		{"4.2.1", 4, 2, 1, true},
		{"5.1", 5, 1, 0, true},
		{"5.1.0-alpha3", 5, 1, 0, true},
		{"", 0, 0, 0, false},
		{"abc", 0, 0, 0, false},
		{"5", 0, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			v := parseRelease(tt.input)
			if v.valid != tt.valid {
				t.Fatalf("valid = %v, want %v", v.valid, tt.valid)
			}
			if !v.valid {
				return
			}
			if v.major != tt.major || v.minor != tt.minor || v.patch != tt.patch {
				t.Errorf("got %d.%d.%d, want %d.%d.%d", v.major, v.minor, v.patch, tt.major, tt.minor, tt.patch)
			}
		})
	}
}

func TestParseReleaseAtLeastLogic(t *testing.T) {
	// Test the comparison logic directly via parsedVersion
	tests := []struct {
		release      string
		major, minor int
		want         bool
	}{
		{"5.1.3", 5, 1, true},   // 5.1 >= 5.1
		{"5.1.3", 5, 0, true},   // 5.1 >= 5.0
		{"5.0.14", 5, 1, false}, // 5.0 < 5.1
		{"5.0.14", 5, 0, true},  // 5.0 >= 5.0
		{"4.2.1", 5, 0, false},  // 4.x < 5.0
		{"5.2.0", 5, 1, true},   // 5.2 >= 5.1
		{"6.0.0", 5, 1, true},   // 6.0 >= 5.1
	}
	for _, tt := range tests {
		t.Run(tt.release, func(t *testing.T) {
			v := parseRelease(tt.release)
			if !v.valid {
				t.Fatal("expected valid parse")
			}
			var got bool
			if v.major != tt.major {
				got = v.major > tt.major
			} else {
				got = v.minor >= tt.minor
			}
			if got != tt.want {
				t.Errorf("AtLeast(%d, %d) for %s = %v, want %v", tt.major, tt.minor, tt.release, got, tt.want)
			}
		})
	}
}
