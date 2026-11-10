package packetcap

import (
	"encoding/binary"
	"fmt"
)

const (
	ethernetHeader = 14
	etherTypeIPv4  = 0x0800
	etherTypeIPv6  = 0x86dd
)

// ParseFrame strips Ethernet II and up to two 802.1Q/802.1ad tags. AF_PACKET
// SOCK_RAW on a Linux bridge supplies this link-layer framing; packet.Parse*
// consumes the returned IP bytes.
func ParseFrame(raw []byte) ([]byte, error) {
	if len(raw) < ethernetHeader {
		return nil, ErrTruncated
	}
	typ := binary.BigEndian.Uint16(raw[12:14])
	offset := ethernetHeader
	for tags := 0; isVLAN(typ); tags++ {
		if tags == 2 {
			return nil, fmt.Errorf("%w: more than two VLAN tags", ErrMalformed)
		}
		if len(raw) < offset+4 {
			return nil, ErrTruncated
		}
		typ = binary.BigEndian.Uint16(raw[offset+2 : offset+4])
		offset += 4
	}
	if typ != etherTypeIPv4 && typ != etherTypeIPv6 {
		return nil, ErrNotIP
	}
	if len(raw) == offset {
		return nil, ErrTruncated
	}
	return raw[offset:], nil
}

func isVLAN(typ uint16) bool {
	return typ == 0x8100 || typ == 0x88a8 || typ == 0x9100
}
