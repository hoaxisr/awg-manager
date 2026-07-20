package shadowcrypt

import (
	"errors"
	"testing"
)

// Vectors generated with glibc crypt(3) (python3 crypt.crypt) — see the
// provenance note in shadowcrypt_test.go.
var desVectors = []struct{ pw, hash string }{
	{"keenetic", "Nrzc8yOSz4klA"},
	{"12345", "n3ir9fmvQaZ9k"},
	{"a", "9Ri1KlNwIe6dY"},
	{"", "ISjxqTXrvELTA"},
	{"p@s:w!", "ePYEST63TiLhs"},
	{"тест", "ozwp6kznTMyGE"},
}

func TestDESCrypt_GlibcVectors(t *testing.T) {
	for _, v := range desVectors {
		if err := Verify(v.pw, v.hash); err != nil {
			t.Errorf("Verify(%q, %s) = %v, want nil", v.pw, v.hash, err)
		}
		// DES учитывает только первые 8 байт пароля — mismatch-проверка
		// осмысленна лишь для коротких паролей (см. отдельный тест ниже).
		if len(v.pw) < 8 {
			if err := Verify(v.pw+"#", v.hash); !errors.Is(err, ErrMismatch) {
				t.Errorf("Verify(%q+#, %s) = %v, want ErrMismatch", v.pw, v.hash, err)
			}
		}
	}
}

// Traditional DES uses only the first 8 password bytes — crypt(3) semantics
// shared with glibc/dropbear, pinned here so nobody "fixes" it.
func TestDESCrypt_TruncatesAtEightBytes(t *testing.T) {
	if err := Verify("keeneticEXTRA", "Nrzc8yOSz4klA"); err != nil {
		t.Fatalf("8-byte truncation must accept longer password: %v", err)
	}
}

func TestIsDESCryptHash(t *testing.T) {
	for _, ok := range []string{"Nrzc8yOSz4klA", "ab./0123456ZZ"} {
		if !isDESCryptHash(ok) {
			t.Errorf("%q must be recognized as DES hash", ok)
		}
	}
	for _, bad := range []string{"", "short", "$1$s$hhhhhhhhhhhhhhhhhhhhhh"[:13], "Nrzc8yOSz4kl!", "Nrzc8yOSz4klAA"} {
		if isDESCryptHash(bad) {
			t.Errorf("%q must NOT be recognized as DES hash", bad)
		}
	}
}
