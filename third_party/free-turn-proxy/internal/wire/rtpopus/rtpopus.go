// SPDX-License-Identifier: MIT

// Package rtpopus реализует AEAD-фрейминг с мимикрией под RTP/opus (один
// из планируемых wire-профилей обфускации в internal/wire/). Цель - обход
// VK TURN content-filter.
//
// Назначение: обфускация, а не безопасность. DTLS уже обеспечивает
// конфиденциальность и целостность внутреннего канала. Этот слой существует,
// чтобы трафик выглядел как SRTP - VK content-filter его не дропает;
// сам по себе не является защитой от активного противника.
//
// Wire-формат:
//
//	[12B RTP header | 12B explicit nonce | AEAD ciphertext | 16B tag]
//
// RTP header (RFC 3550):
//
//	byte 0: 0x80         V=2, P=0, X=0, CC=0
//	byte 1: 0x6F         M=0, PT=111 (opus, типичный voice PT)
//	byte 2-3: seq16 BE   монотонный, init random
//	byte 4-7: ts32 BE    монотонный, init random, шаг 960 (20ms @ 48kHz)
//	byte 8-11: SSRC      random per conn, MSB кодирует направление
//
// 12B explicit nonce = 4B sessionID || 8B counter (BE). MSB sessionID
// совпадает с MSB SSRC (direction bit). counter стартует с random uint64.
// AAD = первые 24 байта (RTP header || nonce).
//
// Wire-формат заморожен - требуется побитовая совместимость с задеплоенными пирами.
package rtpopus

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	KeyLen    = 32
	rtpHdrLen = 12
	nonceLen  = 12
	tagLen    = 16
	headerLen = rtpHdrLen + nonceLen // 24
	// HeaderLen - offset, с которого начинается plaintext в wire-буфере.
	// Экспонирован для in-place API (WrapInPlace/UnwrapInPlace): вызывающий
	// читает payload сразу в buf[HeaderLen:], избегая копии.
	HeaderLen  = headerLen
	Overhead   = headerLen + tagLen // 40
	rtpVersion = 0x80               // V=2, P=0, X=0, CC=0
	rtpPT      = 0x6F               // M=0, PT=111 (opus)
	tsStep     = 960                // 20ms @ 48kHz
)

func MaxWire(payloadLen int) int { return Overhead + payloadLen }

// State хранит AEAD-экземпляр, выведенный из общего ключа.
// Один State может разделяться между многими Conn (напр. server-side listener).
type State struct {
	aead cipher.AEAD
}

func NewState(key []byte) (*State, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("rtpopus:key must be %d bytes (got %d)", KeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("rtpopus:aead init: %w", err)
	}
	return &State{aead: aead}, nil
}

// Conn несёт per-stream RTP-состояние (seq/timestamp/SSRC/counter) и
// ссылку на общий AEAD State.
type Conn struct {
	state     *State
	sessionID [4]byte // 4B префикс nonce; MSB кодирует направление
	ssrc      [4]byte // SSRC для RTP header; MSB кодирует направление
	counter   atomic.Uint64
	seq       atomic.Uint32 // RTP sequence (used as uint16)
	timestamp atomic.Uint32 // RTP timestamp
}

func NewConn(key []byte, isServer bool) (*Conn, error) {
	s, err := NewState(key)
	if err != nil {
		return nil, err
	}
	return NewConnFromState(s, isServer)
}

// NewConnFromState создаёт Conn со случайными per-stream RTP-полями,
// переиспользуя переданный State.
func NewConnFromState(state *State, isServer bool) (*Conn, error) {
	if state == nil {
		return nil, errors.New("rtpopus:nil state")
	}
	c := &Conn{state: state}

	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("rtpopus:rand init: %w", err)
	}
	copy(c.sessionID[:], rnd[0:4])
	copy(c.ssrc[:], rnd[4:8])
	if isServer {
		c.sessionID[0] |= 0x80
		c.ssrc[0] |= 0x80
	} else {
		c.sessionID[0] &^= 0x80
		c.ssrc[0] &^= 0x80
	}
	c.seq.Store(uint32(binary.BigEndian.Uint16(rnd[8:10])))
	c.timestamp.Store(binary.BigEndian.Uint32(rnd[10:14]))

	var cb [8]byte
	if _, err := rand.Read(cb[:]); err != nil {
		return nil, fmt.Errorf("rtpopus:counter rand: %w", err)
	}
	c.counter.Store(binary.BigEndian.Uint64(cb[:]))
	return c, nil
}

// HeaderLen, Overhead, MaxWire - методы под интерфейс wire.Codec; значения
// совпадают с пакетными HeaderLen/Overhead/MaxWire.
func (*Conn) HeaderLen() int    { return headerLen }
func (*Conn) Overhead() int     { return Overhead }
func (*Conn) MaxWire(n int) int { return Overhead + n }

// WrapInto кодирует payload в dst (минимум MaxWire(len(payload)) байт)
// и возвращает число записанных wire-байт.
func (c *Conn) WrapInto(dst, payload []byte) (int, error) {
	if len(dst) < Overhead+len(payload) {
		return 0, errors.New("rtpopus:dst buffer too small")
	}
	copy(dst[headerLen:], payload)
	return c.WrapInPlace(dst, len(payload))
}

// WrapInPlace кодирует plaintext, который вызывающий уже разместил в
// buf[HeaderLen:HeaderLen+plainLen], дописывая RTP-заголовок+nonce перед ним
// и AEAD-tag после - без копии payload. buf должен вмещать MaxWire(plainLen).
// Возвращает число записанных wire-байт.
func (c *Conn) WrapInPlace(buf []byte, plainLen int) (int, error) {
	wireLen := Overhead + plainLen
	if len(buf) < wireLen {
		return 0, errors.New("rtpopus:dst buffer too small")
	}

	// RTP-заголовок.
	buf[0] = rtpVersion
	buf[1] = rtpPT
	seq := uint16(c.seq.Add(1) - 1) //nolint:gosec // RTP sequence number is intentionally mod 2^16
	binary.BigEndian.PutUint16(buf[2:4], seq)
	ts := c.timestamp.Add(tsStep) - tsStep
	binary.BigEndian.PutUint32(buf[4:8], ts)
	copy(buf[8:12], c.ssrc[:])

	// Явный nonce.
	noncePos := rtpHdrLen
	copy(buf[noncePos:noncePos+4], c.sessionID[:])
	ctr := c.counter.Add(1) - 1
	binary.BigEndian.PutUint64(buf[noncePos+4:noncePos+nonceLen], ctr)

	nonce := buf[noncePos : noncePos+nonceLen]
	aad := buf[:headerLen]
	ctPos := headerLen
	c.state.aead.Seal(buf[ctPos:ctPos], nonce, buf[ctPos:ctPos+plainLen], aad)

	return wireLen, nil
}

// Unwrap декодирует wire-пакет в dst и возвращает длину plaintext.
func (c *Conn) Unwrap(wire, dst []byte) (int, error) {
	plain, err := c.UnwrapInPlace(wire)
	if err != nil {
		return 0, err
	}
	if len(plain) > len(dst) {
		return 0, errors.New("rtpopus:dst buffer too small")
	}
	copy(dst[:len(plain)], plain)
	return len(plain), nil
}

// UnwrapInPlace декодирует wire-пакет на месте и возвращает subslice plaintext
// внутри wire (без копии в отдельный буфер). AEAD открывает in-place - wire
// после вызова считается потреблённым, результат валиден до следующей записи в wire.
func (c *Conn) UnwrapInPlace(wire []byte) ([]byte, error) {
	if len(wire) < Overhead {
		return nil, errors.New("rtpopus:packet too short")
	}
	nonce := wire[rtpHdrLen : rtpHdrLen+nonceLen]
	aad := wire[:headerLen]
	ct := wire[headerLen:]

	plain, err := c.state.aead.Open(ct[:0], nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("rtpopus:AEAD open: %w", err)
	}
	return plain, nil
}

func GenKeyHex() (string, error) {
	key := make([]byte, KeyLen)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("rtpopus:key gen: %w", err)
	}
	return hex.EncodeToString(key), nil
}

// DecodeKey декодирует hex-ключ и проверяет длину если enabled.
// Если enabled=false, возвращает (nil, nil).
func DecodeKey(enabled bool, raw string) ([]byte, error) {
	if !enabled {
		return nil, nil
	}
	if raw == "" {
		return nil, errors.New("-obf-profile != none requires -obf-key")
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("-obf-key invalid hex: %w", err)
	}
	if len(key) != KeyLen {
		return nil, fmt.Errorf("-obf-key must decode to %d bytes (got %d)", KeyLen, len(key))
	}
	return key, nil
}
