package l3

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// offloaded builds what a receive-offloaded raw socket hands over: one TCP packet of several
// segments, timestamp options, DF set, PSH (and optionally FIN) as the last segment had them.
func offloaded(payload []byte, flags byte) []byte {
	const thl = 32
	p := make([]byte, 20+thl+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	binary.BigEndian.PutUint16(p[4:6], 700)
	p[6] = 0x40
	p[8] = 57
	p[9] = 6
	copy(p[12:16], []byte{203, 0, 113, 7})
	copy(p[16:20], clientIPBytes[:])
	binary.BigEndian.PutUint16(p[20:22], 443)
	binary.BigEndian.PutUint16(p[22:24], 50000)
	binary.BigEndian.PutUint32(p[24:28], 0xfffff000) // the sequence number wraps inside the packet
	binary.BigEndian.PutUint32(p[28:32], 12345)
	p[32] = (thl / 4) << 4
	p[33] = flags
	binary.BigEndian.PutUint16(p[34:36], 16)
	copy(p[40:52], []byte{1, 1, 8, 10, 0, 0, 0, 7, 0, 0, 0, 9}) // NOP NOP timestamps
	copy(p[52:], payload)
	fixChecksums(p)
	return p
}

func validTCPChecksums(p []byte) bool {
	ihl := int(p[0]&15) * 4
	var src, dst [4]byte
	copy(src[:], p[12:16])
	copy(dst[:], p[16:20])
	return onesComplementSum(p[:ihl]) == 0 && transportChecksum(p[ihl:], src, dst, 6) == 0
}

func TestSplitTCPCutsAnAggregate(t *testing.T) {
	payload := make([]byte, 2696+43) // two full segments and a tail, as the node saw them
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	const limit = 1400
	for _, flags := range []byte{0x10, 0x18, 0x19} { // ACK, ACK|PSH, ACK|PSH|FIN
		p := offloaded(payload, flags)
		parts := splitTCP(p, limit)
		if len(parts) != 3 {
			t.Fatalf("flags %#x: %d parts, want 3", flags, len(parts))
		}
		var got []byte
		seq := binary.BigEndian.Uint32(p[24:28])
		for i, part := range parts {
			if len(part) > limit {
				t.Fatalf("part %d is %d bytes, over the limit", i, len(part))
			}
			if int(binary.BigEndian.Uint16(part[2:4])) != len(part) {
				t.Fatalf("part %d: total length does not match", i)
			}
			if !validTCPChecksums(part) {
				t.Fatalf("part %d: bad checksum", i)
			}
			if !bytes.Equal(part[12:20], p[12:20]) || !bytes.Equal(part[20:24], p[20:24]) || !bytes.Equal(part[28:32], p[28:32]) {
				t.Fatalf("part %d: addresses, ports or ack changed", i)
			}
			if !bytes.Equal(part[40:52], p[40:52]) || part[6] != 0x40 || part[8] != 57 {
				t.Fatalf("part %d: options, DF or TTL changed", i)
			}
			if s := binary.BigEndian.Uint32(part[24:28]); s != seq {
				t.Fatalf("part %d: seq %d, want %d", i, s, seq)
			}
			if id := binary.BigEndian.Uint16(part[4:6]); id != 700+uint16(i) {
				t.Fatalf("part %d: id %d", i, id)
			}
			last := i == len(parts)-1
			want := flags
			if !last {
				want &^= 0x09 // FIN and PSH only on the last
			}
			if part[33] != want {
				t.Fatalf("part %d: flags %#x, want %#x", i, part[33], want)
			}
			body := part[52:]
			seq += uint32(len(body))
			got = append(got, body...)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("flags %#x: payload changed", flags)
		}
	}
}

func TestSplitTCPLeavesWhatItCannotCut(t *testing.T) {
	big := bytes.Repeat([]byte{1}, 3000)
	if splitTCP(offloaded(big[:1000], 0x10), 1400) != nil {
		t.Fatal("cut a packet that fits")
	}
	for name, flags := range map[string]byte{"SYN": 0x12, "RST": 0x14, "URG": 0x30} {
		if splitTCP(offloaded(big, flags), 1400) != nil {
			t.Fatalf("cut a %s packet", name)
		}
	}
	frag := offloaded(big, 0x10)
	frag[6] |= 0x20 // more fragments
	if splitTCP(frag, 1400) != nil {
		t.Fatal("cut a fragment")
	}
	udp := udpPacket([4]byte{203, 0, 113, 7}, clientIPBytes, 443, 50000, big)
	if splitTCP(udp, 1400) != nil {
		t.Fatal("cut a UDP packet")
	}
	if splitTCP(offloaded(big, 0x10), 40) != nil {
		t.Fatal("cut where the headers leave no room")
	}
}

func TestReturnPathCutsAnAggregateInsteadOfRefusingIt(t *testing.T) {
	b := &recordingBackend{}
	out := &limitedTransport{recordingTransport{packets: make(chan []byte, 8)}}
	ex := &L3Exit{backend: b, trans: out}
	payload := bytes.Repeat([]byte{9}, 2696)
	wire := offloaded(payload, 0x18)
	p := append([]byte(nil), wire...)
	if err := ex.sendClient(p, wire); err != nil {
		t.Fatal(err)
	}
	if b.sent != nil {
		t.Fatal("answered an offload aggregate with an ICMP error")
	}
	var got []byte
	n := 0
	for len(out.packets) > 0 {
		part := <-out.packets
		if len(part) > 1280 {
			t.Fatalf("part of %d bytes, over the 1280 limit", len(part))
		}
		n++
		got = append(got, part[52:]...)
	}
	if n != 3 || !bytes.Equal(got, payload) {
		t.Fatalf("%d parts, payload intact: %v", n, bytes.Equal(got, payload))
	}
}

// carrierLimited is a session whose peer takes 65000-byte packets but one of whose carriers moves
// only limit: Send refuses what does not fit, as Session.send does next to a stream carrier.
type carrierLimited struct {
	recordingTransport
	limit int
}

func (c *carrierLimited) PeerParameters() (transport.PeerParameters, bool) {
	return transport.PeerParameters{Capabilities: transport.CapabilityIPv4 | transport.CapabilityTCP | transport.CapabilityUDP, MaxPacketSize: 65000}, true
}

func (c *carrierLimited) Send(p []byte) error {
	if len(p) > c.limit {
		return &transport.PacketTooLargeError{Limit: c.limit}
	}
	return c.recordingTransport.Send(p)
}

func TestReturnPathHonoursACarrierLimit(t *testing.T) {
	const limit = 1500
	newExit := func() (*L3Exit, *recordingBackend, *carrierLimited) {
		b := &recordingBackend{}
		out := &carrierLimited{recordingTransport{packets: make(chan []byte, 16)}, limit}
		return &L3Exit{backend: b, trans: out}, b, out
	}

	// TCP the carrier refuses is cut to what it takes, not lost.
	ex, b, out := newExit()
	payload := bytes.Repeat([]byte{5}, 2696)
	wire := offloaded(payload, 0x18)
	if err := ex.sendClient(append([]byte(nil), wire...), wire); err != nil {
		t.Fatal(err)
	}
	if b.sent != nil {
		t.Fatal("answered a TCP aggregate with an ICMP error")
	}
	var got []byte
	for len(out.packets) > 0 {
		part := <-out.packets
		if len(part) > limit || !validTCPChecksums(part) {
			t.Fatalf("part of %d bytes (limit %d), checksums valid: %v", len(part), limit, validTCPChecksums(part))
		}
		got = append(got, part[52:]...)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("TCP payload changed on the way")
	}

	// A packet that has the packet-size limit's answer for it keeps getting it, now with the carrier's number.
	for _, df := range []bool{true, false} {
		ex, b, out := newExit()
		wire := udpPacket([4]byte{203, 0, 113, 7}, b.EgressIP(), 443, 40000, bytes.Repeat([]byte{3}, 2000))
		if df {
			wire[6] = 0x40
			fixIPChecksum(wire)
		}
		p := append([]byte(nil), wire...)
		copy(p[16:20], clientIPBytes[:])
		binary.BigEndian.PutUint16(p[22:24], 50000)
		fixChecksums(p)
		if err := ex.sendClient(p, wire); err != nil {
			t.Fatal(err)
		}
		if df {
			if b.sent == nil || b.sent[20] != 3 || b.sent[21] != 4 || int(binary.BigEndian.Uint16(b.sent[26:28])) != limit {
				t.Fatal("no ICMP \"fragmentation needed\" naming the carrier's limit")
			}
			continue
		}
		var r reassembler
		var whole []byte
		for len(out.packets) > 0 {
			f := <-out.packets
			if len(f) > limit {
				t.Fatalf("fragment of %d bytes", len(f))
			}
			whole, _ = r.add(f, time.Now())
		}
		if whole == nil || !bytes.Equal(whole[20:], p[20:]) {
			t.Fatal("fragments lost the payload")
		}
	}

	// A packet that fits goes as it is, and any other refusal is still an error.
	ex, _, out = newExit()
	small := offloaded(bytes.Repeat([]byte{1}, 900), 0x10)
	if err := ex.sendClient(small, small); err != nil || len(out.packets) != 1 {
		t.Fatalf("a packet that fits: err %v, %d sent", err, len(out.packets))
	}
}
