package managed

import (
	"net"
	"strings"
	"testing"
)

// TestValidatePeerTunnelIP locks the canonical peer-IP rule set used by AddPeer,
// restore preflight and merge preflight. Regression: preflight paths previously
// lacked the network/broadcast carve-out that AddPeer had, so a broadcast IP was
// accepted on restore but rejected on AddPeer.
func TestValidatePeerTunnelIP(t *testing.T) {
	subnet, err := parseManagedSubnet("10.0.0.0", "24")
	if err != nil {
		t.Fatal(err)
	}
	serverIP := net.ParseIP("10.0.0.1")

	cases := []struct {
		ip      string
		wantErr string // substring, "" = must pass
	}{
		{"10.0.0.2", ""},
		{"10.0.0.0", "network address"},
		{"10.0.0.255", "broadcast address"},
		{"10.0.0.1", "server's own address"},
		{"10.9.0.5", "not in server subnet"},
	}
	for _, c := range cases {
		ip, _, perr := net.ParseCIDR(c.ip + "/32")
		if perr != nil {
			t.Fatalf("parse %s: %v", c.ip, perr)
		}
		err := validatePeerTunnelIP(subnet, serverIP, ip)
		if c.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.ip, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: got %v, want substring %q", c.ip, err, c.wantErr)
		}
	}

	// /31 has no network/broadcast addresses — both hosts are valid.
	p2p, _ := parseManagedSubnet("10.0.0.0", "31")
	for _, h := range []string{"10.0.0.0", "10.0.0.1"} {
		ip, _, _ := net.ParseCIDR(h + "/32")
		if err := validatePeerTunnelIP(p2p, nil, ip); err != nil {
			t.Errorf("/31 host %s rejected: %v", h, err)
		}
	}
}
