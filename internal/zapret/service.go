package zapret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

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

// Времена. Обнаружение разделено на два яруса по стоимости, и это
// единственное место, где нужны числа:
//
//   - resolveLayoutState reads the installation tree and /proc for each
//     request, so selection always follows current process ownership.
//   - listInstalledPackages — opkg list-installed, около секунды на
//     MIPS. Кэшируется. Симметричный TTL здесь был бы ошибкой:
//     отсутствие zapret мы хотим узнать быстро (пользователь только что
//     поставил пакет и ждёт, что он появится), а наличие — редко
//     меняется. Это ровно та асимметрия, что применена в
//     singbox/router/bypassset для ipset.
const (
	packagesTTLPresent = 24 * time.Hour
	packagesTTLAbsent  = 10 * time.Minute
	versionTTL         = 6 * time.Hour
	// A missing installation is common on routers without Zapret. Check the
	// /opt directory mtime on each status read, but avoid walking /opt and
	// /proc again until it changes or this short fallback expires.
	statusAbsentTTL = 10 * time.Second
	versionRetryTTL = 30 * time.Second
	// versionProbeTimeout ограничивает --version: демон печатает версию
	// на старте, но если что-то пойдёт не так, не будем висеть.
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

	mu sync.Mutex
	// opMu serializes config, list, and service mutations.
	opMu sync.Mutex

	lastRunningLoaded bool
	lastRunningBase   string
	// packagesCached хранит пары имя→версия целиком: терять версию нельзя,
	// она идёт в карточку установки.
	packagesCached  map[string]string
	packagesAt      time.Time
	packagesErrorAt time.Time
	probeInFlight   bool
	absentStatus    Status
	absentStatusAt  time.Time
	absentRootStamp string

	versionCached        string
	versionAt            time.Time
	versionFinger        string
	versionError         string
	versionErrorAt       time.Time
	versionErrorFinger   string
	versionProbeInFlight bool

	lastError string
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

// ErrConflict is returned when the target changed or selection would be
// ambiguous or unsafe. Callers should reread state before retrying.
var ErrConflict = errors.New("Zapret state changed; reload before retrying")

// GetStatus возвращает актуальный снимок. Дёшево: os.Stat плюс, возможно,
// кэш версии и пакетов.
func (s *Service) GetStatus() Status {
	if st, ok := s.cachedAbsentStatus(); ok {
		return st
	}
	r, resolveErr := s.resolveLayoutState()
	if resolveErr != nil {
		if errors.Is(resolveErr, ErrNotInstalled) {
			st := Status{ProcessState: StateNotInstalled, LastError: s.errorSnapshot()}
			s.rememberAbsentStatus(st)
			return st
		}
		s.clearAbsentStatus()
		st := Status{
			Installed: len(r.candidates) > 0,
			Running:   r.anyOwned,
			Conflict:  true,
			LastError: resolveErr.Error(),
		}
		if st.Running {
			st.ProcessState = StateRunning
		} else if st.Installed {
			st.ProcessState = StateStopped
		} else {
			st.ProcessState = StateNotInstalled
		}
		for _, candidate := range r.candidates {
			st.OtherInstalls = append(st.OtherInstalls, candidate.base)
		}
		return st
	}
	s.clearAbsentStatus()
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
		st.Version = s.getVersionCached(l.binary)
	}

	if pkgs := s.listInstalledPackages(context.Background()); len(pkgs) > 0 {
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
	if s.versionError == "" {
		return s.lastError
	}
	if s.lastError == "" {
		return s.versionError
	}
	return s.lastError + "; " + s.versionError
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) cachedAbsentStatus() (Status, bool) {
	rootStamp := statusRootStamp()
	now := s.currentTime()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.absentStatusAt.IsZero() || now.Sub(s.absentStatusAt) < 0 || now.Sub(s.absentStatusAt) >= statusAbsentTTL || rootStamp != s.absentRootStamp {
		return Status{}, false
	}
	return s.absentStatus, true
}

func (s *Service) rememberAbsentStatus(st Status) {
	rootStamp := statusRootStamp()
	s.mu.Lock()
	s.absentStatus = st
	s.absentStatusAt = s.currentTime()
	s.absentRootStamp = rootStamp
	s.mu.Unlock()
}

func (s *Service) clearAbsentStatus() {
	s.mu.Lock()
	s.absentStatus = Status{}
	s.absentStatusAt = time.Time{}
	s.absentRootStamp = ""
	s.mu.Unlock()
}

// statusRootStamp is intentionally only a stat: a changed /opt mtime means a
// Zapret-shaped directory may have appeared. The TTL below covers changes
// beneath an already-existing candidate directory.
func statusRootStamp() string {
	root := filepath.Clean(optRoot)
	info, err := os.Stat(root)
	if err == nil {
		return fmt.Sprintf("%s|%d|%d|%s", root, info.ModTime().UnixNano(), info.Size(), info.Mode())
	}
	parent := filepath.Dir(root)
	parentInfo, parentErr := os.Stat(parent)
	if parentErr == nil {
		return fmt.Sprintf("%s|missing|%d|%d", root, parentInfo.ModTime().UnixNano(), parentInfo.Size())
	}
	return root + "|missing"
}

// Recheck сбрасывает кэш и перечитывает состояние — для кнопки
// «Обновить» и после действий пользователя.
func (s *Service) Recheck() Status {
	s.mu.Lock()
	s.packagesAt = time.Time{}
	s.packagesErrorAt = time.Time{}
	s.absentStatusAt = time.Time{}
	s.absentRootStamp = ""
	s.versionAt = time.Time{}
	s.versionFinger = ""
	s.versionError = ""
	s.versionErrorAt = time.Time{}
	s.versionErrorFinger = ""
	s.mu.Unlock()
	return s.GetStatus()
}
