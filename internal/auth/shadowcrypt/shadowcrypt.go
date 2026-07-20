// Package shadowcrypt verifies passwords against crypt(3)-style hashes as
// found in /etc/shadow. It implements MD5-crypt ($1$), SHA-256-crypt ($5$),
// SHA-512-crypt ($6$) per Ulrich Drepper's specification
// (https://www.akkadia.org/drepper/SHA-crypt.txt) and traditional 13-char
// DES crypt (the historical busybox `passwd` default — see descrypt.go)
// using only the standard library — the Entware daemon vendors minimal
// dependencies and must not pull in x/crypto.
//
// Unsupported schemes (yescrypt $y$, bcrypt $2a$/$2b$, …) return
// ErrUnsupported so callers can log a clear diagnostic while still
// treating the attempt as invalid credentials toward the client.
package shadowcrypt

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"
)

var (
	// ErrMismatch means the hash is well-formed but the password is wrong.
	ErrMismatch = errors.New("password mismatch")
	// ErrUnsupported means the hash uses a scheme this package cannot verify.
	ErrUnsupported = errors.New("unsupported hash scheme")
)

const (
	shaRoundsDefault = 5000
	shaRoundsMin     = 1000
	shaRoundsMax     = 999999999
)

// itoa64 is the crypt(3) base64 alphabet (NOT RFC 4648).
const itoa64 = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Verify checks password against a full crypt(3) hash string (e.g. the
// second field of an /etc/shadow entry). Returns nil when the password
// matches, ErrMismatch when it does not, ErrUnsupported for schemes other
// than $1$/$5$/$6$, and a descriptive error for malformed hashes.
func Verify(password, encoded string) error {
	computed, err := computeFromEncoded(password, encoded)
	if err != nil {
		return err
	}
	// Constant-time comparison of the full recomputed crypt string.
	if subtle.ConstantTimeCompare([]byte(computed), []byte(encoded)) != 1 {
		return ErrMismatch
	}
	return nil
}

// computeFromEncoded recomputes the crypt string for password using the
// scheme/salt/rounds parameters embedded in encoded.
func computeFromEncoded(password, encoded string) (string, error) {
	switch {
	case strings.HasPrefix(encoded, "$1$"):
		salt, err := extractSalt(encoded, "$1$", 8)
		if err != nil {
			return "", err
		}
		return md5Crypt([]byte(password), salt), nil
	case strings.HasPrefix(encoded, "$5$"):
		salt, rounds, explicit, err := extractShaSalt(encoded, "$5$")
		if err != nil {
			return "", err
		}
		return shaCrypt([]byte(password), salt, rounds, explicit, "$5$", sha256.New, sha256Order[:]), nil
	case strings.HasPrefix(encoded, "$6$"):
		salt, rounds, explicit, err := extractShaSalt(encoded, "$6$")
		if err != nil {
			return "", err
		}
		return shaCrypt([]byte(password), salt, rounds, explicit, "$6$", sha512.New, sha512Order[:]), nil
	case isDESCryptHash(encoded):
		return desCrypt([]byte(password), encoded[:2]), nil
	default:
		return "", ErrUnsupported
	}
}

// extractSalt returns the salt portion of encoded after prefix, truncated
// to maxLen characters (crypt(3) semantics).
func extractSalt(encoded, prefix string, maxLen int) (string, error) {
	rest := strings.TrimPrefix(encoded, prefix)
	salt, _, ok := strings.Cut(rest, "$")
	if !ok {
		return "", fmt.Errorf("malformed %shash: missing '$' after salt", prefix)
	}
	if len(salt) > maxLen {
		salt = salt[:maxLen]
	}
	return salt, nil
}

// extractShaSalt parses the optional rounds=N$ parameter and the salt of a
// $5$/$6$ hash. Returns the (possibly truncated) salt, the effective round
// count clamped to [1000, 999999999], and whether rounds were explicit.
func extractShaSalt(encoded, prefix string) (salt string, rounds int, explicit bool, err error) {
	rest := strings.TrimPrefix(encoded, prefix)
	rounds = shaRoundsDefault
	if strings.HasPrefix(rest, "rounds=") {
		spec, tail, ok := strings.Cut(rest, "$")
		if !ok {
			return "", 0, false, fmt.Errorf("malformed %shash: missing '$' after rounds", prefix)
		}
		n, convErr := strconv.Atoi(strings.TrimPrefix(spec, "rounds="))
		if convErr != nil {
			return "", 0, false, fmt.Errorf("malformed %shash: bad rounds value", prefix)
		}
		// Clamp per spec, do not reject.
		if n < shaRoundsMin {
			n = shaRoundsMin
		}
		if n > shaRoundsMax {
			n = shaRoundsMax
		}
		rounds = n
		explicit = true
		rest = tail
	}
	salt, _, ok := strings.Cut(rest, "$")
	if !ok {
		return "", 0, false, fmt.Errorf("malformed %shash: missing '$' after salt", prefix)
	}
	if len(salt) > 16 {
		salt = salt[:16]
	}
	return salt, rounds, explicit, nil
}

// md5Crypt implements Poul-Henning Kamp's MD5-crypt as used by crypt(3).
func md5Crypt(password []byte, salt string) string {
	saltB := []byte(salt)

	alt := md5.New()
	alt.Write(password)
	alt.Write(saltB)
	alt.Write(password)
	altSum := alt.Sum(nil)

	ctx := md5.New()
	ctx.Write(password)
	ctx.Write([]byte("$1$"))
	ctx.Write(saltB)
	for i := len(password); i > 0; i -= 16 {
		if i > 16 {
			ctx.Write(altSum)
		} else {
			ctx.Write(altSum[:i])
		}
	}
	for i := len(password); i > 0; i >>= 1 {
		if i&1 != 0 {
			ctx.Write([]byte{0})
		} else {
			ctx.Write(password[:1])
		}
	}
	sum := ctx.Sum(nil)

	for i := 0; i < 1000; i++ {
		c := md5.New()
		if i&1 != 0 {
			c.Write(password)
		} else {
			c.Write(sum)
		}
		if i%3 != 0 {
			c.Write(saltB)
		}
		if i%7 != 0 {
			c.Write(password)
		}
		if i&1 != 0 {
			c.Write(sum)
		} else {
			c.Write(password)
		}
		sum = c.Sum(nil)
	}

	var out strings.Builder
	out.WriteString("$1$")
	out.WriteString(salt)
	out.WriteByte('$')
	b64From24Bit(&out, sum[0], sum[6], sum[12], 4)
	b64From24Bit(&out, sum[1], sum[7], sum[13], 4)
	b64From24Bit(&out, sum[2], sum[8], sum[14], 4)
	b64From24Bit(&out, sum[3], sum[9], sum[15], 4)
	b64From24Bit(&out, sum[4], sum[10], sum[5], 4)
	b64From24Bit(&out, 0, 0, sum[11], 2)
	return out.String()
}

// sha256Order and sha512Order are the digest-byte permutations from the
// reference implementations in the SHA-crypt specification (steps 22e/22h).
// Each triple is (B2, B1, B0) of one b64_from_24bit call; the final short
// group is handled explicitly in shaCrypt.
var sha256Order = [10][3]int{
	{0, 10, 20}, {21, 1, 11}, {12, 22, 2}, {3, 13, 23}, {24, 4, 14},
	{15, 25, 5}, {6, 16, 26}, {27, 7, 17}, {18, 28, 8}, {9, 19, 29},
}

var sha512Order = [21][3]int{
	{0, 21, 42}, {22, 43, 1}, {44, 2, 23}, {3, 24, 45}, {25, 46, 4},
	{47, 5, 26}, {6, 27, 48}, {28, 49, 7}, {50, 8, 29}, {9, 30, 51},
	{31, 52, 10}, {53, 11, 32}, {12, 33, 54}, {34, 55, 13}, {56, 14, 35},
	{15, 36, 57}, {37, 58, 16}, {59, 17, 38}, {18, 39, 60}, {40, 61, 19},
	{62, 20, 41},
}

// shaCrypt implements SHA-256-crypt / SHA-512-crypt per Drepper's spec.
// explicitRounds controls whether the output embeds "rounds=N$" — the spec
// omits it when the input had no rounds parameter.
func shaCrypt(password []byte, salt string, rounds int, explicitRounds bool, prefix string, newHash func() hash.Hash, order [][3]int) string {
	saltB := []byte(salt)
	size := newHash().Size()

	// Steps 4-8: digest B.
	b := newHash()
	b.Write(password)
	b.Write(saltB)
	b.Write(password)
	bSum := b.Sum(nil)

	// Steps 1-3, 9-12: digest A.
	a := newHash()
	a.Write(password)
	a.Write(saltB)
	for i := len(password); i > size; i -= size {
		a.Write(bSum)
	}
	rem := len(password) % size
	if rem == 0 && len(password) > 0 {
		rem = size
	}
	a.Write(bSum[:rem])
	for i := len(password); i > 0; i >>= 1 {
		if i&1 != 0 {
			a.Write(bSum)
		} else {
			a.Write(password)
		}
	}
	aSum := a.Sum(nil)

	// Steps 13-16: sequence P.
	dp := newHash()
	for i := 0; i < len(password); i++ {
		dp.Write(password)
	}
	dpSum := dp.Sum(nil)
	p := repeatTo(dpSum, len(password))

	// Steps 17-20: sequence S.
	ds := newHash()
	for i := 0; i < 16+int(aSum[0]); i++ {
		ds.Write(saltB)
	}
	dsSum := ds.Sum(nil)
	s := repeatTo(dsSum, len(saltB))

	// Step 21: the rounds loop.
	sum := aSum
	for i := 0; i < rounds; i++ {
		c := newHash()
		if i&1 != 0 {
			c.Write(p)
		} else {
			c.Write(sum)
		}
		if i%3 != 0 {
			c.Write(s)
		}
		if i%7 != 0 {
			c.Write(p)
		}
		if i&1 != 0 {
			c.Write(sum)
		} else {
			c.Write(p)
		}
		sum = c.Sum(nil)
	}

	// Step 22: assemble the output string.
	var out strings.Builder
	out.WriteString(prefix)
	if explicitRounds {
		fmt.Fprintf(&out, "rounds=%d$", rounds)
	}
	out.WriteString(salt)
	out.WriteByte('$')
	for _, g := range order {
		b64From24Bit(&out, sum[g[0]], sum[g[1]], sum[g[2]], 4)
	}
	if size == sha256.Size {
		b64From24Bit(&out, 0, sum[31], sum[30], 3)
	} else {
		b64From24Bit(&out, 0, 0, sum[63], 2)
	}
	return out.String()
}

// repeatTo repeats block until n bytes are produced (truncating the tail).
func repeatTo(block []byte, n int) []byte {
	out := make([]byte, 0, n)
	for len(out) < n {
		remain := n - len(out)
		if remain > len(block) {
			remain = len(block)
		}
		out = append(out, block[:remain]...)
	}
	return out
}

// b64From24Bit emits n characters of crypt(3) base64 for the 24-bit word
// b2<<16 | b1<<8 | b0, least-significant 6 bits first.
func b64From24Bit(out *strings.Builder, b2, b1, b0 byte, n int) {
	w := uint32(b2)<<16 | uint32(b1)<<8 | uint32(b0)
	for i := 0; i < n; i++ {
		out.WriteByte(itoa64[w&0x3f])
		w >>= 6
	}
}
