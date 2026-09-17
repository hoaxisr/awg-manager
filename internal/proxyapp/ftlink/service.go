// Package ftlink — ссылки freeturn:// и список разрешённых Client ID
// freeturn-сервера. Перенос internal/freeturn/{link,allowlist,allowlist_service,names}.go
// на швы нового мира: источник состояния — запись инстанса (ClientsFile —
// поле конфига роли), правка — через Mutator.
package ftlink

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttlink"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
)

// ErrInstanceNotFound — ключа нет в источнике записей (ответ 404).
var ErrInstanceNotFound = errors.New("инстанс не найден")

// Deps — зависимости пакета. Формы RecordSource и Mutator предписаны задачей 8
// и здесь не переобъявляются.
type Deps struct {
	Records wdttlink.RecordSource
	// Mutator нужен ТОЛЬКО включению и выключению списка: они правят
	// ClientsFile конфига роли. Сами записи списка живут в файле.
	Mutator wdttlink.Mutator
	// DataDir — каталог данных awg-manager: в нём заводится файл списка,
	// когда пользователь включает проверку впервые.
	DataDir string
}

// Service — список разрешённых Client ID и ручка разбора ссылки.
type Service struct{ deps Deps }

func New(d Deps) *Service { return &Service{deps: d} }

// serverConfig — запись инстанса и её конфиг роли. Роль сверяется ОБЯЗАТЕЛЬНО:
// список разрешённых есть только у freeturn-сервера, а id «default» носят все
// четыре роли (докстрока instancestore.Record.Key). В старом мире роль задавал
// сам путь (/freeturn/servers/{id}/…), здесь её несёт только ключ.
func (s *Service) serverConfig(key string) (instancestore.Record, string, error) {
	if s.deps.Records == nil {
		return instancestore.Record{}, "", errors.New("источник записей не подключён")
	}
	rec, ok := s.deps.Records.Get(key)
	if !ok {
		return instancestore.Record{}, "", fmt.Errorf("%w: %s", ErrInstanceNotFound, key)
	}
	cfg, err := rec.FreeTurnServerConfig()
	if err != nil {
		return instancestore.Record{}, "", err
	}
	return rec, cfg.ClientsFile, nil
}

// List — состояние списка. Пустой ClientsFile означает «проверка выключена»:
// путь едет в аргумент старта -clients-file, и без него сервер не проверяет id.
// Файл по умолчанию при этом читается всё равно: выключение путь снимает, а
// файл оставляет, и его записи при следующем включении снова получат доступ —
// показывать их обязаны и в выключенном состоянии, иначе «Список пуст» врёт.
func (s *Service) List(key string) (AllowlistStatus, error) {
	rec, clientsFile, err := s.serverConfig(key)
	if err != nil {
		return AllowlistStatus{}, err
	}
	var st AllowlistStatus
	if strings.TrimSpace(clientsFile) != "" {
		st, err = loadAllowlistStatus(clientsFile)
		if err != nil {
			return st, err
		}
	} else {
		st = AllowlistStatus{Enabled: false, Clients: []AllowlistEntry{}}
		if strings.TrimSpace(s.deps.DataDir) == "" {
			return st, nil
		}
		data, dErr := readAllowlistFile(defaultAllowlistPath(s.deps.DataDir, rec.ID))
		if dErr != nil {
			return st, dErr
		}
		st.Clients = allowlistEntriesFromFile(data)
	}
	if err := s.fillLinks(rec.ID, st.Clients); err != nil {
		return st, err
	}
	return st, nil
}

// fillLinks подставляет записям выданные ссылки (#919). Отказ чтения — отказ
// списка: молча показать список без ссылок значит соврать «ссылки нет», и
// владелец полезет перевыпускать её на ровном месте.
func (s *Service) fillLinks(serverID string, clients []AllowlistEntry) error {
	if len(clients) == 0 {
		return nil
	}
	links, err := s.storedLinks(serverID)
	if err != nil {
		return err
	}
	applyLinks(clients, links)
	return nil
}

// storedLinks — выданные ссылки сервера. Без каталога данных файла нет и быть
// не может: пустая карта, а не отказ.
func (s *Service) storedLinks(serverID string) (map[string]string, error) {
	if strings.TrimSpace(s.deps.DataDir) == "" {
		return nil, nil
	}
	return clientLinks(instancestore.FreeTurnLinksPath(s.deps.DataDir, serverID))
}

func applyLinks(clients []AllowlistEntry, links map[string]string) {
	for i := range clients {
		clients[i].Link = links[strings.ToLower(clients[i].ClientID)]
	}
}

// Add вносит Client ID в файл списка. Если список был выключен — включает его,
// заведя путь в конфиге роли, и просит перезапуск: -clients-file читается при
// старте процесса. Добавление в УЖЕ включённый список перезапуска не требует —
// сервер перечитывает файл сам.
func (s *Service) Add(ctx context.Context, key, clientID, comment string) (AddAllowlistResult, error) {
	rec, clientsFile, err := s.serverConfig(key)
	if err != nil {
		return AddAllowlistResult{}, err
	}

	needsRestart := false
	path := strings.TrimSpace(clientsFile)
	if path == "" {
		if s.deps.Mutator == nil {
			return AddAllowlistResult{}, errors.New("правка инстансов не подключена")
		}
		// Fail-closed: без каталога данных путь получился бы относительным, и
		// сервер искал бы список относительно СВОЕГО рабочего каталога —
		// проверка молча пропускала бы всех.
		if strings.TrimSpace(s.deps.DataDir) == "" {
			return AddAllowlistResult{}, errors.New("каталог данных не подключён")
		}
		path = defaultAllowlistPath(s.deps.DataDir, rec.ID)
		if err := s.setClientsFile(ctx, key, path); err != nil {
			return AddAllowlistResult{}, err
		}
		needsRestart = true
	}

	// Ссылки читаются ДО записи списка: их отказ после неё оставил бы
	// наполовину сделанную работу — запись в файле есть, а ручка ответила
	// ошибкой, по которой фронт откатывает созданного абоненту WG-пира.
	links, err := s.storedLinks(rec.ID)
	if err != nil {
		return AddAllowlistResult{}, err
	}

	if err := addAllowlistClient(path, clientID, comment); err != nil {
		return AddAllowlistResult{}, err
	}
	st, err := loadAllowlistStatus(path)
	if err != nil {
		return AddAllowlistResult{}, err
	}
	// Ответ — тот же состав, что отдаёт List: фронт рисует список по нему, и
	// без ссылок у только что внесённого абонента не было бы кнопки «Ссылка»
	// до перезагрузки страницы.
	applyLinks(st.Clients, links)
	return AddAllowlistResult{AllowlistStatus: st, NeedsRestart: needsRestart}, nil
}

// Remove вычёркивает один Client ID из файла списка. У выключенного списка —
// из файла по умолчанию: его записи List показывает, значит, их можно и снять.
func (s *Service) Remove(key, clientID string) error {
	rec, clientsFile, err := s.serverConfig(key)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(clientsFile)
	if path == "" {
		if strings.TrimSpace(s.deps.DataDir) == "" {
			return fmt.Errorf("allowlist не включён")
		}
		path = defaultAllowlistPath(s.deps.DataDir, rec.ID)
	}
	if err := removeAllowlistClient(path, clientID); err != nil {
		return err
	}
	// Ссылка снимается ПОСЛЕ записи списка: осиротевшая ссылка невидима, а вот
	// снятая при живой записи выглядела бы как «абонент есть, ссылки нет».
	if strings.TrimSpace(s.deps.DataDir) == "" {
		return nil
	}
	return dropClientLink(instancestore.FreeTurnLinksPath(s.deps.DataDir, rec.ID), clientID)
}

// Disable выключает проверку Client ID: путь снимается с конфига роли.
// Возвращает needsRestart — как и Add: живой сервер продолжает проверять id,
// пока его не перезапустят. Уже выключенный список ничего не меняет — false.
func (s *Service) Disable(ctx context.Context, key string) (bool, error) {
	_, clientsFile, err := s.serverConfig(key)
	if err != nil {
		return false, err
	}
	if clientsFile == "" {
		return false, nil
	}
	if s.deps.Mutator == nil {
		return false, errors.New("правка инстансов не подключена")
	}
	if err := s.setClientsFile(ctx, key, ""); err != nil {
		return false, err
	}
	return true, nil
}

// setClientsFile правит поле ПО МЕСТУ: пересборка записи литералом потеряла бы
// имя, тумблеры и остальные поля конфига роли.
func (s *Service) setClientsFile(ctx context.Context, key, path string) error {
	return s.deps.Mutator.Update(ctx, key, func(r *instancestore.Record) error {
		if r.FreeTurnServer == nil {
			return fmt.Errorf("инстанс %s: конфиг freeturn-сервера отсутствует", key)
		}
		r.FreeTurnServer.ClientsFile = path
		return nil
	})
}
