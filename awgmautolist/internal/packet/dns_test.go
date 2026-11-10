package packet

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"testing"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/dnsfeed"
)

func testUDPPacket(version, ihl int, frag uint16, srcPort, dstPort uint16, payload []byte) []byte {
	ipLen := 40
	if version == 4 {
		ipLen = ihl * 4
	}
	out := make([]byte, ipLen+8+len(payload))
	if version == 4 {
		out[0] = 0x40 | byte(ihl)
		binary.BigEndian.PutUint16(out[2:4], uint16(len(out)))
		binary.BigEndian.PutUint16(out[6:8], frag)
		out[8] = 64
		out[9] = ProtocolUDP
		copy(out[12:16], []byte{192, 0, 2, 53})
		copy(out[16:20], []byte{192, 168, 1, 42})
	} else {
		out[0] = 0x60
		binary.BigEndian.PutUint16(out[4:6], uint16(8+len(payload)))
		out[6] = ProtocolUDP
		out[7] = 64
		out[8] = 0x20
		out[9] = 0x01
		out[23] = 53
		out[24] = 0xfd
		out[39] = 42
	}
	udp := out[ipLen:]
	binary.BigEndian.PutUint16(udp[0:2], srcPort)
	binary.BigEndian.PutUint16(udp[2:4], dstPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	copy(udp[8:], payload)
	return out
}

func TestParseUDP4And6(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	for _, test := range []struct {
		name string
		raw  []byte
		v6   bool
	}{
		{"IPv4", testUDPPacket(4, 5, 0, 53, 5353, payload), false},
		{"IPv4 options", testUDPPacket(4, 8, 0, 53, 5353, payload), false},
		{"IPv6", testUDPPacket(6, 0, 0, 53, 5353, payload), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, ok := ParseUDP(test.raw)
			if !ok || p.Protocol != ProtocolUDP || p.SrcPort != 53 || p.DstPort != 5353 {
				t.Fatalf("ParseUDP = %+v, %v", p, ok)
			}
			if !bytes.Equal(p.Payload, payload) {
				t.Errorf("payload = %x, want %x", p.Payload, payload)
			}
			if p.Seq != 0 || p.Ack != 0 || p.Flags != 0 {
				t.Errorf("TCP fields populated for UDP: %+v", p)
			}
			if test.v6 {
				if p.Src != netip.MustParseAddr("2001::35") || p.Dst != netip.MustParseAddr("fd00::2a") {
					t.Errorf("IPv6 addresses = %v -> %v", p.Src, p.Dst)
				}
			} else if p.Src != netip.MustParseAddr("192.0.2.53") || p.Dst != netip.MustParseAddr("192.168.1.42") {
				t.Errorf("IPv4 addresses = %v -> %v", p.Src, p.Dst)
			}
		})
	}
}

func TestParseUDPRejectsFragmentsAndOtherProtocols(t *testing.T) {
	for _, frag := range []uint16{0x2000, 0x0001, 0x2001} {
		if _, ok := ParseUDP(testUDPPacket(4, 5, frag, 53, 50000, nil)); ok {
			t.Errorf("accepted fragment field %#x", frag)
		}
	}
	if _, ok := ParseUDP(testUDPPacket(4, 5, 0x4000, 53, 50000, nil)); !ok {
		t.Error("DF was mistaken for a fragment")
	}
	raw := testUDPPacket(4, 5, 0, 53, 50000, nil)
	raw[9] = ProtocolTCP
	if _, ok := ParseUDP(raw); ok {
		t.Error("accepted TCP as UDP")
	}
	raw6 := testUDPPacket(6, 0, 0, 53, 50000, nil)
	raw6[6] = 0
	if _, ok := ParseUDP(raw6); ok {
		t.Error("accepted an IPv6 extension chain as UDP")
	}
}

func TestParseUDPLengthsAndTruncation(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5}
	raw := testUDPPacket(4, 5, 0, 53, 50000, payload)
	for _, length := range []uint16{0, 1, 7} {
		bad := append([]byte(nil), raw...)
		binary.BigEndian.PutUint16(bad[24:26], length)
		if _, ok := ParseUDP(bad); ok {
			t.Errorf("accepted UDP length %d", length)
		}
	}
	for cut := 0; cut < 28; cut++ {
		if _, ok := ParseUDP(raw[:cut]); ok {
			t.Errorf("accepted UDP header cut at %d", cut)
		}
	}

	partial := raw[:len(raw)-2]
	p, ok := ParseUDP(partial)
	if !ok || !bytes.Equal(p.Payload, payload[:len(payload)-2]) {
		t.Errorf("short capture = %+v, %v", p, ok)
	}
	if _, ok := ParseDNSResponse(partial); ok {
		t.Error("DNS extraction accepted an incomplete UDP datagram")
	}

	// Bytes beyond the UDP length are not payload, even when the IP length is
	// enlarged to include them.
	withPadding := append(append([]byte(nil), raw...), 99, 98, 97)
	binary.BigEndian.PutUint16(withPadding[2:4], uint16(len(withPadding)))
	p, ok = ParseUDP(withPadding)
	if !ok || !bytes.Equal(p.Payload, payload) {
		t.Errorf("UDP trailing bytes were not ignored: %x, %v", p.Payload, ok)
	}
}

func TestParseUDPEveryTruncationIsSafe(t *testing.T) {
	for _, version := range []int{4, 6} {
		raw := testUDPPacket(version, 5, 0, 53, 50000, []byte{1, 2, 3, 4})
		for cut := 0; cut <= len(raw); cut++ {
			p, ok := ParseUDP(raw[:cut])
			if ok && len(p.Payload) > cut {
				t.Fatalf("v%d cut %d: impossible payload length %d", version, cut, len(p.Payload))
			}
			_, _ = ParseDNSResponse(raw[:cut])
		}
	}
}

func testDNSMessage() []byte {
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:2], 0xbeef)
	binary.BigEndian.PutUint16(msg[2:4], 0x8180)
	binary.BigEndian.PutUint16(msg[4:6], 1)
	binary.BigEndian.PutUint16(msg[6:8], 1)
	msg = append(msg, 1, 'a', 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 0)
	msg = binary.BigEndian.AppendUint16(msg, 1) // A
	msg = binary.BigEndian.AppendUint16(msg, 1) // IN
	msg = append(msg, 0xc0, 0x0c)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	msg = binary.BigEndian.AppendUint32(msg, 60)
	msg = binary.BigEndian.AppendUint16(msg, 4)
	msg = append(msg, 192, 0, 2, 1)
	return msg
}

func TestParseDNSResponseUDP(t *testing.T) {
	message := testDNSMessage()
	raw := testUDPPacket(4, 5, 0, 53, 51000, message)
	response, ok := ParseDNSResponse(raw)
	if !ok || response.Protocol != ProtocolUDP || !bytes.Equal(response.Payload, message) {
		t.Fatalf("UDP DNS response = %+v, %v", response, ok)
	}
	if response.Client != netip.MustParseAddr("192.168.1.42") || response.Server != netip.MustParseAddr("192.0.2.53") ||
		response.ClientPort != 51000 || response.ServerPort != 53 {
		t.Errorf("wrong hint metadata: %+v", response)
	}
	if response.Packet.Src != response.Server || response.Packet.Dst != response.Client {
		t.Errorf("captured direction changed: %+v", response.Packet)
	}
	answer, err := dnsfeed.ParseResponse(response.Payload)
	if err != nil || answer.QueriedName != "a.example" || len(answer.RealIPs) != 1 || answer.RealIPs[0] != netip.MustParseAddr("192.0.2.1") {
		t.Errorf("dnsfeed.ParseResponse = %+v, %v", answer, err)
	}
}

func TestParseDNSResponseUDP6(t *testing.T) {
	message := testDNSMessage()
	response, ok := ParseDNSResponse(testUDPPacket(6, 0, 0, 53, 51000, message))
	if !ok || response.Protocol != ProtocolUDP || !response.Client.Is6() || !response.Server.Is6() || !bytes.Equal(response.Payload, message) {
		t.Errorf("IPv6 UDP DNS response = %+v, %v", response, ok)
	}
}

func TestParseDNSResponseTCP(t *testing.T) {
	message := testDNSMessage()
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(message)))
	frame = append(frame, message...)
	raw := testTCPPacket(4, 5, 5, 0, TCPACK, frame)
	// The test TCP helper uses 49152 as its source port; this is a server reply.
	binary.BigEndian.PutUint16(raw[20:22], 53)
	binary.BigEndian.PutUint16(raw[22:24], 51000)

	response, ok := ParseDNSResponse(raw)
	if !ok || response.Protocol != ProtocolTCP || !bytes.Equal(response.Payload, frame) {
		t.Fatalf("TCP DNS response = %+v, %v", response, ok)
	}
	if response.Client != netip.MustParseAddr("104.21.0.5") || response.Server != netip.MustParseAddr("192.168.1.42") ||
		response.ClientPort != 51000 || response.ServerPort != 53 {
		t.Errorf("wrong TCP hint metadata: %+v", response)
	}
	answer, err := dnsfeed.ParseTCPResponse(response.Payload)
	if err != nil || answer.QueriedName != "a.example" || len(answer.RealIPs) != 1 {
		t.Errorf("dnsfeed.ParseTCPResponse = %+v, %v", answer, err)
	}
}

func TestParseDNSResponseTCPReturnsExactlyOneFrame(t *testing.T) {
	message := testDNSMessage()
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(message)))
	frame = append(frame, message...)
	payload := append(append([]byte(nil), frame...), 0, 1, 0xff) // beginning of another frame
	raw := testTCPPacket(6, 0, 5, 0, TCPACK, payload)
	binary.BigEndian.PutUint16(raw[40:42], 53)
	response, ok := ParseDNSResponse(raw)
	if !ok || !bytes.Equal(response.Payload, frame) {
		t.Fatalf("first TCP frame = %x, %v; want %x", response.Payload, ok, frame)
	}
	if _, err := dnsfeed.ParseTCPResponse(response.Payload); err != nil {
		t.Errorf("extracted frame has trailing stream bytes: %v", err)
	}
}

func TestParseDNSResponseRejectsWrongPortsTransportsAndFragments(t *testing.T) {
	message := testDNSMessage()
	for _, raw := range [][]byte{
		testUDPPacket(4, 5, 0, 5353, 51000, message),
		testUDPPacket(6, 0, 0, 5353, 51000, message),
		testUDPPacket(4, 5, 0x2000, 53, 51000, message),
		testUDPPacket(4, 5, 1, 53, 51000, message),
	} {
		if _, ok := ParseDNSResponse(raw); ok {
			t.Errorf("accepted invalid UDP DNS packet %x", raw[:28])
		}
	}

	frame := binary.BigEndian.AppendUint16(nil, uint16(len(message)))
	frame = append(frame, message...)
	tcp := testTCPPacket(4, 5, 5, 0, TCPACK, frame) // source 49152, not 53
	if _, ok := ParseDNSResponse(tcp); ok {
		t.Error("accepted TCP DNS response with a non-53 source port")
	}
	fragment := testTCPPacket(4, 5, 5, 0x2000, TCPACK, frame)
	binary.BigEndian.PutUint16(fragment[20:22], 53)
	if _, ok := ParseDNSResponse(fragment); ok {
		t.Error("accepted fragmented TCP DNS response")
	}
	icmp := testUDPPacket(4, 5, 0, 53, 51000, message)
	icmp[9] = 1
	if _, ok := ParseDNSResponse(icmp); ok {
		t.Error("accepted ICMP as DNS")
	}
}

func TestParseDNSResponseRejectsPartialTCPFrame(t *testing.T) {
	message := testDNSMessage()
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(message)))
	frame = append(frame, message...)
	for _, payload := range [][]byte{nil, {0}, frame[:1], frame[:2], frame[:len(frame)-1]} {
		raw := testTCPPacket(4, 5, 5, 0, TCPACK, payload)
		binary.BigEndian.PutUint16(raw[20:22], 53)
		if _, ok := ParseDNSResponse(raw); ok {
			t.Errorf("accepted partial TCP frame %x", payload)
		}
	}

	// The remainder in a later packet is not a new frame and must not be
	// combined with the earlier partial capture.
	remainder := testTCPPacket(4, 5, 5, 0, TCPACK, frame[len(frame)-1:])
	binary.BigEndian.PutUint16(remainder[20:22], 53)
	if _, ok := ParseDNSResponse(remainder); ok {
		t.Error("reassembled or accepted a later partial TCP frame")
	}
}

func FuzzParseUDP(f *testing.F) {
	f.Add([]byte{})
	f.Add(testUDPPacket(4, 5, 0, 53, 51000, testDNSMessage()))
	f.Add(testUDPPacket(6, 0, 0, 53, 51000, []byte{1, 2, 3}))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ParseUDP(raw)
	})
}

func FuzzParseDNSResponse(f *testing.F) {
	f.Add([]byte{})
	f.Add(testUDPPacket(4, 5, 0, 53, 51000, testDNSMessage()))
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(testDNSMessage())))
	frame = append(frame, testDNSMessage()...)
	f.Add(testTCPPacket(6, 0, 5, 0, TCPACK, frame))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ParseDNSResponse(raw)
	})
}
