package config

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestValidateBasePath(t *testing.T) {
	valid := []string{
		"",
		"a",
		"dobry-cola-super-app",
		"MixedCase",
		"two/segments",
		"a/b/c9_x-y",
		"kecjyr5ti4qtvquhyva43e5h24",
		strings.Repeat("a", MaxBasePathLength),
	}
	for _, path := range valid {
		if err := ValidateBasePath(path); err != nil {
			t.Fatalf("base path %q was rejected: %v", path, err)
		}
	}
	invalid := []string{
		"/leading",
		"trailing/",
		"empty//segment",
		"-lead",
		"_lead",
		"a/-lead",
		"dot.ted",
		"..",
		"with space",
		"per%20cent",
		"unicode-é",
		strings.Repeat("a", MaxBasePathLength+1),
	}
	for _, path := range invalid {
		if err := ValidateBasePath(path); err == nil {
			t.Fatalf("base path %q was accepted", path)
		}
	}
}

func TestBaseIsAlwaysSlashDelimited(t *testing.T) {
	if got := Base(""); got != "/" {
		t.Fatalf("root base was %q", got)
	}
	if got := Base("a/b"); got != "/a/b/" {
		t.Fatalf("prefixed base was %q", got)
	}
}

// A base path derives through the v2 context, and the root keeps the frozen v1
// derivation byte for byte, so existing deployments are unaffected.
func TestDeriveCapabilityBindsTheBasePath(t *testing.T) {
	const host = "proxy.example.com"
	const path = "dobry-cola-super-app"
	tests := []struct {
		secret   string
		basePath string
		want     string
	}{
		{"000102030405060708090a0b0c0d0e0f", "", "MHLEY5PmW1GWqJkSrlmJpvJUiLhBH_QKy6yKg8a0JPk"},
		{"dd000102030405060708090a0b0c0d0e0f", "", "IpJrt3e7sKtzPyoXy6w-Zj6GGEvsvclN66JzQEfPYLA"},
		{"000102030405060708090a0b0c0d0e0f", path, "hHz99Xs93EN1j91G9gpNepXwGNNt5YdAFkEVk_LlqdQ"},
		{"dd000102030405060708090a0b0c0d0e0f", path, "TGUkZaevsavLbHvlNWipnRoYxgzZ51ioWvbxgGT3wHo"},
	}
	for _, test := range tests {
		secret, err := hex.DecodeString(test.secret)
		if err != nil {
			t.Fatal(err)
		}
		got := CapabilityString(DeriveCapability(host, test.basePath, secret))
		if got != test.want {
			t.Fatalf(
				"capability for base path %q and secret %s was %q, want %q",
				test.basePath,
				test.secret,
				got,
				test.want)
		}
	}
	secret, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f")
	if DeriveCapability(host, "one", secret) == DeriveCapability(host, "two", secret) {
		t.Fatal("two base paths derived the same capability")
	}
}
