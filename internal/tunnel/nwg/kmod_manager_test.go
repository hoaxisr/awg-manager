package nwg

import "testing"

func TestCompareKmodVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.4.4", "2.4.4", 0},
		{"2.4.3", "2.4.4", -1},
		{"2.4.5", "2.4.4", 1},
		{"2.5.0", "2.4.4", 1},
		{"1.0.0", "2.0.0", -1},
		{"2.4", "2.4.0", 0},
	}
	for _, tt := range tests {
		got := compareKmodVersions(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("compareKmodVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestPubKeyToHex(t *testing.T) {
	key := "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXoxMjM0NTY="
	hex := pubKeyToHex(key)
	if len(hex) != 64 {
		t.Errorf("pubKeyToHex: expected 64 hex chars, got %d", len(hex))
	}
	if got := pubKeyToHex("invalid"); got != "" {
		t.Errorf("pubKeyToHex(invalid) = %q, want empty", got)
	}
}

func TestBuildProcLine(t *testing.T) {
	cfg := KmodConfig{
		EndpointIP:   "1.2.3.4",
		EndpointPort: 51820,
		H1: "1", H2: "2", H3: "3", H4: "4",
		S1: 10, S2: 20, S3: 0, S4: 0,
		Jc: 5, Jmin: 50, Jmax: 1000,
	}
	line := buildProcLine(cfg)
	if line == "" {
		t.Error("buildProcLine returned empty")
	}
	expected := "1.2.3.4:51820"
	if len(line) < len(expected) || line[:len(expected)] != expected {
		t.Errorf("buildProcLine prefix = %q, want prefix %q", line[:20], expected)
	}
}
