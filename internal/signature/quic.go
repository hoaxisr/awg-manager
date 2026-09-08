// Adapted from payloadGen (MIT) — https://github.com/Sketchystan1/payloadGen
package signature

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
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
	ch, err := buildClientHello(r, pickHost(r), scid)
	if err != nil {
		return GeneratedPackets{}, err
	}
	frame := &wire{}
	frame.u8(0x06) // CRYPTO
	frame.varint(0)
	frame.varint(uint64(len(ch)))
	frame.raw(ch)
	payload := padInitial(frame.bytes(), len(dcid), len(scid), len(pn))
	pkt, err := protectInitial(dcid, scid, pn, payload)
	if err != nil {
		return GeneratedPackets{}, err
	}
	if len(pkt) != quicInitialSize {
		return GeneratedPackets{}, fmt.Errorf("quic initial: %d bytes, want %d", len(pkt), quicInitialSize)
	}
	return GeneratedPackets{I1: tokB(pkt)}, nil
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
// Требование к pn (packet number): 1 ≤ len(pn) ≤ 4 — иначе `0xC0 | byte((len(pn)-1)&0x03)`
// в первом байте заголовка молча съедает лишние биты длины (единственный вызов ниже передаёт 4).
func protectInitial(dcid, scid, pn, payload []byte) ([]byte, error) {
	key, iv, hpKey, err := deriveInitialKeys(dcid)
	if err != nil {
		return nil, err
	}

	h := &wire{}
	h.u8(int(0xC0 | byte((len(pn)-1)&0x03)))
	h.u32(0x00000001) // QUIC v1
	h.u8(len(dcid))
	h.raw(dcid)
	h.u8(len(scid))
	h.raw(scid)
	h.varint(0) // token length
	h.varint(uint64(len(pn) + len(payload) + quicAEADTagLen))
	h.raw(pn)
	header := h.bytes()

	packetBlock, err := aesBlock(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(packetBlock)
	if err != nil {
		return nil, err
	}
	nonce := append([]byte(nil), iv...)
	for i, b := range pn {
		nonce[len(nonce)-len(pn)+i] ^= b
	}
	ct := aead.Seal(nil, nonce, payload, header)

	hpBlock, err := aesBlock(hpKey)
	if err != nil {
		return nil, err
	}
	sampleOff := 4 - len(pn)
	mask := make([]byte, aes.BlockSize)
	hpBlock.Encrypt(mask, ct[sampleOff:sampleOff+aes.BlockSize])
	header[0] ^= mask[0] & 0x0F // длинный заголовок — только младшие 4 бита
	pnOff := len(header) - len(pn)
	for i := range pn {
		header[pnOff+i] ^= mask[1+i]
	}
	return append(header, ct...), nil
}

// aesBlock — AES на ключе фиксированной длины. Ошибка означала бы неверную
// длину ключа, чего вывод по RFC 9001 не допускает, но AddPeer fails closed —
// прокидываем, а не паникуем.
func aesBlock(key []byte) (cipher.Block, error) {
	return aes.NewCipher(key)
}
