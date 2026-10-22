package zapret

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
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

type layoutResolution struct {
	layout       layout
	candidates   []layout
	activeBase   string
	selectedPIDs []int
	anyOwned     bool
}

// resolveLayout returns the one installation that is safe to operate on.
// The live process is authoritative. When stopped, a persisted last-running
// base is preferred; modification time is never used as a selection signal.
func (s *Service) resolveLayout() (layout, error) {
	r, err := s.resolveLayoutState()
	return r.layout, err
}

func (s *Service) resolveLayoutState() (layoutResolution, error) {
	r := layoutResolution{candidates: discoverLayouts()}
	byBase := make(map[string]layout, len(r.candidates))
	for _, candidate := range r.candidates {
		byBase[canonicalBase(candidate.base)] = candidate
	}

	owners := make(map[string][]int)
	var unknown []int
	for _, proc := range nfqProcessesHook() {
		if proc.exe == "" {
			// The process name matched nfqws/tpws, but /proc did not let us
			// identify its executable. Do not fall back to a stopped copy.
			unknown = append(unknown, proc.pid)
			r.anyOwned = true
			continue
		}
		base := baseOfExe(proc.exe)
		if base == "" {
			// Ignore known NFQWS binaries outside /opt unless their path is
			// Zapret-shaped. An unclassifiable process under /opt is a conflict.
			if strings.Contains(strings.ToLower(proc.exe), "zapret") || pathWithin(optRoot, proc.exe) {
				unknown = append(unknown, proc.pid)
				r.anyOwned = true
			}
			continue
		}
		base = canonicalBase(base)
		if _, ok := byBase[base]; !ok {
			unknown = append(unknown, proc.pid)
			r.anyOwned = true
			continue
		}
		owners[base] = append(owners[base], proc.pid)
		r.anyOwned = true
	}

	if len(unknown) > 0 {
		return r, fmt.Errorf("%w: could not map PID(s) %v to a valid installation", ErrConflict, unknown)
	}
	if len(owners) > 1 {
		bases := sortedOwnerBases(owners)
		return r, fmt.Errorf("%w: daemons from multiple installations are running: %s", ErrConflict, strings.Join(bases, ", "))
	}
	if len(owners) == 1 {
		for base, pids := range owners {
			r.layout = byBase[base]
			r.activeBase = r.layout.base
			r.selectedPIDs = append([]int(nil), pids...)
			if err := s.rememberRunningBase(base); err != nil {
				return r, fmt.Errorf("%w: could not save the last running installation: %v", ErrConflict, err)
			}
			return r, nil
		}
	}

	if len(r.candidates) == 0 {
		return r, ErrNotInstalled
	}

	cached, err := s.readLastRunningBase()
	if err != nil {
		return r, fmt.Errorf("%w: could not read the last running installation: %v", ErrConflict, err)
	}
	if cached != "" {
		cached = canonicalBase(cached)
		if candidate, ok := byBase[cached]; ok {
			r.layout = candidate
			return r, nil
		}
		// A remembered installation disappearing is a state change that can
		// invalidate a pending operation. Do not silently redirect mutations
		// to the only remaining stopped copy; wait until a live process
		// identifies the new owner and replaces the cache.
		return r, fmt.Errorf("%w: saved installation %s is missing; found %d other installations", ErrConflict, cached, len(r.candidates))
	}
	if len(r.candidates) == 1 {
		r.layout = r.candidates[0]
		return r, nil
	}
	return r, fmt.Errorf("%w: Zapret is stopped; found %d installations and no last running installation", ErrConflict, len(r.candidates))
}

func sortedOwnerBases(owners map[string][]int) []string {
	bases := make([]string, 0, len(owners))
	for base := range owners {
		bases = append(bases, base)
	}
	sort.Strings(bases)
	return bases
}

func (s *Service) readLastRunningBase() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRunningLoaded {
		return s.lastRunningBase, nil
	}
	if s.dataDir == "" {
		s.lastRunningLoaded = true
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(s.dataDir, "zapret-last-running"))
	if errors.Is(err, os.ErrNotExist) {
		s.lastRunningLoaded = true
		return "", nil
	}
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(string(data))
	if base == "" || !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return "", fmt.Errorf("invalid cached base %q", base)
	}
	s.lastRunningBase = base
	s.lastRunningLoaded = true
	return base, nil
}

func (s *Service) rememberRunningBase(base string) error {
	if _, err := s.readLastRunningBase(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRunningLoaded && s.lastRunningBase == base {
		return nil
	}
	if s.dataDir != "" {
		if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(s.dataDir, "zapret-last-running"), []byte(base+"\n"), 0o600); err != nil {
			return err
		}
	}
	s.lastRunningBase = base
	s.lastRunningLoaded = true
	return nil
}

func defaultOpkgListInstalled(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, opkgTimeout)
	defer cancel()
	result, err := sysexec.RunWithOptions(ctx, "/opt/bin/opkg", []string{"list-installed"}, sysexec.Options{Timeout: opkgTimeout})
	if err != nil {
		// A missing opkg binary means the router has no Entware package
		// manager. Timeouts and command failures remain errors so they are
		// never cached as package absence.
		if errors.Is(err, osexec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", sysexec.FormatError(result, err)
	}
	return result.Stdout, nil
}

func defaultRunVersion(ctx context.Context, binary string) (string, error) {
	var lastErr error
	for _, args := range versionArgs {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			break
		}
		ctxRun, cancel := context.WithTimeout(ctx, versionProbeTimeout)
		result, err := sysexec.RunWithOptions(ctxRun, binary, args, sysexec.Options{Timeout: versionProbeTimeout})
		cancel()
		if err != nil {
			lastErr = sysexec.FormatError(result, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		if v := parseVersionOutput(result.Stdout + "\n" + result.Stderr); v != "" {
			return v, nil
		}
		lastErr = fmt.Errorf("%s %s returned no parseable version", binary, strings.Join(args, " "))
	}
	if lastErr == nil {
		lastErr = errors.New("version probe did not run")
	}
	return "", fmt.Errorf("could not read zapret version: %w", lastErr)
}

// versionRe требует хотя бы одну точку в числе: у self-built сборок в
// выводе первым кандидатом иначе оказывается «Sep  9 2026» — дата, а не
// версия. Настоящие версии zapret — 1.71.4, 2.0.1-p86.16, 2.1.0.
var versionRe = regexp.MustCompile(`(?i)\bv?(\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.\-]+)?)\b`)

// parseVersionOutput достаёт версию из вывода демона. nfqws2 печатает
// строку вида «github version 2.1.0 (abc1234) lua_compat_ver 3», но
// сборки без ZAPRET_GH_VER дают «self-built version <дата> <время>»,
// поэтому берём первое число с плавающей точкой и не привязываемся к
// формату.
func parseVersionOutput(out string) string {
	m := versionRe.FindStringSubmatch(out)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

// zapretPackageKeyword — по которему ищем пакет. Пользователь ставит
// zapret22 / zapret2 / zapret / zapret-gui-keenetic; в репозитории
// Entware такого пакета нет вовсе (проверено по Packages.gz), поэтому
// сравнение по точному имени здесь бесполезно — ищем по подстроке.
const zapretPackageKeyword = "zapret"

// installedLineRe разбирает строку «имя - версия» из opkg list-installed.
var installedLineRe = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)`)

// listInstalledPackages отдаёт пары (имя, версия) zapret-пакетов.
//
// Кэш: TTL 24 часа при наличии, 10 минут при отсутствии. Промах по
// отрицательному ответу стоит пользователю 10 минут ожидания перед
// появлением установленного пакета; промах по положительному —
// сутки устаревания версии, что для anti-DPI некритично.
func (s *Service) listInstalledPackages(ctx context.Context) map[string]string {
	s.mu.Lock()
	ttl := packagesTTLAbsent
	if len(s.packagesCached) > 0 {
		ttl = packagesTTLPresent
	}
	now := s.currentTime()
	if !s.packagesAt.IsZero() && now.Sub(s.packagesAt) < ttl {
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}
	// A failed command has a short retry delay, separate from the absence
	// cache, so a broken opkg cannot turn into a cached "not installed".
	if !s.packagesErrorAt.IsZero() && now.Sub(s.packagesErrorAt) < 30*time.Second {
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}
	// Keep the previous snapshot while one caller probes opkg. The command
	// runs without mu so other status and mutation requests can proceed.
	if s.probeInFlight {
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}
	s.probeInFlight = true
	s.mu.Unlock()

	out, err := s.opkgListInstalled(ctx)
	s.mu.Lock()
	s.probeInFlight = false
	if err != nil {
		s.lastError = fmt.Sprintf("opkg list-installed: %v", err)
		s.packagesErrorAt = s.currentTime()
		cached := s.cachedPackagesLocked()
		s.mu.Unlock()
		return cached
	}

	found := parseZapretPackages(out)
	s.packagesCached = found
	s.packagesAt = s.currentTime()
	s.packagesErrorAt = time.Time{}
	s.lastError = ""
	s.mu.Unlock()
	return found
}

func (s *Service) cachedPackagesLocked() map[string]string {
	out := make(map[string]string, len(s.packagesCached))
	for k, v := range s.packagesCached {
		out[k] = v
	}
	return out
}

// parseZapretPackages отбирает из вывода opkg всё, что похоже на zapret.
// Ключевое слово — «zapret» в имени, регистронезависимо и по подстроке:
// покрывает zapret22, zapret2, zapret, zapret-gui-keenetic и любые
// будущие имена с этим словом.
func parseZapretPackages(out string) map[string]string {
	found := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		m := installedLineRe.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) != 3 {
			continue
		}
		name, version := m[1], m[2]
		if !strings.Contains(strings.ToLower(name), zapretPackageKeyword) {
			continue
		}
		found[name] = version
	}
	return found
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

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

// binaryFingerprint — ключ кэша версии. Смена размера или mtime бинаря
// означает новую установку/обновление, и кэш надо сбросить. Формат
// взят из hydraroute.getVersionCachedLocked: версии у нас меняются
// только при замене файла.
func binaryFingerprint(path string) string {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return ""
	}
	return fmt.Sprintf("%s|%s|%s|%d", filepath.Clean(path),
		st.ModTime().UTC().Format(time.RFC3339Nano), st.Mode().String(), st.Size())
}

// versionCached returns the cached daemon version and probes outside mu.
func (s *Service) getVersionCached(binary string) string {
	finger := binaryFingerprint(binary)
	if finger == "" {
		return ""
	}

	s.mu.Lock()
	if s.versionFinger != "" && s.versionFinger != finger {
		s.versionCached, s.versionAt, s.versionFinger = "", time.Time{}, ""
		s.versionError, s.versionErrorAt, s.versionErrorFinger = "", time.Time{}, ""
	}
	now := s.currentTime()
	if s.versionFinger == finger && !s.versionAt.IsZero() && now.Sub(s.versionAt) < versionTTL {
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	if s.versionErrorFinger == finger && !s.versionErrorAt.IsZero() && now.Sub(s.versionErrorAt) >= 0 && now.Sub(s.versionErrorAt) < versionRetryTTL {
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	if s.versionProbeInFlight {
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	s.versionProbeInFlight = true
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), versionProbeTimeout)
	v, err := s.runVersion(ctx, binary)
	cancel()
	if err == nil && strings.TrimSpace(v) == "" {
		err = errors.New("version probe returned no version")
	}

	s.mu.Lock()
	s.versionProbeInFlight = false
	if err != nil {
		s.versionError = fmt.Sprintf("zapret version: %v", err)
		s.versionErrorAt = s.currentTime()
		s.versionErrorFinger = finger
		version := s.versionCached
		s.mu.Unlock()
		return version
	}
	v = strings.TrimSpace(v)
	s.versionCached, s.versionAt, s.versionFinger = v, s.currentTime(), finger
	s.versionError, s.versionErrorAt, s.versionErrorFinger = "", time.Time{}, ""
	s.mu.Unlock()
	return v
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

// nfqProc — найденный процесс демона zapret.
type nfqProc struct {
	pid int
	exe string
}

// nfqProcesses ищет процессы демона zapret через /proc.
//
// Pidfile читать не нужно: имя инстанса различается между форками
// (nfqws2.pid, nfqws2_1.pid), а KZM2 не пишет pidfile вовсе и определяет
// живость через `pidof`. Листинг /proc дешевле запуска внешней команды и
// не зависит от соглашений конкретного форка.
//
// Имя процесса сверяется по comm, а принадлежность zapret — по пути к
// исполняемому файлу: одноимённый бинарь из чужого каталога мы считать
// своим не будем.
func nfqProcesses() []nfqProc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []nfqProc
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, ok := nfqExe(pid)
		if !ok {
			continue
		}
		out = append(out, nfqProc{pid: pid, exe: exe})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

// nfqExe возвращает путь к исполняемому файлу, если процесс — демон
// zapret. comm обрезан ядром до 15 байт, поэтому сверяем по префиксу.
func nfqExe(pid int) (string, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	return nfqExeInDir(dir)
}

func nfqExeInDir(dir string) (string, bool) {
	comm, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(string(comm)))
	if !isNFQWSName(name) {
		return "", false
	}
	// A terminated daemon can retain its comm until its parent reaps it,
	// but it no longer has an executable and cannot process traffic. Do not
	// mistake a zombie (or a process disappearing mid-read) for an unknown
	// live owner and turn a successful stop into a selection conflict.
	status, statusErr := os.ReadFile(filepath.Join(dir, "status"))
	if os.IsNotExist(statusErr) {
		return "", false
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "State:" && (fields[1] == "Z" || fields[1] == "X") {
			return "", false
		}
	}
	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	if err != nil {
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return "", false
		}
		// Симлинк недоступен: у процесса, запущенного не нами, прав
		// может не хватать. comm уже совпал — считаем своим, иначе
		// работающий демон воссит как остановленный.
		return "", true
	}
	// Принадлежность проверяем по пути: /opt/zapret22/… и есть наш
	// демон, а одноимённый бинарь из /usr/local/bin — нет.
	if !strings.Contains(strings.ToLower(exe), "zapret") && !pathWithin(optRoot, exe) {
		return "", false
	}
	return exe, true
}

// isNFQWSName узнаёт демон zapret по имени процесса. Форки зовут его
// по-разному: zapret2 — nfqws2, zapret — nfqws, BSD-вариант — dvtws,
// прокси-режим v1 — tpws.
func isNFQWSName(comm string) bool {
	for _, p := range []string{"nfqws", "dvtws", "tpws"} {
		if strings.HasPrefix(comm, p) {
			return true
		}
	}
	return false
}

func runningPIDsForBase(base string) (bool, []int) {
	want := canonicalBase(base)
	var pids []int
	for _, proc := range nfqProcessesHook() {
		if proc.exe == "" {
			continue
		}
		if got := canonicalBase(baseOfExe(proc.exe)); got != "" && got == want {
			pids = append(pids, proc.pid)
		}
	}
	return len(pids) > 0, pids
}
