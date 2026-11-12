package warpscout

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const localKeyLen = 32

// LocalCipher шифрует секреты учётной записи WARP локальным ключом установки
// (<dataDir>/.warpscout-key, AES-256-GCM, 0600). На чтении ключ не создаётся,
// чтобы восстановленный с другого устройства бэкап честно отмечался как
// AccountUnreadable до явной перерегистрации.
type LocalCipher struct {
	path string
}

// NewLocalCipher возвращает шифр, привязанный к каталогу данных демона.
func NewLocalCipher(dataDir string) *LocalCipher {
	return &LocalCipher{path: filepath.Join(dataDir, storage.WarpScoutKeyFile)}
}

func (c *LocalCipher) Encrypt(plaintext string) (string, error) {
	key, err := c.keyForWrite()
	if err != nil {
		return "", err
	}
	aead, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("warpscout cipher: nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *LocalCipher) Decrypt(token string) (string, error) {
	key, err := os.ReadFile(c.path)
	if err != nil {
		return "", fmt.Errorf("warpscout cipher: key: %w", err)
	}
	if len(key) != localKeyLen {
		return "", errors.New("warpscout cipher: invalid key length")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", fmt.Errorf("warpscout cipher: base64: %w", err)
	}
	aead, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("warpscout cipher: short ciphertext")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("warpscout cipher: decrypt: %w", err)
	}
	return string(plain), nil
}

func (c *LocalCipher) keyForWrite() ([]byte, error) {
	if key, err := os.ReadFile(c.path); err == nil && len(key) == localKeyLen {
		return key, nil
	} else if err == nil && len(key) != localKeyLen {
		storage.QuarantineCorrupt(c.path, errors.New("bad key length"))
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return nil, fmt.Errorf("warpscout cipher: mkdir: %w", err)
	}
	key := make([]byte, localKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("warpscout cipher: keygen: %w", err)
	}
	// O_EXCL защищает от гонки двух одновременных записей при первом заведении.
	f, err := os.OpenFile(c.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if existing, rerr := os.ReadFile(c.path); rerr == nil && len(existing) == localKeyLen {
				return existing, nil
			}
		}
		if werr := storage.AtomicWritePerm(c.path, key, 0o600); werr != nil {
			return nil, fmt.Errorf("warpscout cipher: write key: %w", werr)
		}
		return key, nil
	}
	_, werr := f.Write(key)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(c.path)
		return nil, werr
	}
	if cerr != nil {
		_ = os.Remove(c.path)
		return nil, cerr
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
