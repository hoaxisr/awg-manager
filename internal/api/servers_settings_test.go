package api

import "testing"

func TestDetectSystemServerNATMode(t *testing.T) {
	cases := []struct {
		nat, static bool
		want        string
	}{
		{true, false, "full"},
		{false, true, "internet-only"},
		{false, false, "none"},
		{true, true, "full"},
	}
	for _, c := range cases {
		if got := detectSystemServerNATMode(c.nat, c.static); got != c.want {
			t.Fatalf("detect(%v,%v) = %q, want %q", c.nat, c.static, got, c.want)
		}
	}
}

func TestIsValidServerEndpointHost(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"  ", true},
		{"203.0.113.42", true},
		{"vpn.example.com", true},
		{"router.keenetic.pro", true},
		{"not a host!", false},
		{"bad..domain", false},
	}
	for _, c := range cases {
		if got := isValidServerEndpointHost(c.in); got != c.want {
			t.Fatalf("isValidServerEndpointHost(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatWireguardEndpointHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"203.0.113.42", "203.0.113.42"},
		{"vpn.example.com", "vpn.example.com"},
		{"2001:db8::1", "[2001:db8::1]"},
		{"", ""},
	}
	for _, c := range cases {
		if got := formatWireguardEndpointHost(c.in); got != c.want {
			t.Fatalf("formatWireguardEndpointHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveWireguardClientEndpointHost(t *testing.T) {
	cases := []struct {
		stored, keenDNS, want string
	}{
		{"", "", ""},
		{" 203.0.113.1 ", "router.keenetic.pro", "203.0.113.1"},
		{"", "router.keenetic.pro", "router.keenetic.pro"},
		{"", "  ", ""},
		{"vpn.example.com", "router.keenetic.pro", "vpn.example.com"},
	}
	for _, c := range cases {
		if got := resolveWireguardClientEndpointHost(c.stored, c.keenDNS); got != c.want {
			t.Fatalf("resolve(%q, %q) = %q, want %q", c.stored, c.keenDNS, got, c.want)
		}
	}
}
