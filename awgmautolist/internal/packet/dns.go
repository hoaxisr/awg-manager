package packet

import (
	"encoding/binary"
	"net/netip"
)

// DNSResponse is the transport payload and addressing metadata from one DNS
// response packet. Packet retains the captured server-to-client direction.
// Client and Server make the hint attribution explicit; ClientPort is the
// original destination port and ServerPort is 53.
//
// Payload is suitable for dnsfeed.ParseResponse when Protocol is UDP, or
// dnsfeed.ParseTCPResponse when Protocol is TCP. It borrows from the parser's
// input buffer. This type deliberately does not parse or validate DNS wire
// data; dnsfeed remains responsible for that.
type DNSResponse struct {
	Packet
	Client     netip.Addr
	Server     netip.Addr
	ClientPort uint16
	ServerPort uint16
}

// ParseDNSResponse extracts one DNS response payload from an unfragmented UDP
// or TCP packet whose source port is 53.
//
// UDP requires the complete datagram and returns its DNS message without the
// UDP header. TCP requires one complete two-byte-length-prefixed frame in this
// packet and returns exactly that frame, including its prefix. Bytes after the
// first frame are left for a later observation. Partial TCP frames are
// rejected: no stream reassembly or cross-packet state is attempted.
func ParseDNSResponse(raw []byte) (DNSResponse, bool) {
	if len(raw) == 0 {
		return DNSResponse{}, false
	}
	var protocol uint8
	switch raw[0] >> 4 {
	case 4:
		if len(raw) < 10 {
			return DNSResponse{}, false
		}
		protocol = raw[9]
	case 6:
		if len(raw) < 7 {
			return DNSResponse{}, false
		}
		protocol = raw[6]
	default:
		return DNSResponse{}, false
	}

	var p Packet
	switch protocol {
	case ProtocolUDP:
		udp, complete, ok := parseUDP(raw)
		if !ok || !complete {
			return DNSResponse{}, false
		}
		p = udp
	case ProtocolTCP:
		tcp, ok := ParseTCP(raw)
		if !ok {
			return DNSResponse{}, false
		}
		p = tcp
	default:
		return DNSResponse{}, false
	}

	if p.SrcPort != 53 {
		return DNSResponse{}, false
	}
	if p.Protocol == ProtocolTCP {
		if len(p.Payload) < 2 {
			return DNSResponse{}, false
		}
		frameLen := 2 + int(binary.BigEndian.Uint16(p.Payload[:2]))
		if frameLen > len(p.Payload) {
			return DNSResponse{}, false
		}
		p.Payload = p.Payload[:frameLen]
	}
	return DNSResponse{
		Packet:     p,
		Client:     p.Dst,
		Server:     p.Src,
		ClientPort: p.DstPort,
		ServerPort: p.SrcPort,
	}, true
}
