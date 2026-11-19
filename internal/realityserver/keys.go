// Package realityserver runs an in-process VLESS + REALITY inbound.
//
// REALITY lets the router accept direct TLS 1.3 connections on its public
// (white) IP without owning a certificate: authenticated clients get a VLESS
// stream, everyone else (active probes, scanners, browsers) is transparently
// proxied to a real third-party "dest" site and sees its genuine certificate.
//
// Two transports are supported on top of REALITY:
//   - "tcp"   — raw VLESS stream (fastest, lowest CPU, recommended);
//   - "xhttp" — VLESS over XHTTP (stream-one over HTTP/2), reusing the built-in
//     xhttpserver handler; slightly slower but looks like regular HTTP/2.
//
// The VLESS handling (client allowlist, egress, UDP, mux) is delegated to
// xhttpserver.Server so the REALITY entry behaves exactly like the CDN entry.
package realityserver

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the X25519 key length in bytes.
const KeySize = 32

var (
	ErrInvalidPrivateKey = errors.New("realityserver: invalid X25519 private key")
	ErrInvalidShortID    = errors.New("realityserver: invalid short id (expected 0-16 hex chars, even length)")
)

// EncodeKey encodes a raw 32-byte key in the Xray/REALITY format (base64 raw URL).
func EncodeKey(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeKey accepts the Xray format (base64 raw URL) and tolerates padded or
// standard base64 variants.
func DecodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrInvalidPrivateKey
	}
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return b, nil
		}
	}
	return nil, ErrInvalidPrivateKey
}

// GenerateKeyPair returns a fresh X25519 key pair encoded for Xray clients.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("realityserver: generate x25519 key: %w", err)
	}
	return EncodeKey(priv.Bytes()), EncodeKey(priv.PublicKey().Bytes()), nil
}

// PublicKeyFromPrivate derives the client-facing public key (pbk) from the private key.
func PublicKeyFromPrivate(privateKey string) (string, error) {
	raw, err := DecodeKey(privateKey)
	if err != nil {
		return "", err
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", ErrInvalidPrivateKey
	}
	return EncodeKey(priv.PublicKey().Bytes()), nil
}

// GenerateShortID returns a random 8-byte (16 hex chars) short id.
func GenerateShortID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ParseShortID converts a hex short id into the fixed 8-byte form used by REALITY
// (left-aligned, zero padded). Empty string is a valid (all-zero) short id.
func ParseShortID(s string) ([8]byte, error) {
	var out [8]byte
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > 16 || len(s)%2 != 0 {
		return out, ErrInvalidShortID
	}
	if s == "" {
		return out, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, ErrInvalidShortID
	}
	copy(out[:], b)
	return out, nil
}
