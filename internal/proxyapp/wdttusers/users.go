package wdttusers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// defaultUserName — имя абонента, которого заводит инвариант непустоты.
const defaultUserName = "Абонент 1"

// Reload — судьба SIGHUP после изменения состава абонентов: узнал ли живой
// сервер об изменении ПРЯМО СЕЙЧАС. Пустое значение — сигнал не посылался
// (чтение списка, переименование: имя серверу безразлично).
type Reload string

const (
	// ReloadDelivered — SIGHUP доставлен живому серверу: passwords.json
	// перечитан, новый состав действует сейчас.
	ReloadDelivered Reload = "delivered"
	// ReloadServerStopped — сервер не запущен: файл записан, состав вступит в
	// силу при следующем запуске.
	ReloadServerStopped Reload = "serverStopped"
	// ReloadFailed — сигнал не доставлен. Файл записан, поэтому доступ
	// появится при следующем запуске; «применено сейчас» обещать нельзя.
	ReloadFailed Reload = "failed"
)

// UserEntry — один абонент наружу. json-имена вербатим старого
// wdtt.ServerClientEntry (api/wdtt_server.go отдавал именно его).
type UserEntry struct {
	Password string `json:"password"`
	Comment  string `json:"comment,omitempty"`
	VkHash   string `json:"vkHash,omitempty"`
	// IsDeactivated — признак из passwords.json (деактивацию ставит форк).
	IsDeactivated bool `json:"isDeactivated"`
	// IsExpired — срок, назначенный сервером, истёк. Такой абонент в
	// passwords.json не пишется и подключиться не может; в списке он остаётся,
	// чтобы пользователь понимал, почему доступ пропал.
	IsExpired bool `json:"isExpired"`
	// IsMainPassword — пароль абонента совпадает с главным паролем сервера.
	// Сам главный пароль наружу не уходит, сравнить на фронте нечем.
	IsMainPassword bool `json:"isMainPassword"`
	// IsAuto — абонента завёл инвариант непустоты (ServerUser.Auto).
	IsAuto bool `json:"isAuto"`
}

// UsersStatus — форма ответа ручек users (вербатим wdtt.ServerClientsStatus).
type UsersStatus struct {
	// Available — passwords.json прочитан. Пусто до первого старта сервера:
	// список при этом всё равно отдаётся, из записи инстанса.
	Available bool        `json:"available"`
	Users     []UserEntry `json:"users"`
	// Reload — результат доставки SIGHUP для ЭТОЙ мутации. Заполняют только
	// ручки, переписывающие passwords.json (добавление, удаление); у чтения и
	// переименования пусто.
	Reload Reload `json:"reload,omitempty"`
}

var (
	// ErrInstanceNotFound — ключа нет в источнике записей (ответ 404).
	ErrInstanceNotFound = errors.New("инстанс не найден")

	// ErrFileNotWritten — частичный успех добавления: абонент уже лежит в
	// записи, а passwords.json записать не удалось. Отката нет намеренно
	// (порядок «запись → файл» держит инвариант непустоты), и отличать этот
	// исход от полного отказа обязана ручка: абонент существует, ссылку по
	// нему выдавать можно, доступ появится при следующем запуске сервера — не
	// «ничего не произошло».
	ErrFileNotWritten = errors.New("абонент создан, но не записан в файл сервера")

	// ErrMainPasswordNotSaved — второй частичный успех добавления: абонент
	// заведён и применён целиком, а пароль сервера, пришедший той же формой,
	// в запись не сохранился. Терять его молча нельзя (без пароля конфиг
	// сервера не проходит валидацию), но и объявлять абонента несозданным —
	// враньё: он в записи, в passwords.json и уже принят живым сервером.
	ErrMainPasswordNotSaved = errors.New("абонент создан, но пароль сервера не сохранён — задайте его в настройках сервера")
)

// Deps — зависимости пакета. Формы RecordSource и Mutator предписаны задачей 8
// и здесь не переобъявляются.
type Deps struct {
	Records wdttlink.RecordSource
	// Mutator — ЕДИНСТВЕННЫЙ путь правки записей: и усыновление, и инвариант
	// непустоты, и ручки ходят через него.
	Mutator wdttlink.Mutator
	// SignalReload просит ЖИВОЙ процесс перечитать passwords.json (SIGHUP по
	// pid из снимка). Первый результат — «доставлен»; false без ошибки —
	// сервер не запущен. Механизма сигнала в internal/proxyrt нет, поэтому
	// его проводит задача 14, и это ЕДИНСТВЕННЫЙ производитель поля reload.
	SignalReload func(key string) (delivered bool, err error)
	// Warn — app-журнал (необязателен). Сюда уходят исходы, которые не
	// роняют операцию, но терять их молча нельзя.
	Warn func(msg string)
	Now  func() time.Time
}

// Service — абоненты сервера: материализация файла, путь старта и ручки.
type Service struct {
	deps Deps

	// mu защищает карту замков; locks — по одному замку на инстанс.
	//
	// Замок нужен по той же причине, что и lockServerClients старого мира:
	// каждая операция — цикл «прочитать файл → поправить запись → переписать
	// файл», и он обязан быть сериализован. Без него чтение, попавшее между
	// вычёркиванием абонента из записи и перезаписью файла, усыновит
	// удалённого обратно. Мутатор менеджера атомарен ПОКАДРОВО и этот цикл не
	// закрывает.
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func New(d Deps) *Service {
	return &Service{deps: d, locks: map[string]*sync.Mutex{}}
}

func (s *Service) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

func (s *Service) warn(msg string) {
	if s.deps.Warn != nil {
		s.deps.Warn(msg)
	}
}

// lock берёт замок инстанса. Мутатор менеджера асинхронен (Post воркеру), в
// наш код он не возвращается — рекурсивного захвата здесь быть не может.
func (s *Service) lock(key string) func() {
	s.mu.Lock()
	m, ok := s.locks[key]
	if !ok {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// serverRecord — запись инстанса и её конфиг сервера. Роль сверяется здесь:
// ID уникален только ВНУТРИ роли, и «default» есть у всех четырёх.
func (s *Service) serverRecord(key string) (instancestore.Record, roles.WdttServerConfig, error) {
	if s.deps.Records == nil {
		return instancestore.Record{}, roles.WdttServerConfig{}, errors.New("источник записей не подключён")
	}
	rec, ok := s.deps.Records.Get(key)
	if !ok {
		return instancestore.Record{}, roles.WdttServerConfig{}, fmt.Errorf("%w: %s", ErrInstanceNotFound, key)
	}
	cfg, err := rec.WdttServerConfig()
	if err != nil {
		return instancestore.Record{}, roles.WdttServerConfig{}, err
	}
	return rec, cfg, nil
}

// ── материализация passwords.json (Г-2, Г-3) ─────────────────────

// Materialize собирает passwords.json из записи и уводит журнал статистики.
//
// Это СЛИЯНИЕ, а не сборка: preparePasswordsJSONForServer читает существующий
// файл и накладывает поверх ТОЛЬКО наши три поля (label, vk_hash, expires_at).
// Всё остальное — привязка абонентов к адресам 10.66.0.x/10.70.0.x (devices),
// счётчики трафика, max_devices, device_ids, ports, is_deactivated — принадлежит
// форку и обязано пережить запись. Наивная пересборка из Record.Users обнулила
// бы всё это разом.
func (s *Service) Materialize(rec instancestore.Record) error {
	return s.materialize(rec, "")
}

// materialize — тот же путь с ЭФФЕКТИВНЫМ главным паролем: у добавления
// пришедший формой пароль ещё не сохранён в запись, а предикат пригодности
// сверяется именно с ним.
func (s *Service) materialize(rec instancestore.Record, mainOverride string) error {
	cfg, err := rec.WdttServerConfig()
	if err != nil {
		return err
	}
	main := cfg.Password
	if override := strings.TrimSpace(mainOverride); override != "" {
		main = override
	}
	dir := strings.TrimSpace(cfg.ConfigDir)
	if dir == "" {
		// Fail-closed. Молчаливый пропуск (так делал старый syncPasswordsJSON)
		// оставил бы сервер без единого пароля — форк на этом падает в
		// log.Fatalf, — а симлинк журнала лёг бы в текущий каталог демона.
		return fmt.Errorf("инстанс %s: не задан configDir сервера — passwords.json писать некуда", rec.Key())
	}
	sanitized, err := syncPasswordsJSON(dir, main, cfg.AdminID, cfg.BotToken, rec.Users, s.now())
	if err != nil {
		return err
	}
	if sanitized {
		// Признак истинен только на НАСТОЯЩЕМ снятии: собственный резерв
		// шлюза sanitizePasswordsDevices пропускает.
		s.warn("из passwords.json сняты устройства с IP шлюза: абонент переподключится и получит свободный адрес")
	}
	// Г-3: форк переписывает server.log каждые ~2 с. Без увода журнала это
	// изнашивающая запись на флеш-память роутера у всех, у кого настройка по
	// умолчанию. Симлинк переживает старты — переставить его достаточно на
	// боте и на смене настройки, но дешевле держать его верным на каждой
	// материализации.
	return redirectServerStatsLog(dir, rec.ID, rec.StatsLog)
}

// ── путь старта (Г-2) ────────────────────────────────────────────

// SyncOnStart — цикл абонентов на пути старта, три ОБЯЗАТЕЛЬНЫХ шага:
// усыновить чужие записи → удержать инвариант непустоты → переписать файл.
// Зовёт фабрика процесса (задача 14).
func (s *Service) SyncOnStart(ctx context.Context, key string) error {
	unlock := s.lock(key)
	defer unlock()
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return err
	}
	// (1) Усыновление — ПЕРВЫМ действием. Без него следующая же материализация
	// отобрала бы доступ у абонентов, заведённых телеграм-ботом или admin-API
	// форка: их нет в записи, значит их не будет и в файле.
	rec, err = s.adopt(ctx, rec, cfg.ConfigDir, cfg.Password)
	if err != nil {
		return err
	}
	// (2) Инвариант непустоты — ПОСЛЕ усыновления: наоборот он завёл бы
	// «Абонент 1» рядом с живым абонентом бота, которого ещё не увидел.
	rec, err = s.ensureUsable(ctx, rec, cfg.Password)
	if err != nil {
		return err
	}
	// (3) Файл — последним, по итоговому составу.
	return s.Materialize(rec)
}

// adopt переносит в запись абонентов passwords.json, которых она ещё не знает,
// и подтягивает срок действия для уже известных. Возвращает АКТУАЛЬНУЮ запись.
func (s *Service) adopt(ctx context.Context, rec instancestore.Record, cfgDir, mainPassword string) (instancestore.Record, error) {
	file, _, err := loadUserEntries(cfgDir)
	if err != nil {
		return rec, err
	}
	next, changed := adoptUsers(rec.Users, file, mainPassword)
	if !changed {
		return rec, nil
	}
	if s.deps.Mutator == nil {
		return rec, errors.New("мутатор записей не подключён")
	}
	if err := s.deps.Mutator.Update(ctx, rec.Key(), func(r *instancestore.Record) error {
		r.Users = next
		return nil
	}); err != nil {
		return rec, err
	}
	return s.reread(rec.Key())
}

// adoptUsers — чистая половина усыновления.
//
// Порядок обхода фиксирован: карта отдаёт ключи вразнобой, а список абонентов
// уезжает в запись и виден в UI.
func adoptUsers(list []instancestore.ServerUser, file map[string]passwordsJSONUser, mainPassword string) ([]instancestore.ServerUser, bool) {
	if len(file) == 0 {
		return list, false
	}
	main := strings.TrimSpace(mainPassword)
	out := slices.Clone(list)
	known := make(map[string]int, len(out))
	for i, u := range out {
		known[strings.TrimSpace(u.Password)] = i
	}
	changed := false
	for _, pass := range slices.Sorted(maps.Keys(file)) {
		entry := file[pass]
		pass = strings.TrimSpace(pass)
		// Запись, равную главному паролю, создаёт admin-API форка. Усыновить
		// её нельзя: главный пароль — ключ администрирования, а не абонент, и
		// после усыновления проверка «пароль сервера не совпадает с паролем
		// абонента» валила бы каждое сохранение конфига.
		if pass == "" || pass == main {
			continue
		}
		if i, ok := known[pass]; ok {
			// У известного обновляем только срок и только ненулевой: админ
			// мог продлить доступ через admin-API форка.
			if entry.ExpiresAt != 0 && out[i].ExpiresAt != entry.ExpiresAt {
				out[i].ExpiresAt = entry.ExpiresAt
				changed = true
			}
			continue
		}
		known[pass] = len(out)
		out = append(out, instancestore.ServerUser{
			Password:  pass,
			Comment:   strings.TrimSpace(entry.Label),
			VkHash:    strings.TrimSpace(entry.VkHash),
			ExpiresAt: entry.ExpiresAt,
		})
		changed = true
	}
	return out, changed
}

// ensureUsable — ЕДИНСТВЕННАЯ опора, которая заводит абонента, и стоит она на
// пути старта, между усыновлением и записью файла: если пароль сервера задан, а
// рабочих абонентов не осталось, заводит «Абонент 1».
//
// Это последняя линия для путей МИМО UI: лечение существующих установок (пароль
// задан давно, абонентов нет), апгрейд, автостарт. Она же покрывает «все
// абоненты просрочены». На путях UI абонента не заводит никто.
func (s *Service) ensureUsable(ctx context.Context, rec instancestore.Record, mainPassword string) (instancestore.Record, error) {
	main := strings.TrimSpace(mainPassword)
	if main == "" || len(UsableUsers(rec.Users, main, s.now())) > 0 {
		return rec, nil
	}
	pass, err := randomUserPassword()
	if err != nil {
		return rec, err
	}
	// Auto=true — поле записи задачи 2: вычислить признак нечем (пользователь
	// переименовывает абонента), поэтому он ХРАНИТСЯ.
	user := instancestore.ServerUser{Password: pass, Comment: defaultUserName, Auto: true}
	if err := s.putUser(ctx, rec.Key(), user); err != nil {
		return rec, err
	}
	return s.reread(rec.Key())
}

// ── ручки ────────────────────────────────────────────────────────

// List отдаёт абонентов: состав из записи, живые признаки — из passwords.json.
// Список не пустеет, даже если файла нет: это и есть смысл единственного
// источника правды.
func (s *Service) List(ctx context.Context, key string) (UsersStatus, error) {
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	// Замок берёт и чтение: без него запрос, попавший между вычёркиванием
	// абонента из записи и перезаписью файла, усыновит удалённого обратно.
	unlock := s.lock(key)
	defer unlock()
	if adopted, err := s.adopt(ctx, rec, cfg.ConfigDir, cfg.Password); err != nil {
		s.warn("записи passwords.json не усыновлены: " + err.Error())
	} else {
		rec = adopted
	}
	return s.status(rec, cfg.ConfigDir, cfg.Password), nil
}

// Add заводит отдельного абонента сервера.
func (s *Service) Add(ctx context.Context, key, password, comment, vkHash, mainPassword string) (UsersStatus, error) {
	// Нормализация входа — ПЕРВЫМ ДЕЛОМ, до всех проверок и любой записи:
	// иначе " pass1 " обойдёт отказ на занятом пароле (в списке пароли уже
	// подрезаны), а " <главный> " проскочит сравнение с главным.
	password = strings.TrimSpace(password)
	comment = strings.TrimSpace(comment)
	vkHash = strings.TrimSpace(vkHash)
	mainPassword = strings.TrimSpace(mainPassword)

	_, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	// Эффективный главный пароль: сохранённый, а при пустом — присланный
	// формой. Сверять только с сохранённым нельзя: на пустом сервере проверка
	// пропустила бы пароль абонента, равный тому, который мы через несколько
	// строк сделаем главным.
	main := strings.TrimSpace(cfg.Password)
	if main == "" {
		main = mainPassword
	}
	if main == "" {
		return UsersStatus{}, errors.New("сначала задайте пароль сервера")
	}

	unlock := s.lock(key)
	status, addErr := s.addLocked(ctx, key, main, password, comment, vkHash)
	unlock()
	if addErr != nil {
		return UsersStatus{}, addErr
	}
	// Побочный эффект «дописать пароль сервера, если он пуст» идёт ПОСЛЕ
	// абонента: на отказе добавления пароль остаётся несохранённым, и сервер
	// не получает состояния «пароль есть, абонента нет».
	if strings.TrimSpace(cfg.Password) == "" {
		if err := s.setMainPassword(ctx, key, main); err != nil {
			// Частичный успех, а не отказ: абонент уже и в записи, и в
			// passwords.json, и SIGHUP по нему ушёл — отката нет и быть не
			// может. Не сохранился только пароль сервера.
			return UsersStatus{}, fmt.Errorf("%w: %w", ErrMainPasswordNotSaved, err)
		}
	}
	return status, nil
}

// addLocked — цикл добавления под уже взятым замком: усыновить → проверить →
// изменить запись → переписать файл → сигнал.
func (s *Service) addLocked(ctx context.Context, key, main, password, comment, vkHash string) (UsersStatus, error) {
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	rec, err = s.adopt(ctx, rec, cfg.ConfigDir, main)
	if err != nil {
		return UsersStatus{}, err
	}
	if password == "" {
		if password, err = randomUserPassword(); err != nil {
			return UsersStatus{}, err
		}
	}
	// Все проверки — до единой записи.
	if password == main {
		return UsersStatus{}, errors.New("пароль совпадает с главным паролем сервера — задайте абоненту другой пароль")
	}
	if err := userPasswordFree(rec.Users, password, s.now()); err != nil {
		return UsersStatus{}, err
	}
	if err := validateMainPassword(main, rec.Users); err != nil {
		return UsersStatus{}, err
	}

	if err := s.putUser(ctx, key, instancestore.ServerUser{Password: password, Comment: comment, VkHash: vkHash}); err != nil {
		return UsersStatus{}, err
	}
	if rec, err = s.reread(key); err != nil {
		return UsersStatus{}, err
	}
	// Файл пишем с ЭФФЕКТИВНЫМ главным паролем: сохранить его в запись мы ещё
	// не успели, а предикат пригодности сверяется именно с ним.
	if err := s.materialize(rec, main); err != nil {
		// Частичный успех: абонент уже в записи (строкой выше) и оттуда никуда
		// не денется — старт сервера перепишет файл сам.
		return UsersStatus{}, fmt.Errorf("%w: %w", ErrFileNotWritten, err)
	}
	// Сигнал — сразу после записи файла, до сбора ответа.
	reload := s.notifyChanged(key)
	st := s.status(rec, cfg.ConfigDir, main)
	st.Reload = reload
	return st, nil
}

// setMainPassword дописывает пароль сервера в запись. Правка ПО МЕСТУ:
// пересборка записи литералом потеряла бы абонентов и слоты адресов.
func (s *Service) setMainPassword(ctx context.Context, key, main string) error {
	if s.deps.Mutator == nil {
		return errors.New("мутатор записей не подключён")
	}
	return s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		if r.WdttServer == nil {
			return fmt.Errorf("инстанс %s: конфиг сервера отсутствует", key)
		}
		r.WdttServer.Password = main
		return nil
	})
}

// Remove удаляет одного абонента сервера.
func (s *Service) Remove(ctx context.Context, key, password string) (UsersStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return UsersStatus{}, errors.New("пароль абонента не задан")
	}
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	if password == strings.TrimSpace(cfg.Password) {
		return UsersStatus{}, errors.New("нельзя удалить основной пароль сервера")
	}

	unlock := s.lock(key)
	defer unlock()
	// Усыновление — ПЕРВЫМ действием. После вычёркивания оно вернуло бы
	// удалённого абонента из ещё не переписанного файла, и удаление стало бы
	// no-op.
	rec, err = s.adopt(ctx, rec, cfg.ConfigDir, cfg.Password)
	if err != nil {
		return UsersStatus{}, err
	}
	remaining := dropUser(rec.Users, password)
	if err := refuseLastUsable(rec.Users, remaining, cfg.Password, s.now()); err != nil {
		return UsersStatus{}, err
	}
	return s.applyUsers(ctx, key, cfg, remaining)
}

// RemoveAll снимает ВСЕХ абонентов сервера. Инвариант тот же, что у удаления
// одного: пока есть хоть один рабочий, снести всех нельзя — живой сервер после
// перечитывания файла остался бы без единого wrap-ключа, а следующий старт
// умер бы вовсе. Смысл ручки — вычистить остатки, когда рабочих уже нет.
func (s *Service) RemoveAll(ctx context.Context, key string) (UsersStatus, error) {
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	unlock := s.lock(key)
	defer unlock()
	rec, err = s.adopt(ctx, rec, cfg.ConfigDir, cfg.Password)
	if err != nil {
		return UsersStatus{}, err
	}
	if err := refuseLastUsable(rec.Users, nil, cfg.Password, s.now()); err != nil {
		return UsersStatus{}, err
	}
	return s.applyUsers(ctx, key, cfg, nil)
}

// applyUsers — общий хвост удаления: записать состав, переписать файл,
// сигналить, собрать ответ.
func (s *Service) applyUsers(ctx context.Context, key string, cfg roles.WdttServerConfig, users []instancestore.ServerUser) (UsersStatus, error) {
	if s.deps.Mutator == nil {
		return UsersStatus{}, errors.New("мутатор записей не подключён")
	}
	if err := s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		r.Users = users
		return nil
	}); err != nil {
		return UsersStatus{}, err
	}
	rec, err := s.reread(key)
	if err != nil {
		return UsersStatus{}, err
	}
	if err := s.Materialize(rec); err != nil {
		return UsersStatus{}, err
	}
	reload := s.notifyChanged(key)
	st := s.status(rec, cfg.ConfigDir, cfg.Password)
	st.Reload = reload
	return st, nil
}

// Rename меняет ИМЯ абонента и больше ничего: пароль, срок действия и
// деактивация принадлежат другим операциям.
//
// passwords.json здесь НЕ переписывается и SIGHUP не шлётся: имя уезжает в файл
// полем label, а по нему сервер никого не пускает и wrap-ключи не собирает.
// Ближайшая штатная запись файла (добавление, удаление, старт) перенесёт новое
// имя сама.
func (s *Service) Rename(ctx context.Context, key, password, name string) (UsersStatus, error) {
	// Трим — первым делом и на обоих входах: пароли в списке уже подрезаны, и
	// " client1 " иначе не нашёлся бы вовсе.
	password = strings.TrimSpace(password)
	name = strings.TrimSpace(name)
	if password == "" {
		return UsersStatus{}, errors.New("пароль абонента не задан")
	}
	// Пустое имя ОТКЛОНЯЕТСЯ, а не очищает: слияние переносит в файл только
	// непустой Comment, а список при пустом Comment показывает label из файла —
	// «очистка» вернула бы старое имя и выглядела бы как молча не применённая
	// правка.
	if name == "" {
		return UsersStatus{}, errors.New("имя абонента не задано")
	}
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}

	unlock := s.lock(key)
	defer unlock()
	// Усыновление — первым действием, как у соседей: без него абонент,
	// заведённый телеграм-ботом и лежащий пока только в passwords.json, получил
	// бы «не найден».
	rec, err = s.adopt(ctx, rec, cfg.ConfigDir, cfg.Password)
	if err != nil {
		return UsersStatus{}, err
	}
	next, ok := setUserComment(rec.Users, password, name)
	if !ok {
		return UsersStatus{}, errors.New("абонент с таким паролем не найден")
	}
	if s.deps.Mutator == nil {
		return UsersStatus{}, errors.New("мутатор записей не подключён")
	}
	if err := s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		r.Users = next
		return nil
	}); err != nil {
		return UsersStatus{}, err
	}
	if rec, err = s.reread(key); err != nil {
		return UsersStatus{}, err
	}
	// Reload остаётся пустым: passwords.json здесь не переписывается,
	// сигналить не о чем — «применено сейчас» было бы неправдой.
	return s.status(rec, cfg.ConfigDir, cfg.Password), nil
}

// ── общее ────────────────────────────────────────────────────────

// reread перечитывает запись после мутации: мутатор — единственный писатель, и
// продолжать со снимка, взятого до него, значило бы затирать чужие правки.
func (s *Service) reread(key string) (instancestore.Record, error) {
	rec, _, err := s.serverRecord(key)
	return rec, err
}

// putUser добавляет или замещает одну запись абонента.
//
// Сравнение — по ПОДРЕЗАННОМУ паролю, как во всём остальном конвейере: пароль
// с пробелами мог попасть в запись ручной правкой или из старых конфигов, и
// сырое сравнение завело бы рядом второй экземпляр того же абонента.
func (s *Service) putUser(ctx context.Context, key string, user instancestore.ServerUser) error {
	if s.deps.Mutator == nil {
		return errors.New("мутатор записей не подключён")
	}
	password := strings.TrimSpace(user.Password)
	return s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		for i, u := range r.Users {
			if strings.TrimSpace(u.Password) == password {
				r.Users[i] = user
				return nil
			}
		}
		r.Users = append(r.Users, user)
		return nil
	})
}

// dropUser убирает одного абонента по ПОДРЕЗАННОМУ паролю. Сырое сравнение не
// находило абонента с пробелами в пароле, и удаление отвечало УСПЕХОМ, ничего
// не удалив, — доступ оставался живым.
func dropUser(list []instancestore.ServerUser, password string) []instancestore.ServerUser {
	out := make([]instancestore.ServerUser, 0, len(list))
	for _, u := range list {
		if strings.TrimSpace(u.Password) != password {
			out = append(out, u)
		}
	}
	return out
}

// setUserComment правит РОВНО Comment одной записи. Замещение записи целиком
// тут не годится: оно стёрло бы ExpiresAt (нашу память об отозванном доступе)
// вместе с VkHash и Auto.
func setUserComment(list []instancestore.ServerUser, password, name string) ([]instancestore.ServerUser, bool) {
	out := slices.Clone(list)
	for i, u := range out {
		if strings.TrimSpace(u.Password) != password {
			continue
		}
		out[i].Comment = name
		return out, true
	}
	return list, false
}

// loadUserEntries читает живые записи passwords.json. Второе значение — «файл
// есть и разобран»; отсутствие файла ошибкой не является.
func loadUserEntries(configDir string) (map[string]passwordsJSONUser, bool, error) {
	if _, err := os.Stat(passwordsJSONPath(configDir)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	doc, err := loadPasswordsJSON(configDir)
	if err != nil {
		return nil, false, err
	}
	return doc.Passwords, true, nil
}

// status накладывает на состав записи живые признаки из passwords.json.
// Зовётся под уже взятым замком; своего захвата здесь нет.
func (s *Service) status(rec instancestore.Record, cfgDir, mainPassword string) UsersStatus {
	file, available, err := loadUserEntries(cfgDir)
	if err != nil {
		s.warn("passwords.json не прочитан: " + err.Error())
	}
	return mergeUsers(rec.Users, file, available, mainPassword, s.now())
}

// mergeUsers собирает список для UI: состав из записи, признаки — из
// passwords.json и из запомненного срока. mainPassword нужен ровно для признака
// IsMainPassword: сам пароль наружу не уходит.
func mergeUsers(users []instancestore.ServerUser, file map[string]passwordsJSONUser, available bool, mainPassword string, now time.Time) UsersStatus {
	// Срок считается ТЕМ ЖЕ предикатом, что и запись файла: непросроченные —
	// это ровно те, кого вернул UsableUsers. Главный пароль ему не передаём: по
	// нему отсев исказил бы признак «истёк».
	usable := make(map[string]struct{}, len(users))
	for _, u := range UsableUsers(users, "", now) {
		usable[u.Password] = struct{}{}
	}
	main := strings.TrimSpace(mainPassword)
	out := UsersStatus{Available: available, Users: []UserEntry{}}
	for _, u := range users {
		pass := strings.TrimSpace(u.Password)
		if pass == "" {
			continue
		}
		e := UserEntry{
			Password: pass,
			Comment:  strings.TrimSpace(u.Comment),
			VkHash:   strings.TrimSpace(u.VkHash),
			// Признак авто-создания хранится в записи: вычислять его по имени
			// нечем — пользователь переименовывает абонента.
			IsAuto: u.Auto,
			// Пустой главный пароль совпадением не станет сам: абонента с
			// пустым паролем цикл сюда не пускает.
			IsMainPassword: pass == main,
		}
		if live, ok := file[pass]; ok {
			e.IsDeactivated = live.IsDeactivated
			if e.Comment == "" {
				e.Comment = strings.TrimSpace(live.Label)
			}
			if e.VkHash == "" {
				e.VkHash = strings.TrimSpace(live.VkHash)
			}
		}
		if _, ok := usable[pass]; !ok {
			e.IsExpired = true
		}
		out.Users = append(out.Users, e)
	}
	return out
}

// notifyChanged просит живой сервер перечитать passwords.json.
//
// Зовётся ТОЛЬКО после успешной записи файла: сигнал без изменившегося файла
// заставил бы сервер перебирать пиры впустую, а после неудачной записи —
// перечитать старое содержимое, выдав его за применённое.
//
// Отказ доставки не роняет ручку: абонент уже записан и в запись, и в файл, и
// следующий старт сервера его подхватит; единственная потеря — применение
// «прямо сейчас», и она возвращается наружу отдельным исходом.
func (s *Service) notifyChanged(key string) Reload {
	if s.deps.SignalReload == nil {
		s.warn("перечитывание passwords.json не подключено: состав вступит в силу при следующем запуске")
		return ReloadFailed
	}
	delivered, err := s.deps.SignalReload(key)
	if err != nil {
		s.warn("перечитывание passwords.json: " + err.Error())
		return ReloadFailed
	}
	if !delivered {
		return ReloadServerStopped
	}
	return ReloadDelivered
}

// userPasswordFree отказывает на ЛЮБОМ уже занятом пароле, двумя текстами.
// Занять пароль просроченного абонента нельзя: замещение записи обнулит нашу
// память о сроке, а слияние всё равно запишет в файл старый expires_at — сервер
// после SIGHUP отвергнет всех. Занять пароль живого нельзя по тому же классу,
// но тише: перезаведение бот-пароля со сроком делает временный доступ
// бессрочным.
func userPasswordFree(users []instancestore.ServerUser, password string, now time.Time) error {
	for _, u := range users {
		if strings.TrimSpace(u.Password) != password {
			continue
		}
		if u.ExpiresAt != 0 && u.ExpiresAt <= now.Unix() {
			return errors.New("пароль принадлежит просроченному абоненту, задайте новый")
		}
		return errors.New("пароль занят живым абонентом")
	}
	return nil
}

// validateMainPassword отвергает главный пароль, совпадающий с паролем
// абонента: wdtt-server использует main_password для WRAP, и совпадение с
// хешем абонента ломает DTLS/WG-рукопожатия.
func validateMainPassword(main string, users []instancestore.ServerUser) error {
	main = strings.TrimSpace(main)
	if main == "" {
		return nil
	}
	for _, u := range users {
		if strings.TrimSpace(u.Password) == main {
			return errors.New("пароль сервера не должен совпадать с паролем клиента")
		}
	}
	return nil
}

// refuseLastUsable отказывает, если после операции рабочих абонентов не
// останется, а до неё они были. Обе величины считает UsableUsers: собственный
// отбор здесь разошёлся бы с тем, что уезжает в passwords.json.
//
// Если рабочих не было и до операции (единственный абонент просрочен), она
// разрешена: запрещать выход из уже сломанного состояния бессмысленно.
// Проверка живёт здесь, а не только в UI: инвариант обязан держаться и для
// запросов мимо нашего фронта.
func refuseLastUsable(before, after []instancestore.ServerUser, mainPassword string, now time.Time) error {
	if len(UsableUsers(before, mainPassword, now)) == 0 {
		return nil
	}
	if len(UsableUsers(after, mainPassword, now)) > 0 {
		return nil
	}
	return errors.New("нельзя удалить последнего рабочего абонента: без единого пароля wdtt-server не запускается")
}

func randomUserPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
