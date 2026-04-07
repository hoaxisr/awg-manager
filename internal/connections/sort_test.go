package connections

import (
	"testing"
)

func TestApplySort_NoSort(t *testing.T) {
	conns := []Connection{
		{Protocol: "tcp", Bytes: 100},
		{Protocol: "udp", Bytes: 200},
		{Protocol: "icmp", Bytes: 50},
	}
	applySort(conns, "", "asc")
	// No-op when SortBy is empty — preserve original order.
	if conns[0].Protocol != "tcp" || conns[1].Protocol != "udp" || conns[2].Protocol != "icmp" {
		t.Errorf("expected unchanged order, got %+v", conns)
	}
}

func TestApplySort_UnknownColumn(t *testing.T) {
	conns := []Connection{
		{Protocol: "tcp", Bytes: 100},
		{Protocol: "udp", Bytes: 200},
	}
	applySort(conns, "unknown", "asc")
	// Unknown column → no-op, preserve original order.
	if conns[0].Protocol != "tcp" || conns[1].Protocol != "udp" {
		t.Errorf("expected unchanged order, got %+v", conns)
	}
}

func TestApplySort_Proto_AscCaseInsensitive(t *testing.T) {
	conns := []Connection{
		{Protocol: "UDP"},
		{Protocol: "tcp"},
		{Protocol: "icmp"},
	}
	applySort(conns, "proto", "asc")
	if conns[0].Protocol != "icmp" || conns[1].Protocol != "tcp" || conns[2].Protocol != "UDP" {
		t.Errorf("expected [icmp, tcp, UDP], got %+v", conns)
	}
}

func TestApplySort_Proto_Desc(t *testing.T) {
	conns := []Connection{
		{Protocol: "tcp"},
		{Protocol: "icmp"},
		{Protocol: "udp"},
	}
	applySort(conns, "proto", "desc")
	if conns[0].Protocol != "udp" || conns[1].Protocol != "tcp" || conns[2].Protocol != "icmp" {
		t.Errorf("expected [udp, tcp, icmp], got %+v", conns)
	}
}

func TestApplySort_Bytes_AscDesc(t *testing.T) {
	conns := []Connection{
		{Bytes: 500},
		{Bytes: 100},
		{Bytes: 1000},
	}
	applySort(conns, "bytes", "asc")
	if conns[0].Bytes != 100 || conns[1].Bytes != 500 || conns[2].Bytes != 1000 {
		t.Errorf("asc: expected [100, 500, 1000], got %+v", conns)
	}
	applySort(conns, "bytes", "desc")
	if conns[0].Bytes != 1000 || conns[1].Bytes != 500 || conns[2].Bytes != 100 {
		t.Errorf("desc: expected [1000, 500, 100], got %+v", conns)
	}
}

func TestApplySort_State_Alphabetic(t *testing.T) {
	conns := []Connection{
		{State: "TIME_WAIT"},
		{State: "ESTABLISHED"},
		{State: "SYN_SENT"},
	}
	applySort(conns, "state", "asc")
	if conns[0].State != "ESTABLISHED" || conns[1].State != "SYN_SENT" || conns[2].State != "TIME_WAIT" {
		t.Errorf("expected [ESTABLISHED, SYN_SENT, TIME_WAIT], got %+v", conns)
	}
}

func TestApplySort_Iface_ByTunnelName(t *testing.T) {
	conns := []Connection{
		{TunnelName: "Wireguard5"},
		{TunnelName: "Direct"},
		{TunnelName: "AmneziaWG_ru"},
	}
	applySort(conns, "iface", "asc")
	if conns[0].TunnelName != "AmneziaWG_ru" || conns[1].TunnelName != "Direct" || conns[2].TunnelName != "Wireguard5" {
		t.Errorf("expected alphabetic by TunnelName, got %+v", conns)
	}
}

func TestApplySort_Src_IPv4Numeric(t *testing.T) {
	conns := []Connection{
		{Src: "192.168.1.1"},
		{Src: "9.9.9.9"},
		{Src: "10.0.0.1"},
	}
	applySort(conns, "src", "asc")
	// Numeric IPv4: 9.9.9.9 < 10.0.0.1 < 192.168.1.1.
	if conns[0].Src != "9.9.9.9" || conns[1].Src != "10.0.0.1" || conns[2].Src != "192.168.1.1" {
		t.Errorf("expected numeric IPv4 order, got %+v", conns)
	}
}

func TestApplySort_Src_PortTiebreaker(t *testing.T) {
	conns := []Connection{
		{Src: "1.1.1.1", SrcPort: 443},
		{Src: "1.1.1.1", SrcPort: 80},
		{Src: "1.1.1.1", SrcPort: 22},
	}
	applySort(conns, "src", "asc")
	// Same IP — sort by port ascending.
	if conns[0].SrcPort != 22 || conns[1].SrcPort != 80 || conns[2].SrcPort != 443 {
		t.Errorf("expected port tiebreaker [22, 80, 443], got %+v", conns)
	}
}

func TestApplySort_Dst_IPv4Numeric(t *testing.T) {
	conns := []Connection{
		{Dst: "172.16.0.5"},
		{Dst: "8.8.8.8"},
		{Dst: "1.1.1.1"},
	}
	applySort(conns, "dst", "asc")
	if conns[0].Dst != "1.1.1.1" || conns[1].Dst != "8.8.8.8" || conns[2].Dst != "172.16.0.5" {
		t.Errorf("expected numeric IPv4 order, got %+v", conns)
	}
}

func TestApplySort_Src_IPv6FallbackLexical(t *testing.T) {
	conns := []Connection{
		{Src: "fe80::2"},
		{Src: "fe80::1"},
		{Src: "1.2.3.4"}, // mixed v4 + v6
	}
	applySort(conns, "src", "asc")
	// IPv4 (parses to uint32) sorts ahead of IPv6/non-IPv4 entries by design
	// (deterministic ordering across address families). Within IPv6 entries
	// we fall back to lexical compare on the address string.
	if conns[0].Src != "1.2.3.4" {
		t.Errorf("expected IPv4 first, got %+v", conns)
	}
	if conns[1].Src != "fe80::1" || conns[2].Src != "fe80::2" {
		t.Errorf("expected fe80::1 before fe80::2, got %+v", conns)
	}
}

func TestApplySort_Src_MalformedFallback(t *testing.T) {
	conns := []Connection{
		{Src: "not-an-ip"},
		{Src: "10.0.0.1"},
	}
	// Should not panic. Result is implementation-defined but deterministic.
	applySort(conns, "src", "asc")
	if len(conns) != 2 {
		t.Errorf("expected 2 elements, got %d", len(conns))
	}
}

func TestApplySort_StableForEqualKeys(t *testing.T) {
	// Stable sort: equal keys preserve insertion order (matters for grouping
	// use cases like "all connections to one destination").
	conns := []Connection{
		{Dst: "1.1.1.1", DstPort: 80, Bytes: 10},
		{Dst: "1.1.1.1", DstPort: 80, Bytes: 20},
		{Dst: "1.1.1.1", DstPort: 80, Bytes: 30},
	}
	applySort(conns, "dst", "asc")
	if conns[0].Bytes != 10 || conns[1].Bytes != 20 || conns[2].Bytes != 30 {
		t.Errorf("expected stable order [10, 20, 30], got %+v", conns)
	}
}
