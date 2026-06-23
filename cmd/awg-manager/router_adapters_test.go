package main

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// filterBindable must offer egress interfaces (security-level "public") minus
// our own auto-managed ones and minus interfaces already bound by an existing
// direct outbound, while rescuing KeenOS-native proxies in the native set (#323).
func TestFilterBindable(t *testing.T) {
	ifaces := []ndms.AllInterface{
		{Name: "t2s0", SecurityLevel: "public", Type: "Proxy", Label: "My-Socks5"}, // native, free — keep
		{Name: "t2s1", SecurityLevel: "public", Type: "Proxy", Label: "ours"},      // our sing-box proxy — drop
		{Name: "t2s2", SecurityLevel: "public", Type: "Proxy"},                     // native but occupied — drop
		{Name: "ipsec0", SecurityLevel: "public", Type: "IPSec"},                   // user VPN, free — keep
		{Name: "ppp0", SecurityLevel: "public", Type: "PPPoE"},                     // occupied — drop
		{Name: "Home", SecurityLevel: "private", Type: "Bridge"},                   // LAN bridge — drop (private)
		{Name: "opkgtun0", SecurityLevel: "public", Type: "Wireguard"},             // managed AWG — drop
	}
	native := map[string]bool{"t2s0": true, "t2s2": true}
	occupied := map[string]bool{"ppp0": true, "t2s2": true}
	got := filterBindable(ifaces, native, occupied)

	names := map[string]bool{}
	for _, g := range got {
		names[g.Name] = true
	}
	for _, want := range []string{"t2s0", "ipsec0"} {
		if !names[want] {
			t.Errorf("expected %q kept, missing from %v", want, names)
		}
	}
	for _, drop := range []string{"t2s1", "t2s2", "ppp0", "Home", "opkgtun0"} {
		if names[drop] {
			t.Errorf("expected %q dropped, still present in %v", drop, names)
		}
	}
}
