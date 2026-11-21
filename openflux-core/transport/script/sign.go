package script

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
)

// VerifyScript checks that sig is a valid ed25519 signature of src under
// pubKey. Every script transport must pass this before its bytes are ever
// handed to goja - there is no "unsigned but trusted" path. Freedom inside
// the runtime (unrestricted host API) is traded for a hard gate at the
// door: if it isn't signed by a key the exit/node trusts, it never loads.
func VerifyScript(src, sig []byte, pubKey ed25519.PublicKey) error {
	if len(pubKey) != ed25519.PublicKeySize {
		return fmt.Errorf("script: invalid public key length %d (want %d)", len(pubKey), ed25519.PublicKeySize)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("script: invalid signature length %d (want %d)", len(sig), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pubKey, src, sig) {
		return fmt.Errorf("script: signature verification failed")
	}
	return nil
}

// LoadSigned reads a script file and its detached signature (path + ".sig")
// and verifies it against pubKey before returning the source bytes.
func LoadSigned(path string, pubKey ed25519.PublicKey) ([]byte, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("script: read %s: %w", path, err)
	}
	sig, err := os.ReadFile(path + ".sig")
	if err != nil {
		return nil, fmt.Errorf("script: read signature %s.sig: %w", path, err)
	}
	if err := VerifyScript(src, sig, pubKey); err != nil {
		return nil, fmt.Errorf("script: %s: %w", path, err)
	}
	return src, nil
}

// DecodePublicKeyHex decodes a hex-encoded ed25519 public key, as passed
// via a transport config's "pubkey" param.
func DecodePublicKeyHex(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("script: bad pubkey hex: %w", err)
	}
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("script: pubkey must be %d bytes, got %d", ed25519.PublicKeySize, len(b))
	}
	return ed25519.PublicKey(b), nil
}
