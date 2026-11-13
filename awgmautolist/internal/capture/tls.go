package capture

import (
	"encoding/binary"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// tlsServerName extracts SNI from a complete ClientHello contained in one TLS
// record. Fragmented handshakes return empty and retain an IP-only target.
func tlsServerName(payload []byte) string {
	if len(payload) < 9 || payload[0] != 0x16 {
		return ""
	}
	recordLen := int(binary.BigEndian.Uint16(payload[3:5]))
	if recordLen < 4 || len(payload) < 5+recordLen {
		return ""
	}
	handshake := payload[5 : 5+recordLen]
	handshakeLen := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	if handshake[0] != 1 || handshakeLen+4 != len(handshake) || len(handshake) < 38 {
		return ""
	}
	b := handshake[4:]
	if len(b) < 35 {
		return ""
	}
	b = b[34:] // legacy version and random
	sessionLen := int(b[0])
	b = b[1:]
	if len(b) < sessionLen+2 {
		return ""
	}
	b = b[sessionLen:]
	cipherLen := int(binary.BigEndian.Uint16(b[:2]))
	b = b[2:]
	if len(b) < cipherLen+1 {
		return ""
	}
	b = b[cipherLen:]
	compressionLen := int(b[0])
	b = b[1:]
	if len(b) < compressionLen+2 {
		return ""
	}
	b = b[compressionLen:]
	extensionsLen := int(binary.BigEndian.Uint16(b[:2]))
	b = b[2:]
	if extensionsLen > len(b) {
		return ""
	}
	b = b[:extensionsLen]
	for len(b) >= 4 {
		typ := binary.BigEndian.Uint16(b[:2])
		size := int(binary.BigEndian.Uint16(b[2:4]))
		b = b[4:]
		if len(b) < size {
			return ""
		}
		extension := b[:size]
		b = b[size:]
		if typ != 0 || len(extension) < 2 {
			continue
		}
		listLen := int(binary.BigEndian.Uint16(extension[:2]))
		extension = extension[2:]
		if listLen > len(extension) {
			return ""
		}
		extension = extension[:listLen]
		for len(extension) >= 3 {
			nameType := extension[0]
			nameLen := int(binary.BigEndian.Uint16(extension[1:3]))
			extension = extension[3:]
			if len(extension) < nameLen {
				return ""
			}
			if nameType == 0 {
				name, err := model.NormalizeDomain(string(extension[:nameLen]))
				if err != nil {
					return ""
				}
				return name
			}
			extension = extension[nameLen:]
		}
	}
	return ""
}
