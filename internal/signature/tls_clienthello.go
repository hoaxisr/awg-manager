// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	"crypto/ecdh"
	crand "crypto/rand"
	mrand "math/rand"
)

// Стандартный QUIC-отпечаток payloadGen (порт-спека §1.7.1–§1.7.4).
var greasePool = []uint16{0x0A0A, 0x1A1A, 0x2A2A, 0x3A3A, 0x4A4A, 0x5A5A, 0x6A6A, 0x7A7A,
	0x8A8A, 0x9A9A, 0xAAAA, 0xBABA, 0xCACA, 0xDADA, 0xEAEA, 0xFAFA}
var quicCipherSuites = []uint16{0x1301, 0x1302, 0x1303}
var tlsSupportedGroups = []uint16{0x001D, 0x0017, 0x0018}
var tlsSignatureAlgorithms = []uint16{0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601, 0x0807}

const tlsPaddingTarget = 512

// quicActiveConnectionIDLimit — значение active_connection_id_limit (§1.7.4).
const quicActiveConnectionIDLimit = 8

// buildClientHello собирает TLS 1.3 ClientHello для QUIC: 14 расширений в
// порядке §1.7.2 (GREASE первым, вторичный GREASE предпоследним, padding
// последним). Отступление от payloadGen: key_share x25519 — настоящий
// публичный ключ crypto/ecdh, а не 32 случайных байта.
func buildClientHello(r *mrand.Rand, host string, scid []byte) ([]byte, error) {
	i1 := r.Intn(len(greasePool))
	// Вторичный GREASE обязан отличаться от первого (§1.7).
	i2 := (i1 + 1 + r.Intn(len(greasePool)-1)) % len(greasePool)
	grease1, grease2 := greasePool[i1], greasePool[i2]

	keyShare, err := keyShareData(grease1)
	if err != nil {
		return nil, err
	}
	exts := [][]byte{
		tlsExt(grease1, nil),
		tlsExt(0x0000, sniData(host)),
		tlsExt(0x000A, supportedGroupsData(grease1)),
		tlsExt(0x0010, []byte{0x00, 0x03, 0x02, 'h', '3'}),
		tlsExt(0x0005, []byte{0x01, 0x00, 0x00, 0x00, 0x00}),
		tlsExt(0x000D, signatureAlgorithmsData()),
		tlsExt(0x0012, nil),
		tlsExt(0x002B, supportedVersionsData(grease1)),
		tlsExt(0x0033, keyShare),
		tlsExt(0x002D, []byte{0x01, 0x01}),
		tlsExt(0x0039, quicTransportParams(scid)),
		tlsExt(0x001B, []byte{0x02, 0x00, 0x02}),
		tlsExt(grease2, nil),
	}
	exts = append(exts, tlsExt(0x0015, make([]byte, tlsPaddingLen(exts))))

	body := &wire{}
	body.u16(0x0303)
	body.raw(randBytes(32))
	body.u8(32)
	body.raw(randBytes(32))
	body.u16(2 * (1 + len(quicCipherSuites)))
	body.u16(int(grease1))
	for _, cs := range quicCipherSuites {
		body.u16(int(cs))
	}
	body.u8(1)
	body.u8(0)
	extLen := 0
	for _, e := range exts {
		extLen += len(e)
	}
	body.u16(extLen)
	for _, e := range exts {
		body.raw(e)
	}

	ch := &wire{}
	ch.u8(0x01)
	ch.u24(len(body.bytes()))
	ch.raw(body.bytes())
	return ch.bytes(), nil
}

// tlsExt — u16(type) u16(len) data.
func tlsExt(typ uint16, data []byte) []byte {
	w := &wire{}
	w.u16(int(typ))
	w.u16(len(data))
	w.raw(data)
	return w.bytes()
}

func sniData(host string) []byte {
	w := &wire{}
	w.u16(3 + len(host))
	w.u8(0x00)
	w.u16(len(host))
	w.str(host)
	return w.bytes()
}

func supportedGroupsData(grease uint16) []byte {
	w := &wire{}
	w.u16(2 * (1 + len(tlsSupportedGroups)))
	w.u16(int(grease))
	for _, g := range tlsSupportedGroups {
		w.u16(int(g))
	}
	return w.bytes()
}

func signatureAlgorithmsData() []byte {
	w := &wire{}
	w.u16(2 * len(tlsSignatureAlgorithms))
	for _, a := range tlsSignatureAlgorithms {
		w.u16(int(a))
	}
	return w.bytes()
}

func supportedVersionsData(grease uint16) []byte {
	w := &wire{}
	w.u8(4)
	w.u16(int(grease))
	w.u16(0x0304)
	return w.bytes()
}

func keyShareData(grease uint16) ([]byte, error) {
	priv, err := ecdh.X25519().GenerateKey(crand.Reader)
	if err != nil {
		return nil, err
	}
	entries := &wire{}
	entries.u16(int(grease))
	entries.u16(1)
	entries.u8(0x00)
	entries.u16(0x001D)
	pub := priv.PublicKey().Bytes()
	entries.u16(len(pub))
	entries.raw(pub)

	w := &wire{}
	w.u16(len(entries.bytes()))
	w.raw(entries.bytes())
	return w.bytes(), nil
}

// quicTransportParams — расширение 0x0039, порядок и значения §1.7.4.
func quicTransportParams(scid []byte) []byte {
	params := []struct {
		id  uint64
		val []byte
	}{
		{0x01, varintBytes(30000)},                       // max_idle_timeout
		{0x03, varintBytes(1472)},                        // max_udp_payload_size
		{0x04, varintBytes(15728640)},                    // initial_max_data
		{0x05, varintBytes(6291456)},                     // initial_max_stream_data_bidi_local
		{0x06, varintBytes(6291456)},                     // initial_max_stream_data_bidi_remote
		{0x07, varintBytes(6291456)},                     // initial_max_stream_data_uni
		{0x08, varintBytes(100)},                         // initial_max_streams_bidi
		{0x09, varintBytes(100)},                         // initial_max_streams_uni
		{0x0a, varintBytes(3)},                           // ack_delay_exponent
		{0x0b, varintBytes(25)},                          // max_ack_delay
		{0x0c, nil},                                      // disable_active_migration — значение пустое
		{0x0e, varintBytes(quicActiveConnectionIDLimit)}, // active_connection_id_limit
		{0x0f, scid},                                     // initial_source_connection_id
	}
	w := &wire{}
	for _, p := range params {
		w.varint(p.id)
		w.varint(uint64(len(p.val)))
		w.raw(p.val)
	}
	return w.bytes()
}

// tlsPaddingLen — формула payloadGen (§1.7.3): 109 — фиксированная оценка
// префикса ClientHello, 4 — заголовок самого расширения padding.
func tlsPaddingLen(parts [][]byte) int {
	current := 0
	for _, p := range parts {
		current += len(p)
	}
	n := tlsPaddingTarget - 109 - current - 4
	if n < 0 {
		return 0
	}
	return n
}
