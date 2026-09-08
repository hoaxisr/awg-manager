package signature

import (
	crand "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	mrand "math/rand"
)

// wire — big-endian байтовый буфер для сборки пакетов.
type wire struct{ b []byte }

func (w *wire) u8(v int)      { w.b = append(w.b, byte(v)) }
func (w *wire) u16(v int)     { w.b = binary.BigEndian.AppendUint16(w.b, uint16(v)) }
func (w *wire) u24(v int)     { w.b = append(w.b, byte(v>>16), byte(v>>8), byte(v)) }
func (w *wire) u32(v uint32)  { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *wire) raw(p []byte)  { w.b = append(w.b, p...) }
func (w *wire) str(s string)  { w.b = append(w.b, s...) }
func (w *wire) bytes() []byte { return w.b }

// varint — RFC 9000 §16 (1/2/4/8 байт).
func (w *wire) varint(v uint64) {
	switch {
	case v < 1<<6:
		w.u8(int(v))
	case v < 1<<14:
		w.u16(int(v) | 0x4000)
	case v < 1<<30:
		w.u32(uint32(v) | 0x80000000)
	default:
		w.b = binary.BigEndian.AppendUint64(w.b, v|0xC000000000000000)
	}
}

func varintLen(v uint64) int {
	switch {
	case v < 1<<6:
		return 1
	case v < 1<<14:
		return 2
	case v < 1<<30:
		return 4
	}
	return 8
}

// varintBytes — значение в кодировке варинта (RFC 9000 §16) отдельным срезом.
func varintBytes(v uint64) []byte {
	w := &wire{}
	w.varint(v)
	return w.bytes()
}

// tokB — один токен <b 0x…> со строчным hex (NDMS принимает 1200 байт одним токеном, замер 2026-09-08).
func tokB(p []byte) string { return "<b 0x" + hex.EncodeToString(p) + ">" }

// randBytes — n случайных байт из crypto/rand. Паника при ошибке: сбой
// crypto/rand невосстановим.
func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// randHex — n случайных байт из инжектированного *mrand.Rand в hex; для
// текстовых полей, которые обязаны быть seed-детерминированными (в отличие
// от randBytes/crypto/rand, зарезервированного для ключевого материала QUIC
// и DCID/SCID/PN).
func randHex(r *mrand.Rand, n int) string {
	b := make([]byte, n)
	_, _ = r.Read(b) // math/rand: always fills, never errors
	return hex.EncodeToString(b)
}
