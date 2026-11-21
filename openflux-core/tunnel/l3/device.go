package l3

import "encoding/binary"

// SplitTCP is splitTCP for the clients that take the packets of a device's own TCP from a TUN
// interface: the interface has an MTU of its own, the carrier may move less, and the segments that do
// not fit have to be cut where nobody else can do it.
func SplitTCP(p []byte, limit int) [][]byte { return splitTCP(p, limit) }

// ClampSynMSS lowers the MSS option of a TCP SYN or SYN-ACK in place to what maxMSS returns, when it
// is higher, and reports whether it did. maxMSS is called only for a SYN that has the option.
//
// This is what a router with a smaller MTU on the path does (iptables TCPMSS --clamp-mss-to-pmtu), at
// the end of a tunnel that cannot tell a device's TCP about its MTU in any other way: the segments are
// sized right from the start, in both directions, instead of being cut and sent again.
func ClampSynMSS(p []byte, maxMSS func() int) bool {
	p, ok := sliceIPv4(p)
	if !ok || p[9] != 6 || isFragmentedIPv4(p) {
		return false
	}
	ihl := int(p[0]&0x0f) * 4
	if len(p) < ihl+20 || p[ihl+13]&0x02 == 0 {
		return false
	}
	thl := int(p[ihl+12]>>4) * 4
	if thl < 24 || len(p) < ihl+thl {
		return false
	}
	opts := p[ihl+20 : ihl+thl]
	for i := 0; i < len(opts); {
		switch opts[i] {
		case 0:
			return false
		case 1:
			i++
		default:
			if i+1 >= len(opts) {
				return false
			}
			n := int(opts[i+1])
			if n < 2 || i+n > len(opts) {
				return false
			}
			if opts[i] != 2 || n != 4 {
				i += n
				continue
			}
			want := maxMSS()
			if want < 64 || int(binary.BigEndian.Uint16(opts[i+2:])) <= want {
				return false
			}
			binary.BigEndian.PutUint16(opts[i+2:], uint16(want))
			fixChecksums(p)
			return true
		}
	}
	return false
}
