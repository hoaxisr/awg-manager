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

func TestHasComponent(t *testing.T) {
	mu.Lock()
	cached = &VersionInfo{
		NDW: struct {
			Components string `json:"components"`
		}{
			Components: "acl,base,dhcpd,pingcheck,ppe,wireguard,zerotier",
		},
	}
	mu.Unlock()
	t.Cleanup(Reset)

	tests := []struct {
		name string
		want bool
	}{
		{"wireguard", true},
		{"pingcheck", true},
		{"acl", true},
		{"zerotier", true},       // last element
		{"base", true},            // second element, edge at ","
		{"nonexistent", false},
		{"wire", false},           // substring, must not match
		{"guard", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasComponent(tt.name); got != tt.want {
				t.Errorf("HasComponent(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestHasComponent_EmptyCache(t *testing.T) {
	Reset()
	if HasComponent("wireguard") {
		t.Error("HasComponent should return false when cache is empty")
	}
}

func TestHasWireguardComponent(t *testing.T) {
	mu.Lock()
	cached = &VersionInfo{
		NDW: struct {
			Components string `json:"components"`
		}{Components: "base,wireguard,opkg"},
	}
	mu.Unlock()
	t.Cleanup(Reset)

	if !HasWireguardComponent() {
		t.Error("expected HasWireguardComponent() = true when 'wireguard' is in list")
	}

	mu.Lock()
	cached.NDW.Components = "base,opkg"
	mu.Unlock()

	if HasWireguardComponent() {
		t.Error("expected HasWireguardComponent() = false when 'wireguard' is absent")
	}
}

func TestHasPingCheckComponent(t *testing.T) {
	mu.Lock()
	cached = &VersionInfo{
		NDW: struct {
			Components string `json:"components"`
		}{Components: "base,pingcheck,opkg"},
	}
	mu.Unlock()
	t.Cleanup(Reset)

	if !HasPingCheckComponent() {
		t.Error("expected HasPingCheckComponent() = true when 'pingcheck' is in list")
	}

	mu.Lock()
	cached.NDW.Components = "base,opkg"
	mu.Unlock()

	if HasPingCheckComponent() {
		t.Error("expected HasPingCheckComponent() = false when 'pingcheck' is absent")
	}
}
