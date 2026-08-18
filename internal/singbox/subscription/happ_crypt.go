package subscription

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

var (
	// ErrHappKeysNotConfigured indicates that user has not supplied RSA decryption keys.
	ErrHappKeysNotConfigured = errors.New("happ: RSA decryption keys not configured")

	happKeysMu     sync.RWMutex
	happParsedKeys []*rsa.PrivateKey
)

func cleanB64(s string) string {
	s = strings.Trim(s, `"',`)
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
}

// ParseHappKeysInput extracts PKCS1 Base64 RSA private keys from various formats (JSON array, PEM blocks, or Base64 lines).
func ParseHappKeysInput(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, errors.New("empty keys input")
	}

	// 1. Try JSON array of strings
	var jsonArr []string
	if err := json.Unmarshal([]byte(input), &jsonArr); err == nil && len(jsonArr) > 0 {
		var valid []string
		for _, k := range jsonArr {
			k = cleanB64(k)
			if k != "" {
				valid = append(valid, k)
			}
		}
		if len(valid) > 0 {
			return valid, nil
		}
	}

	// 2. Try PEM blocks
	var keys []string
	rest := []byte(input)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "RSA PRIVATE KEY" {
			keys = append(keys, base64.StdEncoding.EncodeToString(block.Bytes))
		} else if block.Type == "PRIVATE KEY" {
			pk, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err == nil {
				if rsaKey, ok := pk.(*rsa.PrivateKey); ok {
					keys = append(keys, base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(rsaKey)))
				}
			}
		}
	}
	if len(keys) > 0 {
		return keys, nil
	}

	// 3. Smart Base64 RSA tokenizer
	fields := strings.Fields(input)
	var candidates []string
	var current strings.Builder
	for _, f := range fields {
		f = strings.Trim(f, `"',[]`)
		if len(f) == 0 {
			continue
		}
		if strings.HasPrefix(f, "MII") && current.Len() > 500 {
			candidates = append(candidates, current.String())
			current.Reset()
		}
		current.WriteString(f)
	}
	if current.Len() > 0 {
		candidates = append(candidates, current.String())
	}

	for _, cand := range candidates {
		cleaned := cleanB64(cand)
		if len(cleaned) > 64 {
			if der, err := b64DecodeUrlSafe(cleaned); err == nil {
				if _, err1 := x509.ParsePKCS1PrivateKey(der); err1 == nil {
					keys = append(keys, cleaned)
					continue
				}
				if _, err2 := x509.ParsePKCS8PrivateKey(der); err2 == nil {
					keys = append(keys, cleaned)
					continue
				}
			}
		}
	}

	if len(keys) == 0 {
		return nil, errors.New("no valid RSA private keys recognized")
	}
	return keys, nil
}

// SetCustomHappKeys configures PKCS1 Base64 RSA private keys at runtime.
func SetCustomHappKeys(b64Keys []string) error {
	var parsed []*rsa.PrivateKey
	for _, b64 := range b64Keys {
		b64 = strings.TrimSpace(b64)
		if b64 == "" {
			continue
		}
		var der []byte
		var err error
		block, _ := pem.Decode([]byte(b64))
		if block != nil && strings.Contains(block.Type, "PRIVATE KEY") {
			der = block.Bytes
		} else {
			der, err = b64DecodeUrlSafe(cleanB64(b64))
			if err != nil {
				return fmt.Errorf("invalid base64 key: %w", err)
			}
		}

		pk, err := x509.ParsePKCS1PrivateKey(der)
		if err != nil {
			// Try PKCS8 fallback
			p8, err8 := x509.ParsePKCS8PrivateKey(der)
			if err8 == nil {
				if rsaKey, ok := p8.(*rsa.PrivateKey); ok {
					pk = rsaKey
					err = nil
				}
			}
		}
		if err != nil {
			if strings.Contains(err.Error(), "data truncated") || strings.Contains(err.Error(), "too short") {
				return fmt.Errorf("key #%d is corrupted: data truncated", len(parsed)+1)
			}
			return fmt.Errorf("key #%d is not a valid PKCS1/PKCS8 RSA private key: %w", len(parsed)+1, err)
		}
		parsed = append(parsed, pk)
	}

	happKeysMu.Lock()
	happParsedKeys = parsed
	happKeysMu.Unlock()
	return nil
}

// LoadHappKeysFromFile attempts to load RSA keys from a JSON file containing a string array.
func LoadHappKeysFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	return SetCustomHappKeys(keys)
}

// HasHappKeys reports whether any RSA keys are currently configured.
func HasHappKeys() bool {
	happKeysMu.RLock()
	defer happKeysMu.RUnlock()
	return len(happParsedKeys) > 0
}

// GetHappKeysCount returns the number of currently loaded RSA keys.
func GetHappKeysCount() int {
	happKeysMu.RLock()
	defer happKeysMu.RUnlock()
	return len(happParsedKeys)
}

func getHappKeys() ([]*rsa.PrivateKey, error) {
	happKeysMu.RLock()
	defer happKeysMu.RUnlock()
	if len(happParsedKeys) == 0 {
		return nil, ErrHappKeysNotConfigured
	}
	return happParsedKeys, nil
}

func (s *Service) keysPath() string {
	if s.store != nil && s.store.path != "" {
		return filepath.Join(filepath.Dir(s.store.path), "happ_keys.json")
	}
	return "happ_keys.json"
}

// LoadHappKeys reads happ_keys.json from disk if present.
func (s *Service) LoadHappKeys() error {
	path := s.keysPath()
	if _, err := os.Stat(path); err == nil {
		return LoadHappKeysFromFile(path)
	}
	return nil
}

// SaveHappKeys persists custom RSA keys to happ_keys.json and applies them to memory.
func (s *Service) SaveHappKeys(keys []string) error {
	if err := SetCustomHappKeys(keys); err != nil {
		return err
	}
	path := s.keysPath()
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(path, data)
}

// ClearHappKeys removes happ_keys.json and clears keys from memory.
func (s *Service) ClearHappKeys() error {
	happKeysMu.Lock()
	happParsedKeys = nil
	happKeysMu.Unlock()
	path := s.keysPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

	if len(payload) > 16384 {
		return "", errors.New("encrypted payload too large")
	}

	keys, err := getHappKeys()
	if err != nil {
		return "", err
	}
	if ordinal >= len(keys) {
		return "", fmt.Errorf("happ: key #%d for %s not configured", ordinal+1, path[:strings.Index(path, "/")+1])
	}

	privKey := keys[ordinal]
	if privKey == nil {
		return "", fmt.Errorf("happ: key #%d is nil", ordinal+1)
	}
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
