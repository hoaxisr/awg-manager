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

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

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
	// IsAuto — абонент приехал посевом из старого wdtt.json, а не заведён в
	// панели (ServerUser.Auto): единственный, кто сегодня ставит признак, —
	// instancestore/seed.go. Фронт по нему подсказывает, откуда абонент взялся.
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
	// записи, а passwords.json записать не удалось. Отката нет намеренно:
	// запись — источник правды, файл из неё материализуется заново на
	// следующем запуске. Отличать этот исход от полного отказа обязана
	// ручка: абонент существует, ссылку по нему выдавать можно, доступ
	// появится при следующем запуске сервера — не «ничего не произошло».
	ErrFileNotWritten = errors.New("абонент создан, но не записан в файл сервера")
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
// файл и накладывает поверх ТОЛЬКО наши два поля (label, vk_hash).
// Всё остальное — привязка абонентов к адресам 10.66.0.x/10.70.0.x (devices),
// счётчики трафика, max_devices, device_ids, ports — принадлежит
// форку и обязано пережить запись. Наивная пересборка из Record.Users обнулила
// бы всё это разом.
func (s *Service) Materialize(rec instancestore.Record) error {
	return s.materialize(rec)
}

// materialize — тот же путь с ЭФФЕКТИВНЫМ главным паролем: у добавления
// пришедший формой пароль ещё не сохранён в запись, а предикат пригодности
// сверяется именно с ним.
func (s *Service) materialize(rec instancestore.Record) error {
	cfg, err := rec.WdttServerConfig()
	if err != nil {
		return err
	}
	dir := strings.TrimSpace(cfg.ConfigDir)
	if dir == "" {
		// Fail-closed. Молчаливый пропуск (так делал старый syncPasswordsJSON)
		// оставил бы сервер без единого пароля — форк на этом падает в
		// log.Fatalf, — а симлинк журнала лёг бы в текущий каталог демона.
		return fmt.Errorf("инстанс %s: не задан configDir сервера — passwords.json писать некуда", rec.Key())
	}
	sanitized, err := syncPasswordsJSON(dir, rec.Users)
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

// SyncOnStart — цикл абонентов на пути старта, два ОБЯЗАТЕЛЬНЫХ шага:
// усыновить чужие записи → переписать файл. Зовёт фабрика процесса (задача 14).
func (s *Service) SyncOnStart(ctx context.Context, key string) error {
	unlock := s.lock(key)
	defer unlock()
	_, cfg, err := s.serverRecord(key)
	if err != nil {
		return err
	}
	// (1) Усыновление — ПЕРВЫМ действием: запись, лежащая только в
	// passwords.json (например, от прежней версии панели), иначе потеряла бы
	// доступ на следующей же материализации.
	if _, err := s.adopt(ctx, key, cfg.ConfigDir); err != nil {
		return err
	}
	rec, err := s.reread(key)
	if err != nil {
		return err
	}
	// (2) Файл — последним, по итоговому составу.
	return s.Materialize(rec)
}

// mutateUsers — ЕДИНСТВЕННЫЙ способ поправить состав абонентов.
//
// Новый состав И проверки считает КОЛБЭК, от АКТУАЛЬНОГО r.Users, а не от
// снимка, взятого вызывающим. Разница не косметическая: снимок, взятый до
// захвата замка (или просто раньше по времени), при записи целиком ВОСКРЕШАЕТ
// абонента, которого параллельная ручка успела удалить, — материализация
// вернёт его в passwords.json, а SIGHUP восстановит отозванный доступ.
//
// Отказ колбэка отменяет запись целиком: и менеджер (ReplaceChecked), и
// хранилище не сохраняют состояние, если мутатор вернул ошибку. Поэтому
// проверка инварианта внутри колбэка — атомарная «проверь-и-запиши», а не
// «проверь, потом когда-нибудь запиши».
func (s *Service) mutateUsers(ctx context.Context, key string, mutate func([]instancestore.ServerUser) ([]instancestore.ServerUser, error)) error {
	if s.deps.Mutator == nil {
		return errors.New("мутатор записей не подключён")
	}
	return s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		next, err := mutate(r.Users)
		if err != nil {
			return err
		}
		r.Users = next
		return nil
	})
}

// adopt переносит в запись абонентов passwords.json, которых она ещё не знает,
// и подтягивает срок действия для уже известных. Возвращает АКТУАЛЬНУЮ запись.
//
// Работает по КЛЮЧУ, а не по переданной записи: запись читается здесь, уже под
// замком вызывающего.
func (s *Service) adopt(ctx context.Context, key, cfgDir string) (instancestore.Record, error) {
	rec, err := s.reread(key)
	if err != nil {
		return rec, err
	}
	file, _, err := loadUserEntries(cfgDir)
	if err != nil {
		return rec, err
	}
	if _, changed := adoptUsers(rec.Users, file); !changed {
		// Пустая запись store на каждом чтении списка стоила бы полного цикла
		// с объявлением выходов реестру. Решение «писать или нет» принимается
		// по свежепрочитанной записи, а САМ состав всё равно считает колбэк.
		return rec, nil
	}
	if err := s.mutateUsers(ctx, key, func(list []instancestore.ServerUser) ([]instancestore.ServerUser, error) {
		next, _ := adoptUsers(list, file)
		return next, nil
	}); err != nil {
		return rec, err
	}
	return s.reread(key)
}

// adoptUsers — чистая половина усыновления.
//
// Порядок обхода фиксирован: карта отдаёт ключи вразнобой, а список абонентов
// уезжает в запись и виден в UI.
func adoptUsers(list []instancestore.ServerUser, file map[string]passwordsJSONUser) ([]instancestore.ServerUser, bool) {
	if len(file) == 0 {
		return list, false
	}
	out := slices.Clone(list)
	known := make(map[string]int, len(out))
	for i, u := range out {
		known[strings.TrimSpace(u.Password)] = i
	}
	changed := false
	for _, pass := range slices.Sorted(maps.Keys(file)) {
		entry := file[pass]
		pass = strings.TrimSpace(pass)
		if pass == "" {
			continue
		}
		if _, ok := known[pass]; ok {
			continue
		}
		known[pass] = len(out)
		out = append(out, instancestore.ServerUser{
			Password: pass,
			Comment:  strings.TrimSpace(entry.Label),
			VkHash:   strings.TrimSpace(entry.VkHash),
		})
		changed = true
	}
	return out, changed
}

// ── ручки ────────────────────────────────────────────────────────

// List отдаёт абонентов: состав из записи, живые признаки — из passwords.json.
// Список не пустеет, даже если файла нет: это и есть смысл единственного
// источника правды.
func (s *Service) List(ctx context.Context, key string) (UsersStatus, error) {
	// Гейт роли и существования — до замка: он же отвечает 404.
	if _, _, err := s.serverRecord(key); err != nil {
		return UsersStatus{}, err
	}
	// Замок берёт и чтение: без него запрос, попавший между вычёркиванием
	// абонента из записи и перезаписью файла, усыновит удалённого обратно.
	unlock := s.lock(key)
	defer unlock()
	// Конфиг читается ПОД замком. Запись здесь нужна только для ветки ОТКАЗА
	// усыновления: на успешном пути её замещает результат adopt, который сам
	// читает актуальное состояние.
	rec, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	if adopted, err := s.adopt(ctx, key, cfg.ConfigDir); err != nil {
		s.warn("записи passwords.json не усыновлены: " + err.Error())
	} else {
		rec = adopted
	}
	return s.status(rec, cfg.ConfigDir), nil
}

// Add заводит отдельного абонента сервера.
func (s *Service) Add(ctx context.Context, key, password, comment, vkHash string) (UsersStatus, error) {
	// Нормализация входа — ПЕРВЫМ ДЕЛОМ, до всех проверок и любой записи:
	// иначе " pass1 " обойдёт отказ на занятом пароле (в списке пароли уже
	// подрезаны), а " <главный> " проскочит сравнение с главным.
	password = strings.TrimSpace(password)
	comment = strings.TrimSpace(comment)
	vkHash = strings.TrimSpace(vkHash)

	if _, _, err := s.serverRecord(key); err != nil {
		return UsersStatus{}, err
	}

	unlock := s.lock(key)
	defer unlock()
	return s.addLocked(ctx, key, password, comment, vkHash)
}

// addLocked — цикл добавления под уже взятым замком: усыновить → проверить →
// изменить запись → переписать файл → сигнал. Своего захвата здесь нет:
// замок берёт Add, и повторный был бы взаимоблокировкой.
func (s *Service) addLocked(ctx context.Context, key, password, comment, vkHash string) (UsersStatus, error) {
	_, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	rec, err := s.adopt(ctx, key, cfg.ConfigDir)
	if err != nil {
		return UsersStatus{}, err
	}
	if password == "" {
		if password, err = randomUserPassword(); err != nil {
			return UsersStatus{}, err
		}
	}
	user := instancestore.ServerUser{Password: password, Comment: comment, VkHash: vkHash}
	if err := s.mutateUsers(ctx, key, func(list []instancestore.ServerUser) ([]instancestore.ServerUser, error) {
		// Проверки состава — ВНУТРИ колбэка, по актуальному списку: между
		// чтением и записью пароль мог занять параллельный запрос.
		if err := userPasswordFree(list, password); err != nil {
			return nil, err
		}
		return putUser(list, user), nil
	}); err != nil {
		return UsersStatus{}, err
	}
	if rec, err = s.reread(key); err != nil {
		return UsersStatus{}, err
	}
	if err := s.materialize(rec); err != nil {
		// Частичный успех: абонент уже в записи (строкой выше) и оттуда никуда
		// не денется — старт сервера перепишет файл сам.
		return UsersStatus{}, fmt.Errorf("%w: %w", ErrFileNotWritten, err)
	}
	// Сигнал — сразу после записи файла, до сбора ответа.
	reload := s.notifyChanged(key)
	st := s.status(rec, cfg.ConfigDir)
	st.Reload = reload
	return st, nil
}

// Remove удаляет одного абонента сервера.
func (s *Service) Remove(ctx context.Context, key, password string) (UsersStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return UsersStatus{}, errors.New("пароль абонента не задан")
	}
	if _, _, err := s.serverRecord(key); err != nil {
		return UsersStatus{}, err
	}

	unlock := s.lock(key)
	defer unlock()
	// Конфиг перечитывается ПОД замком.
	_, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	// Усыновление — ПЕРВЫМ действием. После вычёркивания оно вернуло бы
	// удалённого абонента из ещё не переписанного файла, и удаление стало бы
	// no-op.
	if _, err := s.adopt(ctx, key, cfg.ConfigDir); err != nil {
		return UsersStatus{}, err
	}
	if err := s.mutateUsers(ctx, key, func(list []instancestore.ServerUser) ([]instancestore.ServerUser, error) {
		remaining := dropUser(list, password)
		// Страж инварианта считается по ТОМУ ЖЕ списку, что и записывается:
		// проверка по снимку разошлась бы с записью.
		if err := refuseLastUsable(list, remaining); err != nil {
			return nil, err
		}
		return remaining, nil
	}); err != nil {
		return UsersStatus{}, err
	}
	return s.finishMutation(ctx, key, cfg)
}

// RemoveAll снимает ВСЕХ абонентов сервера. Инвариант тот же, что у удаления
// одного: пока есть хоть один рабочий, снести всех нельзя — живой сервер после
// перечитывания файла остался бы без единого wrap-ключа, а следующий старт
// умер бы вовсе. Смысл ручки — вычистить остатки, когда рабочих уже нет.
func (s *Service) RemoveAll(ctx context.Context, key string) (UsersStatus, error) {
	if _, _, err := s.serverRecord(key); err != nil {
		return UsersStatus{}, err
	}
	unlock := s.lock(key)
	defer unlock()
	_, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	// Усыновление обязательно и здесь: без него живой абонент бота, лежащий
	// только в файле, не будет сочтён рабочим — инвариант пропустит снос, и
	// доступ у него отберёт следующая материализация.
	if _, err := s.adopt(ctx, key, cfg.ConfigDir); err != nil {
		return UsersStatus{}, err
	}
	if err := s.mutateUsers(ctx, key, func(list []instancestore.ServerUser) ([]instancestore.ServerUser, error) {
		if err := refuseLastUsable(list, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}); err != nil {
		return UsersStatus{}, err
	}
	return s.finishMutation(ctx, key, cfg)
}

// finishMutation — общий хвост мутаций состава: перечитать запись, переписать
// файл, сигналить, собрать ответ.
func (s *Service) finishMutation(ctx context.Context, key string, cfg roles.WdttServerConfig) (UsersStatus, error) {
	rec, err := s.reread(key)
	if err != nil {
		return UsersStatus{}, err
	}
	if err := s.Materialize(rec); err != nil {
		return UsersStatus{}, err
	}
	reload := s.notifyChanged(key)
	st := s.status(rec, cfg.ConfigDir)
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
	if _, _, err := s.serverRecord(key); err != nil {
		return UsersStatus{}, err
	}

	unlock := s.lock(key)
	defer unlock()
	_, cfg, err := s.serverRecord(key)
	if err != nil {
		return UsersStatus{}, err
	}
	// Усыновление — первым действием, как у соседей: без него абонент,
	// заведённый телеграм-ботом и лежащий пока только в passwords.json, получил
	// бы «не найден».
	if _, err := s.adopt(ctx, key, cfg.ConfigDir); err != nil {
		return UsersStatus{}, err
	}
	if err := s.mutateUsers(ctx, key, func(list []instancestore.ServerUser) ([]instancestore.ServerUser, error) {
		next, ok := setUserComment(list, password, name)
		if !ok {
			return nil, errors.New("абонент с таким паролем не найден")
		}
		return next, nil
	}); err != nil {
		return UsersStatus{}, err
	}
	rec, err := s.reread(key)
	if err != nil {
		return UsersStatus{}, err
	}
	// Reload остаётся пустым: passwords.json здесь не переписывается,
	// сигналить не о чем — «применено сейчас» было бы неправдой.
	return s.status(rec, cfg.ConfigDir), nil
}

// ── общее ────────────────────────────────────────────────────────

// reread перечитывает запись: мутатор — единственный писатель, и продолжать со
// снимка, взятого до него, значило бы затирать чужие правки.
func (s *Service) reread(key string) (instancestore.Record, error) {
	rec, _, err := s.serverRecord(key)
	return rec, err
}

// putUser добавляет или замещает одну запись абонента, возвращая НОВЫЙ список.
//
// Сравнение — по ПОДРЕЗАННОМУ паролю, как во всём остальном конвейере: пароль
// с пробелами мог попасть в запись ручной правкой или из старых конфигов, и
// сырое сравнение завело бы рядом второй экземпляр того же абонента.
func putUser(list []instancestore.ServerUser, user instancestore.ServerUser) []instancestore.ServerUser {
	password := strings.TrimSpace(user.Password)
	out := slices.Clone(list)
	for i, u := range out {
		if strings.TrimSpace(u.Password) == password {
			out[i] = user
			return out
		}
	}
	return append(out, user)
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
// тут не годится: оно стёрло бы VkHash и Auto.
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
func (s *Service) status(rec instancestore.Record, cfgDir string) UsersStatus {
	file, available, err := loadUserEntries(cfgDir)
	if err != nil {
		s.warn("passwords.json не прочитан: " + err.Error())
	}
	return mergeUsers(rec.Users, file, available)
}

// mergeUsers собирает список для UI: состав из записи, имя и VK-хеш при
// пустых наших значениях доподставляются из passwords.json.
func mergeUsers(users []instancestore.ServerUser, file map[string]passwordsJSONUser, available bool) UsersStatus {
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
		}
		if live, ok := file[pass]; ok {
			if e.Comment == "" {
				e.Comment = strings.TrimSpace(live.Label)
			}
			if e.VkHash == "" {
				e.VkHash = strings.TrimSpace(live.VkHash)
			}
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

// userPasswordFree отказывает на уже занятом пароле: два абонента с одним
// паролем неразличимы для сервера.
func userPasswordFree(users []instancestore.ServerUser, password string) error {
	for _, u := range users {
		if strings.TrimSpace(u.Password) == password {
			return errors.New("пароль занят живым абонентом")
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
func refuseLastUsable(before, after []instancestore.ServerUser) error {
	if len(UsableUsers(before)) == 0 {
		return nil
	}
	if len(UsableUsers(after)) > 0 {
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
