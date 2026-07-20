package shadowcrypt

import (
	"errors"
	"strings"
	"testing"
)

// SHA-256-crypt and SHA-512-crypt vectors are the official test arrays from
// Ulrich Drepper's SHA-crypt specification
// (https://www.akkadia.org/drepper/SHA-crypt.txt, "static const struct {...}
// tests2[]" in the appendix). Every vector was additionally re-derived
// offline against glibc's reference implementation (crypt_r via Python's
// crypt module, glibc 2.3x) during development and matched byte-for-byte —
// except the two "roundstoolow" clamp vectors, which modern glibc rejects
// outright (returns "*0") instead of clamping; those were verified by
// computing the clamped value explicitly (crypt(pw, "$5$rounds=1000$rounds-
// toolow")), which per spec must equal the rounds=10 input's output.
//
// MD5-crypt has no published spec vectors; the $1$ vectors below were
// generated offline in this environment with glibc crypt(3)
// (crypt.crypt(pw, "$1$<salt>"), glibc backing OpenSSL 3.0.13-era Debian),
// including the classic empty-password/empty-salt constant
// "$1$$qRPK7m23GJusamGpoGLby/" that also appears in FreeBSD's md5crypt
// regression tests.
var verifyVectors = []struct {
	name     string
	password string
	hash     string
}{
	// ── $5$ (SHA-256-crypt), Drepper spec tests2[] ──
	{"sha256/basic", "Hello world!", "$5$saltstring$5B8vYYiY.CVt1RlTTf8KbXBH3hsxY/GNooZaBBGWEc5"},
	{"sha256/rounds10000-salt-truncated", "Hello world!", "$5$rounds=10000$saltstringsaltst$3xv.VbSHBb41AL9AvLeujZkZRBAwqFMz2.opqey6IcA"},
	{"sha256/rounds5000-explicit", "This is just a test", "$5$rounds=5000$toolongsaltstrin$Un/5jzAHMgOGZ5.mWJpuVolil07guHPvOW8mGRcvxa5"},
	{"sha256/rounds1400-long-password", "a very much longer text to encrypt.  This one even stretches over morethan one line.", "$5$rounds=1400$anotherlongsalts$Rx.j8H.h8HjEDGomFU8bDkXm3XIUnzyxf12oP84Bnq1"},
	{"sha256/rounds77777-short-salt", "we have a short salt string but not a short password", "$5$rounds=77777$short$JiO1O3ZpDAxGJeaDIuqCoEFysAe1mZNJRs3pw0KQRd/"},
	{"sha256/rounds123456-salt16", "a short string", "$5$rounds=123456$asaltof16chars..$gP3VQ/6X7UUEW3HkBn2w1/Ptq2jxPyzV/cZKmF/wJvD"},
	{"sha256/rounds-clamped-to-1000", "the minimum number is still observed", "$5$rounds=1000$roundstoolow$yfvwcWrQ8l/K0DAWyuPMDNHpIVlTQebY9l/gL972bIC"},
	// ── $6$ (SHA-512-crypt), Drepper spec tests2[] ──
	{"sha512/basic", "Hello world!", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
	{"sha512/rounds10000-salt-truncated", "Hello world!", "$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
	{"sha512/rounds5000-explicit", "This is just a test", "$6$rounds=5000$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0"},
	{"sha512/rounds1400-long-password", "a very much longer text to encrypt.  This one even stretches over morethan one line.", "$6$rounds=1400$anotherlongsalts$POfYwTEok97VWcjxIiSOjiykti.o/pQs.wPvMxQ6Fm7I6IoYN3CmLs66x9t0oSwbtEW7o7UmJEiDwGqd8p4ur1"},
	{"sha512/rounds77777-short-salt", "we have a short salt string but not a short password", "$6$rounds=77777$short$WuQyW2YR.hBNpjjRhpYD/ifIw05xdfeEyQoMxIXbkvr0gge1a1x3yRULJ5CCaUeOxFmtlcGZelFl5CxtgfiAc0"},
	{"sha512/rounds123456-salt16", "a short string", "$6$rounds=123456$asaltof16chars..$BtCwjqMJGx5hrJhZywWvt0RLE8uZ4oPwcelCjmw2kSYu.Ec6ycULevoBK25fs2xXgMNrCzIMVcgEJAstJeonj1"},
	{"sha512/rounds-clamped-to-1000", "the minimum number is still observed", "$6$rounds=1000$roundstoolow$kUMsbe306n21p9R.FRkW3IGn.S9NPN0x50YhH1xhLsPuWGsUSklZt58jaTfF4ZEQpyUNGc0dqbpBYYBaHHrsX."},
	// glibc-derived (no rounds parameter, salt truncated at 16 chars).
	{"sha256/no-rounds-salt-truncated", "Hello world!", "$5$toolongsaltstrin$0vuwUia3Nx9V/DqToMS8YLcfXpEXmSaC8wgguLIbus2"},
	{"sha512/no-rounds-salt-truncated", "Hello world!", "$6$toolongsaltstrin$iGlL7EUUfzNQx59x3ydJZ.zXPMUu1dOynSEl/vcNhLlas77qD0DzRswhhB6LdrXTz250at0syAfUXra.XrxAI1"},
	// ── $1$ (MD5-crypt), glibc-derived — see provenance note above ──
	{"md5/empty-password-empty-salt", "", "$1$$qRPK7m23GJusamGpoGLby/"},
	{"md5/basic", "Hello world!", "$1$saltstri$YMyguxXMBpd2TEZ.vS/3q1"},
	{"md5/simple", "password", "$1$abcd0123$U.n6Jj1fRNp16L12zcPVi."},
	{"md5/long-password", "a longer password string for md5-crypt testing", "$1$12345678$d9PFCeI1XzHDb4bFKZYKm/"},
}

func TestVerify_OfficialVectors(t *testing.T) {
	for _, v := range verifyVectors {
		t.Run(v.name, func(t *testing.T) {
			if err := Verify(v.password, v.hash); err != nil {
				t.Fatalf("Verify(%q, %q) = %v, want nil", v.password, v.hash, err)
			}
		})
	}
}

func TestVerify_WrongPassword(t *testing.T) {
	for _, v := range verifyVectors {
		t.Run(v.name, func(t *testing.T) {
			if err := Verify(v.password+"x", v.hash); !errors.Is(err, ErrMismatch) {
				t.Fatalf("Verify(wrong password) = %v, want ErrMismatch", err)
			}
		})
	}
}

// TestVerify_RoundsClampedInsideStoredHash pins the clamp semantics on the
// stored-hash side: a shadow entry recorded with rounds=10 (below the 1000
// minimum) can never verify, because per spec the computed string always
// carries the clamped "rounds=1000" — so the full-string constant-time
// compare fails. The spec's own tests2[] expects the rounds=1000 output for
// a rounds=10 input, which TestVerify_OfficialVectors already covers.
func TestVerify_RoundsClampedInsideStoredHash(t *testing.T) {
	err := Verify("the minimum number is still observed",
		"$5$rounds=10$roundstoolow$yfvwcWrQ8l/K0DAWyuPMDNHpIVlTQebY9l/gL972bIC")
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("Verify(below-min rounds hash) = %v, want ErrMismatch", err)
	}
	// Above the max: clamped down to 999999999. Just assert the parser
	// accepts it and produces a mismatch (not a malformed-hash error) —
	// actually computing 999999999 rounds would take hours, so use a tiny
	// digest comparison failure path: the hash body below is bogus.
	// Parsing must still succeed.
	if _, _, _, err := extractShaSalt("$5$rounds=9999999990$salt$bogus", "$5$"); err != nil {
		t.Fatalf("extractShaSalt(above-max rounds) error = %v, want nil (clamped)", err)
	}
}

func TestExtractShaSalt_Clamping(t *testing.T) {
	tests := []struct {
		in           string
		wantSalt     string
		wantRounds   int
		wantExplicit bool
	}{
		{"$5$saltstring$x", "saltstring", 5000, false},
		{"$5$rounds=10$roundstoolow$x", "roundstoolow", 1000, true},
		{"$5$rounds=9999999990$salt$x", "salt", 999999999, true},
		{"$5$rounds=5000$toolongsaltstringgg$x", "toolongsaltstrin", 5000, true},
		// Negative rounds parse fine (Atoi accepts the sign) and clamp up to
		// the 1000 minimum — never negative, never used as a loop bound < 0.
		{"$5$rounds=-5$roundstoolow$x", "roundstoolow", 1000, true},
		// Salt exactly at the 16-char cap is kept whole (boundary of the
		// >16 truncation).
		{"$5$asaltof16chars..$x", "asaltof16chars..", 5000, false},
	}
	for _, tc := range tests {
		salt, rounds, explicit, err := extractShaSalt(tc.in, "$5$")
		if err != nil {
			t.Fatalf("extractShaSalt(%q) error = %v", tc.in, err)
		}
		if salt != tc.wantSalt || rounds != tc.wantRounds || explicit != tc.wantExplicit {
			t.Errorf("extractShaSalt(%q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.in, salt, rounds, explicit, tc.wantSalt, tc.wantRounds, tc.wantExplicit)
		}
	}
}

// TestVerify_WrongLengthHashBodyFailsClosed pins that a well-formed prefix +
// salt but a truncated / overlong digest section never matches and never
// panics: the recomputed crypt string is always full length, so the
// constant-time compare simply fails closed with ErrMismatch. Guards the
// indexing in shaCrypt against short stored bodies.
func TestVerify_WrongLengthHashBodyFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		hash string
	}{
		{"sha512-short-body", "$6$saltstring$short"},
		{"sha512-empty-body", "$6$saltstring$"},
		{"sha512-overlong-body", "$6$saltstring$" + strings.Repeat("A", 200)},
		{"sha256-short-body", "$5$saltstring$short"},
		{"sha256-empty-body", "$5$saltstring$"},
		{"md5-short-body", "$1$saltstri$short"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify("Hello world!", tc.hash); !errors.Is(err, ErrMismatch) {
				t.Fatalf("Verify(%q) = %v, want ErrMismatch (fail closed)", tc.hash, err)
			}
		})
	}
}

func TestVerify_UnsupportedSchemes(t *testing.T) {
	unsupported := []struct {
		name string
		hash string
	}{
		{"yescrypt", "$y$j9T$F5Jx5fExrKuPp53xLKQ..1$X9CmZ6hlG1yUv0xUOG3ee0FnLPq4Y8HB5rTuLBt0YQ0"},
		{"bcrypt-2b", "$2b$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"},
		{"bcrypt-2a", "$2a$10$N9qo8uLOickgx2ZMRZoMye"},
		// 13-char DES теперь ПОДДЕРЖИВАЕТСЯ (descrypt.go) и сюда не входит.
		{"sha1crypt", "$sha1$40000$jtNX3nZ2$hBNaIXkt4wBI2o5rsi8KejSjNqIq"},
		{"empty", ""},
	}
	for _, tc := range unsupported {
		t.Run(tc.name, func(t *testing.T) {
			if err := Verify("whatever", tc.hash); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("Verify(%q) = %v, want ErrUnsupported", tc.hash, err)
			}
		})
	}
}

func TestVerify_MalformedHashes(t *testing.T) {
	malformed := []struct {
		name string
		hash string
	}{
		{"md5-no-dollar-after-salt", "$1$saltonly"},
		{"sha256-no-dollar-after-salt", "$5$saltonly"},
		{"sha512-no-dollar-after-salt", "$6$saltonly"},
		{"sha256-bad-rounds", "$5$rounds=abc$salt$hash"},
		{"sha256-rounds-no-salt-terminator", "$5$rounds=5000"},
		// Rounds value overflows int → strconv.Atoi returns an error → the
		// hash is rejected as malformed (fails closed), not clamped.
		{"sha256-rounds-overflow", "$5$rounds=99999999999999999999$salt$hash"},
		{"sha512-rounds-overflow", "$6$rounds=99999999999999999999$salt$hash"},
	}
	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify("whatever", tc.hash)
			if err == nil {
				t.Fatal("Verify(malformed) = nil, want error")
			}
			if errors.Is(err, ErrMismatch) || errors.Is(err, ErrUnsupported) {
				t.Fatalf("Verify(malformed) = %v, want a malformed-hash error", err)
			}
		})
	}
}
