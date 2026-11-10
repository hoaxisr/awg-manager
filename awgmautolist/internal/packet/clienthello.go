package packet

import "strings"

// ClientHelloSNI extracts the first host_name from a TLS ClientHello. The
// input is one or more bytes of a TLS handshake record, normally a TCP payload
// from ParseTCP. Outer record, handshake and extension lengths are clamped to
// the capture, while a name itself is returned only when every byte arrived.
// The result is ASCII lower case and one trailing root dot is removed.
func ClientHelloSNI(raw []byte) (string, bool) {
	record := cursor{data: raw}
	if record.u8() != 0x16 { // TLS handshake record
		return "", false
	}
	record.skip(2) // legacy record version
	handshake := record.enterArrived(int(record.u16()))
	if handshake.u8() != 0x01 { // ClientHello, not ServerHello
		return "", false
	}
	hello := handshake.enterArrived(int(handshake.u24()))

	hello.skip(2)  // client_version
	hello.skip(32) // random
	hello.skip(int(hello.u8()))
	hello.skip(int(hello.u16()))
	hello.skip(int(hello.u8()))
	if hello.bad || hello.remaining() == 0 {
		return "", false
	}

	extensions := hello.enterArrived(int(hello.u16()))
	for !extensions.bad && extensions.remaining() > 0 {
		typ := extensions.u16()
		extension := extensions.enter(int(extensions.u16()))
		if extensions.bad {
			return "", false
		}
		if typ != 0x0000 { // server_name
			continue
		}

		list := extension.enter(int(extension.u16()))
		for !list.bad && list.remaining() > 0 {
			nameType := list.u8()
			nameLen := int(list.u16())
			if list.bad {
				return "", false
			}
			name, ok := list.take(nameLen)
			if !ok {
				return "", false
			}
			if nameType != 0x00 { // not host_name
				continue
			}
			return normalizeHostName(name)
		}
		// RFC 6066 permits only one host_name. Do not use a later SNI
		// extension when the first one is empty or malformed.
		return "", false
	}
	return "", false
}

// TLSClientHelloSNI is an explicit alias for ClientHelloSNI.
func TLSClientHelloSNI(raw []byte) (string, bool) { return ClientHelloSNI(raw) }

type cursor struct {
	data []byte
	pos  int
	bad  bool
}

func (c *cursor) remaining() int {
	if c.bad || c.pos > len(c.data) {
		return 0
	}
	return len(c.data) - c.pos
}

func (c *cursor) take(n int) ([]byte, bool) {
	if c.bad || n < 0 || n > c.remaining() {
		c.bad = true
		return nil, false
	}
	start := c.pos
	c.pos += n
	return c.data[start:c.pos], true
}

func (c *cursor) skip(n int) { _, _ = c.take(n) }

func (c *cursor) u8() uint8 {
	b, ok := c.take(1)
	if !ok {
		return 0
	}
	return b[0]
}

func (c *cursor) u16() uint16 {
	b, ok := c.take(2)
	if !ok {
		return 0
	}
	return uint16(b[0])<<8 | uint16(b[1])
}

func (c *cursor) u24() uint32 {
	b, ok := c.take(3)
	if !ok {
		return 0
	}
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

// enter narrows to exactly n bytes and advances the outer cursor past them.
func (c *cursor) enter(n int) cursor {
	b, ok := c.take(n)
	if !ok {
		return cursor{bad: true}
	}
	return cursor{data: b}
}

// enterArrived is enter for outer TLS lengths: a short capture is ordinary,
// so only bytes that arrived are exposed to the nested cursor.
func (c *cursor) enterArrived(n int) cursor {
	if n > c.remaining() {
		n = c.remaining()
	}
	return c.enter(n)
}

func normalizeHostName(name []byte) (string, bool) {
	if len(name) > 1 && name[len(name)-1] == '.' {
		name = name[:len(name)-1]
	}
	if len(name) == 0 || len(name) > 253 || name[0] == '.' || name[len(name)-1] == '.' {
		return "", false
	}

	var out strings.Builder
	out.Grow(len(name))
	labelLen := 0
	for _, ch := range name {
		if ch == '.' {
			if labelLen == 0 {
				return "", false
			}
			labelLen = 0
			out.WriteByte('.')
			continue
		}
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			return "", false
		}
		labelLen++
		if labelLen > 63 {
			return "", false
		}
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		out.WriteByte(ch)
	}
	return out.String(), true
}
