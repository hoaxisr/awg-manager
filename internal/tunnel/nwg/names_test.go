package nwg

import "testing"

func TestNWGNames(t *testing.T) {
	n := NewNWGNames(3)
	if n.Index != 3 {
		t.Errorf("Index = %d, want 3", n.Index)
	}
	if n.NDMSName != "Wireguard3" {
		t.Errorf("NDMSName = %q, want Wireguard3", n.NDMSName)
	}
	if n.IfaceName != "nwg3" {
		t.Errorf("IfaceName = %q, want nwg3", n.IfaceName)
	}
}

func TestNWGNamesZero(t *testing.T) {
	n := NewNWGNames(0)
	if n.NDMSName != "Wireguard0" {
		t.Errorf("NDMSName = %q, want Wireguard0", n.NDMSName)
	}
	if n.IfaceName != "nwg0" {
		t.Errorf("IfaceName = %q, want nwg0", n.IfaceName)
	}
}

func TestParseNDMSCreatedName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantIdx  int
		wantName string
		wantErr  bool
	}{
		{"single digit", `Network::Interface::Repository: "Wireguard3" interface created.`, 3, "Wireguard3", false},
		{"double digit", `Network::Interface::Repository: "Wireguard10" interface created.`, 10, "Wireguard10", false},
		{"zero index", `Network::Interface::Repository: "Wireguard0" interface created.`, 0, "Wireguard0", false},
		{"garbage", "some random output", 0, "", true},
		{"empty", "", 0, "", true},
		{"non-wireguard", `Network::Interface::Repository: "OpkgTun0" interface created.`, 0, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, name, err := ParseNDMSCreatedName(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got idx=%d name=%q", idx, name)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if idx != tt.wantIdx {
				t.Errorf("index = %d, want %d", idx, tt.wantIdx)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}
