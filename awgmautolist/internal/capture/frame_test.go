package capture

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func ethernetFrame(etherType uint16, payload []byte) []byte {
	frame := make([]byte, 14, 14+len(payload))
	for i := 0; i < 12; i++ {
		frame[i] = byte(i + 1)
	}
	binary.BigEndian.PutUint16(frame[12:14], etherType)
	return append(frame, payload...)
}

func ipv4Packet(t *testing.T, protocol uint8, src, dst string, transport []byte) []byte {
	t.Helper()
	packet := make([]byte, 20, 20+len(transport))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(20+len(transport)))
	packet[8] = 64
	packet[9] = protocol
	copy(packet[12:16], netip.MustParseAddr(src).AsSlice())
	copy(packet[16:20], netip.MustParseAddr(dst).AsSlice())
	return append(packet, transport...)
}

func tcpPacket(srcPort, dstPort uint16, flags uint8, payload []byte) []byte {
	packet := make([]byte, 20, 20+len(payload))
	binary.BigEndian.PutUint16(packet[0:2], srcPort)
	binary.BigEndian.PutUint16(packet[2:4], dstPort)
	binary.BigEndian.PutUint32(packet[4:8], 1000)
	packet[12] = 5 << 4
	packet[13] = flags
	return append(packet, payload...)
}

func udpPacket(srcPort, dstPort uint16, payload []byte) []byte {
	packet := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint16(packet[0:2], srcPort)
	binary.BigEndian.PutUint16(packet[2:4], dstPort)
	binary.BigEndian.PutUint16(packet[4:6], uint16(8+len(payload)))
	return append(packet, payload...)
}

func TestParseTCPAndUDPFrames(t *testing.T) {
	tcp := ethernetFrame(0x0800, ipv4Packet(t, networkTCP, "192.168.31.50", "185.59.211.40", tcpPacket(51000, 443, tcpSYN|tcpACK, []byte("payload"))))
	frame, ok := ParseFrame(tcp)
	if !ok || frame.Network != networkTCP || frame.SrcPort != 51000 || frame.DstPort != 443 ||
		frame.Src.String() != "192.168.31.50" || string(frame.Payload) != "payload" || frame.SourceMAC == "" {
		t.Fatalf("unexpected TCP frame: %+v ok=%t", frame, ok)
	}
	udp := ethernetFrame(0x0800, ipv4Packet(t, networkUDP, "192.168.31.50", "157.240.0.18", udpPacket(53000, 3478, []byte("stun"))))
	frame, ok = ParseFrame(udp)
	if !ok || frame.Network != networkUDP || frame.DstPort != 3478 || string(frame.Payload) != "stun" {
		t.Fatalf("unexpected UDP frame: %+v ok=%t", frame, ok)
	}
}

func TestParseQinQ(t *testing.T) {
	ip := ipv4Packet(t, networkTCP, "192.168.31.50", "185.59.211.40", tcpPacket(51000, 443, tcpSYN, nil))
	vlan := make([]byte, 8, 8+len(ip))
	binary.BigEndian.PutUint16(vlan[2:4], 0x8100)
	binary.BigEndian.PutUint16(vlan[6:8], 0x0800)
	vlan = append(vlan, ip...)
	if _, ok := ParseFrame(ethernetFrame(0x88a8, vlan)); !ok {
		t.Fatal("QinQ frame was rejected")
	}
}

func TestParseIPv6ExtensionHeaders(t *testing.T) {
	src := netip.MustParseAddr("2001:db8:1::10").AsSlice()
	dst := netip.MustParseAddr("2606:4700:10::6814:1508").AsSlice()
	transport := udpPacket(53000, 3478, []byte("stun"))
	packet := make([]byte, 40, 40+8+len(transport))
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], uint16(8+len(transport)))
	packet[6] = 60 // destination options
	packet[7] = 64
	copy(packet[8:24], src)
	copy(packet[24:40], dst)
	extension := []byte{networkUDP, 0, 0, 0, 0, 0, 0, 0}
	packet = append(packet, extension...)
	packet = append(packet, transport...)
	frame, ok := ParseFrame(ethernetFrame(0x86dd, packet))
	if !ok || frame.Network != networkUDP || frame.DstPort != 3478 || string(frame.Payload) != "stun" {
		t.Fatalf("unexpected IPv6 frame: %+v ok=%t", frame, ok)
	}
}

func TestParseFrameEveryTruncationIsSafe(t *testing.T) {
	full := ethernetFrame(0x0800, ipv4Packet(t, networkTCP, "192.168.31.50", "185.59.211.40", tcpPacket(51000, 443, tcpACK, []byte("payload"))))
	for size := 0; size < len(full); size++ {
		if frame, ok := ParseFrame(full[:size]); ok {
			t.Fatalf("truncated frame accepted at %d/%d: %+v", size, len(full), frame)
		}
	}
}

func TestParseFrameRejectsFragmentsAndInvalidLengths(t *testing.T) {
	ip := ipv4Packet(t, networkUDP, "192.168.31.50", "157.240.0.18", udpPacket(53000, 3478, nil))
	binary.BigEndian.PutUint16(ip[6:8], 0x2000)
	if _, ok := ParseFrame(ethernetFrame(0x0800, ip)); ok {
		t.Fatal("fragmented IPv4 packet accepted")
	}
	ip = ipv4Packet(t, networkUDP, "192.168.31.50", "157.240.0.18", udpPacket(53000, 3478, nil))
	binary.BigEndian.PutUint16(ip[20+4:20+6], 100)
	if _, ok := ParseFrame(ethernetFrame(0x0800, ip)); ok {
		t.Fatal("invalid UDP length accepted")
	}
}
