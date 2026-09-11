package storage

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// Отдельные экземпляры — воспроизведение перезапуска после порчи файла.
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

// longTestSubscriptionKey — фикстура размером с настоящую ссылку Amnezia
// (сотни байт base64), но заведомо ненастоящая: репозиторий публичный.
// Содержимое детерминированное и почти без повторов, чтобы утечка ЛЮБОГО
// куска открытого текста, а не только его начала, была видна в шифротексте.
func longTestSubscriptionKey() string {
	var b strings.Builder
	b.WriteString("vpn://test-key-")
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&b, "%08x", uint32(i)*2654435761)
	}
	return b.String()
}

// Т1. Существующий секрет переиспользуется. Без этой проверки мутация
// «всегда генерировать новый секрет» проходит зелёной — то есть первое же
// шифрование после перезапуска панели хоронило бы сохранённый ключ подписки.
func TestDeviceCipher_ReusesExistingKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, DeviceKeyFile)

	first, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("чтение секрета: %v", err)
	}

	// Холодный экземпляр на том же каталоге — это перезапуск панели.
	cold := NewDeviceCipher(dir)
	second, err := cold.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt холодным экземпляром: %v", err)
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("чтение секрета: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("секрет переписан вторым экземпляром")
	}
	for name, token := range map[string]string{"первый": first, "второй": second} {
		got, err := cold.Decrypt(token)
		if err != nil || got != testSubscriptionKey {
			t.Fatalf("%s шифротекст не читается: (%q, %v)", name, got, err)
		}
	}
}

// Т2. Нечитаемый секрет — отказ закрытый. Каталог вместо файла даёт EISDIR,
// который root не обходит; проверять то же самое через chmod 000 нельзя —
// на роутере панель работает root'ом, и тест был бы зелёным по неверной
// причине.
func TestDeviceCipher_UnreadableKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, DeviceKeyFile)
	if err := os.Mkdir(keyPath, 0o700); err != nil {
		t.Fatal(err)
	}

	token, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	if err == nil {
		t.Fatalf("Encrypt на нечитаемом секрете = (%q, nil), want ошибку", token)
	}
	if errors.Is(err, ErrDeviceKeyMissing) {
		t.Fatalf("нечитаемый секрет выдан за отсутствующий: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("путь секрета подменён: (%v, %v)", info, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("в каталоге данных %d записей, want 1 (ничего не создано)", len(entries))
	}
}

// Т3. Экземпляры на одном dataDir сходятся к одному секрету: победителя
// выбирает O_EXCL, проигравшие берут его файл. Мьютекс этого не давал —
// он поле экземпляра и между процессами не работает вовсе.
func TestDeviceCipher_ConcurrentInstancesShareKey(t *testing.T) {
	dir := t.TempDir()
	const n = 8

	tokens := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
		}(i)
	}
	close(start)
	wg.Wait()

	reader := NewDeviceCipher(dir)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Encrypt #%d: %v", i, err)
		}
		got, err := reader.Decrypt(tokens[i])
		if err != nil || got != testSubscriptionKey {
			t.Fatalf("шифротекст #%d не читается общим секретом: (%q, %v)", i, got, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != DeviceKeyFile {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("в каталоге данных %v, want только %s", names, DeviceKeyFile)
	}
}

// Т4. Длина токена связана с длиной открытого текста. Литералы 12 (nonce
// GCM) и 16 (тег) намеренно не берутся из реализации: иначе проверка
// поехала бы вместе с мутацией. Ловит и «в токен положен сам секрет
// устройства», и «часть открытого текста ушла в токен как есть».
func TestDeviceCipher_TokenLengthTracksPlaintext(t *testing.T) {
	c := NewDeviceCipher(t.TempDir())
	for _, plain := range []string{"", "x", testSubscriptionKey, longTestSubscriptionKey()} {
		token, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("Encrypt(%d байт): %v", len(plain), err)
		}
		raw, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("не base64: %v", err)
		}
		if want := len(plain) + 12 + 16; len(raw) != want {
			t.Fatalf("токен %d байт при открытом тексте %d, want %d", len(raw), len(plain), want)
		}
	}
}

// Т5. Ни один кусок открытого текста не виден в шифротексте. Проверка
// срезами, а не двумя литералами: литералы помещаются в первые 16 байт, и
// утечка хвоста длинной ссылки им невидима.
func TestDeviceCipher_NoPlaintextSliceLeaks(t *testing.T) {
	plain := longTestSubscriptionKey()
	if len(plain) < 256 {
		t.Fatalf("фикстура %d байт, нужна не меньше 256", len(plain))
	}
	c := NewDeviceCipher(t.TempDir())
	token, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("не base64: %v", err)
	}
	const window = 8
	for i := 0; i+window <= len(plain); i++ {
		if bytes.Contains(raw, []byte(plain[i:i+window])) {
			t.Fatalf("срез открытого текста с позиции %d виден в шифротексте", i)
		}
	}
	if got, err := c.Decrypt(token); err != nil || got != plain {
		t.Fatalf("длинное значение не читается обратно: (%d байт, %v)", len(got), err)
	}
}

// Т6. Длина секрета закреплена литералом 32 (AES-256). Сравнение с самой
// константой реализации не поймало бы понижение deviceKeyLen до 16.
func TestDeviceCipher_KeyLengthIs32(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, DeviceKeyFile))
	if err != nil {
		t.Fatalf("чтение секрета: %v", err)
	}
	if len(raw) != 32 {
		t.Fatalf("секрет %d байт, want 32 (AES-256)", len(raw))
	}
}

// Т7. Секрет случаен: секреты разных установок не совпадают. Ловит замену
// crypto/rand на генератор с фиксированным seed — предсказуемый секрет
// устройства равносилен его отсутствию.
func TestDeviceCipher_KeysAreUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 16; i++ {
		dir := t.TempDir()
		if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
			t.Fatalf("Encrypt #%d: %v", i, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, DeviceKeyFile))
		if err != nil {
			t.Fatalf("чтение секрета #%d: %v", i, err)
		}
		if seen[string(raw)] {
			t.Fatalf("секрет установки #%d повторяет уже выданный", i)
		}
		seen[string(raw)] = true
	}
}

// Непригодный по длине секрет уносится в карантин, а не уничтожается:
// лишний байт чаще всего — дописанный \n, и первые 32 байта тогда ещё
// настоящий секрет.
func TestDeviceCipher_BadLengthKeyQuarantined(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, DeviceKeyFile)
	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	original, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	// Кто-то посмотрел файл редактором, и тот дописал перевод строки.
	if err := os.WriteFile(keyPath, append(append([]byte(nil), original...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt на негодном секрете: %v", err)
	}

	saved, err := os.ReadFile(keyPath + ".corrupt")
	if err != nil {
		t.Fatalf("негодный секрет не сохранён в карантине: %v", err)
	}
	if !bytes.Equal(saved, append(append([]byte(nil), original...), '\n')) {
		t.Fatalf("в карантине не тот файл")
	}
	fresh, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("новый секрет не заведён: %v", err)
	}
	if len(fresh) != 32 || bytes.Equal(fresh, original) {
		t.Fatalf("новый секрет негоден: %d байт, совпадает со старым: %v", len(fresh), bytes.Equal(fresh, original))
	}
}
