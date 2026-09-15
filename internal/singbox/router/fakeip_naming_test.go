package router

import (
	"fmt"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
)

// TestFakeIPNames pins the EXACT OpkgTun naming convention for every номер,
// который режим роутера может получить: NDMS name "OpkgTun<i>" (CamelCase,
// required by NDMS RCI) and kernel iface name "opkgtun<i>" (lowercase). These
// derive from tunnel.NewNames now, but the wire strings must stay
// byte-identical — a single character change here previously broke the entire
// feature (NDMS rejects the lowercase name with "unsupported interface type").
// This is the safety net.
//
// Диапазон — весь пул до потолка, а не прежнее окно 0..9: с #891 режим роутера
// поднимается доверху, и страж, оставшийся на девятке, молча перестал бы
// охранять имена, которые режим теперь получает.
func TestFakeIPNames(t *testing.T) {
	for i := 0; i <= opkgtun.Ceiling("arm64"); i++ {
		wantNDMS := fmt.Sprintf("OpkgTun%d", i)
		wantIface := fmt.Sprintf("opkgtun%d", i)
		if got := tunNDMSName(i); got != wantNDMS {
			t.Errorf("tunNDMSName(%d) = %q, want %q", i, got, wantNDMS)
		}
		if got := tunIfaceName(i); got != wantIface {
			t.Errorf("tunIfaceName(%d) = %q, want %q", i, got, wantIface)
		}
	}
}

// TestSplitCIDRToAddrMask pins the dotted-quad netmask output for representative
// prefixes (the simplified net.IP(m).String() form must produce identical bytes
// to the old fmt.Sprintf("%d.%d.%d.%d", ...) form).
func TestSplitCIDRToAddrMask(t *testing.T) {
	cases := []struct {
		cidr     string
		wantAddr string
		wantMask string
	}{
		{"10.128.0.0/10", "10.128.0.0", "255.192.0.0"},
		{"172.18.0.1/30", "172.18.0.1", "255.255.255.252"},
		{"10.0.0.0/24", "10.0.0.0", "255.255.255.0"},
		{"0.0.0.0/0", "0.0.0.0", "0.0.0.0"},
		{"1.2.3.4/32", "1.2.3.4", "255.255.255.255"},
	}
	for _, tc := range cases {
		addr, mask, err := splitCIDRToAddrMask(tc.cidr)
		if err != nil {
			t.Errorf("splitCIDRToAddrMask(%q): unexpected error %v", tc.cidr, err)
			continue
		}
		if addr != tc.wantAddr || mask != tc.wantMask {
			t.Errorf("splitCIDRToAddrMask(%q) = (%q, %q), want (%q, %q)", tc.cidr, addr, mask, tc.wantAddr, tc.wantMask)
		}
	}
}
