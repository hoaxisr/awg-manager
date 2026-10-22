package zapret

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
)

// Status — снимок состояния zapret для карточек и вкладки.
type Status struct {
	// Installed — найдены каталог с config'ом и переменная опций.
	Installed bool `json:"installed"`
	// Flavor — что именно нашлось: "zapret2" или "zapret".
	Flavor string `json:"flavor,omitempty"`
	Base   string `json:"base,omitempty"`
	Config string `json:"config,omitempty"`
	Binary string `json:"binary,omitempty"`
	// OptVar — NFQWS2_OPT или NFQWS_OPT; в каких именно переменных
	// лежат стратегии, показываем явно, иначе непонятно что править.
	OptVar string `json:"optVar,omitempty"`

	// Version — nfqws2 --version, если удалось получить.
	Version string `json:"version,omitempty"`
	// Package — имя opkg-пакета, если zapret ставился пакетом. В
	// репозитории Entware его нет, поэтому это всегда локальный .ipk.
	Package        string `json:"package,omitempty"`
	PackageVersion string `json:"packageVersion,omitempty"`

	Running      bool         `json:"running"`
	PIDs         []int        `json:"pids,omitempty"`
	ProcessState ProcessState `json:"processState"`
	LastError    string       `json:"lastError,omitempty"`
	// Conflict means the resolver found candidates but could not safely map
	// one selected installation (for example, two live bases or an unknown
	// process owner). Mutating endpoints reject the same condition.
	Conflict bool `json:"conflict,omitempty"`

	InitScript string `json:"initScript,omitempty"`

	// OtherInstalls — прочие найденные установки, кроме Base. На роутере
	// их бывает несколько (zapret v1 в /opt/zapret, zapret2 в
	// /opt/zapret2 и, скажем, zapret22 в /opt/zapret22), и пользователю
	// полезно знать, что AWGM смотрит только на рабочую: иначе непонятно,
	// почему правка в одном каталоге не изменила поведение.
	OtherInstalls []string `json:"otherInstalls,omitempty"`
	// ActiveBase — каталог, из которого реально запущен демон. Пусто, если
	// демон сейчас не запущен и выбрать нечего.
	ActiveBase string `json:"activeBase,omitempty"`

	// StrategyCount / HostlistCount — сводка для бейджа вкладки.
	StrategyCount int `json:"strategyCount"`
	HostlistCount int `json:"hostlistCount"`
	// HasUserHostlist — есть ли непустой пользовательский список.
	HasUserHostlist bool `json:"hasUserHostlist"`
}

// ProcessState — закрытый набор, тот же, что у hydraroute, чтобы фронт
// мог переиспользовать те же ленты и бейджи.
type ProcessState string

const (
	StateNotInstalled ProcessState = "not_installed"
	StateStopped      ProcessState = "stopped"
	StateRunning      ProcessState = "running"
)

// Времена внешних проб. Статус их не ждёт: выбор установки и /proc читаются
// на каждый запрос, а opkg и --version идут в фоне и кэшируются (см.
// packagesCache.due и cachedVersion).
const (
	packagesTTLPresent = 24 * time.Hour
	packagesTTLAbsent  = 10 * time.Minute
	packagesRetryTTL   = 30 * time.Second
	versionTTL         = 6 * time.Hour
	versionRetryTTL    = 30 * time.Second
	// versionProbeTimeout — на каждую попытку --version / -v: демон
	// печатает версию на старте, но если что-то пойдёт не так, не висим.
	versionProbeTimeout = 3 * time.Second
	opkgTimeout         = 30 * time.Second
)

// Service — фасад над обнаружением и правкой zapret.
type Service struct {
	appLog *logging.ScopedLogger
	// dataDir — каталог данных AWGM. В нём хранится последняя реально
	// работавшая база Zapret, чтобы после остановки не выбирать копию по mtime.
	dataDir string

	// opkgListInstalled — seam для тестов: по умолчанию реальный opkg.
	opkgListInstalled func(ctx context.Context) (string, error)
	// runVersion — seam для проб версии.
	runVersion func(ctx context.Context, binary string) (string, error)
	// now — seam для тестов TTL.
	now func() time.Time
	// pub получает инвалидацию статуса, когда фоновая проба принесла новое.
	pub events.Publisher
	// probes — фоновые пробы opkg и --version; тесты ждут их завершения.
	probes sync.WaitGroup

	mu sync.Mutex
	// opMu serializes config, list, and service mutations.
	opMu sync.Mutex

	lastRunningLoaded bool
	lastRunningBase   string

	packages packagesCache
	version  versionCache
}

// NewService создаёт сервис. appLogger может быть nil — ScopedLogger
// сам nil-безопасен. При пустом dataDir последняя база хранится только в памяти.
func NewService(dataDir string, appLogger logging.AppLogger) *Service {
	s := &Service{
		appLog:            logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubZapret),
		dataDir:           dataDir,
		opkgListInstalled: defaultOpkgListInstalled,
		runVersion:        defaultRunVersion,
		now:               time.Now,
	}
	return s
}

// SetEventBus подключает шину: по ней фоновые пробы сообщают, что статус
// изменился.
func (s *Service) SetEventBus(pub events.Publisher) {
	s.mu.Lock()
	s.pub = pub
	s.mu.Unlock()
}

// ErrConflict is returned when the target changed or selection would be
// ambiguous or unsafe. Callers should reread state before retrying.
var ErrConflict = errors.New("Zapret state changed; reload before retrying")

// GetStatus возвращает актуальный снимок и внешних процессов не ждёт. Без
// установки это один ReadDir /opt; с установкой — ещё обход /proc, а версия
// и пакет берутся из кэша, который обновляют фоновые пробы.
func (s *Service) GetStatus() Status {
	r, resolveErr := s.resolveLayoutState()
	if resolveErr != nil {
		if errors.Is(resolveErr, ErrNotInstalled) {
			return Status{ProcessState: StateNotInstalled, LastError: s.errorSnapshot()}
		}
		// Конфликт возможен только при найденных установках.
		st := Status{
			Installed:    true,
			Running:      r.anyOwned,
			Conflict:     true,
			LastError:    resolveErr.Error(),
			ProcessState: StateStopped,
		}
		if st.Running {
			st.ProcessState = StateRunning
		}
		for _, candidate := range r.candidates {
			st.OtherInstalls = append(st.OtherInstalls, candidate.base)
		}
		return st
	}
	l := r.layout

	st := Status{
		Installed:  true,
		Flavor:     flavorFor(l.base, l.optVar),
		Base:       l.base,
		Config:     l.config,
		Binary:     l.binary,
		OptVar:     l.optVar,
		InitScript: l.init,
		ActiveBase: r.activeBase,
	}
	// Прочие установки показываем явно: если на диске лежат zapret v1 и
	// zapret2 рядом с рабочей, пользователь должен видеть, что AWGM смотрит
	// только на неё — иначе правка «в Zapret» не даст эффекта.
	for _, other := range r.candidates {
		if other.base != l.base {
			st.OtherInstalls = append(st.OtherInstalls, other.base)
		}
	}
	managedPIDs := managedDaemonPIDs(l, r.selectedPIDs)
	st.Running = len(managedPIDs) > 0
	st.PIDs = append([]int(nil), managedPIDs...)
	if st.Running {
		st.ProcessState = StateRunning
	} else {
		st.ProcessState = StateStopped
	}

	if l.binary != "" {
		st.Version = s.cachedVersion(l.binary)
	}

	if pkgs := s.installedPackages(); len(pkgs) > 0 {
		// Показываем первый пакет по имени как основной, остальные — в
		// отдельном поле не влезает; версии достаточно для диагностики.
		st.Package = sortedKeys(pkgs)[0]
		st.PackageVersion = pkgs[st.Package]
	}

	if blk, ok := readOptBlock(l.config, l.optVar); ok {
		st.StrategyCount = len(splitProfiles(blk.value))
	}
	for _, h := range listHostlists(l) {
		if h.Exists {
			st.HostlistCount++
		}
		if h.ID == HostlistUser && h.Entries > 0 {
			st.HasUserHostlist = true
		}
	}
	st.LastError = s.errorSnapshot()
	return st
}

func (s *Service) errorSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.version.err == "" {
		return s.packages.err
	}
	if s.packages.err == "" {
		return s.version.err
	}
	return s.packages.err + "; " + s.version.err
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Recheck сбрасывает кэши проб и перечитывает состояние — после управления
// демоном: версия и пакет могли смениться.
func (s *Service) Recheck() Status {
	s.mu.Lock()
	s.packages.expire()
	s.version.expire()
	s.mu.Unlock()
	return s.GetStatus()
}
