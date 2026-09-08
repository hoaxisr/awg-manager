package signature

import (
	"errors"
	"regexp"
	"testing"
)

var tagRe = regexp.MustCompile(`<(\w+)(?:\s+([^>]*))?>`)

// Токены только из пересечения kernel-модуля и amneziawg-go.
func assertAllowedTokens(t *testing.T, pattern string) {
	t.Helper()
	rest := tagRe.ReplaceAllString(pattern, "")
	if rest != "" {
		t.Fatalf("stray text outside tokens: %q", rest)
	}
	for _, m := range tagRe.FindAllStringSubmatch(pattern, -1) {
		switch m[1] {
		case "b":
			if !regexp.MustCompile(`^0x([0-9a-f]{2})+$`).MatchString(m[2]) {
				t.Fatalf("bad <b> payload %q", m[2])
			}
		case "t":
			if m[2] != "" {
				t.Fatalf("<t> takes no argument: %q", m[0])
			}
		case "r", "rc", "rd":
			if !regexp.MustCompile(`^[1-9]\d{0,3}$`).MatchString(m[2]) {
				t.Fatalf("bad %s size %q", m[1], m[2])
			}
		default:
			t.Fatalf("forbidden token %q", m[0])
		}
	}
}

func TestCanonicalProtocol(t *testing.T) {
	for _, p := range Profiles {
		if CanonicalProtocol(p) != p {
			t.Fatalf("%s not canonical", p)
		}
	}
	for _, bad := range []string{"tls", "quic_0rtt", "http3", "wireguard_noise", "dns_query", ""} {
		if CanonicalProtocol(bad) != "" {
			t.Fatalf("%q must be unknown", bad)
		}
	}
}

func TestGenerate_UnknownProfile(t *testing.T) {
	if _, err := Generate("tls"); !errors.Is(err, ErrUnknownProtocol) {
		t.Fatalf("err = %v, want ErrUnknownProtocol", err)
	}
}

func TestByteSize(t *testing.T) {
	cases := map[string]int{"": 0, "<b 0x0102>": 2, "<r 10><rc 3><rd 2><t>": 19, "<b 0xAB><t>": 5}
	for in, want := range cases {
		if got := ByteSize(in); got != want {
			t.Errorf("ByteSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTokB(t *testing.T) {
	if got := tokB([]byte{0xc3, 0x00, 0xff}); got != "<b 0xc300ff>" {
		t.Fatalf("tokB = %q", got)
	}
}
