package main

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
)

var aeadCache sync.Map

func getAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("obfs: key must be %d bytes", wrapKeyLen)
	}
	keyStr := string(key)
	if val, ok := aeadCache.Load(keyStr); ok {
		return val.(cipher.AEAD), nil
	}
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}
	aeadCache.Store(keyStr, aead)
	return aead, nil
}

type ObfsConfig struct {
	SSRC        uint32
	PayloadType uint8
	PaddingMax  int
}

func NewObfsConfig(mode string) *ObfsConfig {
	var buf [4]byte
	rand.Read(buf[:])

	pt := uint8(111)
	pad := 24
	if mode == "video" {
		pt = 96
		pad = 60
	}

	return &ObfsConfig{
		SSRC:        binary.BigEndian.Uint32(buf[:]),
		PayloadType: pt,
		PaddingMax:  pad,
	}
}

type ObfsState struct {
	mu      sync.Mutex
	initSeq uint16
	initTs  uint32
	count   uint64
}

func NewObfsState() *ObfsState {
	var buf [6]byte
	rand.Read(buf[:])
	return &ObfsState{
		initSeq: binary.BigEndian.Uint16(buf[0:2]),
		initTs:  binary.BigEndian.Uint32(buf[2:6]),
		count:   0,
	}
}

func obfsBuildNonce(ssrc uint32, seq uint16, ts uint32) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint32(n[0:4], ssrc)
	binary.BigEndian.PutUint16(n[4:6], seq)

	binary.BigEndian.PutUint32(n[8:12], ts)
	return n
}

func obfsWrapPacket(key, payload []byte, cfg *ObfsConfig, state *ObfsState) ([]byte, error) {
	if len(key) != wrapKeyLen {
		return nil, fmt.Errorf("obfs: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	if len(payload) == 0 {
		return nil, errors.New("obfs: empty payload")
	}

	state.mu.Lock()
	c := state.count
	state.count++
	state.mu.Unlock()

	seq := state.initSeq + uint16(c)
	ts := state.initTs + uint32(c)*960 + uint32(c>>16)

	nonce := obfsBuildNonce(cfg.SSRC, seq, ts)

	padRand := 0
	if cfg.PaddingMax > 0 {
		var rndBuf [1]byte
		rand.Read(rndBuf[:])
		padRand = int(rndBuf[0]) % cfg.PaddingMax
	}
	padTotal := padRand + 1

	outLen := 12 + len(payload) + chacha20poly1305.Overhead + padTotal
	out := make([]byte, outLen)

	out[0] = 0x80 | 0x20
	out[1] = cfg.PayloadType & 0x7F
	binary.BigEndian.PutUint16(out[2:4], seq)
	binary.BigEndian.PutUint32(out[4:8], ts)
	binary.BigEndian.PutUint32(out[8:12], cfg.SSRC)

	aead, err := getAEAD(key)
	if err != nil {
		return nil, fmt.Errorf("obfs: cipher init: %w", err)
	}
	sealed := aead.Seal(out[12:12], nonce, payload, out[:12])

	padStart := 12 + len(sealed)
	if padRand > 0 {
		rand.Read(out[padStart : padStart+padRand])
	}

	out[outLen-1] = byte(padTotal)

	return out, nil
}

func obfsUnwrapPacket(key, wire, dst []byte) (int, error) {
	if len(key) != wrapKeyLen {
		return 0, fmt.Errorf("obfs: key must be %d bytes (got %d)", wrapKeyLen, len(key))
	}
	if len(wire) < 13 {
		return 0, errors.New("obfs: packet too short")
	}

	if (wire[0] >> 6) != 2 {
		return 0, errors.New("obfs: not RTP v2")
	}

	seq := binary.BigEndian.Uint16(wire[2:4])
	ts := binary.BigEndian.Uint32(wire[4:8])
	ssrc := binary.BigEndian.Uint32(wire[8:12])

	payloadEnd := len(wire)
	if wire[0]&0x20 != 0 {
		padLen := int(wire[len(wire)-1])
		if padLen == 0 || padLen > payloadEnd-12 {
			return 0, fmt.Errorf("obfs: invalid padding length %d", padLen)
		}
		payloadEnd -= padLen
	}

	ciphertextLen := payloadEnd - 12
	if ciphertextLen <= chacha20poly1305.Overhead {
		return 0, errors.New("obfs: no payload after stripping header/padding")
	}
	if ciphertextLen-chacha20poly1305.Overhead > len(dst) {
		return 0, errors.New("obfs: dst buffer too small")
	}

	nonce := obfsBuildNonce(ssrc, seq, ts)
	aead, err := getAEAD(key)
	if err != nil {
		return 0, fmt.Errorf("obfs: cipher init: %w", err)
	}
	plain, err := aead.Open(dst[:0], nonce, wire[12:payloadEnd], wire[:12])
	if err != nil {
		return 0, fmt.Errorf("obfs: auth: %w", err)
	}

	return len(plain), nil
}

func obfsIsRTPPacket(wire []byte) bool {
	if len(wire) < 13 {
		return false
	}

	if (wire[0] >> 6) != 2 {
		return false
	}

	pt := wire[1] & 0x7F
	return pt == 111 || pt == 96
}
