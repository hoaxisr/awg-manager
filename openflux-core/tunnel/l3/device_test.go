package l3

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// deviceTCP builds a packet as a phone's kernel writes it to the TUN interface.
func deviceTCP(flags byte, opts, payload []byte) []byte {
	thl := 20 + len(opts)
	p := make([]byte, 20+thl+len(payload))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	binary.BigEndian.PutUint16(p[4:6], 11)
	p[6] = 0x40
	p[8] = 64
	p[9] = 6
	copy(p[12:16], []byte{10, 10, 10, 2})
	copy(p[16:20], []byte{149, 154, 167, 41})
	binary.BigEndian.PutUint16(p[20:22], 50000)
	binary.BigEndian.PutUint16(p[22:24], 443)
	binary.BigEndian.PutUint32(p[24:28], 1000)
	p[32] = byte(thl/4) << 4
	p[33] = flags
	binary.BigEndian.PutUint16(p[34:36], 65535)
	copy(p[40:], opts)
	copy(p[40+len(opts):], payload)
	fixChecksums(p)
	return p
}

// MSS 1360, SACK permitted, timestamps, window scale: what an Android kernel puts in a SYN.
var synOptions = []byte{2, 4, 0x05, 0x50, 4, 2, 8, 10, 0, 0, 0, 1, 0, 0, 0, 0, 1, 3, 3, 7}

func mssOf(p []byte) int { return int(binary.BigEndian.Uint16(p[42:44])) }

func TestClampSynMSSLowersTheOptionAndKeepsTheRest(t *testing.T) {
	for _, flags := range []byte{0x02, 0x12} { // SYN, SYN-ACK
		p := deviceTCP(flags, synOptions, nil)
		orig := append([]byte(nil), p...)
		calls := 0
		if !ClampSynMSS(p, func() int { calls++; return 1160 }) {
			t.Fatalf("flags %#x: not clamped", flags)
		}
		if calls != 1 || mssOf(p) != 1160 {
			t.Fatalf("flags %#x: %d calls, MSS %d", flags, calls, mssOf(p))
		}
		if !validTCPChecksums(p) {
			t.Fatalf("flags %#x: bad checksum after the change", flags)
		}
		// Only the MSS and the two checksums may differ.
		for i := range p {
			switch i {
			case 10, 11, 36, 37, 42, 43:
			default:
				if p[i] != orig[i] {
					t.Fatalf("flags %#x: byte %d changed", flags, i)
				}
			}
		}
	}
}

func TestClampSynMSSLeavesWhatDoesNotNeedIt(t *testing.T) {
	small := deviceTCP(0x02, []byte{2, 4, 0x04, 0x4c, 4, 2, 1, 1}, nil) // MSS 1100
	data := deviceTCP(0x18, nil, bytes.Repeat([]byte{7}, 1348))
	noOpts := deviceTCP(0x02, nil, nil)
	badLen := deviceTCP(0x02, []byte{1, 1, 2, 9}, nil)           // an MSS option that claims more than there is
	odd := deviceTCP(0x02, []byte{3, 3, 7, 2, 4, 5, 80, 0}, nil) // another option first, MSS 1360 after it
	for name, tc := range map[string]struct {
		p     []byte
		clamp bool
	}{"already small": {small, false}, "data packet": {data, false}, "no options": {noOpts, false}, "malformed": {badLen, false}, "second option": {odd, true}} {
		before := append([]byte(nil), tc.p...)
		called := false
		got := ClampSynMSS(tc.p, func() int { called = true; return 1160 })
		if got != tc.clamp {
			t.Fatalf("%s: clamped = %v, want %v", name, got, tc.clamp)
		}
		if !tc.clamp && !bytes.Equal(tc.p, before) {
			t.Fatalf("%s: the packet changed", name)
		}
		if name == "data packet" && called {
			t.Fatalf("%s: the limit was asked for a packet that is not a SYN", name)
		}
		if tc.clamp && !validTCPChecksums(tc.p) {
			t.Fatalf("%s: bad checksum", name)
		}
	}
	if got := binary.BigEndian.Uint16(odd[45:47]); got != 1160 {
		t.Fatalf("the MSS after another option is %d, want 1160", got)
	}
}
