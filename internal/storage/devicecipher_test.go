package storage

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ключ подписки в фикстурах — заведомо ненастоящий: репозиторий публичный.
const testSubscriptionKey = "vpn://test-key-0123456789abcdef"

func TestDeviceCipher_RoundTrip(t *testing.T) {
	c := NewDeviceCipher(t.TempDir())

	token, err := c.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if strings.Contains(token, "vpn://") || strings.Contains(token, "test-key") {
		t.Fatalf("шифротекст содержит исходное значение: %q", token)
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("не base64: %v", err)
	}
	if strings.Contains(string(raw), "vpn://") {
		t.Fatalf("исходное значение видно в байтах шифротекста")
	}

	got, err := c.Decrypt(token)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if got != testSubscriptionKey {
		t.Fatalf("Decrypt = %q, want %q", got, testSubscriptionKey)
	}
}

// Nonce обязан быть случайным: одинаковые шифротексты одного значения
// означали бы фиксированный nonce, а это повторное использование пары
// (ключ, nonce) в GCM.
func TestDeviceCipher_NonceIsRandom(t *testing.T) {
	c := NewDeviceCipher(t.TempDir())

	first, err := c.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	second, err := c.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if first == second {
		t.Fatalf("два шифрования одного значения совпали: %q", first)
	}
}

func TestDeviceCipher_ForeignKeyDoesNotDecrypt(t *testing.T) {
	mine := NewDeviceCipher(t.TempDir())
	token, err := mine.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Другой dataDir — другой секрет, как на чужом роутере.
	foreign := NewDeviceCipher(t.TempDir())
	if _, err := foreign.Encrypt("что-нибудь"); err != nil {
		t.Fatalf("Encrypt чужим: %v", err)
	}
	got, err := foreign.Decrypt(token)
	if !errors.Is(err, ErrDeviceCiphertext) {
		t.Fatalf("Decrypt чужим = (%q, %v), want ErrDeviceCiphertext", got, err)
	}
}

func TestDeviceCipher_RejectsDamagedCiphertext(t *testing.T) {
	c := NewDeviceCipher(t.TempDir())
	token, err := c.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("не base64: %v", err)
	}

	damaged := append([]byte(nil), raw...)
	damaged[len(damaged)-1] ^= 0x01 // последний байт — часть тега GCM

	cases := map[string]string{
		"порченый тег":  base64.StdEncoding.EncodeToString(damaged),
		"не base64":     "не-base64-!!!",
		"короче nonce":  base64.StdEncoding.EncodeToString(raw[:4]),
		"пустая строка": "",
		"только nonce":  base64.StdEncoding.EncodeToString(raw[:12]),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := c.Decrypt(token)
			if !errors.Is(err, ErrDeviceCiphertext) {
				t.Fatalf("Decrypt = (%q, %v), want ErrDeviceCiphertext", got, err)
			}
		})
	}
}

func TestDeviceCipher_KeyFilePermissions(t *testing.T) {
	dir := t.TempDir()
	c := NewDeviceCipher(dir)
	if _, err := c.Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, DeviceKeyFile))
	if err != nil {
		t.Fatalf("Stat секрета: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права секрета %o, want 600", perm)
	}
	if info.Size() != deviceKeyLen {
		t.Fatalf("размер секрета %d, want %d", info.Size(), deviceKeyLen)
	}
}

// Путь чтения секрет не создаёт: иначе первая же расшифровка после потери
// файла завела бы новый секрет и похоронила шифротекст, который ещё мог
// вернуться из бэкапа.
func TestDeviceCipher_DecryptDoesNotCreateKey(t *testing.T) {
	dir := t.TempDir()
	c := NewDeviceCipher(dir)

	got, err := c.Decrypt("не важно что")
	if !errors.Is(err, ErrDeviceKeyMissing) {
		t.Fatalf("Decrypt без секрета = (%q, %v), want ErrDeviceKeyMissing", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, DeviceKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("путь чтения создал файл секрета: %v", err)
	}
}

// Обрезанный секрет — ОСОЗНАННАЯ потеря ключа подписки: расшифровать старый
// шифротекст нечем, поэтому шифрование заводит новый секрет и работает
// дальше, а прежнее значение объявляется непригодным (usable:false), но не
// стирается.
func TestDeviceCipher_TruncatedKey(t *testing.T) {
	dir := t.TempDir()
	token, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	keyPath := filepath.Join(dir, DeviceKeyFile)
	if err := os.WriteFile(keyPath, []byte("короткий хвост"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Отдельные экземпляры: кэш секрета живёт до конца процесса, а здесь
	// воспроизводится перезапуск после порчи файла.
	if got, err := NewDeviceCipher(dir).Decrypt(token); !errors.Is(err, ErrDeviceKeyMissing) {
		t.Fatalf("Decrypt на обрезанном секрете = (%q, %v), want ErrDeviceKeyMissing", got, err)
	}

	after := NewDeviceCipher(dir)
	fresh, err := after.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt на обрезанном секрете: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("Stat секрета: %v", err)
	}
	if info.Size() != deviceKeyLen {
		t.Fatalf("секрет не перегенерирован: размер %d", info.Size())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("права перегенерированного секрета %o, want 600", info.Mode().Perm())
	}
	if got, err := after.Decrypt(fresh); err != nil || got != testSubscriptionKey {
		t.Fatalf("новый шифротекст не читается: (%q, %v)", got, err)
	}
	if got, err := after.Decrypt(token); !errors.Is(err, ErrDeviceCiphertext) {
		t.Fatalf("старый шифротекст = (%q, %v), want ErrDeviceCiphertext", got, err)
	}
}
