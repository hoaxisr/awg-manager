package packet

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func testTCPPacket(version, ihl, doff int, frag uint16, flags uint8, payload []byte) []byte {
	ipLen := 40
	if version == 4 {
		ipLen = ihl * 4
	}
	tcpLen := doff * 4
	out := make([]byte, ipLen+tcpLen+len(payload))
	if version == 4 {
		out[0] = 0x40 | byte(ihl)
		binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
		binary.BigEndian.PutUint16(out[6:8], frag)
		out[8] = 64
		out[9] = ProtocolTCP
		copy(out[12:16], []byte{192, 168, 1, 42})
		copy(out[16:20], []byte{104, 21, 0, 5})
	} else {
		out[0] = 0x60
		binary.BigEndian.PutUint16(out[4:6], uint16(tcpLen+len(payload)))
		out[6] = ProtocolTCP
		out[7] = 64
		out[8] = 0xfd
		out[23] = 1
		out[24] = 0x20
		out[39] = 2
	}
	tcp := out[ipLen:]
	binary.BigEndian.PutUint16(tcp[0:2], 49152)
	binary.BigEndian.PutUint16(tcp[2:4], 443)
	binary.BigEndian.PutUint32(tcp[4:8], 0x01020304)
	binary.BigEndian.PutUint32(tcp[8:12], 0xa0b0c0d0)
	tcp[12] = byte(doff << 4)
	tcp[13] = flags
	copy(tcp[tcpLen:], payload)
	return out
}

func TestKindStringAndValid(t *testing.T) {
	kinds := []Kind{HELLO, RESET, ANSWERED, FIN, CLOSE_RST, SYN, NEW}
	names := []string{"HELLO", "RESET", "ANSWERED", "FIN", "CLOSE_RST", "SYN", "NEW"}
	for i, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("%v should be valid", kind)
		}
		if got := kind.String(); got != names[i] {
			t.Errorf("kind %d: got %q, want %q", kind, got, names[i])
		}
	}
	for _, kind := range []Kind{KindInvalid, NEW + 1, 255} {
		if kind.Valid() || kind.String() != "INVALID" {
			t.Errorf("invalid kind %d was not normalized", kind)
		}
		if _, ok := Parse(kind, nil); ok {
			t.Errorf("Parse accepted invalid kind %d", kind)
		}
	}
}

func TestParseTCP4(t *testing.T) {
	raw := testTCPPacket(4, 5, 5, 0, TCPPSH|TCPACK, []byte{0x16, 3, 1, 0, 0})
	p, ok := ParseTCP(raw)
	if !ok {
		t.Fatal("ParseTCP rejected a normal IPv4 packet")
	}
	if p.Src != netip.MustParseAddr("192.168.1.42") || p.Dst != netip.MustParseAddr("104.21.0.5") {
		t.Errorf("wrong addresses: %v -> %v", p.Src, p.Dst)
	}
	if p.SrcPort != 49152 || p.DstPort != 443 {
		t.Errorf("wrong ports: %d -> %d", p.SrcPort, p.DstPort)
	}
	if p.Protocol != ProtocolTCP || p.Seq != 0x01020304 || p.Ack != 0xa0b0c0d0 || p.Flags != TCPPSH|TCPACK {
		t.Errorf("wrong TCP metadata: %+v", p)
	}
	if len(p.Payload) != 5 || p.Payload[0] != 0x16 {
		t.Errorf("wrong payload: %x", p.Payload)
	}
}

func TestParseTCPOptionsAnd6(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"IPv4 options", testTCPPacket(4, 8, 5, 0, TCPACK, []byte{1, 2, 3})},
		{"TCP options", testTCPPacket(4, 5, 9, 0, TCPACK, []byte{4, 5, 6})},
		{"both options", testTCPPacket(4, 7, 8, 0, TCPACK, []byte{7, 8, 9})},
		{"IPv6", testTCPPacket(6, 0, 5, 0, TCPACK, []byte{10, 11, 12})},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, ok := ParseTCP(test.raw)
			if !ok {
				t.Fatal("packet rejected")
			}
			if len(p.Payload) != 3 {
				t.Fatalf("payload length = %d, want 3", len(p.Payload))
			}
			if test.raw[0]>>4 == 6 {
				if !p.Src.Is6() || !p.Dst.Is6() || p.Src != netip.MustParseAddr("fd00::1") || p.Dst != netip.MustParseAddr("2000::2") {
					t.Errorf("wrong IPv6 addresses: %v -> %v", p.Src, p.Dst)
				}
			}
		})
	}
}

func TestParseTCPRejectsFragmentsAndOtherProtocols(t *testing.T) {
	for _, frag := range []uint16{0x2000, 0x0001, 0x2001} {
		raw := testTCPPacket(4, 5, 5, frag, TCPACK, nil)
		if _, ok := ParseTCP(raw); ok {
			t.Errorf("accepted fragment field %#x", frag)
		}
	}
	if _, ok := ParseTCP(testTCPPacket(4, 5, 5, 0x4000, TCPACK, nil)); !ok {
		t.Error("DF was mistaken for a fragment")
	}

	raw := testTCPPacket(4, 5, 5, 0, TCPACK, nil)
	raw[9] = ProtocolUDP
	if _, ok := ParseTCP(raw); ok {
		t.Error("accepted IPv4 UDP as TCP")
	}
	raw = testTCPPacket(6, 0, 5, 0, TCPACK, nil)
	raw[6] = 0 // hop-by-hop extension: deliberately not followed
	if _, ok := ParseTCP(raw); ok {
		t.Error("accepted an IPv6 extension chain as a bare TCP header")
	}
	raw[6] = 58
	if _, ok := ParseTCP(raw); ok {
		t.Error("accepted ICMPv6 as TCP")
	}
}

func TestParseTCPRejectsBadHeaderLengths(t *testing.T) {
	for _, mutate := range []func([]byte){
		func(raw []byte) { raw[0] = 0x44 },
		func(raw []byte) { raw[0] = 0x4f },
		func(raw []byte) { raw[32] = 0x40 },
		func(raw []byte) { raw[32] = 0xf0 },
		func(raw []byte) { raw[0] = 0x50 },
	} {
		raw := testTCPPacket(4, 5, 5, 0, TCPACK, nil)
		mutate(raw)
		if _, ok := ParseTCP(raw); ok {
			t.Errorf("accepted malformed header %x", raw[:40])
		}
	}
}

func TestParseTCPEveryTruncation(t *testing.T) {
	for _, version := range []int{4, 6} {
		raw := testTCPPacket(version, 5, 5, 0, TCPACK, []byte{1, 2, 3, 4, 5})
		for cut := 0; cut <= len(raw); cut++ {
			p, ok := ParseTCP(raw[:cut])
			if ok && len(p.Payload) > cut {
				t.Fatalf("v%d cut %d: impossible payload length %d", version, cut, len(p.Payload))
			}
		}
	}
}

func TestDeclaredLengthsClampCapturedPayload(t *testing.T) {
	raw := testTCPPacket(4, 5, 5, 0, TCPACK, []byte{1, 2})
	raw = append(raw, 99, 98, 97) // bytes outside the IPv4 total length
	p, ok := ParseTCP(raw)
	if !ok || len(p.Payload) != 2 || p.Payload[0] != 1 {
		t.Fatalf("IPv4 trailing bytes were not ignored: %+v, %v", p, ok)
	}
	binary.BigEndian.PutUint16(raw[2:4], 0xffff) // a short capture is ordinary
	if _, ok := ParseTCP(raw); !ok {
		t.Error("an IPv4 declared length beyond the capture was not tolerated")
	}

	raw6 := testTCPPacket(6, 0, 5, 0, TCPACK, []byte{1, 2})
	raw6 = append(raw6, 99, 98)
	p, ok = ParseTCP(raw6)
	if !ok || len(p.Payload) != 2 {
		t.Fatalf("IPv6 trailing bytes were not ignored: %+v, %v", p, ok)
	}
	binary.BigEndian.PutUint16(raw6[4:6], 0xffff)
	if _, ok := ParseTCP(raw6); !ok {
		t.Error("an IPv6 declared length beyond the capture was not tolerated")
	}
}

func TestParseHeadAndNew(t *testing.T) {
	for _, version := range []int{4, 6} {
		raw := testTCPPacket(version, 5, 5, 0, TCPSYN, nil)
		e, ok := Parse(NEW, raw)
		if !ok || e.Kind != NEW || e.SrcPort != 49152 || e.DstPort != 443 || e.Protocol != ProtocolTCP {
			t.Errorf("v%d NEW: %+v, %v", version, e, ok)
		}

		headLen := 20
		if version == 6 {
			headLen = 40
		}
		e, ok = Parse(NEW, raw[:headLen+2])
		if !ok || e.SrcPort != 0 || e.DstPort != 0 {
			t.Errorf("v%d short NEW should have no ports: %+v, %v", version, e, ok)
		}
		e, ok = Parse(NEW, raw[:headLen+4])
		if !ok || e.SrcPort != 49152 || e.DstPort != 443 {
			t.Errorf("v%d four-byte transport head: %+v, %v", version, e, ok)
		}
	}

	raw := testTCPPacket(4, 6, 5, 0x2000, 0, nil) // first fragment is useful for NEW
	if _, ok := Parse(NEW, raw); !ok {
		t.Error("NEW rejected a first IPv4 fragment")
	}
	binary.BigEndian.PutUint16(raw[6:8], 1)
	if _, ok := Parse(NEW, raw); ok {
		t.Error("NEW accepted a later fragment")
	}
}

func TestNewUDPAndProtocolFilter(t *testing.T) {
	raw := testTCPPacket(4, 5, 5, 0, 0, nil)
	raw[9] = ProtocolUDP
	udp := raw[20:]
	for i := range udp {
		udp[i] = 0
	}
	binary.BigEndian.PutUint16(udp[0:2], 50000)
	binary.BigEndian.PutUint16(udp[2:4], 53)
	e, ok := Parse(NEW, raw)
	if !ok || e.Protocol != ProtocolUDP || e.SrcPort != 50000 || e.DstPort != 53 {
		t.Fatalf("UDP NEW: %+v, %v", e, ok)
	}
	raw[9] = 1
	if _, ok := Parse(NEW, raw); ok {
		t.Error("NEW accepted ICMP")
	}
	head, ok := ParseHead(raw)
	if !ok || head.Protocol != 1 || head.SrcPort != 0 || head.DstPort != 0 {
		t.Errorf("generic ICMP head: %+v, %v", head, ok)
	}
}

func TestEventKindFlagPredicates(t *testing.T) {
	hello := testClientHello("Example.COM.", false)
	tests := []struct {
		kind    Kind
		flags   uint8
		payload []byte
	}{
		{HELLO, TCPPSH | TCPACK, hello},
		{RESET, TCPRST, nil},
		{ANSWERED, TCPACK, nil},
		{FIN, TCPFIN | TCPACK, nil},
		{CLOSE_RST, TCPRST | TCPACK, nil},
		{SYN, TCPSYN, nil},
	}
	for _, test := range tests {
		raw := testTCPPacket(4, 5, 5, 0, test.flags, test.payload)
		e, ok := Parse(test.kind, raw)
		if !ok || e.Kind != test.kind {
			t.Errorf("%s was rejected: %+v, %v", test.kind, e, ok)
		}
		if test.kind == HELLO && e.SNI != "example.com" {
			t.Errorf("HELLO SNI = %q", e.SNI)
		}
	}

	bad := []struct {
		kind    Kind
		flags   uint8
		payload []byte
	}{
		{HELLO, TCPACK, []byte("not TLS")},
		{RESET, TCPACK, nil},
		{FIN, TCPACK, nil},
		{CLOSE_RST, TCPRST, nil},
		{SYN, TCPSYN | TCPACK, nil},
	}
	for _, test := range bad {
		raw := testTCPPacket(4, 5, 5, 0, test.flags, test.payload)
		if _, ok := Parse(test.kind, raw); ok {
			t.Errorf("%s accepted flags %#x/payload %q", test.kind, test.flags, test.payload)
		}
	}
}

func TestHelloWithoutSNIIsStillAnEvent(t *testing.T) {
	raw := testTCPPacket(4, 5, 5, 0, TCPACK, testClientHello("", false))
	e, ok := Parse(HELLO, raw)
	if !ok || e.SNI != "" {
		t.Fatalf("ClientHello without SNI: %+v, %v", e, ok)
	}
}

func TestDirectionReversal(t *testing.T) {
	raw := testTCPPacket(4, 5, 5, 0, TCPACK, testClientHello("example.com", false))
	e, ok := Parse(HELLO, raw)
	if !ok {
		t.Fatal("HELLO rejected")
	}
	originalFlow := e.Flow()
	reversed := e.Reverse()
	if reversed.Src != e.Dst || reversed.Dst != e.Src || reversed.SrcPort != e.DstPort || reversed.DstPort != e.SrcPort {
		t.Errorf("endpoints not reversed: %+v", reversed)
	}
	if reversed.Kind != e.Kind || reversed.Seq != e.Seq || reversed.Ack != e.Ack || reversed.SNI != e.SNI {
		t.Errorf("non-directional metadata changed: %+v", reversed)
	}
	if reversed.Reverse().Flow() != originalFlow {
		t.Error("double reversal did not restore the flow")
	}
	if originalFlow.Reverse().Reverse() != originalFlow {
		t.Error("Flow double reversal did not restore the key")
	}
	if e.Packet.Reverse().Flow() != originalFlow.Reverse() {
		t.Error("Packet and Flow reversal disagree")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte{})
	f.Add(testTCPPacket(4, 5, 5, 0, TCPACK, testClientHello("example.com", false)))
	f.Add(testTCPPacket(6, 0, 5, 0, TCPSYN, nil))
	f.Fuzz(func(t *testing.T, raw []byte) {
		for kind := HELLO; kind <= NEW; kind++ {
			_, _ = Parse(kind, raw)
		}
		_, _ = ParseTCP(raw)
		_, _ = ParseHead(raw)
	})
}
