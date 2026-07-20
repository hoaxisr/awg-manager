package shadowcrypt

// Traditional DES-based crypt(3) ("descrypt"): 13-character hashes with a
// 2-character salt and no $-prefix — the historical default of busybox
// `passwd` (CONFIG_FEATURE_DEFAULT_PASSWD_ALGO="des"), so Entware accounts
// whose password predates the sha256 default still carry these. The salt
// perturbs the DES E-box, which is why crypto/des cannot be reused.
//
// Tables are FIPS 46-3; correctness is pinned by glibc-generated vectors in
// descrypt_test.go. Performance is irrelevant on the login path.

// initial permutation and its inverse (bit numbers are 1-based from the MSB,
// per FIPS convention).
var desIP = [64]int{
	58, 50, 42, 34, 26, 18, 10, 2,
	60, 52, 44, 36, 28, 20, 12, 4,
	62, 54, 46, 38, 30, 22, 14, 6,
	64, 56, 48, 40, 32, 24, 16, 8,
	57, 49, 41, 33, 25, 17, 9, 1,
	59, 51, 43, 35, 27, 19, 11, 3,
	61, 53, 45, 37, 29, 21, 13, 5,
	63, 55, 47, 39, 31, 23, 15, 7,
}

var desFP = [64]int{
	40, 8, 48, 16, 56, 24, 64, 32,
	39, 7, 47, 15, 55, 23, 63, 31,
	38, 6, 46, 14, 54, 22, 62, 30,
	37, 5, 45, 13, 53, 21, 61, 29,
	36, 4, 44, 12, 52, 20, 60, 28,
	35, 3, 43, 11, 51, 19, 59, 27,
	34, 2, 42, 10, 50, 18, 58, 26,
	33, 1, 41, 9, 49, 17, 57, 25,
}

// desE is the expansion box (32→48). crypt(3) swaps entries i and i+24 for
// every set bit i (0..11) of the 12-bit salt — the "modified DES" that makes
// hardware DES chips useless for cracking.
var desE = [48]int{
	32, 1, 2, 3, 4, 5,
	4, 5, 6, 7, 8, 9,
	8, 9, 10, 11, 12, 13,
	12, 13, 14, 15, 16, 17,
	16, 17, 18, 19, 20, 21,
	20, 21, 22, 23, 24, 25,
	24, 25, 26, 27, 28, 29,
	28, 29, 30, 31, 32, 1,
}

var desP = [32]int{
	16, 7, 20, 21,
	29, 12, 28, 17,
	1, 15, 23, 26,
	5, 18, 31, 10,
	2, 8, 24, 14,
	32, 27, 3, 9,
	19, 13, 30, 6,
	22, 11, 4, 25,
}

var desPC1 = [56]int{
	57, 49, 41, 33, 25, 17, 9,
	1, 58, 50, 42, 34, 26, 18,
	10, 2, 59, 51, 43, 35, 27,
	19, 11, 3, 60, 52, 44, 36,
	63, 55, 47, 39, 31, 23, 15,
	7, 62, 54, 46, 38, 30, 22,
	14, 6, 61, 53, 45, 37, 29,
	21, 13, 5, 28, 20, 12, 4,
}

var desPC2 = [48]int{
	14, 17, 11, 24, 1, 5,
	3, 28, 15, 6, 21, 10,
	23, 19, 12, 4, 26, 8,
	16, 7, 27, 20, 13, 2,
	41, 52, 31, 37, 47, 55,
	30, 40, 51, 45, 33, 48,
	44, 49, 39, 56, 34, 53,
	46, 42, 50, 36, 29, 32,
}

var desShifts = [16]int{1, 1, 2, 2, 2, 2, 2, 2, 1, 2, 2, 2, 2, 2, 2, 1}

var desSBox = [8][64]byte{
	{
		14, 4, 13, 1, 2, 15, 11, 8, 3, 10, 6, 12, 5, 9, 0, 7,
		0, 15, 7, 4, 14, 2, 13, 1, 10, 6, 12, 11, 9, 5, 3, 8,
		4, 1, 14, 8, 13, 6, 2, 11, 15, 12, 9, 7, 3, 10, 5, 0,
		15, 12, 8, 2, 4, 9, 1, 7, 5, 11, 3, 14, 10, 0, 6, 13,
	},
	{
		15, 1, 8, 14, 6, 11, 3, 4, 9, 7, 2, 13, 12, 0, 5, 10,
		3, 13, 4, 7, 15, 2, 8, 14, 12, 0, 1, 10, 6, 9, 11, 5,
		0, 14, 7, 11, 10, 4, 13, 1, 5, 8, 12, 6, 9, 3, 2, 15,
		13, 8, 10, 1, 3, 15, 4, 2, 11, 6, 7, 12, 0, 5, 14, 9,
	},
	{
		10, 0, 9, 14, 6, 3, 15, 5, 1, 13, 12, 7, 11, 4, 2, 8,
		13, 7, 0, 9, 3, 4, 6, 10, 2, 8, 5, 14, 12, 11, 15, 1,
		13, 6, 4, 9, 8, 15, 3, 0, 11, 1, 2, 12, 5, 10, 14, 7,
		1, 10, 13, 0, 6, 9, 8, 7, 4, 15, 14, 3, 11, 5, 2, 12,
	},
	{
		7, 13, 14, 3, 0, 6, 9, 10, 1, 2, 8, 5, 11, 12, 4, 15,
		13, 8, 11, 5, 6, 15, 0, 3, 4, 7, 2, 12, 1, 10, 14, 9,
		10, 6, 9, 0, 12, 11, 7, 13, 15, 1, 3, 14, 5, 2, 8, 4,
		3, 15, 0, 6, 10, 1, 13, 8, 9, 4, 5, 11, 12, 7, 2, 14,
	},
	{
		2, 12, 4, 1, 7, 10, 11, 6, 8, 5, 3, 15, 13, 0, 14, 9,
		14, 11, 2, 12, 4, 7, 13, 1, 5, 0, 15, 10, 3, 9, 8, 6,
		4, 2, 1, 11, 10, 13, 7, 8, 15, 9, 12, 5, 6, 3, 0, 14,
		11, 8, 12, 7, 1, 14, 2, 13, 6, 15, 0, 9, 10, 4, 5, 3,
	},
	{
		12, 1, 10, 15, 9, 2, 6, 8, 0, 13, 3, 4, 14, 7, 5, 11,
		10, 15, 4, 2, 7, 12, 9, 5, 6, 1, 13, 14, 0, 11, 3, 8,
		9, 14, 15, 5, 2, 8, 12, 3, 7, 0, 4, 10, 1, 13, 11, 6,
		4, 3, 2, 12, 9, 5, 15, 10, 11, 14, 1, 7, 6, 0, 8, 13,
	},
	{
		4, 11, 2, 14, 15, 0, 8, 13, 3, 12, 9, 7, 5, 10, 6, 1,
		13, 0, 11, 7, 4, 9, 1, 10, 14, 3, 5, 12, 2, 15, 8, 6,
		1, 4, 11, 13, 12, 3, 7, 14, 10, 15, 6, 8, 0, 5, 9, 2,
		6, 11, 13, 8, 1, 4, 10, 7, 9, 5, 0, 15, 14, 2, 3, 12,
	},
	{
		13, 2, 8, 4, 6, 15, 11, 1, 10, 9, 3, 14, 5, 0, 12, 7,
		1, 15, 13, 8, 10, 3, 7, 4, 12, 5, 6, 11, 0, 14, 9, 2,
		7, 11, 4, 1, 9, 12, 14, 2, 0, 6, 10, 13, 15, 3, 5, 8,
		2, 1, 14, 7, 4, 10, 8, 13, 15, 12, 9, 0, 3, 5, 6, 11,
	},
}

// desPermute maps src (an in-width-bit value, bit 1 = MSB) through table:
// output bit i (MSB-first) = src bit table[i].
func desPermute(src uint64, table []int, inWidth int) uint64 {
	var out uint64
	for _, b := range table {
		out = out<<1 | (src>>(inWidth-b))&1
	}
	return out
}

// desSubkeys derives the 16 round keys from an 8-byte crypt key
// (password bytes shifted left one bit each).
func desSubkeys(key uint64) [16]uint64 {
	cd := desPermute(key, desPC1[:], 64) // 56 bits
	c := cd >> 28
	d := cd & 0xFFFFFFF
	var ks [16]uint64
	for i, s := range desShifts {
		c = ((c << s) | (c >> (28 - s))) & 0xFFFFFFF
		d = ((d << s) | (d >> (28 - s))) & 0xFFFFFFF
		ks[i] = desPermute(c<<28|d, desPC2[:], 56) // 48 bits
	}
	return ks
}

// desEncryptBlock runs one full DES encryption of block with the salt-modified
// expansion box eBox.
func desEncryptBlock(block uint64, ks *[16]uint64, eBox []int) uint64 {
	v := desPermute(block, desIP[:], 64)
	l := uint32(v >> 32)
	r := uint32(v)
	for i := 0; i < 16; i++ {
		e := desPermute(uint64(r), eBox, 32) ^ ks[i] // 48 bits
		var f uint64
		for box := 0; box < 8; box++ {
			six := byte(e >> (42 - 6*box) & 0x3f)
			// Row = outer bits, column = inner four.
			idx := (six&0x20 | six&1<<4) | six>>1&0xf
			f = f<<4 | uint64(desSBox[box][idx])
		}
		fr := uint32(desPermute(f, desP[:], 32))
		l, r = r, l^fr
	}
	// Final swap + inverse permutation.
	return desPermute(uint64(r)<<32|uint64(l), desFP[:], 64)
}

// desCrypt computes the traditional 13-character crypt(3) hash for password
// with the given 2-character salt (already validated by the caller).
func desCrypt(password []byte, salt string) string {
	var key uint64
	for i := 0; i < 8; i++ {
		var ch byte
		if i < len(password) {
			ch = password[i] << 1
		}
		key = key<<8 | uint64(ch)
	}
	ks := desSubkeys(key)

	saltVal := itoa64Index(salt[0]) | itoa64Index(salt[1])<<6
	eBox := make([]int, 48)
	copy(eBox, desE[:])
	for i := 0; i < 12; i++ {
		if saltVal>>i&1 != 0 {
			eBox[i], eBox[i+24] = eBox[i+24], eBox[i]
		}
	}

	var block uint64
	for i := 0; i < 25; i++ {
		block = desEncryptBlock(block, &ks, eBox)
	}

	out := make([]byte, 0, 13)
	out = append(out, salt[0], salt[1])
	// 64 bits → 11 chars, 6 bits per char MSB-first, zero-padded tail.
	for i := 0; i < 11; i++ {
		var six byte
		for j := 0; j < 6; j++ {
			six <<= 1
			bit := 6*i + j
			if bit < 64 && block>>(63-bit)&1 != 0 {
				six |= 1
			}
		}
		out = append(out, itoa64[six])
	}
	return string(out)
}

// itoa64Index returns the 6-bit value of a crypt(3) base64 character, or -1.
func itoa64Index(c byte) int {
	switch {
	case c == '.':
		return 0
	case c == '/':
		return 1
	case c >= '0' && c <= '9':
		return int(c-'0') + 2
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 12
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 38
	default:
		return -1
	}
}

// isDESCryptHash reports whether encoded looks like a traditional 13-char
// DES crypt hash (2 salt chars + 11 hash chars, all from the crypt alphabet).
func isDESCryptHash(encoded string) bool {
	if len(encoded) != 13 {
		return false
	}
	for i := 0; i < len(encoded); i++ {
		if itoa64Index(encoded[i]) < 0 {
			return false
		}
	}
	return true
}
