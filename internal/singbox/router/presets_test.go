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
	sort.Ints(udp)
	if !reflect.DeepEqual(udp, []int{500, 1701, 4500}) {
		t.Fatalf("l2tp UDP ports: got %v, want [500 1701 4500]", udp)
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
	sort.Ints(udp)
	sort.Ints(tcp)
	if !reflect.DeepEqual(udp, []int{137, 138}) {
		t.Fatalf("netbios-smb UDP: got %v", udp)
	}
	if !reflect.DeepEqual(tcp, []int{139, 445}) {
		t.Fatalf("netbios-smb TCP: got %v", tcp)
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
	if !reflect.DeepEqual(udp, []int{51820}) {
		t.Fatalf("extra UDP: got %v", udp)
	}
	if !reflect.DeepEqual(tcp, []int{1194}) {
		t.Fatalf("extra TCP: got %v", tcp)
	}
}

func TestResolveBypassPorts_CombinesPresetsAndExtra(t *testing.T) {
	udp, tcp, err := resolveBypassPorts([]string{"ntp"}, "51820 UDP")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ntp gives 123 UDP, extra gives 51820 UDP
	sort.Ints(udp)
	if !reflect.DeepEqual(udp, []int{123, 51820}) {
		t.Fatalf("combined UDP: got %v", udp)
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
	if !reflect.DeepEqual(udp, []int{500}) || !reflect.DeepEqual(tcp, []int{1723}) {
		t.Fatalf("got udp=%v tcp=%v", udp, tcp)
	}
}

func TestParseExtraPorts_InvalidFormat(t *testing.T) {
	cases := []string{
		"51820",          // missing protocol
		"51820 SCTP",     // unknown protocol
		"99999 UDP",      // port out of range
		"0 UDP",          // port 0 invalid
		"abc UDP",        // non-numeric port
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
