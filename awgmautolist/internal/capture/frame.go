package capture

import (
	"encoding/binary"
	"net"
	"net/netip"
)

const (
	networkTCP = 6
	networkUDP = 17

	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpACK = 0x10
)

// Frame is one complete IP transport packet observed on a LAN bridge.
type Frame struct {
	Network   uint8
	Src       netip.Addr
	Dst       netip.Addr
	SrcPort   uint16
	DstPort   uint16
	Sequence  uint32
	Flags     uint8
	Payload   []byte
	SourceMAC string
}

// ParseFrame accepts Ethernet II with up to two VLAN tags, IPv4 and common
// IPv6 extension headers. Fragmented and incomplete payloads are ignored: they
// must not produce false TLS, DNS or byte-count evidence.
func ParseFrame(raw []byte) (Frame, bool) {
	if len(raw) < 14 {
		return Frame{}, false
	}
	var frame Frame
	frame.SourceMAC = net.HardwareAddr(raw[6:12]).String()
	etherType := binary.BigEndian.Uint16(raw[12:14])
	offset := 14
	for tags := 0; etherType == 0x8100 || etherType == 0x88a8 || etherType == 0x9100; tags++ {
		if tags == 2 || len(raw) < offset+4 {
			return Frame{}, false
		}
		etherType = binary.BigEndian.Uint16(raw[offset+2 : offset+4])
		offset += 4
	}

	switch etherType {
	case 0x0800:
		if len(raw) < offset+20 || raw[offset]>>4 != 4 {
			return Frame{}, false
		}
		headerLen := int(raw[offset]&0x0f) * 4
		if headerLen < 20 || len(raw) < offset+headerLen {
			return Frame{}, false
		}
		totalLen := int(binary.BigEndian.Uint16(raw[offset+2 : offset+4]))
		if totalLen < headerLen || len(raw) < offset+totalLen {
			return Frame{}, false
		}
		fragment := binary.BigEndian.Uint16(raw[offset+6 : offset+8])
		if fragment&0x3fff != 0 { // more fragments or non-zero offset
			return Frame{}, false
		}
		frame.Network = raw[offset+9]
		frame.Src, _ = netip.AddrFromSlice(raw[offset+12 : offset+16])
		frame.Dst, _ = netip.AddrFromSlice(raw[offset+16 : offset+20])
		offset += headerLen
		raw = raw[:offset+totalLen-headerLen]
	case 0x86dd:
		ipStart := offset
		if len(raw) < ipStart+40 || raw[ipStart]>>4 != 6 {
			return Frame{}, false
		}
		payloadLen := int(binary.BigEndian.Uint16(raw[ipStart+4 : ipStart+6]))
		packetEnd := ipStart + 40 + payloadLen
		if len(raw) < packetEnd {
			return Frame{}, false
		}
		frame.Src, _ = netip.AddrFromSlice(raw[ipStart+8 : ipStart+24])
		frame.Dst, _ = netip.AddrFromSlice(raw[ipStart+24 : ipStart+40])
		next := raw[ipStart+6]
		offset = ipStart + 40
		remaining := payloadLen
	extensions:
		for count := 0; ; count++ {
			if count == 8 {
				return Frame{}, false
			}
			switch next {
			case 0, 43, 60: // hop-by-hop, routing, destination options
				if remaining < 2 || len(raw) < offset+2 {
					return Frame{}, false
				}
				size := (int(raw[offset+1]) + 1) * 8
				if size < 8 || remaining < size || len(raw) < offset+size {
					return Frame{}, false
				}
				next = raw[offset]
				offset += size
				remaining -= size
			case 44: // no fragment reassembly
				return Frame{}, false
			case 51: // authentication header
				if remaining < 2 || len(raw) < offset+2 {
					return Frame{}, false
				}
				size := (int(raw[offset+1]) + 2) * 4
				if size < 8 || remaining < size || len(raw) < offset+size {
					return Frame{}, false
				}
				next = raw[offset]
				offset += size
				remaining -= size
			case 50, 59: // ESP is opaque; no-next-header has no transport
				return Frame{}, false
			default:
				frame.Network = next
				break extensions
			}
		}
		raw = raw[:packetEnd]
	default:
		return Frame{}, false
	}

	switch frame.Network {
	case networkTCP:
		if len(raw) < offset+20 {
			return Frame{}, false
		}
		headerLen := int(raw[offset+12]>>4) * 4
		if headerLen < 20 || len(raw) < offset+headerLen {
			return Frame{}, false
		}
		frame.SrcPort = binary.BigEndian.Uint16(raw[offset : offset+2])
		frame.DstPort = binary.BigEndian.Uint16(raw[offset+2 : offset+4])
		frame.Sequence = binary.BigEndian.Uint32(raw[offset+4 : offset+8])
		frame.Flags = raw[offset+13]
		frame.Payload = raw[offset+headerLen:]
	case networkUDP:
		if len(raw) < offset+8 {
			return Frame{}, false
		}
		udpLen := int(binary.BigEndian.Uint16(raw[offset+4 : offset+6]))
		if udpLen < 8 || len(raw) < offset+udpLen {
			return Frame{}, false
		}
		frame.SrcPort = binary.BigEndian.Uint16(raw[offset : offset+2])
		frame.DstPort = binary.BigEndian.Uint16(raw[offset+2 : offset+4])
		frame.Payload = raw[offset+8 : offset+udpLen]
	default:
		return Frame{}, false
	}
	return frame, true
}
