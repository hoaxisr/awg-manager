package instancestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ErrMissingPins — raw-клиент или wdtt-server без пинов интерфейсов.
// Пустой пин ронял бы SetDeclared ЦЕЛИКОМ (валидация ExitDecl) либо ведомость
// уборщика, поэтому неполная запись не имеет права лечь на диск: пины обязан
// выделить писатель ДО Replace (посев — seed.go, мутаторы — manager).
var ErrMissingPins = errors.New("не выделены пины интерфейсов")

const fileVersion = 1

// fileName — имя файла store. Дрейф имени читается как чистая установка:
// посев (задача 3) прошёл бы второй раз и продублировал инстансы.
const fileName = "proxy-instances.json"

type fileFormat struct {
	Version    int      `json:"version"`
	SeededFrom []string `json:"seededFrom,omitempty"`
	// CleanupPending/LegacyKernelIfaces/OldGenPIDs — отметка «одноразовая
	// уборка не доведена» и весь её вход. Ложатся транзакцией посева,
	// снимаются отдельной транзакцией после успешного прохода шагов:
	// одноразовые шаги (добивание процессов старого поколения, снос его правил
	// и интерфейсов) иначе теряли бы единственный шанс на любом транзиентном
	// отказе.
	CleanupPending     bool     `json:"cleanupPending,omitempty"`
	LegacyKernelIfaces []string `json:"legacyKernelIfaces,omitempty"`
	OldGenPIDs         []int    `json:"oldGenPids,omitempty"`
	Instances          []Record `json:"instances"`
}

// State — снимок хранилища. Seeded выводится из SeededFrom (§9: флаг —
// оптимизация, не замок; см. решение Р8 о дрейфе от мотивировки спеки).
type State struct {
	Seeded     bool
	SeededFrom []string
	// CleanupPending — одноразовая уборка наследия старого движка ещё не
	// доведена; LegacyKernelIfaces и OldGenPIDs — её вход. Прежние
	// kernel-имена пересобрать на повторе неоткуда: старые конфиги к тому
	// времени могут быть уже удалены. Список процессов старого поколения
	// хранится по другой причине — пересбор с диска ОТРАВЛЕН: pid-файлы
	// старого мира никто не удаляет, лежат они на флеше и переживают
	// перезагрузку, а номер из протухшего файла система могла отдать процессу
	// нового поколения (бинари у обоих миров одни и те же, проверка имени не
	// спасает). Список перестаёт быть свежим, зато перестаёт быть отравленным.
	CleanupPending     bool
	LegacyKernelIfaces []string
	OldGenPIDs         []int
	Records            []Record
}

// Store — единственный владелец proxy-instances.json. Все записи — через
// Replace: чтение-мутация-нормализация-валидация-атомарная запись в одной
// критической секции.
type Store struct {
	dir  string
	path string
	mu   sync.Mutex
}

func New(dataDir string) *Store {
	return &Store{dir: dataDir, path: filepath.Join(dataDir, fileName)}
}

// Load — fail-closed: битый, нечитаемый или НЕВАЛИДНЫЙ файл — ошибка, а не
// пустое состояние: пустое состояние здесь равно «инстансов нет», и на нём
// ведомость снесла бы зеркальные записи (класс требования 1 плана 4).
// Отсутствие файла — законная чистая установка.
//
// Нормализация идёт ПЕРЕД проверкой и на этом пути тоже: файл правят руками и
// пишут старые версии, а ненормализованное значение проскакивает мимо
// инвариантов (mode «  raw  » не равен «raw», и проверка пинов не срабатывает).
// Иначе обещание докстроки Record — «до геттеров битая запись не доживёт» —
// держалось бы только на пути записи.
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (State, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("читать %s: %w", s.path, err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return State{}, fmt.Errorf("разобрать %s: %w", s.path, err)
	}
	st := State{Seeded: len(f.SeededFrom) > 0, SeededFrom: f.SeededFrom,
		CleanupPending: f.CleanupPending, LegacyKernelIfaces: f.LegacyKernelIfaces,
		OldGenPIDs: f.OldGenPIDs, Records: f.Instances}
	for i := range st.Records {
		normalizeRecord(&st.Records[i])
	}
	if err := validateState(st); err != nil {
		return State{}, fmt.Errorf("%s: %w", s.path, err)
	}
	return st, nil
}

// Replace — единственный писатель: загрузить, мутировать, нормализовать,
// проверить инварианты, атомарно записать. Ошибка мутатора или инвариантов —
// диск не тронут.
func (s *Store) Replace(mutate func(*State) error) (State, error) {
	return s.ReplaceChecked(mutate, nil)
}

// ReplaceChecked — Replace с хуком beforeWrite, который зовётся ПОСЛЕ
// нормализации и валидации, ПЕРЕД атомарной записью; его ошибка отменяет
// запись. Нужен manager'у (З1 финального круга): объявление реестру обязано
// видеть НОРМАЛИЗОВАННУЮ и провалидированную запись — хук в мутаторе
// объявлял бы необрезанный peer в зеркальную запись, а отказ валидации после
// успешного объявления оставлял бы реестр впереди диска.
//
// ДВА ЗАПРЕТА ХУКУ (контрольный круг; нарушение не ловится компилятором):
// (1) не звать методы Store — s.mu не реентрантен, это дедлок;
// (2) не править переданное состояние — Records это слайс, конфиги за
// указателями, и правка доехала бы до диска МИМО нормализации и валидации.
// Хук — читатель.
func (s *Store) ReplaceChecked(mutate func(*State) error, beforeWrite func(State) error) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.loadLocked()
	if err != nil {
		return State{}, err
	}
	if err := mutate(&st); err != nil {
		return State{}, err
	}
	for i := range st.Records {
		normalizeRecord(&st.Records[i])
	}
	if err := validateState(st); err != nil {
		return State{}, err
	}
	if beforeWrite != nil {
		if err := beforeWrite(st); err != nil {
			return State{}, err
		}
	}
	f := fileFormat{Version: fileVersion, SeededFrom: st.SeededFrom,
		CleanupPending: st.CleanupPending, LegacyKernelIfaces: st.LegacyKernelIfaces,
		OldGenPIDs: st.OldGenPIDs, Instances: st.Records}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return State{}, err
	}
	if err := storage.AtomicWrite(s.path, data); err != nil {
		return State{}, fmt.Errorf("записать %s: %w", s.path, err)
	}
	st.Seeded = len(st.SeededFrom) > 0
	return st, nil
}

// normalizeWorkers — кратно 9 вниз, минимум 9: ровно то, что сделает сам
// клиент (форк, go_client/main.go:260-263, workersPerGroup = 9), чтобы число
// в store не расходилось с числом в процессе (требование (10) плана 3).
// Ноль не трогаем: «не задано» решает DefaultWorkers по архитектуре в argv.
func normalizeWorkers(n int) int {
	if n <= 0 {
		return 0
	}
	if n < 9 {
		return 9
	}
	return n - n%9
}

// normalizeRecord — нормализация на записи. Store — единственный
// нормализующий писатель (закрывает класс «две копии хранилищ нормализуют
// по-разному на чтении», обзор 2026-08-18).
func normalizeRecord(r *Record) {
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	r.Sub = strings.TrimSpace(r.Sub)
	if r.WdttClient != nil {
		d := r.WdttClient
		d.Mode = strings.TrimSpace(d.Mode)
		if d.Mode == "" {
			d.Mode = "wg" // дефолт старого мира (ConnModeWG)
		}
		d.Listen = strings.TrimSpace(d.Listen)
		d.Peer = strings.TrimSpace(d.Peer)
		// Пины тримятся здесь, а проверка ниже сравнивает с пустой строкой:
		// пробельное имя OpkgTun не совпадёт ни с одним реальным интерфейсом,
		// и пропускать его как «пин есть» нельзя.
		d.NdmsIface = strings.TrimSpace(d.NdmsIface)
		d.RawIface = strings.TrimSpace(d.RawIface)
		d.Workers = normalizeWorkers(d.Workers)
		// Инвариант слотов (паритет normalizePeers, types.go:52-64): Peer
		// главнее — свежий адрес приходит и от тех, кто про слоты не знает
		// (подписка, импорт); слот активного режима зеркалит Peer, слот
		// соседнего режима не трогается; пустой Peer восстанавливается из
		// слота активного режима.
		r.PeerWg, r.PeerRaw = strings.TrimSpace(r.PeerWg), strings.TrimSpace(r.PeerRaw)
		slot := &r.PeerWg
		if d.Mode == "raw" {
			slot = &r.PeerRaw
		}
		if d.Peer != "" {
			*slot = d.Peer
		} else {
			d.Peer = *slot
		}
	}
	if r.WdttServer != nil {
		d := r.WdttServer
		d.Listen = strings.TrimSpace(d.Listen)
		d.NdmsIface = strings.TrimSpace(d.NdmsIface)
		d.WgIface = strings.TrimSpace(d.WgIface)
		d.RawNdmsIface = strings.TrimSpace(d.RawNdmsIface)
		d.RawIface = strings.TrimSpace(d.RawIface)
		if d.RelayMode == "" {
			d.RelayMode = "wg"
		}
		if d.NatMode == "" {
			d.NatMode = "none"
		}
	}
	if r.FreeTurnClient != nil {
		d := r.FreeTurnClient
		d.Listen = strings.TrimSpace(d.Listen)
		d.Peer = strings.TrimSpace(d.Peer)
		d.Sub = strings.TrimSpace(d.Sub)
	}
	if r.FreeTurnServer != nil {
		r.FreeTurnServer.Listen = strings.TrimSpace(r.FreeTurnServer.Listen)
	}
}

// validateState — инварианты ХРАНИЛИЩА (граница названа в брифе задачи:
// roles.Validate() здесь не зовётся — приговор неполному конфигу выносит
// роль фазой failed, а не боот отказом на весь store).
func validateState(st State) error {
	seen := make(map[string]bool, len(st.Records))
	servers := 0
	for _, r := range st.Records {
		if r.ID == "" {
			return fmt.Errorf("запись без id")
		}
		// Уникальность — по Key (роль+id), не по голому id: см. докстроку Key.
		if seen[r.Key()] {
			return fmt.Errorf("инстанс %s объявлен дважды", r.Key())
		}
		seen[r.Key()] = true
		filled := 0
		for _, ok := range []bool{r.WdttClient != nil, r.WdttServer != nil,
			r.FreeTurnClient != nil, r.FreeTurnServer != nil} {
			if ok {
				filled++
			}
		}
		want := map[Kind]bool{KindWdttClient: r.WdttClient != nil,
			KindWdttServer:     r.WdttServer != nil,
			KindFreeTurnClient: r.FreeTurnClient != nil,
			KindFreeTurnServer: r.FreeTurnServer != nil}[r.Kind]
		if filled != 1 || !want {
			return fmt.Errorf("инстанс %s: конфиг не соответствует роли %s", r.ID, r.Kind)
		}
		switch r.Kind {
		case KindWdttServer:
			servers++
			d := r.WdttServer
			if d.NdmsIface == "" || d.WgIface == "" || d.RawNdmsIface == "" || d.RawIface == "" {
				return fmt.Errorf("wdtt-server %s: %w", r.ID, ErrMissingPins)
			}
		case KindWdttClient:
			if r.WdttClient.Mode == "raw" && (r.WdttClient.NdmsIface == "" || r.WdttClient.RawIface == "") {
				return fmt.Errorf("raw-клиент %s: %w", r.ID, ErrMissingPins)
			}
		}
	}
	if servers > 1 {
		return fmt.Errorf("wdtt-server может быть только один: правила AWGM_WDTT, CIDR 10.70.0.0/16 и netfilter-хук не несут инстансного дискриминатора (M-7 плана 3)")
	}
	return nil
}
