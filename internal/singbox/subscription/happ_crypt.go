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

// ErrHappKeysNotConfigured indicates that user has not supplied RSA decryption keys.
var ErrHappKeysNotConfigured = errors.New("happ: RSA decryption keys not configured")

// happKeys owns the RSA keys used to decrypt happ://crypt… links: the set is
// per-Service state (one Service = one data dir = one happ_keys.json), not a
// package global.
type happKeys struct {
	mu   sync.RWMutex
	path string
	keys []*rsa.PrivateKey
}

func newHappKeys(path string) *happKeys { return &happKeys{path: path} }

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

// set parses PKCS1/PKCS8 Base64 (or PEM) RSA private keys and installs them.
func (k *happKeys) set(b64Keys []string) error {
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

	k.mu.Lock()
	k.keys = parsed
	k.mu.Unlock()
	return nil
}

// load reads the key file if it exists; a missing file is not an error.
func (k *happKeys) load() error {
	data, err := os.ReadFile(k.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	return k.set(keys)
}

// save installs the keys and persists them next to the subscriptions file.
func (k *happKeys) save(b64Keys []string) error {
	if err := k.set(b64Keys); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b64Keys, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(k.path, data)
}

// clear drops the keys from disk and then from memory.
func (k *happKeys) clear() error {
	// Disk first: clearing memory before a failed os.Remove would report an
	// error while the keys are already unloaded, and the next start would
	// silently load them back from the file that is still there.
	if err := os.Remove(k.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	k.mu.Lock()
	k.keys = nil
	k.mu.Unlock()
	return nil
}

// status reports whether keys are configured and how many.
func (k *happKeys) status() (bool, int) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return len(k.keys) > 0, len(k.keys)
}

func (k *happKeys) loaded() ([]*rsa.PrivateKey, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if len(k.keys) == 0 {
		return nil, ErrHappKeysNotConfigured
	}
	return k.keys, nil
}

// happKeysPath returns the key file path for a store path ("" → cwd).
func happKeysPath(storePath string) string {
	if storePath == "" {
		return "happ_keys.json"
	}
	return filepath.Join(filepath.Dir(storePath), "happ_keys.json")
}

// LoadHappKeys reads happ_keys.json from disk if present.
func (s *Service) LoadHappKeys() error { return s.happKeys.load() }

// SaveHappKeys persists custom RSA keys and applies them to memory.
func (s *Service) SaveHappKeys(keys []string) error { return s.happKeys.save(keys) }

// ClearHappKeys removes the stored RSA keys.
func (s *Service) ClearHappKeys() error { return s.happKeys.clear() }

// HappKeysStatus reports whether decryption keys are configured and how many.
func (s *Service) HappKeysStatus() (bool, int) { return s.happKeys.status() }

// DecryptHappLink decrypts a happ://crypt… link with this service's keys.
func (s *Service) DecryptHappLink(rawLink string) (string, error) {
	return s.happKeys.decrypt(rawLink)
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

// decrypt turns happ://crypt…/<payload> back into the plain subscription URL.
func (k *happKeys) decrypt(rawLink string) (string, error) {
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

	keys, err := k.loaded()
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
