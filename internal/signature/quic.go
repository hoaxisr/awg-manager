// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	"crypto/aes"
	"crypto/cipher"
	mrand "math/rand"
)

const quicInitialSize = 1200 // RFC 9000 §14.1 минимум для Initial
const quicAEADTagLen = 16

func init() { builders["quic_initial"] = buildQUICProfile }

// buildQUICProfile — статический валидный Initial: любой DPI выводит ключи
// Initial из DCID и расшифровывает пакет, поэтому внутри должен лежать
// настоящий ClientHello, а не <r>-мусор. Цена — байты одинаковы в каждом
// рукопожатии.
func buildQUICProfile(r *mrand.Rand) (GeneratedPackets, error) {
	dcid, scid, pn := randBytes(8), randBytes(8), randBytes(4)
	ch := buildClientHello(r, pickHost(r), scid)
	frame := &wire{}
	frame.u8(0x06) // CRYPTO
	frame.varint(0)
	frame.varint(uint64(len(ch)))
	frame.raw(ch)
	payload := padInitial(frame.bytes(), len(dcid), len(scid), len(pn))
	return GeneratedPackets{I1: tokB(protectInitial(dcid, scid, pn, payload))}, nil
}

// padInitial добивает payload нулями (PADDING-фреймы) до пакета в
// quicInitialSize байт. Размер поля длины — varint, поэтому итерируем (§1.5).
func padInitial(payload []byte, dcidLen, scidLen, pnLen int) []byte {
	headerPrefix := 1 + 4 + 1 + dcidLen + 1 + scidLen + 1 // +1 = varint(0) длины токена
	protected := pnLen + len(payload) + quicAEADTagLen
	pad := 0
	for i := 0; i < 4; i++ {
		next := quicInitialSize - (headerPrefix + varintLen(uint64(protected+pad)) + protected)
		if next < 0 {
			next = 0
		}
		if next == pad {
			break
		}
		pad = next
	}
	return append(payload, make([]byte, pad)...)
}

// protectInitial шифрует payload (AEAD, AAD = незащищённый заголовок) и
// накладывает header protection — RFC 9001 §5.3, §5.4.
func protectInitial(dcid, scid, pn, payload []byte) []byte {
	key, iv, hpKey, err := deriveInitialKeys(dcid)
	if err != nil {
		panic(err) // вывод ключей из DCID — чистая арифметика, ошибка невозможна
	}

	h := &wire{}
	h.u8(0xC0 | (len(pn) - 1))
	h.u32(0x00000001) // QUIC v1
	h.u8(len(dcid))
	h.raw(dcid)
	h.u8(len(scid))
	h.raw(scid)
	h.varint(0) // token length
	h.varint(uint64(len(pn) + len(payload) + quicAEADTagLen))
	h.raw(pn)
	header := h.bytes()

	aead, err := cipher.NewGCM(aesBlock(key))
	if err != nil {
		panic(err) // AES-128-GCM всегда доступен
	}
	nonce := append([]byte(nil), iv...)
	for i, b := range pn {
		nonce[len(nonce)-len(pn)+i] ^= b
	}
	ct := aead.Seal(nil, nonce, payload, header)

	sampleOff := 4 - len(pn)
	mask := make([]byte, 16)
	aesBlock(hpKey).Encrypt(mask, ct[sampleOff:sampleOff+16])
	header[0] ^= mask[0] & 0x0F // длинный заголовок — только младшие 4 бита
	pnOff := len(header) - len(pn)
	for i := range pn {
		header[pnOff+i] ^= mask[1+i]
	}
	return append(header, ct...)
}

// aesBlock — AES на ключе фиксированной длины: ошибка означала бы неверную
// длину ключа, чего вывод по RFC 9001 не допускает.
func aesBlock(key []byte) cipher.Block {
	b, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	return b
}
