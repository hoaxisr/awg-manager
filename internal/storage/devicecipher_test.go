package storage

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	if info.Size() != DeviceKeyLen {
		t.Fatalf("размер секрета %d, want %d", info.Size(), DeviceKeyLen)
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
	if info.Size() != DeviceKeyLen {
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
// константой реализации не поймало бы понижение DeviceKeyLen до 16.
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

	SetNoticeSink(func(Notice) {})
	var notices []Notice
	SetNoticeSink(func(n Notice) { notices = append(notices, n) })
	t.Cleanup(func() { SetNoticeSink(nil) })

	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt на негодном секрете: %v", err)
	}

	copies := quarantineCopies(t, dir)
	if len(copies) != 1 {
		t.Fatalf("карантинных копий %d, want 1", len(copies))
	}
	var savedName string
	for name, content := range copies {
		savedName = name
		if !bytes.Equal(content, append(append([]byte(nil), original...), '\n')) {
			t.Fatalf("в карантине не тот файл")
		}
	}

	// Человек узнаёт о потере из журнала, и текст — про ключ подписки, а не
	// про настройки: сообщение QuarantineCorrupt тут звучало бы паникой и
	// звало бы «создать настройки заново».
	if len(notices) != 1 {
		t.Fatalf("уведомлений %d, want 1: %+v", len(notices), notices)
	}
	if notices[0].Target != DeviceKeyFile || !strings.Contains(notices[0].Message, savedName) {
		t.Fatalf("уведомление не называет копию %s: %+v", savedName, notices[0])
	}
	if strings.Contains(notices[0].Message, "Настройки") {
		t.Fatalf("уведомление о секрете говорит про настройки: %q", notices[0].Message)
	}
	fresh, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("новый секрет не заведён: %v", err)
	}
	if len(fresh) != 32 || bytes.Equal(fresh, original) {
		t.Fatalf("новый секрет негоден: %d байт, совпадает со старым: %v", len(fresh), bytes.Equal(fresh, original))
	}
}

// quarantineCopies отдаёт карантинные копии секрета по именам. Имена
// уникальны по построению, поэтому перебор каталога, а не фиксированное имя.
func quarantineCopies(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, DeviceKeyFile+".corrupt.*"))
	if err != nil {
		t.Fatal(err)
	}
	copies := make(map[string][]byte, len(matches))
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("чтение карантинной копии %s: %v", filepath.Base(path), err)
		}
		copies[filepath.Base(path)] = raw
	}
	return copies
}

// Т1. Под целевым именем никогда не лежит недописанный секрет.
//
// Проверка не статистическая: inotify фиксирует КАЖДУЮ операцию в каталоге,
// а не состояние в случайно выбранный момент. Запись через целевое имя
// (IN_MODIFY, IN_CLOSE_WRITE на .device-key) означает окно, в котором файл
// уже существует и ещё неполон, — ровно то, из-за чего проигравший гонки
// уносил в карантин недописанный секрет победителя. Имя должно появляться
// только целиком: IN_CREATE от os.Link или IN_MOVED_TO от rename.
//
// Тест линуксовый по построению (inotify); проект собирается и работает
// только на Linux.
func TestDeviceCipher_KeyNameNeverWrittenThrough(t *testing.T) {
	dir := t.TempDir()

	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatalf("inotify_init1: %v", err)
	}
	defer syscall.Close(fd)
	const mask = syscall.IN_CREATE | syscall.IN_MODIFY | syscall.IN_CLOSE_WRITE | syscall.IN_MOVED_TO
	if _, err := syscall.InotifyAddWatch(fd, dir, mask); err != nil {
		t.Fatalf("inotify_add_watch: %v", err)
	}

	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	seen := false
	for _, ev := range readInotify(t, fd) {
		if ev.name != DeviceKeyFile {
			continue
		}
		seen = true
		if ev.mask&(syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE) != 0 {
			t.Fatalf("в %s писали через целевое имя (маска %#x): файл был виден недописанным", DeviceKeyFile, ev.mask)
		}
	}
	if !seen {
		t.Fatalf("inotify не увидел появления %s — проверка не состоялась", DeviceKeyFile)
	}
}

type inotifyRecord struct {
	mask uint32
	name string
}

// readInotify вычитывает очередь событий целиком. События кладутся в очередь
// внутри самой операции над каталогом, поэтому к моменту возврата Encrypt они
// уже там: ждать нечего, и от времени проверка не зависит.
func readInotify(t *testing.T, fd int) []inotifyRecord {
	t.Helper()
	const header = 16 // int32 wd + uint32 mask + uint32 cookie + uint32 len
	var out []inotifyRecord
	buf := make([]byte, 16*1024)
	for {
		n, err := syscall.Read(fd, buf)
		if err == syscall.EAGAIN {
			return out
		}
		if err != nil {
			t.Fatalf("чтение очереди inotify: %v", err)
		}
		for off := 0; off+header <= n; {
			m := binary.NativeEndian.Uint32(buf[off+4:])
			nameLen := int(binary.NativeEndian.Uint32(buf[off+12:]))
			name := string(bytes.SplitN(buf[off+header:off+header+nameLen], []byte{0}, 2)[0])
			out = append(out, inotifyRecord{mask: m, name: name})
			off += header + nameLen
		}
	}
}

// Т4. После нормального заведения секрета в каталоге данных нет ничего,
// кроме самого секрета: временный файл убран при любом исходе. Копия секрета,
// пережившая заведение, — это лишний экземпляр ключа на флеше и лишний файл,
// который однажды прочитают вместо настоящего.
func TestDeviceCipher_NoLeftoverFilesAfterCreate(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt: %v", err)
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

// Т3. Вторая порча не уничтожает первую карантинную копию. Имя копии у
// QuarantineCorrupt фиксированное (<путь>.corrupt), а os.Rename на Linux
// молча затирает цель — для секрета это означало бы потерю первой копии,
// самой ценной: в ней вероятнее всего лежит настоящий секрет.
func TestDeviceCipher_SecondQuarantineKeepsFirst(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, DeviceKeyFile)

	damaged := [][]byte{
		bytes.Repeat([]byte{'a'}, DeviceKeyLen+1),
		bytes.Repeat([]byte{'b'}, DeviceKeyLen+2),
	}
	for i, content := range damaged {
		if err := os.WriteFile(keyPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
			t.Fatalf("Encrypt на негодном секрете #%d: %v", i, err)
		}
	}

	copies := quarantineCopies(t, dir)
	if len(copies) != 2 {
		t.Fatalf("карантинных копий %d, want 2 (вторая порча затёрла первую)", len(copies))
	}
	for i, want := range damaged {
		found := false
		for _, got := range copies {
			if bytes.Equal(got, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("порченый секрет #%d не сохранился в карантине", i)
		}
	}
}

// childDeviceKeyDirEnv включает дочерний процесс теста конкуренции: без
// переменной TestDeviceCipherChildEncrypt ничего не делает.
const childDeviceKeyDirEnv = "AWGM_TEST_DEVICE_KEY_DIR"

// TestDeviceCipherChildEncrypt — тело дочернего процесса, а не проверка.
// Печатает READY, ждёт закрытия stdin (общий старт) и отдаёт шифротекст.
func TestDeviceCipherChildEncrypt(t *testing.T) {
	dir := os.Getenv(childDeviceKeyDirEnv)
	if dir == "" {
		t.Skip("дочерний процесс TestDeviceCipher_SeparateProcessesShareKey")
	}
	fmt.Println("READY")
	gate := make([]byte, 1)
	os.Stdin.Read(gate) // старт по закрытию pipe родителем
	token, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	if err != nil {
		fmt.Println("FAIL", err)
		t.Fatalf("Encrypt: %v", err)
	}
	fmt.Println("TOKEN", token)
}

// Т2. Секрет один и на несколько ПРОЦЕССОВ. Горутины делят адресное
// пространство, а демон и `--cleanup` — нет: между процессами мьютекс не
// работает вовсе, и заведение секрета стережёт только файловая система.
func TestDeviceCipher_SeparateProcessesShareKey(t *testing.T) {
	if testing.Short() {
		t.Skip("запускает дочерние процессы")
	}
	dir := t.TempDir()
	const n = 8

	type child struct {
		cmd   *exec.Cmd
		stdin *os.File
		out   *bufio.Scanner
	}
	children := make([]child, 0, n)
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDeviceCipherChildEncrypt$")
		cmd.Env = append(os.Environ(), childDeviceKeyDirEnv+"="+dir)
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = r
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatalf("запуск дочернего #%d: %v", i, err)
		}
		r.Close()
		children = append(children, child{cmd: cmd, stdin: w, out: bufio.NewScanner(stdout)})
	}

	// Все процессы доходят до READY, и только потом снимается барьер: иначе
	// первый успевает завести секрет до запуска остальных, и гонки нет.
	for i, ch := range children {
		if !scanUntil(ch.out, "READY") {
			t.Fatalf("дочерний #%d не дошёл до старта", i)
		}
	}
	for _, ch := range children {
		ch.stdin.Close()
	}

	for i, ch := range children {
		line, ok := scanPrefix(ch.out, "TOKEN ")
		if !ok {
			t.Fatalf("дочерний #%d не отдал шифротекст", i)
		}
		if err := ch.cmd.Wait(); err != nil {
			t.Fatalf("дочерний #%d завершился с ошибкой: %v", i, err)
		}
		got, err := NewDeviceCipher(dir).Decrypt(line)
		if err != nil || got != testSubscriptionKey {
			t.Fatalf("шифротекст процесса #%d не читается общим секретом: (%q, %v)", i, got, err)
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

func scanUntil(sc *bufio.Scanner, want string) bool {
	for sc.Scan() {
		if sc.Text() == want {
			return true
		}
	}
	return false
}

func scanPrefix(sc *bufio.Scanner, prefix string) (string, bool) {
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), prefix); ok {
			return rest, true
		}
	}
	return "", false
}

// Т1. Отказ записи не оставляет под именем секрета огрызок. Прежняя вторая
// реализация (O_CREATE|O_EXCL прямо на целевом имени, internal/backup)
// оставляла после EFBIG файл нулевой длины: ближайшее шифрование уносило эту
// пустышку в карантин, заводило новый секрет и сообщало пользователю, что
// прежний ключ подписки расшифровать больше нечем — и всё это из-за ВРЕМЕННОЙ
// нехватки места, после которой повтор ещё мог сработать.
func TestDeviceCipher_WriteFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()

	allow := forbidFileWrites(t)
	_, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	allow()

	if err == nil {
		t.Fatal("Encrypt прошёл при запрете записи — отказ не смоделирован, проверка не состоялась")
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, e := range entries {
		info, statErr := e.Info()
		size := int64(-1)
		if statErr == nil {
			size = info.Size()
		}
		t.Fatalf("после отказа записи в каталоге данных остался %s (%d байт): %v", e.Name(), size, err)
	}
}

// forbidFileWrites запрещает процессу писать в обычные файлы: RLIMIT_FSIZE=0
// разрешает создать файл, но любая запись в него отдаёт EFBIG — так же, как
// при кончившемся месте на флеше. Лимит процессный и снимается возвращённой
// функцией сразу после проверяемого вызова; на stdout тестового процесса он
// не влияет — это канал, а не обычный файл. SIGXFSZ, который ядро шлёт вместе
// с EFBIG, перехватывается, чтобы тестовый процесс не умер от него.
func forbidFileWrites(t *testing.T) func() {
	t.Helper()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGXFSZ)
	var saved syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &saved); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: saved.Max}); err != nil {
		t.Fatalf("setrlimit: %v", err)
	}
	done := false
	allow := func() {
		if done {
			return
		}
		done = true
		// Лимит снимается ПЕРВЫМ: пока он стоит, любая запись в обычный файл
		// отдаёт EFBIG вместе с SIGXFSZ, а действие сигнала по умолчанию —
		// убить процесс. Сними перехват раньше лимита — и в этот зазор
		// тестовый бинарь умирает от собственного сигнала.
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &saved); err != nil {
			t.Fatalf("вернуть RLIMIT_FSIZE: %v", err)
		}
		signal.Stop(sig)
	}
	t.Cleanup(allow)
	return allow
}

// Т2. Карантин не уносит чужой ПРИГОДНЫЙ секрет. Сценарий двух экземпляров
// разыгран по шагам, а не потоками: шаги — настоящие (чтение секрета,
// карантин, шифрование), а порядок закреплён, потому что гонку выигрывают
// по-разному и статистический страж здесь обречён мигать. Остаточное окно
// Stat→Rename сверкой не закрывается (см. quarantineKey), и восьми
// экземплярам на одном негодном файле хватало примерно одного прогона из 2000
// при GOMAXPROCS=8, чтобы в него попасть; та же проверка без сверки краснела
// в трети прогонов из 300. Здесь проверяется именно сверка: B уносит в
// карантин то, что ПРОЧИТАЛ, а не то, что лежит под именем сейчас.
func TestDeviceCipher_QuarantineDoesNotTakeForeignKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, DeviceKeyFile)
	// Негодная длина, а не отсутствие файла: карантин включается только на
	// ней, а гонка живёт именно в нём.
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{'x'}, DeviceKeyLen+1), 0o600); err != nil {
		t.Fatal(err)
	}
	SetNoticeSink(func(Notice) {})
	t.Cleanup(func() { SetNoticeSink(nil) })

	// Экземпляр B прочитал негодный секрет и ещё не дошёл до карантина.
	slow := NewDeviceCipher(dir)
	_, info, readErr := slow.readKeyFile()
	if !errors.Is(readErr, errDeviceKeyBadLen) {
		t.Fatalf("чтение негодного секрета = %v, want errDeviceKeyBadLen", readErr)
	}

	// Экземпляр A тем временем проходит весь путь: уносит негодный файл и
	// заводит годный секрет, которым шифрует ключ подписки.
	tokenA, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt первым экземпляром: %v", err)
	}
	good, err := os.ReadFile(keyPath)
	if err != nil || len(good) != DeviceKeyLen {
		t.Fatalf("первый экземпляр не завёл годный секрет: %d байт, err=%v", len(good), err)
	}

	// Теперь B делает следующий шаг своего круга. Под именем лежит чужой
	// ПРИГОДНЫЙ секрет — уносить его нельзя.
	_, qerr := slow.quarantineKey(info, readErr)
	if got, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(got, good) {
		t.Fatalf("чужой пригодный секрет уведён из-под имени: err=%v", err)
	}
	for name, content := range quarantineCopies(t, dir) {
		if len(content) == DeviceKeyLen {
			t.Fatalf("в карантине %s лежит годный секрет", name)
		}
	}
	if !errors.Is(qerr, errDeviceKeyRaced) {
		t.Fatalf("карантин по устаревшему чтению = %v, want errDeviceKeyRaced", qerr)
	}

	// Круг B заканчивается на общем секрете: оба шифротекста читаются им.
	tokenB, err := slow.Encrypt(testSubscriptionKey)
	if err != nil {
		t.Fatalf("Encrypt вторым экземпляром: %v", err)
	}
	reader := NewDeviceCipher(dir)
	for name, token := range map[string]string{"первого": tokenA, "второго": tokenB} {
		if got, err := reader.Decrypt(token); err != nil || got != testSubscriptionKey {
			t.Fatalf("шифротекст %s экземпляра не читается оставшимся секретом: (%q, %v)", name, got, err)
		}
	}
}

// Т2. PublishDeviceKey требует ровно DeviceKeyLen байт. На пути createKey
// длина верна по построению (32 байта из rand.Read), а на пути переноса из
// откатного каталога (internal/backup) байты берутся из файла КАК ЕСТЬ:
// пустой или раздувшийся файл, опубликованный под именем секрета, ближайшее
// шифрование унесёт в карантин и скажет пользователю, что ключ подписки
// расшифровать больше нечем. Отказ обязан быть закрытым и отличимым от
// «имя занято» сентинелом, а не текстом.
func TestPublishDeviceKey_RejectsWrongLength(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
	}{
		{"nil", nil},
		{"пусто", []byte{}},
		{"короче", bytes.Repeat([]byte{'k'}, DeviceKeyLen-1)},
		{"длиннее", bytes.Repeat([]byte{'k'}, DeviceKeyLen+1)},
		{"огромный", bytes.Repeat([]byte{'k'}, 1<<20)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			err := PublishDeviceKey(dir, tc.key)

			if !errors.Is(err, errDeviceKeyBadLen) {
				t.Fatalf("PublishDeviceKey(%d байт) = %v, want errDeviceKeyBadLen", len(tc.key), err)
			}
			if errors.Is(err, fs.ErrExist) {
				t.Fatalf("отказ по длине неотличим от занятого имени: %v", err)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, e := range entries {
				t.Fatalf("после отказа по длине в каталоге данных остался %s", e.Name())
			}
		})
	}
}

// collectNotices перехватывает уведомления пользователю на время теста.
// Довайринговый буфер выгружается в свежий приёмник, поэтому накопленное
// соседними тестами сбрасывается сразу после подключения: иначе «ровно одно
// уведомление» превратилось бы в счёт чужих.
func collectNotices(t *testing.T) func() []Notice {
	t.Helper()
	var got []Notice
	SetNoticeSink(func(n Notice) { got = append(got, n) })
	got = nil
	t.Cleanup(func() { SetNoticeSink(nil) })
	return func() []Notice { return got }
}

// Т4(а). Уведомление о карантине уходит ОДНО за вызов и только после того,
// как новый секрет действительно заведён. Печатал его раньше сам карантин —
// то есть до заведения, и на каждом круге цикла заново.
func TestDeviceCipher_QuarantineNoticeFollowsNewKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeviceKeyFile), bytes.Repeat([]byte{'x'}, DeviceKeyLen+1), 0o600); err != nil {
		t.Fatal(err)
	}
	notices := collectNotices(t)

	if _, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	got := notices()
	if len(got) != 1 {
		t.Fatalf("уведомлений %d, want 1: %v", len(got), got)
	}
	fresh, err := os.ReadFile(filepath.Join(dir, DeviceKeyFile))
	if err != nil || len(fresh) != DeviceKeyLen {
		t.Fatalf("секрет после карантина: %d байт, %v", len(fresh), err)
	}
	copies := quarantineCopies(t, dir)
	if len(copies) != 1 {
		t.Fatalf("карантинных копий %d, want 1", len(copies))
	}
	for saved := range copies {
		if !strings.Contains(got[0].Message, saved) {
			t.Fatalf("уведомление не называет карантинную копию %s: %q", saved, got[0].Message)
		}
	}
}

// Т4(б). Карантин удался, а заведение нового секрета отказало (кончилось
// место — та самая беда, ради которой у записи один владелец): уведомление
// обязано сказать правду, а не пообещать заведённый секрет, которого нет.
func TestDeviceCipher_NoticeDoesNotPromiseKeyThatFailed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DeviceKeyFile), bytes.Repeat([]byte{'x'}, DeviceKeyLen+1), 0o600); err != nil {
		t.Fatal(err)
	}
	notices := collectNotices(t)

	allow := forbidFileWrites(t)
	_, err := NewDeviceCipher(dir).Encrypt(testSubscriptionKey)
	allow()

	if err == nil {
		t.Fatal("Encrypt прошёл при запрете записи — отказ не смоделирован, проверка не состоялась")
	}
	got := notices()
	if len(got) != 1 {
		t.Fatalf("уведомлений %d, want 1: %v", len(got), got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, DeviceKeyFile)); !os.IsNotExist(statErr) {
		t.Fatalf("секрет не заводился, а под именем что-то есть: %v", statErr)
	}
	if strings.Contains(got[0].Message, "заведён новый") {
		t.Fatalf("уведомление обещает заведённый секрет, которого нет: %q", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "не вышло") {
		t.Fatalf("уведомление не говорит, что завести секрет не вышло: %q", got[0].Message)
	}
}
