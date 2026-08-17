package subscription

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
)

var (
	happParsedKeysOnce sync.Once
	happParsedKeys     []*rsa.PrivateKey
)

func getHappKeys() ([]*rsa.PrivateKey, error) {
	var err error
	happParsedKeysOnce.Do(func() {
		for _, b64 := range happPKCS1KeysB64 {
			der, e := base64.StdEncoding.DecodeString(b64)
			if e != nil {
				err = e
				return
			}
			pk, e := x509.ParsePKCS1PrivateKey(der)
			if e != nil {
				err = e
				return
			}
			happParsedKeys = append(happParsedKeys, pk)
		}
	})
	return happParsedKeys, err
}

func b64DecodeUrlSafe(s string) ([]byte, error) {
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if rem := len(s) % 4; rem != 0 {
		s += strings.Repeat("=", 4-rem)
	}
	return base64.StdEncoding.DecodeString(s)
}

// IsHappCryptLink returns true if link matches happ://crypt... scheme
func IsHappCryptLink(rawLink string) bool {
	trimmed := strings.TrimSpace(rawLink)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "happ://crypt") ||
		strings.HasPrefix(lower, "crypt/") ||
		strings.HasPrefix(lower, "crypt2/") ||
		strings.HasPrefix(lower, "crypt3/") ||
		strings.HasPrefix(lower, "crypt4/")
}

// DecryptHappLink decrypts encrypted happ:// links (happ://crypt/, happ://crypt2/, happ://crypt3/, happ://crypt4/)
func DecryptHappLink(rawLink string) (string, error) {
	trimmed := strings.TrimSpace(rawLink)
	path := trimmed
	if strings.HasPrefix(strings.ToLower(trimmed), "happ://") {
		path = trimmed[7:]
	}

	var ordinal int
	var payload string
	switch {
	case strings.HasPrefix(path, "crypt4/"):
		ordinal = 3
		payload = path[7:]
	case strings.HasPrefix(path, "crypt3/"):
		ordinal = 2
		payload = path[7:]
	case strings.HasPrefix(path, "crypt2/"):
		ordinal = 1
		payload = path[7:]
	case strings.HasPrefix(path, "crypt/"):
		ordinal = 0
		payload = path[6:]
	default:
		return "", errors.New("not an encrypted happ link")
	}

	keys, err := getHappKeys()
	if err != nil || ordinal >= len(keys) {
		return "", errors.New("failed to load happ decryption keys")
	}

	privKey := keys[ordinal]
	keySize := (privKey.N.BitLen() + 7) / 8

	cipherBytes, err := b64DecodeUrlSafe(payload)
	if err != nil {
		return "", err
	}

	var plaintext []byte
	for i := 0; i < len(cipherBytes); i += keySize {
		end := i + keySize
		if end > len(cipherBytes) {
			end = len(cipherBytes)
		}
		chunk := cipherBytes[i:end]
		dec, err := rsa.DecryptPKCS1v15(rand.Reader, privKey, chunk)
		if err != nil {
			return "", err
		}
		plaintext = append(plaintext, dec...)
	}

	return strings.TrimSpace(string(plaintext)), nil
}
