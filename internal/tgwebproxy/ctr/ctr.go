package ctr

import (
	"crypto/aes"
	"crypto/cipher"
)

// New returns an AES-CTR cipher stream, using OpenSSL EVP on supported MIPS targets when available.
func New(key, iv []byte) (cipher.Stream, error) {
	if len(key) == 32 && len(iv) == aes.BlockSize {
		if accelerated := newAccelerated(key, iv); accelerated != nil {
			return accelerated, nil
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewCTR(block, iv), nil
}

// Release frees storage owned by an accelerated stream after its last use.
func Release(stream cipher.Stream) {
	if accelerated, ok := stream.(interface{ release() }); ok {
		accelerated.release()
	}
}
