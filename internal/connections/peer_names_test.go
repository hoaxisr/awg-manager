package connections

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestBuildPeerNames_SystemAndManaged(t *testing.T) {
	servers := []ndms.WireguardServer{{
		Peers: []ndms.WireguardServerPeer{
			{Description: "EasyKill", AllowedIPs: []string{"10.10.0.3/32"}},
			// Site-to-site: маршрутизируемая подсеть впереди host-записи.
			{Description: "Office", AllowedIPs: []string{"192.168.9.0/24", "10.10.0.4/32"}},
			{Description: "", AllowedIPs: []string{"10.10.0.5/32"}},
		},
	}}
	managed := []storage.ManagedServer{{
		Peers: []storage.ManagedPeer{
			{Description: "Laptop", TunnelIP: "10.30.0.5/32"},
		},
	}}

	got := buildPeerNames(servers, managed)

	want := map[string]string{
		"10.10.0.3": "EasyKill",
		"10.10.0.4": "Office",
		"10.30.0.5": "Laptop",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d names, want %d: %#v", len(got), len(want), got)
	}
	for ip, name := range want {
		if got[ip] != name {
			t.Errorf("names[%q] = %q, want %q", ip, got[ip], name)
		}
	}
}

// conntrack отдаёт IPv6 в канонической форме, а описание пира заводит
// человек — ключ обязан канонизироваться, иначе имя не найдётся.
func TestPeerHostIP_CanonicalizesIPv6(t *testing.T) {
	cases := map[string]string{
		"fd00:0:0::2/128": "fd00::2",
		"10.0.0.2/32":     "10.0.0.2",
		" 10.0.0.3 ":      "10.0.0.3",
		"10.0.0.0/24":     "",
		"not-an-ip":       "",
	}
	for in, want := range cases {
		if got := PeerHostIP(in); got != want {
			t.Errorf("PeerHostIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyPeerNames_OnlyFillsEmpty(t *testing.T) {
	conns := []Connection{
		{Src: "10.10.0.3"},
		{Src: "192.168.1.50", ClientName: "My Phone"},
		{Src: "10.99.0.1"},
	}

	applyPeerNames(conns, map[string]string{
		"10.10.0.3":    "EasyKill",
		"192.168.1.50": "peer-name-loses",
	})

	if conns[0].ClientName != "EasyKill" {
		t.Errorf("peer IP: ClientName = %q, want EasyKill", conns[0].ClientName)
	}
	if conns[1].ClientName != "My Phone" {
		t.Errorf("hotspot name must win, got %q", conns[1].ClientName)
	}
	if conns[2].ClientName != "" {
		t.Errorf("unknown IP must stay empty, got %q", conns[2].ClientName)
	}
}
