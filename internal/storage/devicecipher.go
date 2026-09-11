package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DeviceKeyFile — имя файла секрета устройства в dataDir. Экспортировано
// ради internal/backup: секрет привязан к установке и в архив попадать не
// должен, а совпадение имени по литералу в двух пакетах развалится молча.
const DeviceKeyFile = ".device-key"

// DeviceKeyLen — длина секрета, AES-256. Экспортирована по той же причине,
// что и DeviceKeyFile: internal/backup читает файл секрета для переноса из
// откатного каталога, и читать его без границы нельзя — панель живёт на
// роутере со 128 МБ.
const DeviceKeyLen = 32

// ErrDeviceKeyMissing — секрета устройства нет или он непригоден (файл
// отсутствует либо обрезан). Отличается от ErrDeviceCiphertext, потому что
// причины разные: секрет ещё можно достать из отложенного каталога, а
// нерасшифровываемый шифротекст — уже нет.
var ErrDeviceKeyMissing = errors.New("device key missing")

// ErrDeviceCiphertext — значение не расшифровывается этим секретом (чужой
// секрет, порча, не base64). Вызывающий по нему отвечает «ключ непригоден»,
// а не «ключа нет», и сам ключ при этом НЕ стирает.
var ErrDeviceCiphertext = errors.New("device ciphertext undecryptable")

// errDeviceKeyBadLen — секрет негодной длины. На чтении это файл, который
// есть, но длиной не 32 байта: наружу он идёт как ErrDeviceKeyMissing
// (пригодного секрета нет), а внутри пакета отличает «файла нет» от «файл
// негоден» — во втором случае его уносят в карантин, а не затирают. На записи
// (PublishDeviceKey) — те же 32 байта, но уже у того, что просят
// опубликовать; там он идёт наружу голым, чтобы отказ не спутали ни с
// «пригодного секрета нет», ни с занятым именем (fs.ErrExist).
var errDeviceKeyBadLen = errors.New("device key bad length")

// errDeviceKeyRaced — пока мы читали негодный секрет, под его именем оказался
// другой файл: другой экземпляр успел унести негодный в карантин и завести
// годный. Уносить то, что лежит под именем СЕЙЧАС, нельзя — это чужой
// пригодный секрет. Круг начинается заново.
var errDeviceKeyRaced = errors.New("device key changed under us")

// deviceKeyAttempts ограничивает число кругов «прочитать → унести негодный →
// завести». Каждый круг начинается с проигранной гонки, то есть с чужого
// успеха, и на здоровой установке второго круга не бывает вовсе; предел
// нужен, чтобы патология (кто-то раз за разом кладёт под имя негодный файл)
// давала закрытый отказ, а не вечный цикл.
const deviceKeyAttempts = 3

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
	raw, _, err := c.readKeyFile()
	return raw, err
}

// keyForWrite отдаёт секрет, заводя его при отсутствии или непригодности.
// Круг повторяется, только когда гонку выиграл кто-то другой: и карантин, и
// заведение опираются на то, что под именем лежит ожидаемый файл, а это
// между вызовами могло перестать быть правдой.
//
// Уведомление пользователю уходит отсюда, одно на весь вызов и после того,
// как исход известен. Раньше его печатал сам карантин — то есть ДО заведения
// нового секрета: если заведение потом отказывало (кончилось место — та самая
// беда, ради которой у записи один владелец), пользователю уже сказали про
// заведённый секрет, которого нет, а на повторных кругах обещали это ещё раз.
func (c *DeviceCipher) keyForWrite() (key []byte, err error) {
	var (
		bad   error  // чем забракован лежавший под именем файл
		saved string // имя карантинной копии, если унести удалось
		stuck error  // унести не удалось: негодный файл остался под именем
	)
	defer func() {
		switch {
		case stuck != nil:
			recordNotice("quarantine", DeviceKeyFile, fmt.Sprintf(
				"Файл секрета устройства %s негоден (%v), и убрать его в сторону не вышло: %v. Пока он на месте, ключ подписки Amnezia сохранить не получится.",
				DeviceKeyFile, bad, stuck))
		case saved == "":
			// Уносить было нечего (секрета просто нет) либо гонку выиграл
			// другой экземпляр: сообщать пользователю не о чем.
		case err != nil:
			recordNotice("quarantine", DeviceKeyFile, fmt.Sprintf(
				"Файл секрета устройства %s негоден (%v) и убран рядом как %s, но завести новый не вышло: %v. Ранее сохранённый ключ подписки Amnezia расшифровать больше нечем, а сохранить новый пока не получится.",
				DeviceKeyFile, bad, saved, err))
		default:
			// Текст адресован человеку в журнале, а не инженеру в консоли:
			// секрет привязан к установке, и единственное действие
			// пользователя — ввести ключ подписки заново. Паниковать не о
			// чем: на здоровой установке это сообщение не появляется вовсе.
			recordNotice("quarantine", DeviceKeyFile, fmt.Sprintf(
				"Файл секрета устройства %s негоден (%v); заведён новый, прежний сохранён рядом как %s. Ранее сохранённый ключ подписки Amnezia расшифровать больше нечем — введите его заново.",
				DeviceKeyFile, bad, saved))
		}
	}()

	var last error
	for attempt := 0; attempt < deviceKeyAttempts; attempt++ {
		raw, info, rerr := c.readKeyFile()
		switch {
		case rerr == nil:
			return raw, nil
		case errors.Is(rerr, errDeviceKeyBadLen):
			// Файл есть, но длина не та. Самый вероятный способ получить
			// лишний байт — дописанный \n после просмотра редактором, и тогда
			// первые 32 байта — настоящий секрет, который ещё можно достать.
			// Поэтому файл не затирается новым секретом, а уносится в
			// карантин, и человек узнаёт об этом из журнала.
			bad = rerr
			name, qerr := c.quarantineKey(info, rerr)
			switch {
			case qerr == nil:
				saved = name
			case errors.Is(qerr, errDeviceKeyRaced):
				last = qerr
				continue
			default:
				// Негодный файл остался под именем: заводить секрет поверх
				// него нельзя, а читать нечего. Отказ закрытый.
				stuck = qerr
				return nil, qerr
			}
		case !errors.Is(rerr, ErrDeviceKeyMissing):
			// Файл есть, но прочитать его не вышло (EIO, EISDIR). Отказ
			// закрытый: перезаписать секрет здесь — гарантированно потерять
			// то, что ещё читается после починки железа.
			return nil, rerr
		}
		fresh, cerr := c.createKey()
		if cerr == nil {
			return fresh, nil
		}
		if !errors.Is(cerr, ErrDeviceKeyMissing) {
			return nil, cerr
		}
		// Имя занял другой экземпляр, и его файл непригоден. Читать его
		// нечего, затирать нельзя — на следующем круге он поедет в карантин
		// как обычный негодный секрет.
		last = cerr
	}
	return nil, fmt.Errorf("device key: секрет не заведён за %d попыток: %w", deviceKeyAttempts, last)
}

// createKey заводит секрет ровно один раз на dataDir.
func (c *DeviceCipher) createKey() ([]byte, error) {
	fresh := make([]byte, DeviceKeyLen)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("device key: rand: %w", err)
	}
	if err := PublishDeviceKey(c.dataDir, fresh); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Гонку выиграл другой: читаем его секрет один раз, без повторов
			// — под целевым именем пустого файла не бывает.
			return c.keyForRead()
		}
		return nil, err
	}
	return fresh, nil
}

// PublishDeviceKey кладёт key под именем секрета устройства в dataDir и НЕ
// затирает уже лежащий там файл: занятое имя — это fs.ErrExist, и решает
// вызывающий (завести нечего — читаем чужой; перенести из отката — оставляем
// свой). Пустой каталог создаётся по дороге.
//
// Функция экспортирована ради internal/backup: перенос секрета из откатного
// каталога писал файл своей, ВТОРОЙ реализацией — O_CREATE|O_EXCL прямо на
// целевом имени, — и при отказе записи (нет места) оставлял под именем
// огрызок нулевой длины, который ближайшее шифрование уносило в карантин с
// сообщением «ключ подписки расшифровать больше нечем». У файла секрета один
// владелец дисциплины записи, и это storage.
//
// Секрет пишется во временный файл, доводится до носителя и только потом
// получает целевое имя через os.Link: под именем .device-key недописанного
// файла не бывает ни в какой момент, а при отказе не остаётся ничего —
// повтор ещё может сработать. Победителя выбирает сама файловая система —
// Link на занятое имя отдаёт EEXIST, — и проигравший читает чужой секрет,
// ПОЛНЫЙ по построению. Это верно и для двух экземпляров в процессе, и для
// двух процессов (демон и --cleanup живут одновременно), где мьютекс не помог
// бы вовсе.
//
// O_CREATE|O_EXCL прямо на целевом имени так не умеет: он закрывает окно
// «файла нет → создать», но открывает другое — между созданием inode и
// записью 32 байт файл существует и пуст, то есть выглядит негодным. Общий
// AtomicWritePerm не годится тем же боком: он завершается rename'ом, который
// молча затирает чужой файл.
func PublishDeviceKey(dataDir string, key []byte) error {
	// Длина проверяется здесь, а не у вызывающих: на пути createKey она верна
	// по построению (32 байта из rand.Read), а на пути переноса из откатного
	// каталога байты берутся из файла КАК ЕСТЬ. Пустой или раздувшийся файл,
	// опубликованный под именем секрета, ближайшее шифрование уносит в
	// карантин и сообщает пользователю, что ключ подписки расшифровать больше
	// нечем, — то есть негодный файл в откатной копии превращался бы в
	// потерю ключа, а не в отказ переноса.
	if len(key) != DeviceKeyLen {
		return fmt.Errorf("device key: %w: %d байт вместо %d", errDeviceKeyBadLen, len(key), DeviceKeyLen)
	}
	if err := os.MkdirAll(dataDir, DirPermission); err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	// Имя временного файла уникально по построению (os.CreateTemp), а не по
	// pid и часам: два экземпляра в одном процессе успевают получить
	// одинаковую наносекунду. Режим у CreateTemp 0600 — ровно тот, что нужен
	// секрету, и он же уезжает на целевое имя вместе с inode. Префикс
	// .device-key. исключён из бэкапа тем же предикатом, что и сам секрет:
	// временная копия в архив не уедет, даже если её застанут.
	tmp, err := os.CreateTemp(dataDir, DeviceKeyFile+".new.*")
	if err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	// Лишней копии секрета на флеше не остаётся ни при каком исходе: после
	// удачного Link у inode уже есть целевое имя, при отказе — тем более.
	defer os.Remove(tmp.Name())
	if err := writeDeviceKey(tmp, key); err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	if err := os.Link(tmp.Name(), filepath.Join(dataDir, DeviceKeyFile)); err != nil {
		return fmt.Errorf("device key: %w", err)
	}
	syncDir(dataDir)
	return nil
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
//
// read — FileInfo того файла, чьи байты читал вызывающий (снят с открытого
// дескриптора, то есть описывает именно прочитанный inode). Переименование
// адресуется ИМЕНЕМ, а под именем к этому моменту может лежать уже другой
// файл: пока мы читали негодный секрет, другой экземпляр успел унести его в
// карантин и завести годный. Без сверки карантин утаскивал бы чужой
// ПРИГОДНЫЙ секрет, и все выданные им шифротексты переставали читаться.
//
// Первым значением идёт имя карантинной копии. Уведомление пользователю
// отсюда не уходит: что ему сказать, известно только после того, как отработал
// createKey, — см. keyForWrite.
func (c *DeviceCipher) quarantineKey(read fs.FileInfo, reason error) (string, error) {
	holder, err := os.CreateTemp(c.dataDir, DeviceKeyFile+".corrupt.*")
	if err == nil {
		holder.Close()
		// Держатель создаётся ДО сверки, чтобы между ней и переименованием
		// не осталось ничего, кроме самого Rename. Окно между Stat и Rename
		// всё равно остаётся: атомарного «переименовать, если под именем тот
		// же inode» в POSIX нет, и попавший ровно в него экземпляр унесёт
		// чужой секрет. Окно измерено — восемь экземпляров на одном негодном
		// файле попадали в него примерно раз на 2000 прогонов (GOMAXPROCS=8),
		// против трети прогонов без сверки; закрыть его до нуля можно только
		// взаимным исключением на весь цикл «унести → завести», а это другая
		// дисциплина, чем выбор победителя средствами ФС. Обычный проигравший
		// получает ENOENT — имя уже увели — и начинает заново.
		var now fs.FileInfo
		if now, err = os.Stat(c.path()); err != nil || !os.SameFile(read, now) {
			os.Remove(holder.Name())
			return "", errDeviceKeyRaced
		}
		if err = os.Rename(c.path(), holder.Name()); err != nil {
			os.Remove(holder.Name())
			if os.IsNotExist(err) {
				// Имя увели между Stat и Rename — обычный проигрыш гонки.
				return "", errDeviceKeyRaced
			}
		}
	}
	if err != nil {
		// Файл остаётся на месте: заведение нового секрета упрётся в занятое
		// имя и откажет закрыто, а прежний файл никто не затрёт.
		fmt.Fprintf(os.Stderr, "storage: %s is unusable (%v); quarantine failed: %v\n", c.path(), reason, err)
		return "", fmt.Errorf("device key: карантин: %w", err)
	}
	syncDir(c.dataDir)
	saved := filepath.Base(holder.Name())
	fmt.Fprintf(os.Stderr, "storage: %s is unusable (%v); moved to %s\n", c.path(), reason, saved)
	return saved, nil
}

// readKeyFile отдаёт ErrDeviceKeyMissing и на отсутствующий, и на негодный
// по длине файл: и то и другое означает, что пригодного секрета нет. Вторым
// значением идёт FileInfo ПРОЧИТАННОГО inode (снят с дескриптора, а не
// отдельным Stat по имени) — по нему карантин сверяет, что уносит именно
// тот файл, чьи байты забраковал.
func (c *DeviceCipher) readKeyFile() ([]byte, fs.FileInfo, error) {
	f, err := os.Open(c.path())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrDeviceKeyMissing
		}
		return nil, nil, fmt.Errorf("device key: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("device key: %w", err)
	}
	// Читаем на байт больше нужного: этого хватает, чтобы отличить годную
	// длину от негодной, и подложенный на место секрета огромный файл не
	// уедет в память целиком — панель живёт на роутере со 128 МБ.
	raw, err := io.ReadAll(io.LimitReader(f, DeviceKeyLen+1))
	if err != nil {
		return nil, nil, fmt.Errorf("device key: %w", err)
	}
	if len(raw) != DeviceKeyLen {
		// FileInfo отдаётся и здесь: именно этот файл поедет в карантин, и
		// сверять его там будут по этому же inode.
		return nil, info, fmt.Errorf("%w: %d байт вместо %d (%w)", ErrDeviceKeyMissing, info.Size(), DeviceKeyLen, errDeviceKeyBadLen)
	}
	return raw, info, nil
}
