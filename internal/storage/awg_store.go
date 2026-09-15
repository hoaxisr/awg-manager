package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/opkgtun"
	"github.com/hoaxisr/awg-manager/internal/sys/lock"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnelid"
)

// ErrNotFound — записи туннеля нет. Им оборачивается отсутствие файла в Get,
// и через Get он приходит наружу из Update: «туннель удалили, пока шла долгая
// работа снаружи лока» — законный исход, который вызывающий отличает
// errors.Is, а не сравнением строк.
//
// Это ТОТ ЖЕ объект, что tunnel.ErrNotFound, а не одноимённый двойник:
// errors.Is работает под любым из двух имён, и слой, перепутавший пакет,
// не получит молча ложное «не найдено» (тексты у них совпадали бы, а
// идентичность — нет; компилятор такую путаницу не ловит).
var ErrNotFound = tunnel.ErrNotFound

// ErrAlreadyExists — ID занят: Create не перекрывает чужую запись. Тот же
// объект, что tunnel.ErrAlreadyExists, по той же причине.
var ErrAlreadyExists = tunnel.ErrAlreadyExists

// ErrNoChange — условный ответ мутатора Update: «менять нечего». Update
// возвращает nil и НЕ пишет файл. Нужен тем, кто сам решает по свежей записи,
// изменилось ли что-нибудь (гейты DeepEqual и флаги changed): без него
// «ничего не поменялось» пришлось бы кодировать записью-пустышкой.
var ErrNoChange = errors.New("no change")

// AWGTunnelStore provides directory-based storage for AmneziaWG tunnel metadata.
type AWGTunnelStore struct {
	dir      string
	lockName string
	lockDir  string
	timeout  time.Duration
}

// NewAWGTunnelStore creates a new AWG tunnel store.
func NewAWGTunnelStore(dir string) *AWGTunnelStore {
	return NewAWGTunnelStoreWithLockDir(dir, lock.LockDir)
}

// NewAWGTunnelStoreWithLockDir creates a new AWG tunnel store with custom lock directory.
func NewAWGTunnelStoreWithLockDir(dir string, lockDir string) *AWGTunnelStore {
	return &AWGTunnelStore{
		dir:      dir,
		lockName: "tunnels",
		lockDir:  lockDir,
		timeout:  5 * time.Second,
	}
}

// List returns all AWG tunnels by scanning the directory.
func (s *AWGTunnelStore) List() ([]AWGTunnel, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AWGTunnel{}, nil
		}
		return nil, fmt.Errorf("read tunnels directory: %w", err)
	}

	var tunnels []AWGTunnel
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var tunnel AWGTunnel
		if err := json.Unmarshal(data, &tunnel); err != nil {
			// Quarantine instead of skipping silently: a skipped file's ID
			// looks free to NextAvailableID, and the next created tunnel
			// would overwrite the still-recoverable JSON (with its keys).
			QuarantineCorrupt(path, err)
			continue
		}

		if tunnel.Type == "" {
			tunnel.Type = "awg"
		}

		// Migration: old tunnels without DefaultRouteSet default to DefaultRoute=true
		if !tunnel.DefaultRouteSet {
			tunnel.DefaultRoute = true
			tunnel.DefaultRouteSet = true
		}

		tunnels = append(tunnels, tunnel)
	}

	return tunnels, nil
}

// ListStrict — перечисление БЕЗ прощения и БЕЗ побочных действий: любая
// пофайловая беда (чтение, JSON) — ошибка всего вызова; карантина нет.
//
// Нужен там, где «не смогли перечислить» и «записей нет» имеют
// противоположные последствия, а List() их не различает: пофайловую ошибку он
// глотает через continue. Два таких потребителя: занятость номеров OpkgTun и
// гейт посева реестра выходов, где временно нечитаемый каталог выглядел бы как
// «терять нечего». Отсутствие каталога — законное «пусто».
//
// ГРАНИЦА, которую важно понимать: от ПОРЧИ JSON строгое чтение не защищает и
// защищать не должно — побочных действий у него нет вовсе, и это свойство, на
// которое опирается второй потребитель (зеркало реестра выходов: «не смогли
// перечислить» ≠ «записей нет», требование 20). Карантин повреждённой записи
// делает прощающий List(), и на пути выдачи идентификатора он зовётся ПЕРВЫМ —
// см. service.kernelID. К моменту сбора занятости битого файла уже нет, и
// номер честно свободен.
//
// Строгое чтение ловит другой класс — ВРЕМЕННУЮ нечитаемость файла, которую
// List() пропускает молча и без переименования: номер такой записи выглядел бы
// свободным, и его выдали бы второй раз.
//
// МИГРАЦИЙ ЗДЕСЬ НЕТ — в отличие от List() (DefaultRoute и всё, что добавят
// после). Потребители читают только Backend, ID и Interface.Address, которых
// миграции не касаются; добавляя миграцию в List(), решить осознанно, нужна
// ли она и тут — молча разойтись эти два перечисления могут легко.
func (s *AWGTunnelStore) ListStrict() ([]AWGTunnel, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AWGTunnel{}, nil
		}
		return nil, fmt.Errorf("read tunnels directory: %w", err)
	}
	var tunnels []AWGTunnel
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		var tunnel AWGTunnel
		if err := json.Unmarshal(data, &tunnel); err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		if tunnel.Type == "" {
			tunnel.Type = "awg"
		}
		tunnels = append(tunnels, tunnel)
	}
	return tunnels, nil
}

// tunnelPath maps an id to its file. Malformed ids (anything with a
// separator or a dot, see tunnelid) are refused here rather than trusted
// from the caller: the REST layer validates, but the MCP layer and any
// future caller must not be able to read <dataDir>/settings.json through
// "../settings". A refused id reads as "no such tunnel".
func (s *AWGTunnelStore) tunnelPath(id string) (string, error) {
	if !tunnelid.Valid(id) {
		return "", fmt.Errorf("%w: %q is not a valid tunnel id", ErrNotFound, id)
	}
	return filepath.Join(s.dir, id+".json"), nil
}

// Get returns a single tunnel by ID.
func (s *AWGTunnelStore) Get(id string) (*AWGTunnel, error) {
	path, err := s.tunnelPath(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, fmt.Errorf("read tunnel file: %w", err)
	}

	var tunnel AWGTunnel
	if err := json.Unmarshal(data, &tunnel); err != nil {
		return nil, fmt.Errorf("parse tunnel JSON: %w", err)
	}

	if tunnel.Type == "" {
		tunnel.Type = "awg"
	}

	// Migration: old tunnels without DefaultRouteSet default to DefaultRoute=true
	if !tunnel.DefaultRouteSet {
		tunnel.DefaultRoute = true
		tunnel.DefaultRouteSet = true
	}

	return &tunnel, nil
}

// saveLocked — единственный путь записи туннеля на диск. Вызывающий ОБЯЗАН
// держать лок "tunnels": лок нерекурсивен (mkdir), повторный захват изнутри —
// не дедлок, а 5-секундный таймаут-отказ.
//
// Неэкспортируемый, и обёртки «просто записать запись целиком» у него нет:
// снаружи туннель заводится только через Create, а правится только через
// Update. Пока такая обёртка была экспортирована, любой владелец мог записать
// снимок, снятый вне лока, и затереть чужую правку — путь, который K10
// закрывает компилятором, а не договорённостью.
func (s *AWGTunnelStore) saveLocked(tunnel *AWGTunnel) error {
	if tunnel.Type == "" {
		tunnel.Type = "awg"
	}

	// Use Encoder with SetEscapeHTML(false) to preserve < and > in signature fields (I1-I5)
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(tunnel); err != nil {
		return fmt.Errorf("marshal tunnel: %w", err)
	}

	// Remove trailing newline added by Encode
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

	path, err := s.tunnelPath(tunnel.ID)
	if err != nil {
		return err
	}
	if err := AtomicWrite(path, data); err != nil {
		return fmt.Errorf("write tunnel file: %w", err)
	}

	return nil
}

// Update атомарно правит запись туннеля: dir-lock → СВЕЖЕЕ чтение записи →
// мутатор → запись. Единственный законный способ изменить существующий
// туннель.
//
// Зачем транзакция: прежде запись целиком была экспортирована, и вызывающий,
// прочитавший запись сам и потом её сохранивший, работал со снимком, снятым
// ВНЕ лока (метода для этого больше нет). Всё, что записали в туннель,
// пока он делал свою работу (секунды RCI у оркестратора), его снимок затирает
// — классический lost update: правка пользователя молча откатывалась, а
// запись расходилась с .conf. Мутатор Update стартует со свежей записи и
// присваивает ТОЛЬКО свои поля, поэтому чужие переживают запись.
//
// Fail-closed: ошибка чтения (нет файла → ErrNotFound, битый JSON → ошибка
// разбора) отменяет всё — мутатор пустую запись НЕ получает, иначе он затёр
// бы нечитаемый файл дефолтами. Мутатор вернул ErrNoChange → nil, записи нет.
// Иная ошибка мутатора → отказ, записи нет.
//
// Мутатор исполняется ПОД dir-lock: он обязан быть чистым и быстрым — только
// присваивания заранее вычисленных значений. Любой метод стора изнутри
// запрещён: лок нерекурсивен (mkdir), вложенный захват — 5-секундный
// таймаут-отказ. RCI, exec, DNS-резолв, запись .conf делаются ДО вызова.
// Затирать запись целиком (*t = снимок) запрещено — это ровно тот дефект,
// ради которого транзакция и заведена.
func (s *AWGTunnelStore) Update(id string, mut func(*AWGTunnel) error) error {
	if _, err := s.tunnelPath(id); err != nil {
		return err
	}
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	// Get лока не берёт (обычный ReadFile) — вложенного захвата здесь нет.
	tunnel, err := s.Get(id)
	if err != nil {
		return err
	}
	if err := mut(tunnel); err != nil {
		if errors.Is(err, ErrNoChange) {
			return nil
		}
		return err
	}
	return s.saveLocked(tunnel)
}

// Create заводит НОВУЮ запись туннеля: dir-lock → проверка занятости ID →
// запись. Занятый ID — ошибка: молчаливо перекрыть чужую запись (вместе с её
// ключами) нельзя. Пустой ID тоже отказ — иначе на диск лёг бы файл ".json",
// невидимый для List и неудаляемый обычными путями.
//
// Единственный законный способ создать туннель; существующий правится через
// Update.
func (s *AWGTunnelStore) Create(tunnel *AWGTunnel) error {
	if tunnel.ID == "" {
		return fmt.Errorf("create tunnel: empty ID")
	}
	if _, err := s.tunnelPath(tunnel.ID); err != nil {
		return fmt.Errorf("create tunnel: %w", err)
	}

	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	// Exists лока не берёт (обычный Stat) — вложенного захвата здесь нет.
	if s.Exists(tunnel.ID) {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, tunnel.ID)
	}
	return s.saveLocked(tunnel)
}

// Delete removes tunnel file.
func (s *AWGTunnelStore) Delete(id string) error {
	path, err := s.tunnelPath(id)
	if err != nil {
		return err
	}
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("tunnel not found: %s", id)
		}
		return fmt.Errorf("check tunnel file: %w", err)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove tunnel file: %w", err)
	}

	return nil
}

// Exists checks if tunnel exists.
func (s *AWGTunnelStore) Exists(id string) bool {
	path, err := s.tunnelPath(id)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

const (
	// os5NWGMinIndex — исторический пол диапазона ID для NativeWG на OS 5.x.
	// Действующий пол считает nwgFloor: он не даёт диапазонам пересечься с
	// номерами OpkgTun. Сверху диапазон не ограничен: реальную ёмкость задают
	// индексы Wireguard0..99 в NDMS (nwg.MaxTunnels) и слоты awg_proxy
	// (16 одновременных туннелей с обфускацией на прошивках без нативного
	// ASC), а не диапазон ID.
	// Легаси NativeWG-туннели, созданные до разделения диапазонов, могут
	// занимать awg10..awg16 — это допустимо, kernel-аллокатор просто
	// пропускает занятые ими номера (миграция не выполняется).
	os5NWGMinIndex = 20
)

// NextAvailableID finds the next available tunnel ID for the given backend.
//
// Осталось два случая, и оба — не про пул номеров OpkgTun:
//   - OS 4.x: awgm0, awgm1, ... (префикс 'm', NDMS нет; backend не различается)
//   - OS 5.x, nativewg: awg<N> начиная с nwgFloor — awg20 на mips, awg50 на
//     arm/arm64 (живёт как Wireguard<N>, интерфейс OpkgTun не создаёт)
//
// Kernel-туннели на OS 5.x сюда больше не приходят: их идентификатор
// ОДНОВРЕМЕННО является номером интерфейса, а номер выдаёт общий пул
// (internal/opkgtun). Отдельная выдача здесь означала бы окно между чтением
// занятости и записью на диск, в которое номер уводит соседняя подсистема
// (#891), — поэтому kernel на OS 5.x получает отказ, а не «запасной» номер.
func (s *AWGTunnelStore) NextAvailableID(backend string) (string, error) {
	tunnels, err := s.List()
	if err != nil {
		return "", err
	}
	return nextAvailableID(tunnels, backend, osdetect.Is5(), opkgtun.CeilingForHost())
}

// NextAvailableOS4ID — идентификатор awgm<N> для прошивок без OpkgTun.
//
// Отдельным методом, а не аргументом «это 4.x» у NextAvailableID: версию ОС
// там определяет глобальный osdetect, а на этом пути её уже определил
// вызывающий своим предикатом. Двух решающих об одном и том же быть не должно —
// разойдясь, они дадут awgm-идентификатор на пятёрке или отказ на четвёрке.
func (s *AWGTunnelStore) NextAvailableOS4ID() (string, error) {
	tunnels, err := s.List()
	if err != nil {
		return "", err
	}
	// Потолок не при чём: на 4.x интерфейсов OpkgTun нет вовсе, и ветка,
	// которая его читает, недостижима.
	return nextAvailableID(tunnels, "", false, 0)
}

// nwgFloor — первый идентификатор, который выдаётся NativeWG.
//
// Диапазоны РАЗВЕДЕНЫ, а не просто «не совпадают по факту»: идентификатор
// kernel-туннеля awgN — это и номер интерфейса OpkgTunN, поэтому его выбирает
// пул, а идентификатор NativeWG — только ключ хранилища, и его выбирают здесь.
// Пересекись диапазоны, и два выбирающих спорили бы за один ключ: пул видит
// записи на диске, но открытую резервацию kernel'а этот перебор не видит, и
// проигравший получал бы «tunnel already exists» без ретрая (F317).
//
// Поэтому пол поднимается выше потолка OpkgTun этой архитектуры. Исторический
// пол 20 сохраняется, пока он и так выше: на mips потолок 16, и ничего не
// меняется; на arm потолок 49, и до #891 kernel туда не заходил, а теперь
// заходит.
//
// Разведение не отменяет вето по занятым идентификаторам у пула: легаси
// NativeWG мог осесть в kernel-диапазоне до разделения, и его ключ по-прежнему
// занят.
func nwgFloor(opkgTunCeiling int) int {
	return max(os5NWGMinIndex, opkgTunCeiling+1)
}

// nextAvailableID — чистая функция выбора ID (вынесена из NextAvailableID
// для тестируемости без глобального osdetect-состояния). Потолок OpkgTun
// приходит аргументом, а не из runtime.GOARCH, чтобы тест не зависел от
// машины, на которой его запустили.
func nextAvailableID(tunnels []AWGTunnel, backend string, is5 bool, opkgTunCeiling int) (string, error) {
	existing := map[int]bool{}

	if is5 {
		if backend != "nativewg" {
			return "", fmt.Errorf("kernel-туннель на OS 5.x получает номер в пуле OpkgTun, а не здесь")
		}
		// Занятые ИДЕНТИФИКАТОРЫ собираются по ВСЕМ туннелям независимо от
		// backend: awgN — ключ хранилища, и двух записей с одним ключом быть
		// не может. Легаси NativeWG на awg12 поэтому продолжает занимать
		// идентификатор в kernel-диапазоне (номер OpkgTun он при этом не
		// занимает — OpkgTunIndex у него false), и наоборот.
		for _, t := range tunnels {
			if num, ok := AWGIdentifierNum(t.ID); ok {
				existing[num] = true
			}
		}
		for i := nwgFloor(opkgTunCeiling); ; i++ {
			if !existing[i] {
				return "awg" + strconv.Itoa(i), nil
			}
		}
	}
	for _, t := range tunnels {
		if rest, ok := strings.CutPrefix(t.ID, "awgm"); ok {
			if num, err := strconv.Atoi(rest); err == nil {
				existing[num] = true
			}
		}
	}
	for i := 0; ; i++ {
		if !existing[i] {
			return "awgm" + strconv.Itoa(i), nil
		}
	}
}

// AWGIdentifierNum — номер из идентификатора вида "awg<цифры>".
//
// Разбор один на проект (opkgtun.Digits): собственный принимал бы "awg-5" как
// −5, потому что это принимает strconv.Atoi, а ручка создания такой
// идентификатор пропускает (tunnelid.Valid). Расхождение двух разборов имени и
// есть содержание #891.
func AWGIdentifierNum(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "awg")
	if !ok {
		return 0, false
	}
	return opkgtun.Digits(rest)
}
