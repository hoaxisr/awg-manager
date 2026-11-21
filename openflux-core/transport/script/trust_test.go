package script

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestInspectTrustValidSignatureAndOfficialKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)
	src := []byte(echoScript)
	sig := ed25519.Sign(priv, src)

	r := InspectTrust(src, sig, pubHex, pubHex)
	if !r.OK {
		t.Fatalf("ok=false, error=%q", r.Error)
	}
	if r.Signature != "valid" {
		t.Errorf("signature = %q, want valid", r.Signature)
	}
	if !r.Official {
		t.Error("official = false, want true (pubkey matches the official key)")
	}
	if r.Name != "echo" {
		t.Errorf("name = %q, want echo", r.Name)
	}
	if r.Fingerprint != Fingerprint(pubHex) {
		t.Errorf("fingerprint = %q, want %q", r.Fingerprint, Fingerprint(pubHex))
	}
}

func TestInspectTrustBadSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	src := []byte(echoScript)
	wrongSig := ed25519.Sign(otherPriv, src)

	r := InspectTrust(src, wrongSig, hex.EncodeToString(pub), "")
	if !r.OK {
		t.Fatalf("ok=false, error=%q (info is still readable even with a bad signature)", r.Error)
	}
	if r.Signature != "invalid" {
		t.Errorf("signature = %q, want invalid", r.Signature)
	}
	if r.Official {
		t.Error("official = true, want false (no official key supplied)")
	}
}

func TestInspectTrustBadPubkeyHex(t *testing.T) {
	r := InspectTrust([]byte(echoScript), nil, "not-hex", "")
	if r.OK {
		t.Fatal("ok=true, want false for an undecodable pubkey")
	}
	if r.Error == "" {
		t.Error("error is empty, want a reason")
	}
}

func TestFingerprintRoundTrip(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubHex := hex.EncodeToString(pub)
	if got := Fingerprint(pubHex); got == "" {
		t.Error("Fingerprint returned empty for a valid key")
	}
	if got := Fingerprint("nope"); got != "" {
		t.Errorf("Fingerprint(%q) = %q, want empty", "nope", got)
	}
}
