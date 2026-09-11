package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DeviceKeyFile — имя файла секрета устройства в dataDir. Экспортировано
// ради internal/backup: секрет привязан к установке и в архив попадать не
// должен, а совпадение имени по литералу в двух пакетах развалится молча.
const DeviceKeyFile = ".device-key"

const (
	deviceKeyLen  = 32 // AES-256
	deviceKeyPerm = 0o600
	// deviceKeyRaceRetries/deviceKeyRaceDelay — окно между созданием файла
	// победителем гонки и записью в него 32 байт: проигравший в этот момент
	// видит файл нулевой длины. Окно — микросекунды, поэтому ждём коротко и
	// конечное число раз, а потом отказываем закрыто.
	deviceKeyRaceRetries = 20
	deviceKeyRaceDelay   = 5 * time.Millisecond
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

// errDeviceKeyBadLen — файл секрета есть, но его длина не 32 байта. Наружу
// он идёт как ErrDeviceKeyMissing (пригодного секрета нет), а внутри пакета
// отличает «файла нет» от «файл негоден»: во втором случае его уносят в
// карантин, а не затирают.
var errDeviceKeyBadLen = errors.New("device key bad length")

// DeviceCipher шифрует секреты аккаунта (ключ подписки Amnezia) секретом,
// привязанным к установке: <dataDir>/.device-key, 32 случайных байта, 0600.
// Отдельный тип, а не методы SettingsStore, — чтобы шифрование можно было
// проверять и использовать, не поднимая настройки.
//
// Секрет в памяти не кэшируется: это 32 байта, которые ОС держит в
// страничном кэше, а читают их на действие пользователя, а не в цикле.
// Зато кэш скрывал бы от живого процесса подмену файла — а подменяем мы его
// сами при восстановлении из бэкапа.
type DeviceCipher struct {
	dataDir string
}

// NewDeviceCipher creates a cipher rooted at dataDir.
func NewDeviceCipher(dataDir string) *DeviceCipher {
	return &DeviceCipher{dataDir: dataDir}
}

func (c *DeviceCipher) path() string { return filepath.Join(c.dataDir, DeviceKeyFile) }

// Encrypt returns base64(nonce‖ciphertext). Отсутствующий секрет заводится,
// непригодный по длине — сначала уносится в карантин, а потом заводится
// новый: старый шифротекст всё равно уже не читается.
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
	return c.readKeyFile()
}

// keyForWrite отдаёт секрет, заводя его при отсутствии или непригодности.
func (c *DeviceCipher) keyForWrite() ([]byte, error) {
	raw, err := c.readKeyFile()
	switch {
	case err == nil:
		return raw, nil
	case errors.Is(err, errDeviceKeyBadLen):
		// Файл есть, но длина не та. Самый вероятный способ получить лишний
		// байт — дописанный \n после просмотра редактором, и тогда первые
		// 32 байта — настоящий секрет, который ещё можно достать. Поэтому
		// файл не затирается новым секретом, а уносится в <путь>.corrupt,
		// и человек узнаёт об этом из журнала.
		QuarantineCorrupt(c.path(), err)
	case !errors.Is(err, ErrDeviceKeyMissing):
		// Файл есть, но прочитать его не вышло (EIO, EISDIR). Отказ
		// закрытый: перезаписать секрет здесь — гарантированно потерять
		// то, что ещё читается после починки железа.
		return nil, err
	}
	return c.createKey()
}

// createKey заводит секрет ровно один раз на dataDir. Победителя выбирает
// сама файловая система: O_CREATE|O_EXCL создаёт файл либо отказывает с
// EEXIST, и проигравший берёт чужой секрет вместо своего. Это верно и для
// двух экземпляров в процессе, и для двух процессов (демон и --cleanup
// живут одновременно), где мьютекс не помог бы вовсе.
//
// Общий AtomicWritePerm тут не годится: он завершается rename'ом, который
// молча затирает чужой файл, — и второй пришедший похоронил бы шифротекст
// первого.
func (c *DeviceCipher) createKey() ([]byte, error) {
	if err := os.MkdirAll(c.dataDir, DirPermission); err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	fresh := make([]byte, deviceKeyLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("device key: rand: %w", err)
	}
	f, err := os.OpenFile(c.path(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, deviceKeyPerm)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return c.raceWinnerKey()
		}
		return nil, fmt.Errorf("device key: %w", err)
	}
	if err := writeDeviceKey(f, fresh); err != nil {
		// Недописанный секрет хуже отсутствующего: он выглядит пригодным
		// файлом и увёл бы следующий запуск в карантин вместо чистого
		// заведения.
		os.Remove(c.path())
		return nil, fmt.Errorf("device key: %w", err)
	}
	syncDir(c.dataDir)
	return fresh, nil
}

// writeDeviceKey пишет секрет и доводит его до носителя. Sync до Close
// обязателен: состояние живёт на флеше с отложенным выделением, и без fsync
// потеря питания оставляет файл нулевой длины — то есть ключ подписки
// перестаёт расшифровываться навсегда.
func writeDeviceKey(f *os.File, key []byte) error {
	if _, err := f.Write(key); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// raceWinnerKey читает секрет, заведённый тем, кто выиграл O_EXCL. Чтение
// повторяется: между созданием файла и записью 32 байт есть окно, в котором
// файл пуст и выглядит непригодным.
func (c *DeviceCipher) raceWinnerKey() ([]byte, error) {
	for attempt := 0; ; attempt++ {
		raw, err := c.readKeyFile()
		if err == nil || attempt == deviceKeyRaceRetries || !errors.Is(err, ErrDeviceKeyMissing) {
			return raw, err
		}
		time.Sleep(deviceKeyRaceDelay)
	}
}

// readKeyFile отдаёт ErrDeviceKeyMissing и на отсутствующий, и на негодный
// по длине файл: и то и другое означает, что пригодного секрета нет.
func (c *DeviceCipher) readKeyFile() ([]byte, error) {
	raw, err := os.ReadFile(c.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrDeviceKeyMissing
		}
		return nil, fmt.Errorf("device key: %w", err)
	}
	if len(raw) != deviceKeyLen {
		return nil, fmt.Errorf("%w: %d байт вместо %d (%w)", ErrDeviceKeyMissing, len(raw), deviceKeyLen, errDeviceKeyBadLen)
	}
	return raw, nil
}
