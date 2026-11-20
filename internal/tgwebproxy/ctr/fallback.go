//go:build !linux || (!mipsle && !mips) || !cgo

package ctr

import "crypto/cipher"

func newAccelerated(key, iv []byte) cipher.Stream { return nil }
