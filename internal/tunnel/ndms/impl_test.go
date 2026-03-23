package ndms

import (
	"testing"
)

func TestSanitizeDescription(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Hello World", "Hello-World"},
		{"test-tunnel", "test-tunnel"},
		{"test_tunnel", "test_tunnel"},
		{"Special!@#$%^&*()", "Special"},
		{"Mixed 123 Test", "Mixed-123-Test"},
		{"", ""},
		{"   ", "---"},
		{"Тест", ""},
		{"Test Тест", "Test-"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := sanitizeDescription(tt.input); got != tt.want {
				t.Errorf("sanitizeDescription(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsNonISPInterface(t *testing.T) {
	tests := []struct {
		name     string
		isTunnel bool
	}{
		{"opkgtun0", true},
		{"OpkgTun1", true},
		{"awg0", true},
		{"AWG5", true},
		{"nwg0", true},
		{"wg0", true},
		{"Wireguard0", true},
		{"wireguard1", true},
		{"IPSec0", true},
		{"SSTP1", true},
		{"OpenVPN0", true},
		{"PPTP0", false},
		{"pptp1", false},
		{"EoIP0", false},
		{"eoip1", false},
		{"GRE0", false},
		{"gre1", false},
		{"IPIP0", false},
		{"ipip1", false},
		{"PPPoE1", false},
		{"eth0", false},
		{"WifiMaster0", false},
		{"Bridge0", false},
		{"GigabitEthernet0", false},
		{"ISP", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNonISPInterface(tt.name); got != tt.isTunnel {
				t.Errorf("isNonISPInterface(%q) = %v, want %v", tt.name, got, tt.isTunnel)
			}
		})
	}
}

func TestNew(t *testing.T) {
	c := New()
	if c == nil {
		t.Error("New() returned nil")
	}
	if c.rci == nil {
		t.Error("rci client not set")
	}
}
