package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/lock"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
// ГРАНИЦА, которую важно понимать: от ПОРЧИ JSON строгое чтение в пути выдачи
// идентификатора не защищает. Там первым отрабатывает List() и уносит битый
// файл в карантин переименованием — к моменту сбора занятости файла уже нет, и
// номер честно свободен. Это не дыра, а работающий по замыслу карантин:
// повреждённая запись выводится из обращения, о чём пользователю сообщают.
// Строгое чтение ловит другой класс — ВРЕМЕННУЮ нечитаемость файла, которую
// List() пропускает молча и без переименования.
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

// Get returns a single tunnel by ID.
func (s *AWGTunnelStore) Get(id string) (*AWGTunnel, error) {
	path := filepath.Join(s.dir, id+".json")
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

// Save writes tunnel to disk.
func (s *AWGTunnelStore) Save(tunnel *AWGTunnel) error {
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	return s.saveLocked(tunnel)
}

// saveLocked — тело Save без захвата dir-lock. Вызывающий ОБЯЗАН держать лок
// "tunnels": лок нерекурсивен (mkdir), повторный захват изнутри — не дедлок,
// а 5-секундный таймаут-отказ.
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

	path := filepath.Join(s.dir, tunnel.ID+".json")
	if err := AtomicWrite(path, data); err != nil {
		return fmt.Errorf("write tunnel file: %w", err)
	}

	return nil
}

// Update атомарно правит запись туннеля: dir-lock → СВЕЖЕЕ чтение записи →
// мутатор → запись. Единственный законный способ изменить существующий
// туннель.
//
// Зачем поверх Save: вызывающий, прочитавший запись сам и потом позвавший
// Save, работает со снимком, снятым ВНЕ лока. Всё, что записали в туннель,
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
	lk, err := lock.WaitLockDir(s.lockName, s.lockDir, s.timeout)
	if err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer lk.Unlock()

	path := filepath.Join(s.dir, id+".json")
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
	path := filepath.Join(s.dir, id+".json")
	_, err := os.Stat(path)
	return err == nil
}

// ClearRuntimeState clears volatile runtime fields (ActiveWAN, StartedAt)
// for a tunnel. Called after Stop/Suspend when the tunnel is no longer active.
func (s *AWGTunnelStore) ClearRuntimeState(id string) {
	stored, err := s.Get(id)
	if err != nil {
		return
	}
	changed := false
	if stored.ActiveWAN != "" {
		stored.ActiveWAN = ""
		changed = true
	}
	if stored.StartedAt != "" {
		stored.StartedAt = ""
		changed = true
	}
	if changed {
		_ = s.Save(stored)
	}
}

const (
	// OS 5.x: карта числовых индексов туннелей:
	//   OpkgTun0..9   — зарезервированы под fakeip-движок sing-box
	//                   (см. internal/singbox/router, tunNDMSName);
	//   OpkgTun10..16 — kernel-AWG туннели (awg10..awg16); потолок 16 —
	//                   прошивочный лимит NDMS на индекс OpkgTun;
	//   awg20+        — NativeWG: чистые storage-ключи, в OpkgTun НЕ
	//                   отображаются (NDMS-интерфейс — WireguardN по
	//                   NWGIndex, kernel-интерфейс — nwgN).
	os5MinIndex = 10
	os5MaxIndex = 16
	// os5NWGMinIndex — начало диапазона ID для NativeWG на OS 5.x. Сверху
	// диапазон не ограничен: реальную ёмкость задают индексы Wireguard0..99
	// в NDMS (nwg.MaxTunnels) и слоты awg_proxy (16 одновременных туннелей
	// с обфускацией на прошивках без нативного ASC), а не диапазон ID.
	// Легаси NativeWG-туннели, созданные до разделения диапазонов, могут
	// занимать awg10..awg16 — это допустимо, kernel-аллокатор просто
	// пропускает занятые ими номера (миграция не выполняется).
	os5NWGMinIndex = 20
)

// NextAvailableID finds the next available tunnel ID for the given backend
// ("kernel" | "nativewg"; любое другое значение, включая пустое, трактуется
// как kernel).
// - OS 5.x, kernel:   awg10..awg16 → OpkgTun10..OpkgTun16 (прошивочный лимит NDMS — 16)
// - OS 5.x, nativewg: awg20, awg21, ... (в OpkgTun не отображаются, см. os5NWGMinIndex)
// - OS 4.x: awgm0, awgm1, ... (uses 'm' prefix, no NDMS; backend не различается)
// occupancy — внешняя занятость номеров OpkgTun (живые интерфейсы плюс пины
// чужих подсистем). Спрашивается ТОЛЬКО в kernel-ветке на OS 5.x: nativewg
// живёт как Wireguard<N>, а на 4.x интерфейсов OpkgTun нет вовсе — платить
// отказом за источник, который им не нужен, они не должны.
func (s *AWGTunnelStore) NextAvailableID(ctx context.Context, backend string, occupancy OpkgTunPins) (string, error) {
	tunnels, err := s.List()
	if err != nil {
		return "", err
	}
	return nextAvailableID(tunnels, backend, osdetect.Is5(), occupancy, ctx)
}

// nextAvailableID — чистая функция выбора ID (вынесена из NextAvailableID
// для тестируемости без глобального osdetect-состояния).
func nextAvailableID(tunnels []AWGTunnel, backend string, is5 bool, occupancy OpkgTunPins, ctx context.Context) (string, error) {
	existing := make(map[int]bool)

	if is5 {
		// Занятые номера собираются по ВСЕМ туннелям независимо от backend —
		// так диапазоны не коллидируют между собой (легаси NativeWG на awg12
		// продолжает занимать номер в kernel-диапазоне, и наоборот).
		for _, t := range tunnels {
			if len(t.ID) > 3 && t.ID[:3] == "awg" {
				if num, err := strconv.Atoi(t.ID[3:]); err == nil {
					existing[num] = true
				}
			}
		}
		if backend == "nativewg" {
			for i := os5NWGMinIndex; ; i++ {
				if !existing[i] {
					return "awg" + strconv.Itoa(i), nil
				}
			}
		}
		// Дальше — kernel: его идентификатор ОДНОВРЕМЕННО является номером
		// интерфейса OpkgTun, поэтому к занятым идентификаторам добавляется
		// занятость номеров, собранная снаружи. Отсутствие источника — не
		// «занятых нет», а незаконченная проводка: молча вернуться к одному
		// лишь хранилищу значит выдать номер, который уже кем-то занят.
		if occupancy == nil {
			return "", fmt.Errorf("источник занятости OpkgTun не задан")
		}
		taken, err := occupancy(ctx)
		if err != nil {
			return "", fmt.Errorf("занятость OpkgTun: %w", err)
		}
		for i := os5MinIndex; i <= os5MaxIndex; i++ {
			if !existing[i] && !taken[i] {
				return "awg" + strconv.Itoa(i), nil
			}
		}
		return "", fmt.Errorf("maximum number of tunnels reached (%d)", os5MaxIndex-os5MinIndex+1)
	} else {
		for _, t := range tunnels {
			if len(t.ID) > 4 && t.ID[:4] == "awgm" {
				if num, err := strconv.Atoi(t.ID[4:]); err == nil {
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
}
