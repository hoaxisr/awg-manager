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
)

// DeviceKeyFile — имя файла секрета устройства в dataDir. Экспортировано
// ради internal/backup: секрет привязан к установке и в архив попадать не
// должен, а совпадение имени по литералу в двух пакетах развалится молча.
const DeviceKeyFile = ".device-key"

// deviceKeyLen — длина секрета, AES-256.
const deviceKeyLen = 32

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
		// файл не затирается новым секретом, а уносится в карантин, и
		// человек узнаёт об этом из журнала.
		c.quarantineKey(err)
	case !errors.Is(err, ErrDeviceKeyMissing):
		// Файл есть, но прочитать его не вышло (EIO, EISDIR). Отказ
		// закрытый: перезаписать секрет здесь — гарантированно потерять
		// то, что ещё читается после починки железа.
		return nil, err
	}
	return c.createKey()
}

// createKey заводит секрет ровно один раз на dataDir. Секрет пишется во
// временный файл, доводится до носителя и только потом получает целевое имя
// через os.Link: под именем .device-key недописанного файла не бывает ни в
// какой момент. Победителя выбирает сама файловая система — Link на занятое
// имя отдаёт EEXIST, — и проигравший читает чужой секрет, ПОЛНЫЙ по
// построению. Это верно и для двух экземпляров в процессе, и для двух
// процессов (демон и --cleanup живут одновременно), где мьютекс не помог бы
// вовсе.
//
// O_CREATE|O_EXCL прямо на целевом имени так не умеет: он закрывает окно
// «файла нет → создать», но открывает другое — между созданием inode и
// записью 32 байт файл существует и пуст, то есть выглядит негодным. Общий
// AtomicWritePerm не годится тем же боком: он завершается rename'ом, который
// молча затирает чужой файл.
func (c *DeviceCipher) createKey() ([]byte, error) {
	if err := os.MkdirAll(c.dataDir, DirPermission); err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	fresh := make([]byte, deviceKeyLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("device key: rand: %w", err)
	}
	// Имя временного файла уникально по построению (os.CreateTemp), а не по
	// pid и часам: два экземпляра в одном процессе успевают получить
	// одинаковую наносекунду. Режим у CreateTemp 0600 — ровно тот, что нужен
	// секрету, и он же уезжает на целевое имя вместе с inode. Префикс
	// .device-key. исключён из бэкапа тем же предикатом, что и сам секрет:
	// временная копия в архив не уедет, даже если её застанут.
	tmp, err := os.CreateTemp(c.dataDir, DeviceKeyFile+".new.*")
	if err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	// Лишней копии секрета на флеше не остаётся ни при каком исходе: после
	// удачного Link у inode уже есть целевое имя, при отказе — тем более.
	defer os.Remove(tmp.Name())
	if err := writeDeviceKey(tmp, fresh); err != nil {
		return nil, fmt.Errorf("device key: %w", err)
	}
	if err := os.Link(tmp.Name(), c.path()); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Гонку выиграл другой: читаем его секрет один раз, без повторов
			// — под целевым именем пустого файла не бывает.
			return c.readKeyFile()
		}
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

// quarantineKey уносит негодный файл секрета в копию с уникальным по
// построению именем. Общий QuarantineCorrupt тут не годится: он
// переименовывает в фиксированное <путь>.corrupt, а os.Rename на Linux молча
// затирает цель — вторая порча уничтожила бы первую копию, ту самую, где
// вероятнее всего лежит настоящий секрет.
func (c *DeviceCipher) quarantineKey(reason error) {
	holder, err := os.CreateTemp(c.dataDir, DeviceKeyFile+".corrupt.*")
	if err == nil {
		holder.Close()
		if err = os.Rename(c.path(), holder.Name()); err != nil {
			os.Remove(holder.Name())
		}
	}
	if err != nil {
		// Файл остаётся на месте: заведение нового секрета упрётся в занятое
		// имя и откажет закрыто, а прежний файл никто не затрёт.
		fmt.Fprintf(os.Stderr, "storage: %s is unusable (%v); quarantine failed: %v\n", c.path(), reason, err)
		recordNotice("quarantine", DeviceKeyFile, fmt.Sprintf(
			"Файл секрета устройства %s негоден (%v), и убрать его в сторону не вышло: %v. Пока он на месте, ключ подписки Amnezia сохранить не получится.",
			DeviceKeyFile, reason, err))
		return
	}
	syncDir(c.dataDir)
	saved := filepath.Base(holder.Name())
	fmt.Fprintf(os.Stderr, "storage: %s is unusable (%v); moved to %s, new device key generated\n", c.path(), reason, saved)
	// Текст адресован человеку в журнале, а не инженеру в консоли: секрет
	// привязан к установке, и единственное действие пользователя — ввести
	// ключ подписки заново. Паниковать не о чем: на здоровой установке это
	// сообщение не появляется вовсе.
	recordNotice("quarantine", DeviceKeyFile, fmt.Sprintf(
		"Файл секрета устройства %s негоден (%v); заведён новый, прежний сохранён рядом как %s. Ранее сохранённый ключ подписки Amnezia расшифровать больше нечем — введите его заново.",
		DeviceKeyFile, reason, saved))
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
