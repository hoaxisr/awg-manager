package traffic

import (
	"encoding/binary"
	"net"
	"strings"
)

// ExtractSNI parses a TLS handshake packet and returns the Server Name Indication (SNI) hostname if present.
func ExtractSNI(payload []byte) string {
	// TLS record header: ContentType (1 byte: 0x16 for Handshake), Version (2 bytes), Length (2 bytes)
	if len(payload) < 5 || payload[0] != 0x16 {
		return ""
	}

	pos := 5
	// Handshake header: Type (1 byte: 0x01 for ClientHello), Length (3 bytes)
	if len(payload) < pos+4 || payload[pos] != 0x01 {
		return ""
	}
	pos += 4

	// Client Version (2 bytes) + Random (32 bytes)
	if len(payload) < pos+34 {
		return ""
	}
	pos += 34

	// Session ID
	if len(payload) < pos+1 {
		return ""
	}
	sessionIDLen := int(payload[pos])
	pos += 1 + sessionIDLen

	// Cipher Suites
	if len(payload) < pos+2 {
		return ""
	}
	cipherSuitesLen := int(binary.BigEndian.Uint16(payload[pos : pos+2]))
	pos += 2 + cipherSuitesLen

	// Compression Methods
	if len(payload) < pos+1 {
		return ""
	}
	compMethodsLen := int(payload[pos])
	pos += 1 + compMethodsLen

	// Extensions
	if len(payload) < pos+2 {
		return ""
	}
	extensionsLen := int(binary.BigEndian.Uint16(payload[pos : pos+2]))
	pos += 2
	if pos+extensionsLen > len(payload) {
		extensionsLen = len(payload) - pos
	}

	extEnd := pos + extensionsLen
	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(payload[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(payload[pos+2 : pos+4]))
		pos += 4
		if pos+extLen > extEnd {
			break
		}

		if extType == 0x0000 { // Server Name Indication
			sniPos := pos
			if sniPos+2 <= pos+extLen {
				serverNameListLen := int(binary.BigEndian.Uint16(payload[sniPos : sniPos+2]))
				sniPos += 2
				if sniPos+3 <= pos+extLen && serverNameListLen > 0 {
					nameType := payload[sniPos]
					nameLen := int(binary.BigEndian.Uint16(payload[sniPos+1 : sniPos+3]))
					sniPos += 3
					if nameType == 0 && sniPos+nameLen <= pos+extLen {
						hostname := string(payload[sniPos : sniPos+nameLen])
						return strings.TrimSpace(strings.ToLower(hostname))
					}
				}
			}
		}
		pos += extLen
	}

	return ""
}

// ParseDNSAnswers parses a raw DNS response packet and returns a map of IP -> Domain mappings.
func ParseDNSAnswers(dnsPacket []byte) map[string]string {
	result := make(map[string]string)
	if len(dnsPacket) < 12 {
		return result
	}

	// Flags: QR bit must be 1 (Response)
	flags := binary.BigEndian.Uint16(dnsPacket[2:4])
	if (flags & 0x8000) == 0 {
		return result // Query, not response
	}

	qdCount := int(binary.BigEndian.Uint16(dnsPacket[4:6]))
	anCount := int(binary.BigEndian.Uint16(dnsPacket[6:8]))
	if anCount == 0 {
		return result
	}

	pos := 12
	// Parse Questions to find query domain
	var queryDomain string
	for i := 0; i < qdCount && pos < len(dnsPacket); i++ {
		name, newPos := readDNSName(dnsPacket, pos)
		pos = newPos + 4 // skip QTYPE (2) + QCLASS (2)
		if queryDomain == "" {
			queryDomain = name
		}
	}

	if queryDomain == "" {
		return result
	}

	// Parse Answers
	for i := 0; i < anCount && pos < len(dnsPacket); i++ {
		name, newPos := readDNSName(dnsPacket, pos)
		pos = newPos
		if pos+10 > len(dnsPacket) {
			break
		}

		rtype := binary.BigEndian.Uint16(dnsPacket[pos : pos+2])
		// skip rclass (2) and ttl (4)
		rdLength := int(binary.BigEndian.Uint16(dnsPacket[pos+8 : pos+10]))
		pos += 10

		if pos+rdLength > len(dnsPacket) {
			break
		}

		rdata := dnsPacket[pos : pos+rdLength]
		domainToUse := name
		if domainToUse == "" {
			domainToUse = queryDomain
		}

		if rtype == 1 && rdLength == 4 { // Type A (IPv4)
			ip := net.IP(rdata).String()
			result[ip] = domainToUse
		} else if rtype == 28 && rdLength == 16 { // Type AAAA (IPv6)
			ip := net.IP(rdata).String()
			result[ip] = domainToUse
		} else if rtype == 5 { // CNAME
			// can update queryDomain if needed
		}

		pos += rdLength
	}

	return result
}

func readDNSName(packet []byte, startPos int) (string, int) {
	pos := startPos
	var labels []string
	jumped := false
	jumpPos := startPos

	for pos < len(packet) {
		lenByte := int(packet[pos])
		if lenByte == 0 {
			pos++
			if !jumped {
				jumpPos = pos
			}
			break
		}

		// Pointer (0xC0)
		if (lenByte & 0xC0) == 0xC0 {
			if pos+1 >= len(packet) {
				break
			}
			offset := int(binary.BigEndian.Uint16(packet[pos:pos+2])) & 0x3FFF
			if !jumped {
				jumpPos = pos + 2
				jumped = true
			}
			pos = offset
			continue
		}

		pos++
		if pos+lenByte > len(packet) {
			break
		}
		labels = append(labels, string(packet[pos:pos+lenByte]))
		pos += lenByte
	}

	if !jumped && jumpPos == startPos {
		jumpPos = pos
	}

	return strings.ToLower(strings.Join(labels, ".")), jumpPos
}
