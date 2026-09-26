package obfuscator

import (
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestEffectiveKey(t *testing.T) {
	for raw, want := range map[string]string{
		"benchkey-0123456789": "benchkey-0123456789",
		"  spaced\t":          "spaced",
		"ab=cd":               "ab",
		"x#y":                 "x",
		"#secret":             "",
		"=abc":                "",
		"a b":                 "a b",
	} {
		if got := EffectiveKey(raw); got != want {
			t.Errorf("EffectiveKey(%q)=%q, want %q", raw, got, want)
		}
	}
}

func TestEffectiveObfuscateBytes(t *testing.T) {
	if n := EffectiveObfuscateBytes(&storage.Obfuscator{Masking: "MEDIA"}); n != 16 {
		t.Fatalf("MEDIA+0 → %d, want 16", n)
	}
	if n := EffectiveObfuscateBytes(&storage.Obfuscator{Masking: "MEDIA", ObfuscateBytes: 32}); n != 32 {
		t.Fatalf("MEDIA+32 → %d", n)
	}
	if n := EffectiveObfuscateBytes(&storage.Obfuscator{Masking: "STUN"}); n != 0 {
		t.Fatalf("STUN+0 → %d", n)
	}
}

func TestKernelMasking(t *testing.T) {
	for m, want := range map[string]string{"NONE": "none", "AUTO": "none", "STUN": "stun", "MEDIA": "media"} {
		if got := KernelMasking(&storage.Obfuscator{Masking: m}); got != want {
			t.Errorf("%s → %s, want %s", m, got, want)
		}
	}
}

func TestAddLine(t *testing.T) {
	o := &storage.Obfuscator{Flavor: "phobos", Target: "vpn.example.com:51900", Key: "k=ignored",
		Masking: "MEDIA", MaxDummy: 4, LocalPort: 39001}
	line, err := AddLine(o, "176.109.110.182")
	if err != nil {
		t.Fatal(err)
	}
	want := "127.0.0.1:39001 176.109.110.182:51900 transform=phobos key=6b masking=media max-dummy=4 obfuscate-bytes=16"
	if line != want {
		t.Fatalf("\n got %q\nwant %q", line, want)
	}
	if strings.Contains(line, "ignored") {
		t.Fatal("ключ не эффективный")
	}
	if _, err := AddLine(o, "2001:db8::1"); !errors.Is(err, ErrKernelIPv4Only) {
		t.Fatalf("IPv6: %v", err)
	}
}
