// Package packet turns AF_PACKET packet copies into normalized autolist events.
// Parsers accept truncated captures, but never follow a length past the bytes
// that actually arrived.
package packet

import (
	"encoding/binary"
	"net/netip"
)

// Kind identifies the AF_PACKET stream that produced an Event.
type Kind uint8

const (
	KindInvalid Kind = iota
	HELLO
	RESET
	ANSWERED
	FIN
	CLOSE_RST
	SYN
	NEW
)

func (k Kind) String() string {
	switch k {
	case HELLO:
		return "HELLO"
	case RESET:
		return "RESET"
	case ANSWERED:
		return "ANSWERED"
	case FIN:
		return "FIN"
	case CLOSE_RST:
		return "CLOSE_RST"
	case SYN:
		return "SYN"
	case NEW:
		return "NEW"
	default:
		return "INVALID"
	}
}

// Valid reports whether k is one of the seven normalized event kinds.
func (k Kind) Valid() bool { return k >= HELLO && k <= NEW }

// TCP flag values from the TCP header. The NS bit is not part of Packet.Flags.
const (
	TCPFIN = uint8(0x01)
	TCPSYN = uint8(0x02)
	TCPRST = uint8(0x04)
	TCPPSH = uint8(0x08)
	TCPACK = uint8(0x10)
	TCPURG = uint8(0x20)
	TCPECE = uint8(0x40)
	TCPCWR = uint8(0x80)
)

const (
	ProtocolTCP = uint8(6)
	ProtocolUDP = uint8(17)
)

// Flow is the comparable, directional five-tuple used to correlate events.
// Protocol is TCP for every kind except NEW, where it may also be UDP.
type Flow struct {
	Src      netip.Addr
	Dst      netip.Addr
	SrcPort  uint16
	DstPort  uint16
	Protocol uint8
}

// Reverse returns the same flow with its endpoints exchanged.
func (f Flow) Reverse() Flow {
	f.Src, f.Dst = f.Dst, f.Src
	f.SrcPort, f.DstPort = f.DstPort, f.SrcPort
	return f
}

// Packet is a parsed IP/TCP or IP/UDP packet. Payload borrows from the input
// buffer and must not be retained after that buffer is reused. Fields that do
// not apply to UDP or a NEW event are zero.
type Packet struct {
	Src      netip.Addr
	Dst      netip.Addr
	SrcPort  uint16
	DstPort  uint16
	Protocol uint8
	Seq      uint32
	Ack      uint32
	Flags    uint8
	Payload  []byte
}

// Flow returns the packet's directional flow key.
func (p Packet) Flow() Flow {
	return Flow{
		Src:      p.Src,
		Dst:      p.Dst,
		SrcPort:  p.SrcPort,
		DstPort:  p.DstPort,
		Protocol: p.Protocol,
	}
}

// Reverse exchanges addresses and ports. Sequence and acknowledgement numbers
// describe the captured packet, so they are deliberately left unchanged.
func (p Packet) Reverse() Packet {
	p.Src, p.Dst = p.Dst, p.Src
	p.SrcPort, p.DstPort = p.DstPort, p.SrcPort
	return p
}

// Event is a packet normalized with the kind of the AF_PACKET stream. SNI is lower
// case and has one trailing root dot removed; it is empty when a ClientHello
// has no usable server_name.
type Event struct {
	Kind Kind
	Packet
	SNI string
}

// Flow returns the event's directional flow key.
func (e Event) Flow() Flow { return e.Packet.Flow() }

// Reverse exchanges addresses and ports while retaining the event kind, TCP
// numbers, payload and SNI. It is useful for matching replies to client flows.
func (e Event) Reverse() Event {
	e.Packet = e.Packet.Reverse()
	return e
}

// Parse parses raw as an event from the stream identified by kind.
//
// NEW accepts TCP and UDP IP headers and can still be useful when a short copy
// contains no ports. Every other kind requires a complete TCP header. HELLO
// additionally requires a TLS handshake record whose first handshake message
// is ClientHello. Flag checks match the corresponding AF_PACKET rules: RESET needs
// RST, FIN needs FIN, CLOSE_RST needs RST|ACK, and SYN needs SYN without ACK.
func Parse(kind Kind, raw []byte) (Event, bool) {
	if !kind.Valid() {
		return Event{}, false
	}
	if kind == NEW {
		p, ok := ParseHead(raw)
		if !ok || (p.Protocol != ProtocolTCP && p.Protocol != ProtocolUDP) {
			return Event{}, false
		}
		return Event{Kind: kind, Packet: p}, true
	}

	p, ok := ParseTCP(raw)
	if !ok {
		return Event{}, false
	}
	e := Event{Kind: kind, Packet: p}
	switch kind {
	case HELLO:
		if len(p.Payload) <= 5 || p.Payload[0] != 0x16 || p.Payload[5] != 0x01 {
			return Event{}, false
		}
		e.SNI, _ = ClientHelloSNI(p.Payload)
	case RESET:
		if p.Flags&TCPRST == 0 {
			return Event{}, false
		}
	case ANSWERED:
		// The answered stream is selected by connection byte counts; there is
		// no additional TCP flag predicate.
	case FIN:
		if p.Flags&TCPFIN == 0 {
			return Event{}, false
		}
	case CLOSE_RST:
		if p.Flags&(TCPRST|TCPACK) != TCPRST|TCPACK {
			return Event{}, false
		}
	case SYN:
		if p.Flags&(TCPSYN|TCPACK) != TCPSYN {
			return Event{}, false
		}
	}
	return e, true
}

// ParseEvent is an explicit alias for Parse.
func ParseEvent(kind Kind, raw []byte) (Event, bool) { return Parse(kind, raw) }

// ParseTCP parses an unfragmented IPv4 or IPv6 TCP packet. IPv6 extension
// chains are intentionally not followed: a short AF_PACKET copy cannot reliably
// establish that a chain is complete.
func ParseTCP(raw []byte) (Packet, bool) {
	p, l4, ok := ipHeader(raw, true)
	if !ok || p.Protocol != ProtocolTCP {
		return Packet{}, false
	}
	if len(l4) < 20 {
		return Packet{}, false
	}
	headerLen := int(l4[12]>>4) * 4
	if headerLen < 20 || headerLen > len(l4) {
		return Packet{}, false
	}
	p.SrcPort = binary.BigEndian.Uint16(l4[0:2])
	p.DstPort = binary.BigEndian.Uint16(l4[2:4])
	p.Seq = binary.BigEndian.Uint32(l4[4:8])
	p.Ack = binary.BigEndian.Uint32(l4[8:12])
	p.Flags = l4[13]
	p.Payload = l4[headerLen:]
	return p, true
}

// ParseUDP parses an unfragmented IPv4 or IPv6 UDP packet. Payload contains
// the UDP bytes that arrived, excluding the eight-byte header and any bytes
// beyond the UDP length. As with ParseTCP, an AF_PACKET copy may end inside the
// payload; use ParseDNSResponse when a complete UDP datagram is required.
func ParseUDP(raw []byte) (Packet, bool) {
	p, _, ok := parseUDP(raw)
	return p, ok
}

// parseUDP also reports whether the complete UDP datagram arrived.
func parseUDP(raw []byte) (Packet, bool, bool) {
	p, l4, ok := ipHeader(raw, true)
	if !ok || p.Protocol != ProtocolUDP || len(l4) < 8 {
		return Packet{}, false, false
	}
	length := int(binary.BigEndian.Uint16(l4[4:6]))
	if length < 8 {
		return Packet{}, false, false
	}
	p.SrcPort = binary.BigEndian.Uint16(l4[0:2])
	p.DstPort = binary.BigEndian.Uint16(l4[2:4])
	end := length
	complete := end <= len(l4)
	if end > len(l4) {
		end = len(l4)
	}
	p.Payload = l4[8:end]
	return p, complete, true
}

// ParseHead parses an IPv4 or IPv6 header and, when at least four bytes of a
// TCP or UDP header arrived, its ports. It accepts a first IPv4 fragment but
// rejects later fragments. This is intended for NEW; use ParseTCP for streams
// that need flags, sequence numbers or payload.
func ParseHead(raw []byte) (Packet, bool) {
	p, l4, ok := ipHeader(raw, false)
	if !ok {
		return Packet{}, false
	}
	if (p.Protocol == ProtocolTCP || p.Protocol == ProtocolUDP) && len(l4) >= 4 {
		p.SrcPort = binary.BigEndian.Uint16(l4[0:2])
		p.DstPort = binary.BigEndian.Uint16(l4[2:4])
	}
	return p, true
}

// ipHeader returns the transport bytes that actually arrived. IP payload
// lengths may exceed the capture because AF_PACKET deliberately copies only a
// prefix. A smaller valid declared length trims link/datagram trailing bytes.
func ipHeader(raw []byte, wholeTransport bool) (Packet, []byte, bool) {
	if len(raw) == 0 {
		return Packet{}, nil, false
	}
	var p Packet
	switch raw[0] >> 4 {
	case 4:
		if len(raw) < 20 {
			return Packet{}, nil, false
		}
		headerLen := int(raw[0]&0x0f) * 4
		if wholeTransport && (headerLen < 20 || headerLen > len(raw)) {
			return Packet{}, nil, false
		}
		frag := binary.BigEndian.Uint16(raw[6:8])
		if frag&0x1fff != 0 || (wholeTransport && frag&0x2000 != 0) {
			return Packet{}, nil, false
		}
		p.Protocol = raw[9]
		p.Src = netip.AddrFrom4([4]byte(raw[12:16]))
		p.Dst = netip.AddrFrom4([4]byte(raw[16:20]))
		end := len(raw)
		declared := int(binary.BigEndian.Uint16(raw[2:4]))
		if declared >= headerLen && declared < end && declared >= 20 {
			end = declared
		}
		if headerLen < 20 || headerLen > end {
			return p, nil, true
		}
		return p, raw[headerLen:end], true
	case 6:
		if len(raw) < 40 {
			return Packet{}, nil, false
		}
		p.Protocol = raw[6]
		p.Src = netip.AddrFrom16([16]byte(raw[8:24]))
		p.Dst = netip.AddrFrom16([16]byte(raw[24:40]))
		end := len(raw)
		declared := int(binary.BigEndian.Uint16(raw[4:6]))
		if declared <= end-40 {
			end = 40 + declared
		}
		if end < 40 {
			return Packet{}, nil, false
		}
		return p, raw[40:end], true
	default:
		return Packet{}, nil, false
	}
}
