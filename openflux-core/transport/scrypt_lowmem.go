// The Salsa20/8 core, BlockMix and Integerify below are from
// golang.org/x/crypto/scrypt (v0.56.0):
//
// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in golang.org/x/crypto's LICENSE file.

package transport

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/bits"

	"golang.org/x/crypto/pbkdf2"
)

// scryptStride is how many of ROMix's N blocks share one stored block in
// lowMemScrypt: 8 keeps 4 MiB of the 32 MiB that scrypt.Key(…, 32768, 8, 1, …)
// allocates. The iOS packet tunnel lives under a 50 MB limit, and the 32 MiB at
// once of every key derivation (a context rotation while the exit is silent)
// got it killed by jetsam ("per-process-limit" at exactly 50.0 MB).
const scryptStride = 8

// lowMemScrypt returns exactly what scrypt.Key(password, salt, N, r, p, keyLen)
// returns, keeping one in stride of ROMix's table: a block that was not kept
// is recomputed from the kept one before it (scrypt's time-memory trade-off).
// Memory is N/stride blocks instead of N; time is about (3+stride/2)/2 times
// scrypt.Key's, a few tenths of a second once per secret and context.
func lowMemScrypt(password, salt []byte, N, r, p, keyLen, stride int) ([]byte, error) {
	if N <= 1 || N&(N-1) != 0 {
		return nil, errors.New("scrypt: N must be > 1 and a power of 2")
	}
	if stride < 1 || N%stride != 0 {
		return nil, errors.New("scrypt: stride must divide N")
	}
	if uint64(r)*uint64(p) >= 1<<30 || r > maxScryptInt/128/p || r > maxScryptInt/256 || N > maxScryptInt/128/r {
		return nil, errors.New("scrypt: parameters are too large")
	}
	R := 32 * r
	xy := make([]uint32, 4*R) // x, y and two blocks to recompute a dropped one in
	v := make([]uint32, (N/stride)*R)
	b := pbkdf2.Key(password, salt, 1, p*128*r, sha256.New)
	for i := 0; i < p; i++ {
		smixLowMem(b[i*128*r:], r, N, stride, v, xy)
	}
	return pbkdf2.Key(password, b, 1, keyLen, sha256.New), nil
}

const maxScryptInt = int(^uint(0) >> 1)

// smixLowMem is x/crypto's smix with v holding only blocks 0, stride, 2*stride…
func smixLowMem(b []byte, r, N, stride int, v, xy []uint32) {
	var tmp [16]uint32
	R := 32 * r
	x, y := xy[:R], xy[R:2*R]
	t, u := xy[2*R:3*R], xy[3*R:4*R]
	for i, j := 0, 0; i < R; i, j = i+1, j+4 {
		x[i] = binary.LittleEndian.Uint32(b[j:])
	}
	for i := 0; i < N; i++ {
		if i%stride == 0 {
			copy(v[(i/stride)*R:], x)
		}
		scryptBlockMix(&tmp, x, y, r)
		x, y = y, x
	}
	for i := 0; i < N; i++ {
		j := int(scryptInteger(x, r) & uint64(N-1))
		// V[j]: the kept block at or before j, mixed forward to j.
		copy(t, v[(j/stride)*R:(j/stride+1)*R])
		tt, uu := t, u
		for k := 0; k < j%stride; k++ {
			scryptBlockMix(&tmp, tt, uu, r)
			tt, uu = uu, tt
		}
		for k, w := range tt {
			x[k] ^= w
		}
		scryptBlockMix(&tmp, x, y, r)
		x, y = y, x
	}
	for i, j := 0, 0; i < R; i, j = i+1, j+4 {
		binary.LittleEndian.PutUint32(b[j:], x[i])
	}
}

// salsaXOR applies Salsa20/8 to the XOR of 16 numbers from tmp and in,
// and puts the result into both tmp and out.
func scryptSalsaXOR(tmp *[16]uint32, in, out []uint32) {
	w0 := tmp[0] ^ in[0]
	w1 := tmp[1] ^ in[1]
	w2 := tmp[2] ^ in[2]
	w3 := tmp[3] ^ in[3]
	w4 := tmp[4] ^ in[4]
	w5 := tmp[5] ^ in[5]
	w6 := tmp[6] ^ in[6]
	w7 := tmp[7] ^ in[7]
	w8 := tmp[8] ^ in[8]
	w9 := tmp[9] ^ in[9]
	w10 := tmp[10] ^ in[10]
	w11 := tmp[11] ^ in[11]
	w12 := tmp[12] ^ in[12]
	w13 := tmp[13] ^ in[13]
	w14 := tmp[14] ^ in[14]
	w15 := tmp[15] ^ in[15]

	x0, x1, x2, x3, x4, x5, x6, x7, x8 := w0, w1, w2, w3, w4, w5, w6, w7, w8
	x9, x10, x11, x12, x13, x14, x15 := w9, w10, w11, w12, w13, w14, w15

	for i := 0; i < 8; i += 2 {
		x4 ^= bits.RotateLeft32(x0+x12, 7)
		x8 ^= bits.RotateLeft32(x4+x0, 9)
		x12 ^= bits.RotateLeft32(x8+x4, 13)
		x0 ^= bits.RotateLeft32(x12+x8, 18)

		x9 ^= bits.RotateLeft32(x5+x1, 7)
		x13 ^= bits.RotateLeft32(x9+x5, 9)
		x1 ^= bits.RotateLeft32(x13+x9, 13)
		x5 ^= bits.RotateLeft32(x1+x13, 18)

		x14 ^= bits.RotateLeft32(x10+x6, 7)
		x2 ^= bits.RotateLeft32(x14+x10, 9)
		x6 ^= bits.RotateLeft32(x2+x14, 13)
		x10 ^= bits.RotateLeft32(x6+x2, 18)

		x3 ^= bits.RotateLeft32(x15+x11, 7)
		x7 ^= bits.RotateLeft32(x3+x15, 9)
		x11 ^= bits.RotateLeft32(x7+x3, 13)
		x15 ^= bits.RotateLeft32(x11+x7, 18)

		x1 ^= bits.RotateLeft32(x0+x3, 7)
		x2 ^= bits.RotateLeft32(x1+x0, 9)
		x3 ^= bits.RotateLeft32(x2+x1, 13)
		x0 ^= bits.RotateLeft32(x3+x2, 18)

		x6 ^= bits.RotateLeft32(x5+x4, 7)
		x7 ^= bits.RotateLeft32(x6+x5, 9)
		x4 ^= bits.RotateLeft32(x7+x6, 13)
		x5 ^= bits.RotateLeft32(x4+x7, 18)

		x11 ^= bits.RotateLeft32(x10+x9, 7)
		x8 ^= bits.RotateLeft32(x11+x10, 9)
		x9 ^= bits.RotateLeft32(x8+x11, 13)
		x10 ^= bits.RotateLeft32(x9+x8, 18)

		x12 ^= bits.RotateLeft32(x15+x14, 7)
		x13 ^= bits.RotateLeft32(x12+x15, 9)
		x14 ^= bits.RotateLeft32(x13+x12, 13)
		x15 ^= bits.RotateLeft32(x14+x13, 18)
	}
	x0 += w0
	x1 += w1
	x2 += w2
	x3 += w3
	x4 += w4
	x5 += w5
	x6 += w6
	x7 += w7
	x8 += w8
	x9 += w9
	x10 += w10
	x11 += w11
	x12 += w12
	x13 += w13
	x14 += w14
	x15 += w15

	out[0], tmp[0] = x0, x0
	out[1], tmp[1] = x1, x1
	out[2], tmp[2] = x2, x2
	out[3], tmp[3] = x3, x3
	out[4], tmp[4] = x4, x4
	out[5], tmp[5] = x5, x5
	out[6], tmp[6] = x6, x6
	out[7], tmp[7] = x7, x7
	out[8], tmp[8] = x8, x8
	out[9], tmp[9] = x9, x9
	out[10], tmp[10] = x10, x10
	out[11], tmp[11] = x11, x11
	out[12], tmp[12] = x12, x12
	out[13], tmp[13] = x13, x13
	out[14], tmp[14] = x14, x14
	out[15], tmp[15] = x15, x15
}

func scryptBlockMix(tmp *[16]uint32, in, out []uint32, r int) {
	copy(tmp[:], in[(2*r-1)*16:(2*r-1)*16+16])
	for i := 0; i < 2*r; i += 2 {
		scryptSalsaXOR(tmp, in[i*16:], out[i*8:])
		scryptSalsaXOR(tmp, in[i*16+16:], out[i*8+r*16:])
	}
}

func scryptInteger(b []uint32, r int) uint64 {
	j := (2*r - 1) * 16
	return uint64(b[j]) | uint64(b[j+1])<<32
}
