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
func (s *Service) List(key string) (AllowlistStatus, error) {
	_, clientsFile, err := s.serverConfig(key)
	if err != nil {
		return AllowlistStatus{}, err
	}
	return loadAllowlistStatus(clientsFile)
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

	if err := addAllowlistClient(path, clientID, comment); err != nil {
		return AddAllowlistResult{}, err
	}
	st, err := loadAllowlistStatus(path)
	if err != nil {
		return AddAllowlistResult{}, err
	}
	return AddAllowlistResult{AllowlistStatus: st, NeedsRestart: needsRestart}, nil
}

// Remove вычёркивает один Client ID из файла списка.
func (s *Service) Remove(key, clientID string) error {
	_, clientsFile, err := s.serverConfig(key)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(clientsFile)
	if path == "" {
		return fmt.Errorf("allowlist не включён")
	}
	return removeAllowlistClient(path, clientID)
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
