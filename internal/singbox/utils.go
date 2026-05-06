package singbox

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// GenerateUUID generates a random UUID v4 string.
func GenerateUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// GeneratePassword generates a random alphanumeric password of given length.
func GeneratePassword(length int) (string, error) {
	if length <= 0 {
		return "", fmt.Errorf("invalid password length: %d", length)
	}
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b), nil
}

// GenerateRealityPrivateKey generates a random X25519 private key for Reality.
func GenerateRealityPrivateKey() (string, error) {
	curve := ecdh.X25519()
	privKey, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	// sing-box reality expects URL-safe base64 key encoding without padding.
	return base64.RawURLEncoding.EncodeToString(privKey.Bytes()), nil
}

// GenerateRealityShortID generates a random 8-byte hex string for Reality short ID.
func GenerateRealityShortID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func protocolToTagPrefix(protocol string) string {
	switch strings.ToLower(protocol) {
	case "vless":
		return "srv-vless"
	case "hysteria2":
		return "srv-hy2"
	case "naive":
		return "srv-naive"
	default:
		return ""
	}
}
