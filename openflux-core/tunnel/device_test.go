package tunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
	"sync"
	"testing"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

// declaredCarrier stands in for a session whose only carrier declares a packet size, as MAX does: it
// refuses any packet above it, the way Session.send does for TCP.
type declaredCarrier struct {
	limit int

	mu       sync.Mutex
	sent     [][]byte
	refusals int
}

func (c *declaredCarrier) Start() error                    { return nil }
func (c *declaredCarrier) Stop() error                     { return nil }
func (c *declaredCarrier) IsConnected() bool               { return true }
func (c *declaredCarrier) Stats() transport.TransportStats { return transport.TransportStats{} }
func (c *declaredCarrier) Receive(func([]byte))            {}
func (c *declaredCarrier) CarrierMTU() int                 { return c.limit }

func (c *declaredCarrier) Send(p []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(p) > c.limit {
		c.refusals++
		return &transport.PacketTooLargeError{Limit: c.limit}
	}
	c.sent = append(c.sent, append([]byte(nil), p...))
	return nil
}

func onesComplement(parts ...[]byte) uint16 {
	var sum uint32
	for _, b := range parts {
		for i := 0; i+1 < len(b); i += 2 {
			sum += uint32(b[i])<<8 | uint32(b[i+1])
		}
		if len(b)%2 == 1 {
			sum += uint32(b[len(b)-1]) << 8
		}
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

func setChecksums(p []byte) {
	p[10], p[11] = 0, 0
	binary.BigEndian.PutUint16(p[10:12], onesComplement(p[:20]))
	seg := p[20:]
	if p[9] == 6 {
		seg[16], seg[17] = 0, 0
		pseudo := append(append([]byte(nil), p[12:20]...), 0, 6, byte(len(seg)>>8), byte(len(seg)))
		binary.BigEndian.PutUint16(seg[16:18], onesComplement(pseudo, seg))
	}
}

func checksumsValid(p []byte) bool {
	pseudo := append(append([]byte(nil), p[12:20]...), 0, 6, byte(len(p[20:])>>8), byte(len(p[20:])))
	return onesComplement(p[:20]) == 0 && (p[9] != 6 || onesComplement(pseudo, p[20:]) == 0)
}

// deviceSegment is a packet the phone's kernel writes to the TUN: DF set, timestamps on.
func deviceSegment(proto byte, flags byte, opts, payload []byte, seq uint32) []byte {
	l4 := 20 + len(opts)
	if proto == 17 {
		l4 = 8
	}
	p := make([]byte, 20+l4+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	binary.BigEndian.PutUint16(p[4:6], 900)
	p[6] = 0x40
	p[8] = 64
	p[9] = proto
	copy(p[12:16], []byte{10, 10, 10, 2})
	copy(p[16:20], []byte{149, 154, 167, 41})
	binary.BigEndian.PutUint16(p[20:22], 50000)
	binary.BigEndian.PutUint16(p[22:24], 443)
	if proto == 6 {
		binary.BigEndian.PutUint32(p[24:28], seq)
		binary.BigEndian.PutUint32(p[28:32], 77)
		p[32] = byte(l4/4) << 4
		p[33] = flags
		binary.BigEndian.PutUint16(p[34:36], 4096)
		copy(p[40:], opts)
	} else {
		binary.BigEndian.PutUint16(p[24:26], uint16(8+len(payload)))
	}
	copy(p[20+l4:], payload)
	setChecksums(p)
	return p
}

var (
	timestamps = []byte{1, 1, 8, 10, 0, 0, 0, 5, 0, 0, 0, 6}
	androidSyn = []byte{2, 4, 0x05, 0x50, 4, 2, 8, 10, 0, 0, 0, 1, 0, 0, 0, 0, 1, 3, 3, 7} // MSS 1360
)

// The first segment of a connection that is above the declared size used to be refused and lost: the
// device's kernel was never told, and sent it again in the same size.
func TestSendFromDeviceCutsTCPThatTheCarrierRefuses(t *testing.T) {
	c := &declaredCarrier{limit: 1200}
	payload := make([]byte, 1348) // MSS 1360 less the timestamps: a 1400-byte packet
	for i := range payload {
		payload[i] = byte(i * 13)
	}
	p := deviceSegment(6, 0x18, timestamps, payload, 0xffffff00) // PSH|ACK, the sequence number wraps in the packet
	if err := SendFromDevice(c, p); err != nil {
		t.Fatal(err)
	}
	if c.refusals != 1 || len(c.sent) != 2 {
		t.Fatalf("%d refusals, %d packets sent, want 1 and 2", c.refusals, len(c.sent))
	}
	var got []byte
	seq := uint32(0xffffff00)
	for i, part := range c.sent {
		if len(part) > 1200 {
			t.Fatalf("part %d is %d bytes", i, len(part))
		}
		if !checksumsValid(part) {
			t.Fatalf("part %d: bad checksum", i)
		}
		if s := binary.BigEndian.Uint32(part[24:28]); s != seq {
			t.Fatalf("part %d: seq %d, want %d", i, s, seq)
		}
		if !bytes.Equal(part[40:52], timestamps) || !bytes.Equal(part[12:20], p[12:20]) || part[6] != 0x40 {
			t.Fatalf("part %d: options, addresses or DF changed", i)
		}
		pay := part[52:]
		got = append(got, pay...)
		seq += uint32(len(pay))
		if psh := part[33]&0x08 != 0; psh != (i == len(c.sent)-1) {
			t.Fatalf("part %d: PSH = %v", i, psh)
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("the pieces do not make up the segment")
	}
}

func TestSendFromDeviceSendsWhatFitsAsItIs(t *testing.T) {
	c := &declaredCarrier{limit: 1200}
	p := deviceSegment(6, 0x10, timestamps, make([]byte, 1000), 5)
	if err := SendFromDevice(c, p); err != nil {
		t.Fatal(err)
	}
	if c.refusals != 0 || len(c.sent) != 1 || !bytes.Equal(c.sent[0], p) {
		t.Fatalf("%d refusals, %d packets: a packet that fits was changed", c.refusals, len(c.sent))
	}
}

// Only TCP can be cut: a datagram refused by a carrier stays an error the caller sees.
func TestSendFromDeviceDoesNotCutOtherProtocols(t *testing.T) {
	c := &declaredCarrier{limit: 1200}
	p := deviceSegment(17, 0, nil, make([]byte, 1300), 0)
	err := SendFromDevice(c, p)
	var big *transport.PacketTooLargeError
	if !errors.As(err, &big) || len(c.sent) != 0 {
		t.Fatalf("err = %v, %d packets sent", err, len(c.sent))
	}
}

// New connections do not need the cut: the SYN the device writes and the SYN-ACK it receives are held
// to the declared size, so both ends send segments that fit.
func TestSynsAreClampedToTheCarrier(t *testing.T) {
	c := &declaredCarrier{limit: 1200}
	syn := deviceSegment(6, 0x02, androidSyn, nil, 1)
	if err := SendFromDevice(c, syn); err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint16(c.sent[0][42:44]); got != 1160 || !checksumsValid(c.sent[0]) {
		t.Fatalf("outgoing SYN: MSS %d (want 1160), checksums valid %v", got, checksumsValid(c.sent[0]))
	}

	// Telegram announces 1240 in its SYN-ACK; the device would send 1280-byte packets.
	synAck := deviceSegment(6, 0x12, []byte{2, 4, 0x04, 0xd8, 4, 2, 1, 1}, nil, 9)
	ClampDeviceSyn(c, synAck)
	if got := binary.BigEndian.Uint16(synAck[42:44]); got != 1160 || !checksumsValid(synAck) {
		t.Fatalf("incoming SYN-ACK: MSS %d (want 1160), checksums valid %v", got, checksumsValid(synAck))
	}

	// A carrier with no limit leaves them alone.
	free := &declaredCarrier{limit: 1500}
	again := deviceSegment(6, 0x02, androidSyn, nil, 1)
	before := append([]byte(nil), again...)
	ClampDeviceSyn(free, again)
	if !bytes.Equal(again, before) {
		t.Fatal("a SYN was changed for a carrier that moves what the device's MTU allows")
	}
}
