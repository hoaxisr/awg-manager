package staticroute

import (
	"testing"
)

func TestParseBat(t *testing.T) {
	content := `@echo off
rem This is a comment
route delete 0.0.0.0
route add 10.0.0.0 mask 255.0.0.0 192.168.1.1
route add 172.16.0.0 mask 255.240.0.0 192.168.1.1
ROUTE ADD 192.168.100.0 mask 255.255.255.0 10.0.0.1
echo Done
`

	subnets, parseErrors := ParseBat(content)

	if len(parseErrors) != 0 {
		t.Errorf("unexpected parse errors: %v", parseErrors)
	}

	expected := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.100.0/24",
	}

	if len(subnets) != len(expected) {
		t.Fatalf("got %d subnets, want %d: %v", len(subnets), len(expected), subnets)
	}

	for i, s := range subnets {
		if s != expected[i] {
			t.Errorf("subnet[%d] = %q, want %q", i, s, expected[i])
		}
	}
}

func TestParseBatNormalization(t *testing.T) {
	// IP 10.1.2.3 with /8 mask should normalize to 10.0.0.0/8
	content := "route add 10.1.2.3 mask 255.0.0.0 192.168.1.1\n"

	subnets, parseErrors := ParseBat(content)

	if len(parseErrors) != 0 {
		t.Errorf("unexpected parse errors: %v", parseErrors)
	}

	if len(subnets) != 1 || subnets[0] != "10.0.0.0/8" {
		t.Errorf("got %v, want [10.0.0.0/8]", subnets)
	}
}

func TestParseBatDuplicates(t *testing.T) {
	content := `route add 10.0.0.0 mask 255.0.0.0 192.168.1.1
route add 10.0.0.0 mask 255.0.0.0 192.168.1.2
`

	subnets, parseErrors := ParseBat(content)

	if len(parseErrors) != 0 {
		t.Errorf("unexpected parse errors: %v", parseErrors)
	}

	if len(subnets) != 1 {
		t.Errorf("got %d subnets, want 1 (dedup): %v", len(subnets), subnets)
	}

	if len(subnets) > 0 && subnets[0] != "10.0.0.0/8" {
		t.Errorf("got %q, want 10.0.0.0/8", subnets[0])
	}
}

func TestParseBatInvalidMask(t *testing.T) {
	content := "route add 10.0.0.0 mask 255.0.1.0 192.168.1.1\n"

	subnets, parseErrors := ParseBat(content)

	if len(subnets) != 0 {
		t.Errorf("expected no subnets for non-contiguous mask, got %v", subnets)
	}

	if len(parseErrors) != 1 {
		t.Fatalf("expected 1 parse error, got %d: %v", len(parseErrors), parseErrors)
	}

	if !contains(parseErrors[0], "non-contiguous") {
		t.Errorf("error should mention non-contiguous mask: %s", parseErrors[0])
	}
}

func TestParseBatTooFewFields(t *testing.T) {
	content := "route add 10.0.0.0\n"

	subnets, parseErrors := ParseBat(content)

	if len(subnets) != 0 {
		t.Errorf("expected no subnets, got %v", subnets)
	}

	if len(parseErrors) != 1 {
		t.Fatalf("expected 1 parse error, got %d: %v", len(parseErrors), parseErrors)
	}
}

func TestMaskToCIDR(t *testing.T) {
	tests := []struct {
		mask    string
		want    int
		wantOK  bool
	}{
		{"255.255.255.255", 32, true},
		{"255.255.255.0", 24, true},
		{"255.255.0.0", 16, true},
		{"255.0.0.0", 8, true},
		{"0.0.0.0", 0, true},
		{"255.255.128.0", 17, true},
		{"255.255.192.0", 18, true},
		{"255.255.255.128", 25, true},
		// Non-contiguous masks
		{"255.0.1.0", -1, false},
		{"255.0.255.0", -1, false},
		// Invalid
		{"not-a-mask", -1, false},
		{"", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.mask, func(t *testing.T) {
			got, ok := maskToCIDR(tt.mask)
			if ok != tt.wantOK {
				t.Errorf("maskToCIDR(%q) ok = %v, want %v", tt.mask, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("maskToCIDR(%q) = %d, want %d", tt.mask, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
