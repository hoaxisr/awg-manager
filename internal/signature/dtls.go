// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	mrand "math/rand"
)

func init() { builders["dtls"] = buildDTLS }

// dtlsCipherSuites — §4.3, порядок дословный.
var dtlsCipherSuites = []uint16{0xC02B, 0xC02F, 0xCCA9, 0xC02C, 0x009C, 0x009D}

// buildDTLS — DTLS 1.2 ClientHello (WebRTC, §4). Тело собирается целиком, чтобы
// длины (record, handshake, fragment) считались по настоящему размеру, и лишь
// потом random и session_id подменяются рантайм-токенами: random = <t> (4 байта
// unix time) + <r 28>, session_id = <r 32>. Длины полей не меняются, поэтому
// подстановка не ломает разбор. Cookie пустой, эпоха и порядковые номера нулевые.
func buildDTLS(r *mrand.Rand) (GeneratedPackets, error) {
	body := &wire{}
	body.u16(0xFEFD) // client_version = DTLS 1.2
	body.raw(make([]byte, 32))
	body.u8(32) // session_id length
	body.raw(make([]byte, 32))
	body.u8(0) // cookie length = 0
	body.u16(2 * len(dtlsCipherSuites))
	for _, cs := range dtlsCipherSuites {
		body.u16(int(cs))
	}
	body.u8(1) // compression methods length
	body.u8(0) // null compression

	exts := dtlsExtensions(pickHost(r))
	body.u16(len(exts))
	body.raw(exts)

	b := body.bytes()

	head := &wire{}
	head.u8(0x16)             // content type = handshake
	head.u16(0xFEFD)          // version = DTLS 1.2
	head.u16(0)               // epoch
	head.raw(make([]byte, 6)) // sequence number (48 бит)
	head.u16(12 + len(b))     // record length = заголовок handshake + тело
	head.u8(0x01)             // handshake type = client_hello
	head.u24(len(b))          // handshake length
	head.u16(0)               // message_seq
	head.u24(0)               // fragment_offset
	head.u24(len(b))          // fragment_length = вся длина
	head.raw(b[:2])           // client_version

	// b[2:34] — random, b[34] — длина session_id, b[35:67] — session_id.
	i1 := tokB(head.bytes()) + "<t><r 28>" + tokB(b[34:35]) + "<r 32>" + tokB(b[67:])
	return GeneratedPackets{I1: i1}, nil
}

// dtlsExtensions — §4.4, порядок дословный. GREASE в DTLS нет.
func dtlsExtensions(host string) []byte {
	groups := &wire{}
	groups.u16(2 * len(supportedGroups))
	for _, g := range supportedGroups {
		groups.u16(int(g))
	}

	srtp := &wire{}
	srtp.u16(2)      // SRTP profile list length
	srtp.u16(0x0001) // SRTP_AES128_CM_HMAC_SHA1_80
	srtp.u8(0)       // MKI length

	w := &wire{}
	w.raw(tlsExt(0x0000, sniData(host)))
	w.raw(tlsExt(0x000A, groups.bytes()))
	w.raw(tlsExt(0x000B, []byte{0x01, 0x00})) // ec_point_formats: uncompressed
	w.raw(tlsExt(0x000D, signatureAlgorithmsData()))
	w.raw(tlsExt(0x000E, srtp.bytes()))
	w.raw(tlsExt(0x0017, nil)) // extended_master_secret
	return w.bytes()
}
