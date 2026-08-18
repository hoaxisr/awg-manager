package wdtt

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

// defaultServerClientName — имя абонента, которого заводит инвариант непустоты.
const defaultServerClientName = "Абонент 1"

// loadServerClientEntries читает живые записи passwords.json. Второе значение —
// «файл есть и разобран»; отсутствие файла ошибкой не является.
func loadServerClientEntries(configDir string) (map[string]passwordsJSONUser, bool, error) {
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

// ErrServerClientFileNotWritten — частичный успех добавления: абонент уже лежит
// в wdtt.json, а passwords.json записать не удалось. Отката нет намеренно
// (порядок «конфиг → файл» держит инвариант непустоты), и отличать этот исход от
// полного отказа обязана ручка: абонент существует, ссылку по нему выдавать
// можно, доступ появится при следующем запуске сервера — не «ничего не
// произошло».
var ErrServerClientFileNotWritten = errors.New("абонент создан, но не записан в файл сервера")

// ErrServerMainPasswordNotSaved — второй частичный успех добавления: абонент
// заведён и применён целиком, а пароль сервера, пришедший той же формой, в
// wdtt.json не сохранился. Терять его молча нельзя (без пароля
// StartServerInstance отказывается стартовать), но и объявлять абонента
// несозданным — враньё: он в конфиге, в passwords.json и уже принят живым
// сервером.
var ErrServerMainPasswordNotSaved = errors.New("абонент создан, но пароль сервера не сохранён — задайте его в настройках сервера")

// mergeServerClients собирает список для UI: состав из wdtt.json, признаки —
// из passwords.json и из запомненного срока. mainPassword нужен ровно для
// признака IsMainPassword: сам пароль наружу не уходит.
func mergeServerClients(clients []ServerClient, file map[string]passwordsJSONUser, available bool, mainPassword string, now time.Time) ServerClientsStatus {
	// Срок считается ТЕМ ЖЕ предикатом, что и запись файла: непросроченные — это
	// ровно те, кого вернул UsableServerClients. Главный пароль ему не передаём:
	// в списке абонентов его нет, а отсев по нему исказил бы признак «истёк».
	usable := make(map[string]struct{}, len(clients))
	for _, c := range UsableServerClients(clients, "", now) {
		usable[c.Password] = struct{}{}
	}
	main := strings.TrimSpace(mainPassword)
	out := ServerClientsStatus{Available: available, Users: []ServerClientEntry{}}
	for _, c := range clients {
		pass := strings.TrimSpace(c.Password)
		if pass == "" {
			continue
		}
		e := ServerClientEntry{
			Password: pass,
			Comment:  strings.TrimSpace(c.Comment),
			VkHash:   strings.TrimSpace(c.VkHash),
			// Признак авто-создания хранится в записи: вычислять его по имени
			// нечем — пользователь переименовывает абонента.
			IsAuto: c.Auto,
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

// adoptServerClientsFromFile переносит в wdtt.json записи passwords.json,
// которых конфиг ещё не знает (абоненты телеграм-бота), и подтягивает срок
// действия для уже известных. Возвращает АКТУАЛЬНЫЙ список абонентов.
//
// Зовётся первым действием на всех трёх путях — старт, добавление, удаление, —
// и это не стиль, а требование: усыновление ПОСЛЕ мутации вернуло бы из ещё не
// переписанного файла ровно того абонента, которого мутация только что удалила,
// и удаление стало бы no-op.
func (s *Service) adoptServerClientsFromFile(serverID, cfgDir string) ([]ServerClient, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return nil, err
	}
	file, _, err := loadServerClientEntries(cfgDir)
	if err != nil {
		return nil, err
	}
	if len(file) == 0 {
		return inst.Config.Clients, nil
	}
	main := strings.TrimSpace(inst.Config.Password)
	return s.updateServerClients(serverID, func(list []ServerClient) ([]ServerClient, bool) {
		known := make(map[string]int, len(list))
		for i, c := range list {
			known[strings.TrimSpace(c.Password)] = i
		}
		changed := false
		// Порядок обхода фиксирован: карта отдаёт ключи вразнобой, а список
		// абонентов уезжает в wdtt.json и виден в UI.
		for _, pass := range slices.Sorted(maps.Keys(file)) {
			entry := file[pass]
			pass = strings.TrimSpace(pass)
			// Запись, равную главному паролю, создаёт admin-API форка. Усыновить
			// её нельзя: после этого validateServerMainPassword валила бы КАЖДЫЙ
			// UpdateServerInstance, включая путь старта.
			if pass == "" || pass == main {
				continue
			}
			if i, ok := known[pass]; ok {
				// У известного обновляем только срок и только ненулевой:
				// админ мог продлить доступ через admin-API форка.
				if entry.ExpiresAt != 0 && list[i].ExpiresAt != entry.ExpiresAt {
					list[i].ExpiresAt = entry.ExpiresAt
					changed = true
				}
				continue
			}
			known[pass] = len(list)
			list = append(list, ServerClient{
				Password:  pass,
				Comment:   strings.TrimSpace(entry.Label),
				VkHash:    strings.TrimSpace(entry.VkHash),
				ExpiresAt: entry.ExpiresAt,
			})
			changed = true
		}
		return list, changed
	})
}

// writeServerClientsFile переписывает passwords.json по переданному списку.
// Второе значение — «вычищены устройства с IP шлюза».
//
// Признак идёт в журнал: sanitizePasswordsDevices пропускает НАШ резерв
// (gatewayReserveDeviceID), поэтому истинным он становится только на настоящем
// снятии — у абонента отобрали адрес шлюза, и его устройство переподключится за
// свободным. Раньше бит был вырожден собственным резервом, и сообщение
// печаталось бы на каждой записи файла.
func (s *Service) writeServerClientsFile(serverID, cfgDir string, cfg ServerConfig, clients []ServerClient) (bool, error) {
	sanitized, err := syncPasswordsJSON(cfgDir, cfg.Password, cfg.AdminID, cfg.BotToken, clients)
	if err == nil && sanitized && s.appLog != nil {
		s.appLog.Warn("server-clients", serverID,
			"из passwords.json сняты устройства с IP шлюза: абонент переподключится и получит свободный адрес")
	}
	return sanitized, err
}

// ensureUsableServerClient — ЕДИНСТВЕННАЯ опора, которая заводит абонента, и
// стоит она на пути старта, между усыновлением и записью файла: если пароль
// сервера задан, а рабочих абонентов не осталось, заводит «Абонент 1» и
// включает его в записываемый список.
//
// Это последняя линия для путей МИМО UI: лечение существующих установок (пароль
// задан давно, абонентов нет), апгрейд, ручной запуск. Она же покрывает «все
// абоненты просрочены». На путях UI абонента не заводит никто: фронт не даёт
// запустить сервер без рабочего абонента (Дополнение №5).
func (s *Service) ensureUsableServerClient(serverID string, cfg ServerConfig, clients []ServerClient) ([]ServerClient, error) {
	main := strings.TrimSpace(cfg.Password)
	if main == "" || len(UsableServerClients(clients, main, time.Now())) > 0 {
		return clients, nil
	}
	pass, err := randomClientPassword()
	if err != nil {
		return nil, err
	}
	client := ServerClient{Password: pass, Comment: defaultServerClientName, Auto: true}
	if err := s.putServerClient(serverID, client); err != nil {
		return nil, err
	}
	return append(clients, client), nil
}

// notifyServerClientsChanged просит живой сервер перечитать passwords.json.
// Зовётся ТОЛЬКО после успешной записи файла: сигнал без изменившегося файла
// заставил бы сервер перебирать пиры впустую, а после неудачной записи —
// перечитать старое содержимое, выдав его за применённое.
//
// Отказ доставки не роняет ручку: абонент уже записан и в wdtt.json, и в файл,
// и следующий старт сервера его подхватит; единственная потеря — применение
// «прямо сейчас». Раньше об этой потере узнавал только app-журнал, и ручка
// отвечала успехом, из которого «применено сейчас» и «применится при запуске»
// были неразличимы — поэтому исход возвращается наружу.
func (s *Service) notifyServerClientsChanged(serverID string) ServerClientsReload {
	delivered, err := s.serverProcs.get(serverID).Reload()
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("clients", serverID, "перечитывание passwords.json: "+err.Error())
		}
		return ReloadFailed
	}
	if !delivered {
		return ReloadServerStopped
	}
	return ReloadDelivered
}

// syncServerClientsOnStart — цикл абонентов на пути старта: усыновить и
// переписать passwords.json. Замок берётся здесь, ВНУТРИ serverStartLock
// (порядок захвата однонаправленный): старт бежит из супервизора сам по себе, и
// его цикл обязан быть сериализован с ручками так же, как они сериализованы
// между собой.
func (s *Service) syncServerClientsOnStart(serverID, cfgDir string, cfg ServerConfig) error {
	unlock := s.lockServerClients(serverID)
	defer unlock()
	clients, err := s.adoptServerClientsFromFile(serverID, cfgDir)
	if err != nil {
		return err
	}
	// Опора стоит ЗДЕСЬ, а не внутри writeServerClientsFile: тот же файл пишут
	// ручки абонентов, а на путях UI абонент за пользователя не заводится
	// (Дополнение №5). Удаление последнего просроченного намеренно оставляет
	// сервер без рабочих — запустить его не даст фронт; если запуск всё же
	// случится мимо UI (супервизор, автостарт, апгрейд), абонента заведёт эта
	// опора, и он будет виден с бейджем «заведён автоматически».
	clients, err = s.ensureUsableServerClient(serverID, cfg, clients)
	if err != nil {
		return err
	}
	_, err = s.writeServerClientsFile(serverID, cfgDir, cfg, clients)
	return err
}

// ListServerClients отдаёт абонентов сервера: состав из wdtt.json, живые
// признаки — из passwords.json. Список не пустеет, даже если файла нет: это и
// есть смысл единственного источника правды.
func (s *Service) ListServerClients(serverID string) (ServerClientsStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	// Замок берёт и GET: без него чтение, попавшее между вычёркиванием абонента
	// из wdtt.json и перезаписью файла, усыновит удалённого обратно.
	unlock := s.lockServerClients(serverID)
	defer unlock()
	clients := inst.Config.Clients
	if adopted, err := s.adoptServerClientsFromFile(serverID, cfgDir); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("clients", serverID, "записи passwords.json не усыновлены: "+err.Error())
		}
	} else {
		clients = adopted
	}
	return s.serverClientsStatus(serverID, cfgDir, inst.Config.Password, clients), nil
}

// AddServerClient заводит отдельного абонента сервера.
func (s *Service) AddServerClient(serverID, password, comment, vkHash, mainPassword string) (ServerClientsStatus, error) {
	// Нормализация входа — ПЕРВЫМ ДЕЛОМ, до всех проверок и любой записи: иначе
	// " pass1 " обойдёт отказ на занятом пароле (в списке пароли уже подрезаны),
	// а " <главный> " проскочит сравнение с главным и будет валить каждый
	// UpdateServerInstance через validateServerMainPassword.
	password = strings.TrimSpace(password)
	comment = strings.TrimSpace(comment)
	vkHash = strings.TrimSpace(vkHash)
	mainPassword = strings.TrimSpace(mainPassword)

	inst, err := s.serverInstance(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	// Эффективный главный пароль: сохранённый, а при пустом — присланный формой.
	// Сверять с inst.Config.Password нельзя: на пустом сервере проверка
	// пропустила бы пароль абонента, равный тому, который мы через несколько
	// строк сделаем главным.
	main := strings.TrimSpace(inst.Config.Password)
	if main == "" {
		main = mainPassword
	}
	if main == "" {
		return ServerClientsStatus{}, fmt.Errorf("сначала задайте пароль сервера")
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return ServerClientsStatus{}, err
	}

	// Замок снимается ДО побочного эффекта и до сбора ответа: UpdateServerInstance
	// под ним звать нельзя (нереентрантный s.mu плюс собственный цикл абонентов
	// в задаче 4).
	unlock := s.lockServerClients(serverID)
	status, addErr := s.addServerClientLocked(serverID, cfgDir, inst.Config, main, password, comment, vkHash)
	unlock()
	if addErr != nil {
		return ServerClientsStatus{}, addErr
	}
	// Побочный эффект «дописать пароль сервера, если он пуст» идёт ПОСЛЕ
	// абонента: на отказе добавления пароль остаётся несохранённым, и сервер не
	// получает состояния «пароль есть, абонента нет». (Раньше у порядка была
	// вторая причина — первым сработал бы инвариант непустоты и завёл бы
	// автоматического абонента рядом с заказанным; опора на этом пути снята.)
	if strings.TrimSpace(inst.Config.Password) == "" {
		cfg := inst.Config
		cfg.Password = main
		if _, err := s.UpdateServerInstance(serverID, cfg); err != nil {
			// Частичный успех, а не отказ: абонент уже и в wdtt.json, и в
			// passwords.json, и SIGHUP по нему ушёл — отката нет и быть не
			// может. Не сохранился только пароль сервера, и цена этому —
			// отказ следующего старта («укажите пароль подключения»),
			// поэтому исход отличается и от полного отказа, и от SH-26.
			return ServerClientsStatus{}, fmt.Errorf("%w: %w", ErrServerMainPasswordNotSaved, err)
		}
	}
	return status, nil
}

// addServerClientLocked — цикл добавления под уже взятым lockServerClients:
// усыновить → проверить → изменить wdtt.json → записать файл.
func (s *Service) addServerClientLocked(serverID, cfgDir string, cfg ServerConfig, main, password, comment, vkHash string) (ServerClientsStatus, error) {
	clients, err := s.adoptServerClientsFromFile(serverID, cfgDir)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	if password == "" {
		if password, err = randomClientPassword(); err != nil {
			return ServerClientsStatus{}, err
		}
	}
	// Все проверки — до единой записи.
	if password == main {
		return ServerClientsStatus{}, fmt.Errorf("пароль совпадает с главным паролем сервера — задайте абоненту другой пароль")
	}
	if err := serverClientPasswordFree(clients, password, time.Now()); err != nil {
		return ServerClientsStatus{}, err
	}
	if err := validateServerMainPassword(main, clients); err != nil {
		return ServerClientsStatus{}, err
	}

	if err := s.putServerClient(serverID, ServerClient{Password: password, Comment: comment, VkHash: vkHash}); err != nil {
		return ServerClientsStatus{}, err
	}
	clients, err = s.serverClients(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	// Файл пишем с ЭФФЕКТИВНЫМ главным паролем: сохранить его в конфиг мы ещё
	// не успели, а предикат пригодности сверяется именно с ним.
	fileCfg := cfg
	fileCfg.Password = main
	if _, err := s.writeServerClientsFile(serverID, cfgDir, fileCfg, clients); err != nil {
		// Частичный успех, а не отказ: абонент уже в wdtt.json (строкой выше) и
		// оттуда никуда не денется — старт сервера перепишет файл сам. Ручка
		// обязана сказать об этом отдельным исходом, иначе UI объявит абонента
		// несозданным, а он есть и виден в списке.
		return ServerClientsStatus{}, fmt.Errorf("%w: %w", ErrServerClientFileNotWritten, err)
	}
	// Сигнал — сразу после записи файла, до сбора ответа: между ними ничего
	// зависящего от порядка нет, а лишняя задержка перед применением есть.
	reload := s.notifyServerClientsChanged(serverID)
	st := s.serverClientsStatus(serverID, cfgDir, main, clients)
	st.Reload = reload
	return st, nil
}

// RenameServerClient меняет ИМЯ абонента и больше ничего: пароль, срок действия
// и деактивация принадлежат другим операциям.
//
// passwords.json здесь НЕ переписывается и SIGHUP не шлётся: имя уезжает в файл
// полем label, а по нему сервер никого не пускает и wrap-ключи не собирает.
// Ближайшая штатная запись файла (добавление, удаление, старт) перенесёт новое
// имя сама — preparePasswordsJSONForServer берёт label из Comment конфига,
// когда тот непуст.
func (s *Service) RenameServerClient(serverID, password, name string) (ServerClientsStatus, error) {
	// Трим — первым делом и на обоих входах, как в AddServerClient: пароли в
	// списке уже подрезаны, и " client1 " иначе не нашёлся бы вовсе.
	password = strings.TrimSpace(password)
	name = strings.TrimSpace(name)
	if password == "" {
		return ServerClientsStatus{}, fmt.Errorf("пароль абонента не задан")
	}
	// Пустое имя ОТКЛОНЯЕТСЯ, а не очищает: мерж переносит в файл только непустой
	// Comment (ветки else там нет намеренно), а mergeServerClients при пустом
	// Comment показывает label из файла — «очистка» вернула бы старое имя и
	// выглядела бы как молча не применённая правка.
	if name == "" {
		return ServerClientsStatus{}, fmt.Errorf("имя абонента не задано")
	}
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return ServerClientsStatus{}, err
	}

	unlock := s.lockServerClients(serverID)
	defer unlock()
	// Усыновление — первым действием, как у соседей: без него абонент, заведённый
	// телеграм-ботом и лежащий пока только в passwords.json, получил бы «не
	// найден», а конкурентное удаление соседа воскресило бы его из ещё не
	// переписанного файла.
	clients, err := s.adoptServerClientsFromFile(serverID, cfgDir)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	if !hasServerClientPassword(clients, password) {
		return ServerClientsStatus{}, fmt.Errorf("абонент с таким паролем не найден")
	}
	if err := s.setServerClientComment(serverID, password, name); err != nil {
		return ServerClientsStatus{}, err
	}
	clients, err = s.serverClients(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	// Ответ собирает serverClientsStatus — она замка не берёт, поэтому хвост под
	// defer unlock() самодедлока не даёт. ListServerClients звать здесь нельзя
	// именно поэтому.
	//
	// Reload остаётся пустым: passwords.json здесь не переписывается, сигналить
	// не о чем — «применено сейчас» после переименования было бы неправдой.
	return s.serverClientsStatus(serverID, cfgDir, inst.Config.Password, clients), nil
}

// setServerClientComment правит РОВНО Comment одной записи wdtt.json.
// putServerClient тут не годится: он замещает запись целиком и стёр бы ExpiresAt
// (нашу память об отозванном доступе) вместе с VkHash.
func (s *Service) setServerClientComment(serverID, password, name string) error {
	_, err := s.updateServerClients(serverID, func(list []ServerClient) ([]ServerClient, bool) {
		for i, c := range list {
			if strings.TrimSpace(c.Password) != password || c.Comment == name {
				continue
			}
			list[i].Comment = name
			return list, true
		}
		return list, false
	})
	return err
}

// hasServerClientPassword — членство по подрезанному паролю.
func hasServerClientPassword(clients []ServerClient, password string) bool {
	for _, c := range clients {
		if strings.TrimSpace(c.Password) == password {
			return true
		}
	}
	return false
}

// RemoveServerClient удаляет одного абонента сервера.
func (s *Service) RemoveServerClient(serverID, password string) (ServerClientsStatus, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return ServerClientsStatus{}, fmt.Errorf("пароль абонента не задан")
	}
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	if password == strings.TrimSpace(inst.Config.Password) {
		return ServerClientsStatus{}, fmt.Errorf("нельзя удалить основной пароль сервера")
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return ServerClientsStatus{}, err
	}

	unlock := s.lockServerClients(serverID)
	defer unlock()
	// Усыновление — ПЕРВЫМ действием. После вычёркивания оно вернуло бы
	// удалённого абонента из ещё не переписанного файла, и удаление стало бы
	// no-op.
	adopted, err := s.adoptServerClientsFromFile(serverID, cfgDir)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	// Страж инварианта на удалении: последнего РАБОЧЕГО абонента удалить нельзя
	// — живой сервер после перечитывания файла остался бы без единого
	// wrap-ключа, а следующий старт умер бы вовсе. Если рабочих не было и до
	// операции (единственный абонент просрочен), удаление разрешено: запрещать
	// выход из уже сломанного состояния бессмысленно. Абонента взамен здесь
	// никто не заводит — сервер остаётся без рабочих, и запустить его фронт не
	// даст, пока не добавят нового (Дополнение №5).
	//
	// Проверка живёт здесь, а не только в UI: инвариант обязан держаться и для
	// запросов мимо нашего фронта.
	if err := refuseLastUsableServerClient(adopted, inst.Config.Password, password, time.Now()); err != nil {
		return ServerClientsStatus{}, err
	}
	if err := s.dropServerClient(serverID, password); err != nil {
		return ServerClientsStatus{}, err
	}
	clients, err := s.serverClients(serverID)
	if err != nil {
		return ServerClientsStatus{}, err
	}
	if _, err := s.writeServerClientsFile(serverID, cfgDir, inst.Config, clients); err != nil {
		return ServerClientsStatus{}, err
	}
	reload := s.notifyServerClientsChanged(serverID)
	st := s.serverClientsStatus(serverID, cfgDir, inst.Config.Password, clients)
	st.Reload = reload
	return st, nil
}

// serverClients — актуальный список абонентов из wdtt.json.
func (s *Service) serverClients(serverID string) ([]ServerClient, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return nil, err
	}
	return inst.Config.Clients, nil
}

// serverClientsStatus накладывает на список живые признаки из passwords.json.
// Зовётся под уже взятым замком; отдельного захвата здесь нет — иначе хвост
// ручки под defer unlock() дал бы самодедлок.
//
// mainPassword передаёт вызывающий: у добавления он ЭФФЕКТИВНЫЙ (присланный
// формой пароль ещё не сохранён в конфиг), у остальных — сохранённый.
func (s *Service) serverClientsStatus(serverID, cfgDir, mainPassword string, clients []ServerClient) ServerClientsStatus {
	file, available, err := loadServerClientEntries(cfgDir)
	if err != nil && s.appLog != nil {
		s.appLog.Warn("clients", serverID, "passwords.json не прочитан: "+err.Error())
	}
	return mergeServerClients(clients, file, available, mainPassword, time.Now())
}

// serverClientPasswordFree отказывает на ЛЮБОМ уже занятом пароле, двумя
// текстами. Занять пароль просроченного абонента нельзя: putServerClient молча
// замещает запись, обнулив нашу память о сроке, а мерж всё равно запишет в файл
// старый expires_at — сервер после SIGHUP отвергнет всех. Занять пароль живого
// нельзя по тому же классу, но тише: перезаведение бот-пароля со сроком делает
// временный доступ бессрочным.
func serverClientPasswordFree(clients []ServerClient, password string, now time.Time) error {
	for _, c := range clients {
		if strings.TrimSpace(c.Password) != password {
			continue
		}
		if c.ExpiresAt != 0 && c.ExpiresAt <= now.Unix() {
			return fmt.Errorf("пароль принадлежит просроченному абоненту, задайте новый")
		}
		return fmt.Errorf("пароль занят живым абонентом")
	}
	return nil
}

// refuseLastUsableServerClient отказывает, если после удаления рабочих абонентов
// не останется, а до удаления они были. Обе величины считает UsableServerClients:
// собственный отбор здесь разошёлся бы с тем, что уезжает в passwords.json.
func refuseLastUsableServerClient(clients []ServerClient, mainPassword, password string, now time.Time) error {
	if len(UsableServerClients(clients, mainPassword, now)) == 0 {
		return nil
	}
	remaining := make([]ServerClient, 0, len(clients))
	for _, c := range clients {
		if strings.TrimSpace(c.Password) != password {
			remaining = append(remaining, c)
		}
	}
	if len(UsableServerClients(remaining, mainPassword, now)) > 0 {
		return nil
	}
	return errors.New("нельзя удалить последнего рабочего абонента: без единого пароля wdtt-server не запускается")
}

func randomClientPassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
