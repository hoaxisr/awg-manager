package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// filterBindable must offer egress interfaces (security-level "public") minus
// our own auto-managed ones and minus interfaces already bound by an existing
// direct outbound, while rescuing KeenOS-native proxies in the native set (#323).
// Покрытие занятости OpkgTun (живая половина, пины NDMS, fail-closed на отказе
// /sys) живёт в opkgtun_occupancy_test.go — туда его перенёс develop вместе с
// переходом адаптера на поле listSys.
func TestFilterBindable(t *testing.T) {
	ifaces := []ndms.AllInterface{
		{Name: "t2s0", SecurityLevel: "public", Type: "Proxy", Label: "My-Socks5"}, // native, free — keep
		{Name: "t2s1", SecurityLevel: "public", Type: "Proxy", Label: "ours"},      // our sing-box proxy — drop
		{Name: "t2s2", SecurityLevel: "public", Type: "Proxy"},                     // native but occupied — drop
		{Name: "ipsec0", SecurityLevel: "public", Type: "IPSec"},                   // user VPN, free — keep
		{Name: "ppp0", SecurityLevel: "public", Type: "PPPoE"},                     // occupied — drop
		{Name: "Home", SecurityLevel: "private", Type: "Bridge"},                   // LAN bridge — drop (private)
		{Name: "opkgtun0", SecurityLevel: "public", Type: "Wireguard"},             // managed AWG — drop
		{Name: "ra0", SecurityLevel: "public", Type: "AccessPoint"},                // Wi-Fi AP, public у NDMS — drop
		{Name: "rai0", SecurityLevel: "public", Type: "WifiMaster"},                // радио — drop
		{Name: "apcli0", SecurityLevel: "public", Type: "WifiStation"},             // Wi-Fi-клиент как выход — keep
	}
	native := map[string]bool{"t2s0": true, "t2s2": true}
	occupied := map[string]bool{"ppp0": true, "t2s2": true}
	got := filterBindable(ifaces, native, occupied)

	names := map[string]bool{}
	for _, g := range got {
		names[g.Name] = true
	}
	for _, want := range []string{"t2s0", "ipsec0", "apcli0"} {
		if !names[want] {
			t.Errorf("expected %q kept, missing from %v", want, names)
		}
	}
	for _, drop := range []string{"t2s1", "t2s2", "ppp0", "Home", "opkgtun0", "ra0", "rai0"} {
		if names[drop] {
			t.Errorf("expected %q dropped, still present in %v", drop, names)
		}
	}
}

func TestForeignBindable(t *testing.T) {
	sysNet := t.TempDir()
	_ = os.MkdirAll(filepath.Join(sysNet, "csqtt0"), 0o755)
	_ = os.WriteFile(filepath.Join(sysNet, "csqtt0", "carrier"), []byte("1\n"), 0o644)
	list := []ndms.Interface{
		{ID: "OpkgTun7", SystemName: "", Description: "csqtt", Connected: "no"}, // SystemName пуст — сопоставление по номеру
		{ID: "OpkgTun9", SystemName: "opkgtun9", Connected: "yes"},
	}
	got := foreignBindable([]string{"opkgtun7", "csqtt0", "zt9", "busy0"}, list, sysNet, map[string]bool{"busy0": true})
	byName := map[string]router.WANInterfaceInfo{}
	for _, g := range got {
		byName[g.Name] = g
	}
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if o := byName["opkgtun7"]; !o.Foreign || o.Up || o.Absent || o.Label != "csqtt" {
		t.Errorf("opkgtun7 = %+v", o)
	}
	if k := byName["csqtt0"]; !k.Foreign || !k.Up || k.Absent {
		t.Errorf("csqtt0 = %+v", k)
	}
	if z := byName["zt9"]; !z.Foreign || !z.Absent || z.Up {
		t.Errorf("zt9 = %+v", z)
	}
	if _, ok := byName["opkgtun9"]; ok {
		t.Error("неотмеченный opkgtun9 попал в список")
	}
}
