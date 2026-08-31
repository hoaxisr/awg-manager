package instancestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
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
	// CleanupPending/LegacyKernelIfaces/OldGenProcs — отметка «одноразовая
	// уборка не доведена» и весь её вход. Ложатся транзакцией посева,
	// снимаются отдельной транзакцией после успешного прохода шагов:
	// одноразовые шаги (добивание процессов старого поколения, снос его правил
	// и интерфейсов) иначе теряли бы единственный шанс на любом транзиентном
	// отказе.
	CleanupPending     bool         `json:"cleanupPending,omitempty"`
	LegacyKernelIfaces []string     `json:"legacyKernelIfaces,omitempty"`
	OldGenProcs        []OldGenProc `json:"oldGenProcs,omitempty"`
	// SkippedSources — старые конфиги, которые посев не смог разобрать и
	// пропустил. Лежат на диске рядом с SeededFrom и по той же причине:
	// повторного посева не будет никогда, и без записи пользователь после
	// первого же перезапуска не узнал бы, что его инстансы не перенеслись.
	SkippedSources []SkippedSource `json:"skippedSources,omitempty"`
	// MovedListen — переезды listen-порта, сделанные посевом при разведении
	// конфликта. Лежат на диске по той же причине, что и SkippedSources:
	// повторного посева не будет, а у человека снаружи мог быть настроен
	// клиент на прежний порт — узнать о переезде он обязан и после
	// перезапуска.
	MovedListen []ListenMove `json:"movedListen,omitempty"`
	Instances   []Record     `json:"instances"`
}

// SkippedSource — пропущенный на посеве старый конфиг: базовое имя файла (как
// в SeededFrom) и причина, по которой он не разобрался.
type SkippedSource struct {
	File   string `json:"file"`
	Reason string `json:"reason,omitempty"`
}

// ListenMove — один переезд listen-адреса на посеве: чей инстанс, откуда и
// куда. Оба адреса целиком, а не голые порты: у проигравшего мог смениться и
// хост, а пользователю нужно узнать ровно тот адрес, который он видел раньше.
type ListenMove struct {
	Instance string `json:"instance"`
	Name     string `json:"name,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// State — снимок хранилища. Seeded выводится из SeededFrom (§9: флаг —
// оптимизация, не замок; см. решение Р8 о дрейфе от мотивировки спеки).
type State struct {
	Seeded     bool
	SeededFrom []string
	// CleanupPending — одноразовая уборка наследия старого движка ещё не
	// доведена; LegacyKernelIfaces и OldGenProcs — её вход. Прежние
	// kernel-имена пересобрать на повторе неоткуда: старые конфиги к тому
	// времени могут быть уже удалены. Список процессов хранится ради
	// ОТПЕЧАТКОВ: сами номера повтор прочитал бы из тех же pid-файлов (их
	// никто не удаляет), а вот время старта каждого номера снимается только
	// на посеве, пока старое поколение ещё живо. Именно отпечаток отличает
	// процесс старого мира от чужого, которому система отдала тот же номер.
	CleanupPending     bool
	LegacyKernelIfaces []string
	OldGenProcs        []OldGenProc
	// SkippedSources — старые конфиги, не перенесённые посевом: разобрать их
	// не удалось, чинить файл некому, а ретрая посева нет. Непустой список
	// запирает сертификацию посева (manager.Boot).
	SkippedSources []SkippedSource
	// MovedListen — инстансы, которым посев сменил listen-адрес, разводя
	// конфликт за порт (амендмент G2). Молчать об этом нельзя: снаружи мог
	// быть настроен клиент на прежний порт.
	MovedListen []ListenMove
	Records     []Record
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

// Dir — каталог данных. Нужен уборке при удалении инстанса: сносится только
// то, что лежит ВНУТРИ него, а путь, уведённый пользователем наружу, не наш.
func (s *Store) Dir() string { return s.dir }

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
		OldGenProcs: f.OldGenProcs, SkippedSources: f.SkippedSources,
		MovedListen: f.MovedListen, Records: f.Instances}
	for i := range st.Records {
		normalizeRecord(&st.Records[i], s.dir)
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
//
// ЗАПРЕТ (1) ДЕЙСТВУЕТ И НА МУТАТОРА, и это не теория: замок берётся ниже,
// один на весь вызов, поэтому любое обращение к Store из mutate — тот же
// вечный дедлок. Ловушка неочевидна, потому что мутатор обычно правит поля
// напрямую, а до Store дотягивается ЧУЖИМИ руками: manager.Update когда-то
// звал оттуда выделение пинов, а прод-аллокаторы читают этот же store, чтобы
// узнать пины соседних записей. Теперь и мутатор, и его вызывающий обязаны
// подготовить всё, что требует чтения store, ДО транзакции.
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
		normalizeRecord(&st.Records[i], s.dir)
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
		OldGenProcs: st.OldGenProcs, SkippedSources: st.SkippedSources,
		MovedListen: st.MovedListen, Instances: st.Records}
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
//
// Ноль — «не задано» — превращается в дефолт ЗДЕСЬ, а не в argv: argv не
// эмитит -n при нулевом значении вовсе (roles/args.go:29), и число потоков
// определял бы встроенный дефолт стороннего бинаря. Старый мир такого
// состояния не знал — normalizeClientConfig (wdtt/service.go:903-905)
// подставлял свой дефолт на каждой записи. goarch параметром, а не
// runtime.GOARCH внутри: дефолт зависит от архитектуры, и проверять его надо
// для всех сразу.
func normalizeWorkers(n int, goarch string) int {
	if n <= 0 {
		n = roles.DefaultWorkers(goarch)
	}
	if n < 9 {
		return 9
	}
	return n - n%9
}

// normalizeRecord — нормализация на записи. Store — единственный
// нормализующий писатель (закрывает класс «две копии хранилищ нормализуют
// по-разному на чтении», обзор 2026-08-18).
//
// dataDir нужен ровно одному дефолту — configDir сервера: старый мир считал
// его от каталога данных на КАЖДОМ старте (wdtt/server.go:362-366).
func normalizeRecord(r *Record, dataDir string) {
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	r.Sub = strings.TrimSpace(r.Sub)
	if r.WdttClient != nil {
		d := r.WdttClient
		// Паритет normalizeConnMode (wdtt/modes.go:14-20): ВСЁ, кроме "raw",
		// становится "wg". Одного гарда пустоты мало — "RAW" из чужого
		// импорта прошёл бы дальше и молча поехал бы WG-путём: argv сверяет
		// режим точным равенством (roles/args.go:39).
		if strings.ToLower(strings.TrimSpace(d.Mode)) == "raw" {
			d.Mode = "raw"
		} else {
			d.Mode = "wg"
		}
		d.Listen = strings.TrimSpace(d.Listen)
		d.Peer = strings.TrimSpace(d.Peer)
		// Пины тримятся здесь, а проверка ниже сравнивает с пустой строкой:
		// пробельное имя OpkgTun не совпадёт ни с одним реальным интерфейсом,
		// и пропускать его как «пин есть» нельзя.
		d.NdmsIface = strings.TrimSpace(d.NdmsIface)
		d.RawIface = strings.TrimSpace(d.RawIface)
		d.Workers = normalizeWorkers(d.Workers, runtime.GOARCH)
		// Дефолты клиента — паритет normalizeClientConfig (wdtt/service.go:899-919)
		// и builder'а старого мира. Здесь, а не в argv: argv не эмитит пустое
		// значение вовсе, и поведение определял бы встроенный дефолт чужого
		// бинаря, а форма показывала бы «не задано» там, где значение было.
		if d.Obfs = strings.TrimSpace(d.Obfs); d.Obfs == "" {
			d.Obfs = "audio"
		}
		if d.Fingerprint = strings.TrimSpace(d.Fingerprint); d.Fingerprint == "" {
			d.Fingerprint = "chrome"
		}
		// Приведение неизвестного — паритет normalizeCaptchaMode
		// (wdtt/service.go:962-969): старый builder звал его на КАЖДОЙ сборке
		// argv, то есть мусор в поле никогда не доезжал до бинаря.
		switch d.CaptchaMode = strings.ToLower(strings.TrimSpace(d.CaptchaMode)); d.CaptchaMode {
		case "auto", "rjs", "wv":
		default:
			d.CaptchaMode = "rjs"
		}
		// Паритет appendVkAuthArgs (wdtt/service.go:953-960): пусто → vkcalls,
		// регистр приводится, неизвестное значение НЕ подменяется (старый мир
		// его тоже пропускал — режимы добавляет форк).
		if d.VKAuthMode = strings.ToLower(strings.TrimSpace(d.VKAuthMode)); d.VKAuthMode == "" {
			d.VKAuthMode = "vkcalls"
		}
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
		// Паритет normalizeServerConfig (wdtt/server.go:464-484): дефолты
		// listen/wgPort/policy и тримы секретов подставлялись на КАЖДОЙ записи.
		if d.Listen = strings.TrimSpace(d.Listen); d.Listen == "" {
			d.Listen = "0.0.0.0:56002"
		}
		if d.WgPort <= 0 {
			d.WgPort = 56001
		}
		d.Password = strings.TrimSpace(d.Password)
		// Паритет serverConfigDir (wdtt/server.go:362-366): пустой путь
		// старый мир считал от каталога данных на каждом старте. Без дефолта
		// сервер, созданный ручкой нового мира (конфиг приходит пустым),
		// падал бы на записи passwords.json fail-closed — «писать некуда»
		// (proxyapp/wdttusers/users.go:200-206). Форма пути та же, что у
		// посева (seed.go:437-440): файл абонентов с выданными адресами не
		// должен «переехать» на апгрейде.
		if d.ConfigDir = strings.TrimSpace(d.ConfigDir); d.ConfigDir == "" && dataDir != "" && r.ID != "" {
			d.ConfigDir = filepath.Join(dataDir, "wdtt", "server", r.ID)
		}
		d.RawListen = strings.TrimSpace(d.RawListen)
		d.DirectListen = strings.TrimSpace(d.DirectListen)
		d.NdmsIface = strings.TrimSpace(d.NdmsIface)
		d.WgIface = strings.TrimSpace(d.WgIface)
		d.RawNdmsIface = strings.TrimSpace(d.RawNdmsIface)
		d.RawIface = strings.TrimSpace(d.RawIface)
		// Паритет normalizePolicy (wdtt/access.go:39-45). Пустое значение —
		// НЕ синоним "none": managed.ApplyPolicyToInterface отвергает его
		// ошибкой «policy must not be empty», и ресурс ndms_access сервера,
		// созданного без политики, не применялся бы вовсе — вместе с ним
		// молчали бы NAT, LAN-ACL и firewall permit.
		if d.Policy = strings.TrimSpace(d.Policy); d.Policy == "" {
			d.Policy = "none"
		}
		// Паритет normalizeConnMode, как и у клиента: всё, кроме "raw" — "wg".
		if strings.ToLower(strings.TrimSpace(d.RelayMode)) == "raw" {
			d.RelayMode = "raw"
		} else {
			d.RelayMode = "wg"
		}
		// Дефолт и приведение неизвестного — ПАРИТЕТ со старым миром
		// (wdtt.normalizeNatMode, access.go:30-37: всё, кроме трёх известных
		// значений, становится full). Разойдись они — мастер раздачи, который
		// создаёт сервер без конфига, получал бы уже заполненное "none" и
		// сохранял его: абоненты подключаются, интернета нет. Тот же дефолт
		// держит и посев: у пользователя, не трогавшего NAT, поле пустое, и
		// "none" здесь снял бы NAT с работавшей раздачи после обновления.
		switch d.NatMode = strings.TrimSpace(d.NatMode); d.NatMode {
		case "full", "internet-only", "none":
		default:
			d.NatMode = "full"
		}
	}
	if r.FreeTurnClient != nil {
		d := r.FreeTurnClient
		d.Listen = strings.TrimSpace(d.Listen)
		d.Peer = strings.TrimSpace(d.Peer)
		d.Sub = strings.TrimSpace(d.Sub)
		// Дефолты клиента FreeTurn — паритет DefaultClientConfig
		// (freeturn/types.go:46-58). В старом мире их подставлял CreateClient,
		// то есть у любого сохранённого клиента поля были заполнены; новая
		// ручка создания принимает пустой конфиг, и без этих строк инстанс
		// уезжал бы на встроенных дефолтах бинаря, а форма показывала бы
		// пустоту.
		if d.Provider = strings.TrimSpace(d.Provider); d.Provider == "" {
			d.Provider = "vk"
		}
		if d.Streams <= 0 {
			d.Streams = 10
		}
		if d.Transport = strings.TrimSpace(d.Transport); d.Transport == "" {
			d.Transport = "tcp"
		}
		if d.Mode = strings.TrimSpace(d.Mode); d.Mode == "" {
			d.Mode = "udp"
		}
		if d.ObfProfile = strings.TrimSpace(d.ObfProfile); d.ObfProfile == "" {
			d.ObfProfile = "none"
		}
		if d.StreamsPerCred <= 0 {
			d.StreamsPerCred = 10
		}
		// Паритет migrateClientConfig (freeturn/migrate.go:64-75) —
		// он приводил и пустое, и неизвестное значение, причём на КАЖДОЙ
		// загрузке файла.
		switch d.Platform = strings.ToLower(strings.TrimSpace(d.Platform)); d.Platform {
		case "desktop", "mobile":
		default:
			d.Platform = "desktop"
		}
		if d.DNSMode = strings.TrimSpace(d.DNSMode); d.DNSMode == "" {
			d.DNSMode = "auto"
		}
	}
	if r.FreeTurnServer != nil {
		d := r.FreeTurnServer
		// Паритет DefaultServerConfig (freeturn/types.go:78-84).
		if d.Listen = strings.TrimSpace(d.Listen); d.Listen == "" {
			d.Listen = "0.0.0.0:56000"
		}
		if d.Mode = strings.TrimSpace(d.Mode); d.Mode == "" {
			d.Mode = "udp"
		}
		if d.ObfProfile = strings.TrimSpace(d.ObfProfile); d.ObfProfile == "" {
			d.ObfProfile = "none"
		}
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
