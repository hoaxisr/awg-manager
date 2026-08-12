package router

import (
	"reflect"
	"slices"
	"sort"
	"testing"
)

func TestResolveBypassPorts_EmptyInputs(t *testing.T) {
	udp, tcp, err := resolveBypassPorts(nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(udp) != 0 || len(tcp) != 0 {
		t.Fatalf("expected empty slices, got udp=%v tcp=%v", udp, tcp)
	}
}

func TestResolveBypassPorts_L2TPPreset(t *testing.T) {
	udp, tcp, err := resolveBypassPorts([]string{"l2tp"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ports := make([]int, len(udp))
	for i, pr := range udp {
		if pr.From != pr.To {
			t.Fatalf("l2tp preset should produce single ports, got range %v", pr)
		}
		ports[i] = pr.From
	}
	sort.Ints(ports)
	if !reflect.DeepEqual(ports, []int{500, 1701, 4500}) {
		t.Fatalf("l2tp UDP ports: got %v, want [500 1701 4500]", ports)
	}
	if len(tcp) != 0 {
		t.Fatalf("l2tp TCP ports: expected empty, got %v", tcp)
	}
}

func TestResolveBypassPorts_NetBiosSMBPreset(t *testing.T) {
	udp, tcp, err := resolveBypassPorts([]string{"netbios-smb"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	udpPorts := make([]int, len(udp))
	for i, pr := range udp {
		udpPorts[i] = pr.From
	}
	tcpPorts := make([]int, len(tcp))
	for i, pr := range tcp {
		tcpPorts[i] = pr.From
	}
	sort.Ints(udpPorts)
	sort.Ints(tcpPorts)
	if !reflect.DeepEqual(udpPorts, []int{137, 138}) {
		t.Fatalf("netbios-smb UDP: got %v", udpPorts)
	}
	if !reflect.DeepEqual(tcpPorts, []int{139, 445}) {
		t.Fatalf("netbios-smb TCP: got %v", tcpPorts)
	}
}

func TestResolveBypassPorts_UnknownPreset(t *testing.T) {
	_, _, err := resolveBypassPorts([]string{"nonexistent"}, "")
	if err == nil {
		t.Fatal("expected error for unknown preset")
	}
}

func TestResolveBypassPorts_ExtraPorts(t *testing.T) {
	udp, tcp, err := resolveBypassPorts(nil, "51820 UDP, 1194 TCP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(udp) != 1 || udp[0] != (PortRange{51820, 51820}) {
		t.Fatalf("extra UDP: got %v", udp)
	}
	if len(tcp) != 1 || tcp[0] != (PortRange{1194, 1194}) {
		t.Fatalf("extra TCP: got %v", tcp)
	}
}

func TestResolveBypassPorts_CombinesPresetsAndExtra(t *testing.T) {
	udp, tcp, err := resolveBypassPorts([]string{"ntp"}, "51820 UDP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ports := make([]int, len(udp))
	for i, pr := range udp {
		ports[i] = pr.From
	}
	sort.Ints(ports)
	if !reflect.DeepEqual(ports, []int{123, 51820}) {
		t.Fatalf("combined UDP: got %v", ports)
	}
	if len(tcp) != 0 {
		t.Fatalf("TCP should be empty, got %v", tcp)
	}
}

func TestParseExtraPorts_Empty(t *testing.T) {
	udp, tcp, err := parseExtraPorts("")
	if err != nil || len(udp) != 0 || len(tcp) != 0 {
		t.Fatalf("empty string: err=%v udp=%v tcp=%v", err, udp, tcp)
	}
}

func TestParseExtraPorts_CaseInsensitive(t *testing.T) {
	udp, tcp, err := parseExtraPorts("500 udp, 1723 tcp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(udp) != 1 || udp[0] != (PortRange{500, 500}) {
		t.Fatalf("got udp=%v", udp)
	}
	if len(tcp) != 1 || tcp[0] != (PortRange{1723, 1723}) {
		t.Fatalf("got tcp=%v", tcp)
	}
}

func TestParseExtraPorts_Range(t *testing.T) {
	udp, tcp, err := parseExtraPorts("5000-5500 UDP, 8000-9000 TCP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(udp) != 1 || udp[0] != (PortRange{5000, 5500}) {
		t.Fatalf("got udp=%v, want [{5000 5500}]", udp)
	}
	if len(tcp) != 1 || tcp[0] != (PortRange{8000, 9000}) {
		t.Fatalf("got tcp=%v, want [{8000 9000}]", tcp)
	}
}

func TestParseExtraPorts_MixedRangeAndSingle(t *testing.T) {
	udp, tcp, err := parseExtraPorts("51820 UDP, 5000-5500 UDP, 443 TCP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(udp) != 2 {
		t.Fatalf("expected 2 UDP entries, got %v", udp)
	}
	if udp[0] != (PortRange{51820, 51820}) || udp[1] != (PortRange{5000, 5500}) {
		t.Fatalf("unexpected UDP ranges: %v", udp)
	}
	if len(tcp) != 1 || tcp[0] != (PortRange{443, 443}) {
		t.Fatalf("got tcp=%v", tcp)
	}
}

func TestParseExtraPorts_InvalidFormat(t *testing.T) {
	cases := []string{
		"51820",          // missing protocol
		"51820 SCTP",     // unknown protocol
		"99999 UDP",      // port out of range
		"0 UDP",          // port 0 invalid
		"abc UDP",        // non-numeric port
		"5000-4000 UDP",  // reversed range
		"5000-99999 UDP", // end port out of range
	}
	for _, c := range cases {
		_, _, err := parseExtraPorts(c)
		if err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestResolveBypassSubnets(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"cidr", "203.0.113.0/24", []string{"203.0.113.0/24"}, false},
		{"bare ip to /32", "10.8.0.5", []string{"10.8.0.5/32"}, false},
		{"mixed comma+space", "203.0.113.0/24, 10.8.0.5", []string{"203.0.113.0/24", "10.8.0.5/32"}, false},
		{"hostname rejected", "vpn.example.com", nil, true},
		{"garbage rejected", "not-an-ip", nil, true},
		{"ipv6 cidr rejected", "::1/128", nil, true},
		{"ipv6 bare rejected", "fe80::1", nil, true},
		{"bad prefix rejected", "10.0.0.0/99", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBypassSubnets(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !slices.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// ── resolveBypassCIDRs (issue #490: IP-пресеты, keendns) ─────────────────

func TestResolveBypassCIDRs_Empty(t *testing.T) {
	got, err := resolveBypassCIDRs(nil, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestResolveBypassCIDRs_KeenDNSPreset(t *testing.T) {
	got, err := resolveBypassCIDRs([]string{"keendns"}, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Статического CIDR у пресета нет: адрес приходит с роутера в adhoc.
	if len(got) != 0 {
		t.Fatalf("keendns preset CIDRs = %v, want empty (адрес приходит рантаймом)", got)
	}
}

// Адрес KeenDNS приезжает третьим аргументом (issue #729) — он обязан попасть
// в список, схлопнуться с ручным дублем и не сдвинуть порядок пользовательских
// подсетей.
func TestResolveBypassCIDRs_AdhocFromRouter(t *testing.T) {
	got, err := resolveBypassCIDRs([]string{"keendns"}, "10.0.0.0/8, 78.47.125.180",
		[]string{"78.47.125.180/32"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"78.47.125.180/32", "10.0.0.0/8"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveBypassCIDRs_PortPresetsContributeNoCIDRs(t *testing.T) {
	got, err := resolveBypassCIDRs([]string{"l2tp", "ntp", "netbios-smb"}, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("port presets must not contribute CIDRs, got %v", got)
	}
}

func TestResolveBypassCIDRs_DedupWithinExtra(t *testing.T) {
	// Дубль в пользовательском списке схлопывается, порядок стабильный.
	// (Пресеты сюда больше ничего не приносят — keendns ушёл в DNS-rewrite.)
	got, err := resolveBypassCIDRs([]string{"keendns"}, "78.47.125.180, 10.0.0.0/8, 78.47.125.180/32", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"78.47.125.180/32", "10.0.0.0/8"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResolveBypassCIDRs_UnknownPreset(t *testing.T) {
	if _, err := resolveBypassCIDRs([]string{"nonexistent"}, "", nil); err == nil {
		t.Fatal("expected error for unknown preset")
	}
}

func TestResolveBypassCIDRs_ExtraError(t *testing.T) {
	if _, err := resolveBypassCIDRs([]string{"keendns"}, "not-an-ip", nil); err == nil {
		t.Fatal("expected error for malformed extra subnets")
	}
}
