package ctr

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestNewParityAndChunking(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i*7 + 3)
	}
	iv := make([]byte, aes.BlockSize)
	for i := range iv {
		iv[i] = byte(i*11 + 5)
	}

	plain := make([]byte, 8192+17)
	for i := range plain {
		plain[i] = byte((i * 13) ^ (i >> 3))
	}

	refBlock, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	ref := cipher.NewCTR(refBlock, iv)
	want := make([]byte, len(plain))
	ref.XORKeyStream(want, plain)

	// Disjoint buffers fed in 31-byte chunks (not block-aligned) and 4 KiB chunks.
	for _, chunkSize := range []int{31, 4096} {
		stream, err := New(key, iv)
		if err != nil {
			t.Fatalf("New(%d): %v", chunkSize, err)
		}

		// Zero-byte input must be a safe no-op.
		stream.XORKeyStream(nil, nil)

		got := make([]byte, len(plain))
		for off := 0; off < len(plain); off += chunkSize {
			end := off + chunkSize
			if end > len(plain) {
				end = len(plain)
			}
			stream.XORKeyStream(got[off:end], plain[off:end])
		}
		Release(stream)

		if !bytes.Equal(got, want) {
			t.Fatalf("disjoint XORKeyStream mismatch for chunkSize=%d", chunkSize)
		}
	}

	// In-place (dst == src) encryption + double Release safety.
	stream, err := New(key, iv)
	if err != nil {
		t.Fatalf("New in-place: %v", err)
	}
	inPlace := append([]byte(nil), plain...)
	stream.XORKeyStream(inPlace, inPlace)
	Release(stream)
	Release(stream)
	Release(nil)

	if !bytes.Equal(inPlace, want) {
		t.Fatalf("in-place XORKeyStream mismatch")
	}
}

func TestNewKeySizesAndErrors(t *testing.T) {
	iv := make([]byte, aes.BlockSize)
	for _, keyLen := range []int{16, 24} {
		key := bytes.Repeat([]byte{0x5a}, keyLen)
		stream, err := New(key, iv)
		if err != nil {
			t.Fatalf("New(%d-byte key): %v", keyLen, err)
		}
		buf := []byte("hello mtproto ctr")
		stream.XORKeyStream(buf, buf)
		Release(stream)
	}

	if _, err := New([]byte{1, 2, 3}, iv); err == nil {
		t.Fatalf("expected error for invalid key length")
	}
}
