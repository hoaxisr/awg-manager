package ndms

import "testing"

func TestIsValidWireguardName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"Wireguard0", true},
		{"Wireguard1", true},
		{"Wireguard99", true},
		{"wireguard0", false},
		{"Wireguard", false},
		{"WireguardX", false},
		{"Wireguard-1", false},
		{"", false},
		{"OpkgTun0", false},
		{"nwg0", false},
		{"Wireguard0; rm -rf /", false},
	}
	for _, tt := range tests {
		if got := IsValidWireguardName(tt.name); got != tt.valid {
			t.Errorf("IsValidWireguardName(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}
