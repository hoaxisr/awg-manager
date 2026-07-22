// SPDX-License-Identifier: MIT

// Package rtpopus2 - wire-профиль обфускации: RTP-заголовок с extension
// (RFC 8285 one-byte) + ChaCha20-Poly1305 AEAD. Отличие от rtpopus (v1): X=1 с
// extension (ssrc-audio-level + transport-cc). v1 без extension отличим от
// современного WebRTC. Обфускация, не security (DTLS уже шифрует).
//
// Wire-формат (HeaderLen=36, Overhead=52):
//
//	[12B RTP hdr | 12B RTP one-byte ext | 12B explicit nonce | AEAD ciphertext | 16B tag]
//
// RTP header (RFC 3550):
//
//	byte 0:    0x90        V=2, P=0, X=1 (есть extension), CC=0
//	byte 1:    M<<7 | 0x6F M=1 на первом пакете conn (старт talkspurt), дальше 0; PT=111 (opus)
//	byte 2-3:  seq16 BE    монотонный, init random
//	byte 4-7:  ts32 BE     монотонный, шаг 960 (20ms @ 48kHz)
//	byte 8-11: SSRC        полностью random per conn
//
// RTP extension (RFC 8285 one-byte header, фиксировано 12 байт):
//
//	byte 12-13: 0xBE 0xDE  профиль one-byte
//	byte 14-15: 0x0002     длина данных расширения = 2 слова (8 байт)
//	byte 16:    0x10       ssrc-audio-level: id=1, len=1
//	byte 17:    0x80|level V=1 (голос активен), level в -dBov
//	byte 18:    0x21       transport-wide-cc: id=2, len=2
//	byte 19-20: tccSeq16   монотонный transport-cc sequence
//	byte 21-23: 0x00       padding до границы 8 байт данных расширения
//
// 12B explicit nonce = 4B sessionID || 8B counter (BE). MSB sessionID кодирует
// направление (разделяет nonce-пространства client/server под общим ключом).
// AAD = первые 36 байт (RTP hdr || ext || nonce).
//
// Wire-формат замораживается при первом деплое - нужна побитовая совместимость
// с задеплоенными пирами. Не путать с Go-термином wrap (errors.Wrap).
package rtpopus2

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	KeyLen     = 32
	rtpHdrLen  = 12
	rtpExtLen  = 12
	nonceLen   = 12
	tagLen     = 16
	headerLen  = rtpHdrLen + rtpExtLen + nonceLen // 36
	overhead   = headerLen + tagLen               // 52
	rtpVersion = 0x90                             // V=2, P=0, X=1, CC=0
	rtpPT      = 0x6F                             // M=0, PT=111 (opus)
	rtpMarker  = 0x80                             // M=1
	tsStep     = 960                              // 20ms @ 48kHz

	// audio-level и transport-cc id внутри one-byte extension.
	extAudioLevelHdr = 0x10 // id=1, len=1
	extTransportHdr  = 0x21 // id=2, len=2
)

// MaxWire - размер wire-буфера под payload длины payloadLen.
func MaxWire(payloadLen int) int { return overhead + payloadLen }

// State хранит AEAD-экземпляр из общего ключа; может разделяться многими Conn
// (серверный listener держит один State на всех пиров).
type State struct {
	aead cipher.AEAD
}

func NewState(key []byte) (*State, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("rtpopus2:key must be %d bytes (got %d)", KeyLen, len(key))
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("rtpopus2:aead init: %w", err)
	}
	return &State{aead: aead}, nil
}

// Conn несёт per-stream RTP-состояние (seq/ts/SSRC/tcc/counter) и ссылку на общий State.
type Conn struct {
	state     *State
	sessionID [4]byte // префикс nonce; MSB кодирует направление
	ssrc      [4]byte // SSRC для RTP header; полностью random
	counter   atomic.Uint64
	seq       atomic.Uint32 // RTP sequence (uint16)
	timestamp atomic.Uint32 // RTP timestamp
	tcc       atomic.Uint32 // transport-cc sequence (uint16)
	firstPkt  atomic.Bool   // marker bit на первом пакете
}

func NewConn(key []byte, isServer bool) (*Conn, error) {
	s, err := NewState(key)
	if err != nil {
		return nil, err
	}
	return NewConnFromState(s, isServer)
}

// NewConnFromState создаёт Conn со случайными per-stream полями, переиспользуя State.
func NewConnFromState(state *State, isServer bool) (*Conn, error) {
	if state == nil {
		return nil, errors.New("rtpopus2:nil state")
	}
	c := &Conn{state: state}

	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("rtpopus2:rand init: %w", err)
	}
	copy(c.sessionID[:], rnd[0:4])
	copy(c.ssrc[:], rnd[4:8]) // SSRC полностью random (без direction-бита, в отличие от v1)
	if isServer {
		c.sessionID[0] |= 0x80
	} else {
		c.sessionID[0] &^= 0x80
	}
	c.seq.Store(uint32(binary.BigEndian.Uint16(rnd[8:10])))
	c.timestamp.Store(binary.BigEndian.Uint32(rnd[10:14]))
	c.tcc.Store(uint32(binary.BigEndian.Uint16(rnd[14:16])))

	var cb [8]byte
	if _, err := rand.Read(cb[:]); err != nil {
		return nil, fmt.Errorf("rtpopus2:counter rand: %w", err)
	}
	c.counter.Store(binary.BigEndian.Uint64(cb[:]))
	return c, nil
}

// HeaderLen - offset, с которого начинается plaintext в wire-буфере (для in-place API).
func (*Conn) HeaderLen() int { return headerLen }

// Overhead - суммарные накладные wire-байты (header + tag).
func (*Conn) Overhead() int { return overhead }

// MaxWire - размер wire-буфера под payload длины n.
func (*Conn) MaxWire(n int) int { return overhead + n }

// WrapInto кодирует payload в dst (минимум MaxWire(len(payload))) и возвращает
// число записанных wire-байт.
func (c *Conn) WrapInto(dst, payload []byte) (int, error) {
	if len(dst) < overhead+len(payload) {
		return 0, errors.New("rtpopus2:dst buffer too small")
	}
	copy(dst[headerLen:], payload)
	return c.WrapInPlace(dst, len(payload))
}

// WrapInPlace кодирует plaintext, размещённый вызывающим в
// buf[HeaderLen:HeaderLen+plainLen], дописывая RTP-заголовок+расширение+nonce
// перед ним и AEAD-tag после - без копии payload. buf должен вмещать MaxWire(plainLen).
func (c *Conn) WrapInPlace(buf []byte, plainLen int) (int, error) {
	wireLen := overhead + plainLen
	if len(buf) < wireLen {
		return 0, errors.New("rtpopus2:dst buffer too small")
	}

	// RTP-заголовок (X=1).
	buf[0] = rtpVersion
	pt := byte(rtpPT)
	if c.firstPkt.CompareAndSwap(false, true) {
		pt |= rtpMarker // M=1 на первом пакете
	}
	buf[1] = pt
	seq := uint16(c.seq.Add(1) - 1) //nolint:gosec // RTP sequence mod 2^16
	binary.BigEndian.PutUint16(buf[2:4], seq)
	ts := c.timestamp.Add(tsStep) - tsStep
	binary.BigEndian.PutUint32(buf[4:8], ts)
	copy(buf[8:12], c.ssrc[:])

	// RTP one-byte extension: ssrc-audio-level + transport-cc.
	buf[12] = 0xBE
	buf[13] = 0xDE
	binary.BigEndian.PutUint16(buf[14:16], 2) // длина = 2 слова (8 байт)
	buf[16] = extAudioLevelHdr
	buf[17] = 0x80 | byte(seq&0x3F) //nolint:gosec // V=1, level варьируется
	buf[18] = extTransportHdr
	tcc := uint16(c.tcc.Add(1) - 1) //nolint:gosec // transport-cc seq mod 2^16
	binary.BigEndian.PutUint16(buf[19:21], tcc)
	buf[21], buf[22], buf[23] = 0, 0, 0 // padding до 8 байт данных расширения

	// Явный nonce.
	copy(buf[24:28], c.sessionID[:])
	ctr := c.counter.Add(1) - 1
	binary.BigEndian.PutUint64(buf[28:headerLen], ctr)

	nonce := buf[24:headerLen]
	aad := buf[:headerLen]
	c.state.aead.Seal(buf[headerLen:headerLen], nonce, buf[headerLen:headerLen+plainLen], aad)
	return wireLen, nil
}

// Unwrap декодирует wire-пакет в dst и возвращает длину plaintext.
func (c *Conn) Unwrap(wire, dst []byte) (int, error) {
	plain, err := c.UnwrapInPlace(wire)
	if err != nil {
		return 0, err
	}
	if len(plain) > len(dst) {
		return 0, errors.New("rtpopus2:dst buffer too small")
	}
	copy(dst[:len(plain)], plain)
	return len(plain), nil
}

// UnwrapInPlace декодирует wire-пакет на месте, возвращая subslice plaintext
// внутри wire (без копии). wire после вызова считается потреблённым.
func (c *Conn) UnwrapInPlace(wire []byte) ([]byte, error) {
	if len(wire) < overhead {
		return nil, errors.New("rtpopus2:packet too short")
	}
	nonce := wire[rtpHdrLen+rtpExtLen : headerLen]
	aad := wire[:headerLen]
	ct := wire[headerLen:]

	plain, err := c.state.aead.Open(ct[:0], nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("rtpopus2:AEAD open: %w", err)
	}
	return plain, nil
}
