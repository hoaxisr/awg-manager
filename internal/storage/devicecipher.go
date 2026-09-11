package storage

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
	"sync"
)

// DeviceKeyFile — имя файла секрета устройства в dataDir. Экспортировано
// ради internal/backup: секрет привязан к установке и в архив попадать не
// должен, а совпадение имени по литералу в двух пакетах развалится молча.
const DeviceKeyFile = ".device-key"

const (
	deviceKeyLen  = 32 // AES-256
	deviceKeyPerm = 0o600
)

// ErrDeviceKeyMissing — секрета устройства нет или он непригоден (файл
// отсутствует либо обрезан). Отличается от ErrDeviceCiphertext, потому что
// причины разные: секрет ещё можно достать из отложенного каталога, а
// нерасшифровываемый шифротекст — уже нет.
var ErrDeviceKeyMissing = errors.New("device key missing")

// ErrDeviceCiphertext — значение не расшифровывается этим секретом (чужой
// секрет, порча, не base64). Вызывающий по нему отвечает «ключ непригоден»,
// а не «ключа нет», и сам ключ при этом НЕ стирает.
var ErrDeviceCiphertext = errors.New("device ciphertext undecryptable")

// DeviceCipher шифрует секреты аккаунта (ключ подписки Amnezia) секретом,
// привязанным к установке: <dataDir>/.device-key, 32 случайных байта, 0600.
// Отдельный тип, а не методы SettingsStore, — чтобы шифрование можно было
// проверять и использовать, не поднимая настройки.
type DeviceCipher struct {
	dataDir string
	// mu сериализует чтение файла и генерацию секрета: без неё две
	// параллельные первые записи сгенерировали бы разные секреты, и та,
	// что записалась второй, убила бы шифротекст первой.
	mu sync.Mutex
	// key — кэш на время жизни процесса. Внешнюю подмену файла процесс
	// намеренно не замечает: в проде файл правим только мы.
	key []byte
}

// NewDeviceCipher creates a cipher rooted at dataDir.
func NewDeviceCipher(dataDir string) *DeviceCipher {
	return &DeviceCipher{dataDir: dataDir}
}

func (c *DeviceCipher) path() string { return filepath.Join(c.dataDir, DeviceKeyFile) }

// Encrypt returns base64(nonce‖ciphertext). Отсутствующий или обрезанный
// секрет перегенерируется: старый шифротекст всё равно уже мёртв.
func (c *DeviceCipher) Encrypt(plaintext string) (string, error) {
	key, err := c.keyForWrite()
	if err != nil {
		return "", err
	}
	aead, err := newDeviceAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("device cipher: nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt разбирает base64(nonce‖ciphertext). Путь чтения секрет НЕ создаёт:
// иначе первая же расшифровка после потери файла завела бы новый секрет и
// навсегда похоронила шифротекст, который ещё мог вернуться из бэкапа.
func (c *DeviceCipher) Decrypt(token string) (string, error) {
	key, err := c.keyForRead()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return "", fmt.Errorf("%w: не base64: %v", ErrDeviceCiphertext, err)
	}
	aead, err := newDeviceAEAD(key)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("%w: короче nonce (%d байт)", ErrDeviceCiphertext, len(raw))
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDeviceCiphertext, err)
	}
	return string(plain), nil
}

func newDeviceAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("device cipher: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("device cipher: gcm: %w", err)
	}
	return aead, nil
}

// keyForRead отдаёт секрет, ничего не записывая на диск.
func (c *DeviceCipher) keyForRead() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil {
		return c.key, nil
	}
	raw, err := c.readKeyFile()
	if err != nil {
		return nil, err
	}
	c.key = raw
	return raw, nil
}

// keyForWrite отдаёт секрет, заводя его при отсутствии или непригодности.
func (c *DeviceCipher) keyForWrite() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key != nil {
		return c.key, nil
	}
	raw, err := c.readKeyFile()
	switch {
	case err == nil:
		c.key = raw
		return raw, nil
	case !errors.Is(err, ErrDeviceKeyMissing):
		// Файл есть, но прочитать его не вышло (EIO, права). Отказ
		// закрытый: перезаписать секрет здесь — гарантированно потерять
		// то, что ещё читается после починки железа.
		return nil, err
	}

	fresh := make([]byte, deviceKeyLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("device key: rand: %w", err)
	}
	if err := AtomicWritePerm(c.path(), fresh, deviceKeyPerm); err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	c.key = fresh
	return fresh, nil
}

// readKeyFile отдаёт ErrDeviceKeyMissing и на отсутствующий, и на обрезанный
// файл: и то и другое означает, что пригодного секрета нет.
func (c *DeviceCipher) readKeyFile() ([]byte, error) {
	raw, err := os.ReadFile(c.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrDeviceKeyMissing
		}
		return nil, fmt.Errorf("device key: %w", err)
	}
	if len(raw) != deviceKeyLen {
		return nil, fmt.Errorf("%w: %d байт вместо %d", ErrDeviceKeyMissing, len(raw), deviceKeyLen)
	}
	return raw, nil
}
