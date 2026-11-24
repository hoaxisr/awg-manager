package warpscout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/signature"
)

// Logger — журнал приложения (logging.ScopedLogger).
type Logger interface {
	Info(action, target, message string)
	Warn(action, target, message string)
}

// Publisher — шина SSE (events.Bus).
type Publisher interface {
	Publish(eventType string, data any)
}

// SSE-топики модуля.
const (
	TopicScan     = "warpscout:scan"
	TopicWatchdog = "warpscout:watchdog"
)

// CreateOptions — дополнительные параметры создания туннеля (каскад WARP-in-WARP).
type CreateOptions struct {
	ViaMode TargetMode
	ViaID   string
}

// SwapOptions — параметры подмены узла: помимо нового endpoint передают
// протокол ("awg" | "wg") и текущую обфускацию сканера. Если Protocol == "",
// обфускация туннеля не изменяется.
type SwapOptions struct {
	Protocol string
	AWG      AWGParams
}

// TunnelOps — операции над туннелями, которые автопилоту нужны от
// остального приложения. Реализация живёт в internal/api.
type TunnelOps interface {
	// List — полная инвентаризация туннелей (включая защищённые системные с флагом
	// Protected для проверок занятости ключей).
	List(ctx context.Context) ([]TunnelRef, error)
	// Get — один туннель; ошибка, если его нет.
	Get(ctx context.Context, mode TargetMode, id string) (*TunnelRef, error)
	// Create создаёт туннель из .conf и возвращает его.
	Create(ctx context.Context, mode TargetMode, name, conf string, opts CreateOptions) (*TunnelRef, error)
	// SwapEndpoint меняет endpoint пира и (если задан SwapOptions.Protocol)
	// синхронизирует обфускацию туннеля, перезапуская интерфейс при смене маскировки.
	SwapEndpoint(ctx context.Context, mode TargetMode, id, endpoint string, opts SwapOptions) error
	// Rename переименовывает туннель (карточку и описание интерфейса в NDMS).
	Rename(ctx context.Context, mode TargetMode, id, name string) error
}

// WANResolver — имя ядра физического WAN по умолчанию; up=false — линк упал.
type WANResolver func(ctx context.Context) (iface string, up bool)

// Deps — зависимости сервиса.
type Deps struct {
	Store   *Store
	Runner  Runner
	Tunnels TunnelOps
	Prober  TraceProber
	WAN     WANResolver
	// Routes — доступные маршруты (WAN-интерфейсы, VPN-туннели и WARP)
	// для выбора интерфейса сканера.
	Routes func(ctx context.Context) []RouteRef
	// ObfuscationSources — существующие на роутере AWG-туннели, из которых
	// можно скопировать обфускацию для скана и создания туннелей.
	ObfuscationSources func(ctx context.Context) []ObfuscationSource
	// UsedIPv4 — IPv4-адреса, уже занятые существующими туннелями и интерфейсами роутера.
	UsedIPv4 func(ctx context.Context) []string
	HTTP     HTTPClientFactory
	// HTTPDirectBypass — прямой WAN-клиент с фрагментацией TLS ClientHello по SNI
	// для пробивания ТСПУ при блокировке домена регистрации Cloudflare.
	HTTPDirectBypass HTTPClientFactory
	// HTTPProxyClient — фабрика одноразового клиента для ручной регистрации через
	// указанный пользователем URL прокси (http/https/socks5/socks5h).
	HTTPProxyClient func(proxyURL string) (*http.Client, error)
	// HTTPFallbacks — резервные HTTP-клиенты (прокси/туннель) на случай
	// блокировки API регистрации Cloudflare на прямом WAN.
	HTTPFallbacks  func(ctx context.Context) []FallbackClient
	Bus            Publisher
	OnWatchChanged func()
	Log            Logger
	// SupportsASC3 сообщает, поддерживает ли прошивка NDMS поля AWG 3.0
	// (ContentPadding/таймеры) на системных/Native интерфейсах.
	SupportsASC3 func() bool
	// SupportsKernelAWG31 сообщает, поддерживает ли kernel-модуль amneziawg
	// флаги AWG 3.1 (RandomTrailers / DisableCookies, версия >= 3.1).
	SupportsKernelAWG31 func() bool
	// Now/Sleep — для тестов.
	Now func() time.Time
}

// FallbackClient связывает резервный HTTP-клиент с функцией освобождения
// ресурсов (например, downloader.Lease.Close или CloseIdleConnections).
type FallbackClient struct {
	Client *http.Client
	Close  func()
}

// ScanRequest — параметры ручного скана.
type ScanRequest struct {
	Filters       Filters  `json:"filters"`
	MaxHits       int      `json:"maxHits"`
	MaxCandidates int      `json:"maxCandidates"`
	Endpoints     []string `json:"endpoints,omitempty"`
	// Ifname — явный интерфейс выхода для этого скана; пусто — берётся из Config.ScanInterface или основного WAN.
	Ifname string `json:"ifname,omitempty"`
}

// ScanStatus — состояние ручного скана.
type ScanStatus struct {
	Running    bool      `json:"running"`
	Owner      string    `json:"owner,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	Hits       []Hit     `json:"hits"`
	Error      string    `json:"error,omitempty"`
	Aborted    bool      `json:"aborted,omitempty"`
	ElapsedMs  int       `json:"elapsedMs,omitempty"`
}

// Состояния сторожа.
const (
	WatchIdle          = "idle"
	WatchOK            = "ok"
	WatchDegraded      = "degraded"
	WatchSwapping      = "swapping"
	WatchBackoff       = "backoff"
	WatchWANDown       = "wan_down"
	WatchTargetMissing = "target_missing"
	WatchTargetStopped = "target_stopped"
	WatchNoAccount     = "no_account"
	WatchEngineMissing = "engine_missing"
	WatchProtected     = "protected"
	WatchNotWarp       = "not_warp"
	WatchEngineBusy    = "engine_busy"
)

// WatchStatus — живая телеметрия профиля.
type WatchStatus struct {
	ProfileID     string       `json:"profileId"`
	State         string       `json:"state"`
	Message       string       `json:"message,omitempty"`
	LastCheck     time.Time    `json:"lastCheck,omitempty"`
	Trace         *TraceResult `json:"trace,omitempty"`
	Fails         int          `json:"fails"`
	Endpoint      string       `json:"endpoint,omitempty"`
	NextSwapAfter time.Time    `json:"nextSwapAfter,omitempty"`
	LastSwap      *SwapEvent   `json:"lastSwap,omitempty"`
}

// EngineInfo — где C-модуль и найден ли он.
type EngineInfo struct {
	Path      string `json:"path"`
	Available bool   `json:"available"`
}

// State — полный снимок для UI.
type State struct {
	Engine              EngineInfo          `json:"engine"`
	Account             *Account            `json:"account"`
	AccountKeyInUseBy   string              `json:"accountKeyInUseBy,omitempty"`
	HasInnerAccount     bool                `json:"hasInnerAccount"`
	AccountUnreadable   bool                `json:"accountUnreadable"`
	DefaultWAN          string              `json:"defaultWan,omitempty"`
	Routes              []RouteRef          `json:"routes"`
	ObfuscationSources  []ObfuscationSource `json:"obfuscationSources,omitempty"`
	UsedIPv4            []string            `json:"usedIpv4,omitempty"`
	Config              Config              `json:"config"`
	Scan                ScanStatus          `json:"scan"`
	FindJunk            FindJunkStatus      `json:"findJunk"`
	Watch               []WatchStatus       `json:"watch"`
	Stats               []ProfileStats      `json:"stats"`
	History             []SwapEvent         `json:"history"`
	SupportsASC3        bool                `json:"supportsAsc3"`
	SupportsKernelAWG31 bool                `json:"supportsKernelAwg31"`
}

// ApplyRequest — применить найденный эндпоинт.
type ApplyRequest struct {
	// Action: "create" — новый туннель, "swap" — подменить endpoint.
	Action          string     `json:"action"`
	Endpoint        string     `json:"endpoint"`
	Colo            string     `json:"colo,omitempty"`
	Mode            TargetMode `json:"mode"`
	TargetID        string     `json:"targetId,omitempty"`
	Name            string     `json:"name,omitempty"`
	AddressV4       string     `json:"addressV4,omitempty"`
	Protocol        string     `json:"protocol,omitempty"`
	ViaMode         TargetMode `json:"viaMode,omitempty"`
	ViaID           string     `json:"viaId,omitempty"`
	AWG             *AWGParams `json:"awg,omitempty"`
	EnableAutopilot bool       `json:"enableAutopilot"`
	Filters         *Filters   `json:"filters,omitempty"`
	// ReuseAccount разрешает использовать основной аккаунт без обращения к
	// Cloudflare API (офлайн/импортированная учётка), если его ключ ещё не
	// занят другим WARP-туннелем.
	ReuseAccount bool `json:"reuseAccount,omitempty"`
}

// ApplyResult — итог применения.
type ApplyResult struct {
	Tunnel        *TunnelRef `json:"tunnel"`
	ProfileID     string     `json:"profileId,omitempty"`
	AccountReused bool       `json:"accountReused,omitempty"`
}

// ExportRequest — собрать .conf для найденного эндпоинта без создания туннеля.
type ExportRequest struct {
	Endpoint string     `json:"endpoint"`
	Colo     string     `json:"colo,omitempty"`
	Protocol string     `json:"protocol,omitempty"`
	MTU      int        `json:"mtu,omitempty"`
	AWG      *AWGParams `json:"awg,omitempty"`
}

// ExportResult — сгенерированный конфиг .conf и рекомендуемое имя файла.
type ExportResult struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// Ошибки уровня API.
var (
	ErrNoAccount    = errors.New("учётная запись WARP не зарегистрирована")
	ErrScanRunning  = errors.New("сканирование уже идёт")
	ErrEngineBusy   = errors.New("сканер занят автопилотом, попробуйте через минуту")
	ErrNotWarp      = errors.New("целевой туннель не является туннелем Cloudflare WARP")
	ErrProtected    = errors.New("этот интерфейс защищён от изменений автопилотом")
	ErrInvalidInput = errors.New("некорректные параметры")
)

// Service — сервис модуля.
type Service struct {
	d Deps

	engineMu sync.Mutex // один процесс сканера на роутер
	// acctMu — одна операция над учётной записью за раз, включая её сетевую
	// часть: параллельные регистрация и привязка лицензии иначе записали бы
	// каждая свою копию, и локальный ключ разошёлся бы с ключом у Cloudflare.
	acctMu       sync.Mutex
	findJunkOpMu sync.Mutex // сериализует StartFindJunk и ApplyFindJunk (всегда снаружи mu)

	mu                  sync.Mutex
	scan                ScanStatus
	scanCancel          context.CancelFunc
	findJunk            FindJunkStatus
	findJunkCancel      context.CancelFunc
	findJunkSeq         uint64 // монотонный счётчик запусков подбора маскировки
	testBeforeApplySave func() // барьер только для тестов конкурентности
	watch               map[string]*WatchStatus
	stats               map[string]*ProfileStats
	statsDirty          bool // в stats есть изменения, не дошедшие до диска
	watchers            map[string]*watcher
	rootCtx             context.Context
	rootCancel          context.CancelFunc
	wg                  sync.WaitGroup
}

// New создаёт сервис. Сторожа поднимает Start.
func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Runner == nil {
		d.Runner = &BinaryRunner{}
	}
	if d.Prober == nil {
		d.Prober = &HTTPTraceProber{}
	}
	if d.WAN == nil {
		d.WAN = func(context.Context) (string, bool) { return "", true }
	}
	ctx, cancel := context.WithCancel(context.Background())
	statsMap := map[string]*ProfileStats{}
	if d.Store != nil {
		for k, v := range d.Store.Stats() {
			cp := cloneProfileStats(v, false)
			statsMap[k] = &cp
		}
	}
	return &Service{
		d: d, scan: ScanStatus{Hits: []Hit{}},
		findJunk: FindJunkStatus{Attempts: []JunkAttempt{}},
		watch:    map[string]*WatchStatus{}, stats: statsMap, watchers: map[string]*watcher{},
		rootCtx: ctx, rootCancel: cancel,
	}
}

// Start поднимает сторожей включённых профилей.
func (s *Service) Start() {
	s.reconcileWatchers(s.d.Store.Config())
}

// Stop останавливает скан и сторожей и ждёт их выхода.
func (s *Service) Stop() {
	s.rootCancel()
	s.mu.Lock()
	if s.scanCancel != nil {
		s.scanCancel()
	}
	if s.findJunkCancel != nil {
		s.findJunkCancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
	s.mu.Lock()
	dirty := s.statsDirty
	s.mu.Unlock()
	if dirty {
		s.flushStats()
	}
}

func (s *Service) logInfo(action, target, msg string) {
	if s.d.Log != nil {
		s.d.Log.Info(action, target, msg)
	}
}

func (s *Service) logWarn(action, target, msg string) {
	if s.d.Log != nil {
		s.d.Log.Warn(action, target, msg)
	}
}

func (s *Service) publish(topic string, data any) {
	if s.d.Bus != nil {
		s.d.Bus.Publish(topic, data)
	}
}

// Engine — сведения о C-модуле.
func (s *Service) Engine() EngineInfo {
	p, ok := s.d.Runner.Available()
	return EngineInfo{Path: p, Available: ok}
}

// State — снимок для UI.
func (s *Service) State() State {
	cfg := s.d.Store.Config()
	s.mu.Lock()
	scan := s.scan
	scan.Hits = append([]Hit{}, s.scan.Hits...)
	findJunk := cloneFindJunkStatus(s.findJunk)
	watch := make([]WatchStatus, 0, len(cfg.Profiles))
	stats := make([]ProfileStats, 0, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		if ws, ok := s.watch[p.ID]; ok {
			watch = append(watch, *ws)
		} else {
			watch = append(watch, WatchStatus{ProfileID: p.ID, State: WatchIdle})
		}
		if ps, ok := s.stats[p.ID]; ok {
			stats = append(stats, cloneProfileStats(*ps, true))
		} else {
			stats = append(stats, ProfileStats{ProfileID: p.ID})
		}
	}
	s.mu.Unlock()
	defaultWAN, _ := s.d.WAN(s.rootCtx)
	routes := []RouteRef{}
	if s.d.Routes != nil {
		if r := s.d.Routes(s.rootCtx); len(r) > 0 {
			routes = r
		}
	}
	usedIPv4 := []string{}
	if s.d.UsedIPv4 != nil {
		if u := s.d.UsedIPv4(s.rootCtx); len(u) > 0 {
			usedIPv4 = u
		}
	}
	obfSources := []ObfuscationSource{}
	if s.d.ObfuscationSources != nil {
		if src := s.d.ObfuscationSources(s.rootCtx); len(src) > 0 {
			obfSources = src
		}
	}
	acct := s.d.Store.Account()
	accountKeyInUseBy := ""
	if acct != nil {
		if inUse, owner, err := s.accountKeyOwner(s.rootCtx, acct); err == nil && inUse {
			accountKeyInUseBy = owner
		}
	}
	return State{
		Engine:              s.Engine(),
		Account:             acct,
		AccountKeyInUseBy:   accountKeyInUseBy,
		HasInnerAccount:     s.d.Store.InnerAccount() != nil,
		AccountUnreadable:   s.d.Store.AccountUnreadable(),
		DefaultWAN:          defaultWAN,
		Routes:              routes,
		ObfuscationSources:  obfSources,
		UsedIPv4:            usedIPv4,
		Config:              cfg,
		Scan:                scan,
		FindJunk:            findJunk,
		Watch:               watch,
		Stats:               stats,
		History:             s.d.Store.History(),
		SupportsASC3:        s.supportsASC3(),
		SupportsKernelAWG31: s.supportsKernelAWG31(),
	}
}

func (s *Service) supportsASC3() bool {
	return s != nil && s.d.SupportsASC3 != nil && s.d.SupportsASC3()
}

func (s *Service) supportsKernelAWG31() bool {
	return s != nil && s.d.SupportsKernelAWG31 != nil && s.d.SupportsKernelAWG31()
}

func normHostPreset(p string) string {
	c := signature.CanonicalHostPreset(p)
	if c == signature.HostPresetDefault {
		return ""
	}
	return c
}

func validateAndNormalizeAWGSignature(awg *AWGParams) error {
	if strings.TrimSpace(awg.SignatureHostPreset) != "" {
		c := signature.CanonicalHostPreset(awg.SignatureHostPreset)
		if c == "" {
			return fmt.Errorf("%w: неизвестный пресет доменов %q", ErrInvalidInput, awg.SignatureHostPreset)
		}
		awg.SignatureHostPreset = normHostPreset(c)
	} else {
		awg.SignatureHostPreset = ""
	}
	domTrim := strings.TrimSpace(awg.SignatureDomain)
	if domTrim != "" {
		list, err := signature.ParseDomainList(domTrim)
		if err != nil {
			return fmt.Errorf("%w: некорректный домен генерации I1: %v", ErrInvalidInput, err)
		}
		awg.SignatureDomain = strings.Join(list, ", ")
	} else {
		awg.SignatureDomain = ""
		if awg.SignatureHostPreset == signature.HostPresetCustom {
			awg.SignatureHostPreset = ""
		}
	}
	return nil
}

// ResetStats обнуляет статистику профиля (или всех профилей, если profileID пуст).
func (s *Service) ResetStats(profileID string) error {
	now := s.d.Now()
	s.mu.Lock()
	if strings.TrimSpace(profileID) == "" {
		for id, ps := range s.stats {
			ep := ps.CurrentEndpoint
			s.stats[id] = &ProfileStats{
				ProfileID:       id,
				StartedAt:       now,
				CurrentEndpoint: ep,
				EndpointSince:   now,
			}
		}
	} else {
		id := strings.TrimSpace(profileID)
		ep := ""
		if old, ok := s.stats[id]; ok {
			ep = old.CurrentEndpoint
		}
		s.stats[id] = &ProfileStats{
			ProfileID:       id,
			StartedAt:       now,
			CurrentEndpoint: ep,
			EndpointSince:   now,
		}
	}
	s.mu.Unlock()
	s.flushStats()
	return nil
}

// flushStats пишет статистику на диск. Зовётся по событию (подмена endpoint,
// сброс, удаление профиля) и на остановке — не по таймеру.
func (s *Service) flushStats() {
	if s.d.Store == nil {
		return
	}
	s.mu.Lock()
	snap := make(map[string]ProfileStats, len(s.stats))
	for k, v := range s.stats {
		snap[k] = cloneProfileStats(*v, false)
	}
	s.statsDirty = false
	s.mu.Unlock()
	if err := s.d.Store.SaveStats(snap); err != nil {
		s.mu.Lock()
		s.statsDirty = true
		s.mu.Unlock()
	}
}

// ClearHistory очищает журнал автоподмен.
func (s *Service) ClearHistory() error {
	if s.d.Store == nil {
		return nil
	}
	if err := s.d.Store.ClearHistory(); err != nil {
		return err
	}
	s.logInfo("history", "warp", "Журнал автоподмен очищен")
	return nil
}

// Targets — туннели для выбора цели (защищённые системные интерфейсы скрываются).
func (s *Service) Targets(ctx context.Context) ([]TunnelRef, error) {
	if s.d.Tunnels == nil {
		return []TunnelRef{}, nil
	}
	raw, err := s.d.Tunnels.List(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]TunnelRef, 0, len(raw))
	for _, t := range raw {
		if t.Mode == TargetSystem && t.Protected {
			continue
		}
		list = append(list, t)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Mode != list[j].Mode {
			return list[i].Mode < list[j].Mode
		}
		return list[i].Name < list[j].Name
	})
	return list, nil
}

// ── Учётная запись ───────────────────────────────────────────────

func (s *Service) httpClient(ctx context.Context) (*http.Client, error) {
	if s.d.HTTP == nil {
		return nil, errors.New("HTTP-клиент не настроен")
	}
	iface := s.resolveScanIface(ctx, "")
	return s.d.HTTP(iface)
}

func (s *Service) httpBypassClient(ctx context.Context) (*http.Client, error) {
	if s.d.HTTPDirectBypass == nil {
		return nil, errors.New("TLS-bypass HTTP-клиент не настроен")
	}
	iface := s.resolveScanIface(ctx, "")
	return s.d.HTTPDirectBypass(iface)
}

func altAPIBaseFor(primaryBase string, opts RegisterOptions) string {
	if strings.TrimSpace(opts.RelayURL) != "" {
		return ""
	}
	if cfAPIBaseAlt == "" || cfAPIBaseAlt == primaryBase {
		return ""
	}
	return cfAPIBaseAlt
}

func (s *Service) resolveScanIface(ctx context.Context, override string) string {
	if v := sanitizeIfaceName(override); v != "" {
		return v
	}
	if v := sanitizeIfaceName(s.d.Store.Config().ScanInterface); v != "" {
		return v
	}
	wan, _ := s.d.WAN(ctx)
	return wan
}

// doAccountOp выполняет запрос к Cloudflare API сначала напрямую через WAN, а
// при сетевой блокировке ТСПУ перебирает TLS-bypass, запасной хост и резервные выходы.
func (s *Service) doAccountOp(ctx context.Context, fn func(*http.Client) error) error {
	return s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, _ string) error {
		return fn(hc)
	})
}

func (s *Service) doAccountOpWithBase(ctx context.Context, opts RegisterOptions, fn func(*http.Client, string) error) error {
	relayBase, err := NormalizeRelayBaseURL(opts.RelayURL)
	if err != nil {
		return err
	}
	primaryBase := cfAPIBase
	if relayBase != "" {
		primaryBase = relayBase
	}
	altBase := altAPIBaseFor(primaryBase, opts)

	wrapUserDisallowed := func(e error) error {
		if errors.Is(e, ErrDisallowedAddress) && !errors.Is(e, ErrInvalidInput) {
			return fmt.Errorf("%w: %w", ErrInvalidInput, e)
		}
		return e
	}
	allowFallback := func(e error) bool {
		if relayBase != "" && (errors.Is(e, ErrDisallowedAddress) || errors.Is(e, ErrInvalidInput)) {
			return false
		}
		return shouldRetryFallback(e)
	}

	if proxyURL := strings.TrimSpace(opts.ProxyURL); proxyURL != "" {
		if s.d.HTTPProxyClient == nil {
			return errors.New("HTTP-клиент прокси не настроен")
		}
		phc, pErr := s.d.HTTPProxyClient(proxyURL)
		if pErr != nil {
			return SanitizeProxyError(pErr, proxyURL)
		}
		defer phc.CloseIdleConnections()
		err := fn(phc, primaryBase)
		if err != nil && altBase != "" {
			var se *apiStatusError
			if errors.As(err, &se) && shouldRetryFallback(err) {
				err = fn(phc, altBase)
			}
		}
		return SanitizeProxyError(wrapUserDisallowed(err), proxyURL)
	}

	var firstErr error
	recordErr := func(e error) {
		if firstErr == nil && e != nil {
			firstErr = e
		}
	}

	hc, hcErr := s.httpClient(ctx)
	if hcErr == nil && hc != nil {
		defer hc.CloseIdleConnections()
		if err := fn(hc, primaryBase); err == nil || !allowFallback(err) {
			if relayBase != "" {
				err = wrapUserDisallowed(err)
			}
			return err
		} else {
			recordErr(err)
		}
	} else if hcErr != nil {
		recordErr(hcErr)
	}

	var bypassHC *http.Client
	if s.d.HTTPDirectBypass != nil {
		if bhc, bErr := s.httpBypassClient(ctx); bErr == nil && bhc != nil {
			bypassHC = bhc
			defer bypassHC.CloseIdleConnections()
			if err := fn(bypassHC, primaryBase); err == nil || !allowFallback(err) {
				if relayBase != "" {
					err = wrapUserDisallowed(err)
				}
				return err
			} else {
				recordErr(err)
			}
		}
	}

	if altBase != "" {
		altClient := bypassHC
		if altClient == nil {
			altClient = hc
		}
		if altClient != nil {
			if err := fn(altClient, altBase); err == nil || !allowFallback(err) {
				if relayBase != "" {
					err = wrapUserDisallowed(err)
				}
				return err
			} else {
				recordErr(err)
			}
		}
	}

	if s.d.HTTPFallbacks != nil {
		fallbacks := s.d.HTTPFallbacks(ctx)
		defer func() {
			for _, fb := range fallbacks {
				if fb.Close != nil {
					fb.Close()
				} else if fb.Client != nil {
					fb.Client.CloseIdleConnections()
				}
			}
		}()
		for _, fb := range fallbacks {
			if fb.Client == nil || ctx.Err() != nil {
				continue
			}
			err := fn(fb.Client, primaryBase)
			if err == nil || !allowFallback(err) {
				if relayBase != "" {
					err = wrapUserDisallowed(err)
				}
				return err
			}
			recordErr(err)
		}
	}
	if firstErr != nil {
		if relayBase != "" {
			return wrapUserDisallowed(firstErr)
		}
		return firstErr
	}
	return errors.New("HTTP-клиент не настроен")
}

// Register регистрирует (или перевыпускает ключи) учётную запись WARP.
func (s *Service) Register(ctx context.Context) (*Account, error) {
	return s.RegisterWithOptions(ctx, RegisterOptions{})
}

// RegisterWithOptions регистрирует (или перевыпускает ключи) учётную запись WARP
// с необязательными ручными параметрами прокси или HTTP-relay.
func (s *Service) RegisterWithOptions(ctx context.Context, opts RegisterOptions) (*Account, error) {
	if _, err := NormalizeRelayBaseURL(opts.RelayURL); err != nil {
		return nil, err
	}
	opts.ProxyURL = strings.TrimSpace(opts.ProxyURL)
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	old := s.d.Store.Account()
	if inUse, owner, checkErr := s.accountKeyOwner(ctx, old); checkErr != nil {
		return nil, checkErr
	} else if inUse {
		return nil, fmt.Errorf("%w: ключ текущего аккаунта используется живым туннелем %q — перевыпуск разорвёт его соединение; сначала удалите туннель или импортируйте отдельный аккаунт", ErrInvalidInput, owner)
	}
	var acct *Account
	var oldNotFound bool
	useCustomRelay := strings.TrimSpace(opts.RelayURL) != ""
	err := s.doAccountOpWithBase(ctx, opts, func(hc *http.Client, base string) error {
		if !useCustomRelay && old != nil && old.ID != "" && old.Token != "" {
			rotated, rotErr := rotateAccountKeyWithBase(ctx, hc, old, base)
			if rotErr == nil {
				acct = rotated
				return nil
			}
			// К созданию новой регистрации переходим ТОЛЬКО при HTTP 404 (учётка
			// удалена у Cloudflare). Любые другие ошибки (сетевой сбой для fallback,
			// либо 400/401/422) возвращаем наверх, чтобы не затереть аккаунт и WARP+.
			if !isAccountNotFound(rotErr) {
				return rotErr
			}
			oldNotFound = true
		}
		fresh, regErr := registerAccountWithBase(ctx, hc, base)
		if regErr != nil {
			return regErr
		}
		acct = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	if old != nil && strings.TrimSpace(old.AddressV4) != "" && old.AddressV4 != DefaultTunnelIPv4 {
		acct.AddressV4 = old.AddressV4
	}
	if err := s.d.Store.SetAccount(acct); err != nil {
		return nil, err
	}
	if old != nil && old.ID != "" && old.Token != "" && old.ID != acct.ID && !oldNotFound {
		s.dropRegistration(old)
	}
	s.logInfo("register", "warp", "Зарегистрирована учётная запись Cloudflare WARP")
	return s.d.Store.Account(), nil
}

// ImportAccount импортирует готовую учётную запись WARP из JSON, TOML или WireGuard/AWG .conf.
func (s *Service) ImportAccount(ctx context.Context, raw string) (*Account, error) {
	acct, err := ParseAccountImport(raw)
	if err != nil {
		return nil, err
	}
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	if inUse, owner, checkErr := s.accountKeyOwner(ctx, acct); checkErr != nil {
		return nil, checkErr
	} else if inUse {
		return nil, fmt.Errorf("%w: ключ из импортируемых данных уже используется живым туннелем %q — скан и действия с аккаунтом разорвут это соединение; используйте отдельный конфиг или аккаунт", ErrInvalidInput, owner)
	}
	old := s.d.Store.Account()
	oldInUse, _, checkErr := s.accountKeyOwner(ctx, old)
	if checkErr != nil {
		return nil, checkErr
	}
	if err := s.d.Store.SetAccount(acct); err != nil {
		return nil, err
	}
	if old != nil && old.ID != "" && old.Token != "" && old.ID != acct.ID &&
		strings.TrimSpace(old.PublicKey) != strings.TrimSpace(acct.PublicKey) && !oldInUse {
		s.dropRegistration(old)
	}
	s.logInfo("import", "warp", "Импортирована учётная запись Cloudflare WARP")
	return s.d.Store.Account(), nil
}

// dropRegistration снимает регистрацию у Cloudflare лучшим усилием, в фоне,
// под общим WaitGroup сервиса, чтобы Stop дожидался запроса.
func (s *Service) dropRegistration(prev *Account) {
	if prev == nil || prev.ID == "" || prev.Token == "" || s.rootCtx.Err() != nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		dctx, cancel := context.WithTimeout(s.rootCtx, 20*time.Second)
		defer cancel()
		_ = s.doAccountOpWithBase(dctx, RegisterOptions{}, func(hc *http.Client, base string) error {
			return deleteAccountWithBase(dctx, hc, prev, base)
		})
	}()
}

// registerTunnelAccount заводит учётную запись WARP под один создаваемый
// туннель. Общий ключ у нескольких туннелей Cloudflare не обслуживает: при
// трёх туннелях на ключе рукопожатие проходит, а данные не идут (#1192,
// стенд 13.11.2026). Лицензия WARP+ основной записи переносится лучшим
// усилием — у ключа лицензии есть предел устройств.
func (s *Service) registerTunnelAccount(ctx context.Context, primary *Account) (*Account, error) {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	var fresh *Account
	if err := s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, base string) error {
		reg, err := registerAccountWithBase(ctx, hc, base)
		if err != nil {
			return err
		}
		fresh = reg
		return nil
	}); err != nil {
		return nil, err
	}
	if primary != nil && primary.License != "" {
		if err := s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, base string) error {
			return applyLicenseWithBase(ctx, hc, fresh, primary.License, base)
		}); err != nil {
			s.logWarn("register", "warp", "Лицензия WARP+ к учётной записи туннеля не применена: "+err.Error())
		}
	}
	return fresh, nil
}

func (s *Service) isStoredSlotAccount(a *Account) bool {
	if a == nil {
		return false
	}
	pub := strings.TrimSpace(a.PublicKey)
	id := strings.TrimSpace(a.ID)
	for _, slot := range []*Account{s.d.Store.Account(), s.d.Store.InnerAccount()} {
		if slot == nil {
			continue
		}
		if pub != "" && strings.TrimSpace(slot.PublicKey) == pub {
			return true
		}
		if id != "" && strings.TrimSpace(slot.ID) == id {
			return true
		}
	}
	return false
}

func (s *Service) isAccountKeyInUse(ctx context.Context, pubKey string) (bool, string, error) {
	return s.isAccountKeyInUseExcluding(ctx, pubKey, "", "")
}

func (s *Service) isAccountKeyInUseExcluding(ctx context.Context, pubKey string, excludeMode TargetMode, excludeID string) (bool, string, error) {
	pubKey = strings.TrimSpace(pubKey)
	if pubKey == "" {
		return false, "", nil
	}
	var aliveNames map[string]string
	if s.d.Tunnels != nil {
		list, err := s.d.Tunnels.List(ctx)
		if err != nil {
			return false, "", fmt.Errorf("не удалось проверить занятость ключа по списку туннелей: %w", err)
		}
		aliveNames = make(map[string]string, len(list))
		for _, t := range list {
			if excludeID != "" && t.Mode == excludeMode && t.ID == excludeID {
				continue
			}
			key := string(t.Mode) + ":" + t.ID
			name := strings.TrimSpace(t.Name)
			if name == "" {
				name = t.ID
			}
			aliveNames[key] = name
			if strings.TrimSpace(t.LocalPublicKey) == pubKey {
				return true, name, nil
			}
		}
	}
	for _, ta := range s.d.Store.TunnelAccounts() {
		if strings.TrimSpace(ta.PublicKey) != pubKey {
			continue
		}
		if excludeID != "" && ta.TunnelMode == excludeMode && ta.TunnelID == excludeID {
			continue
		}
		key := string(ta.TunnelMode) + ":" + ta.TunnelID
		if aliveNames == nil {
			return true, ta.TunnelID, nil
		}
		if owner, ok := aliveNames[key]; ok {
			return true, owner, nil
		}
	}
	return false, "", nil
}

func (s *Service) accountKeyOwner(ctx context.Context, acct *Account) (bool, string, error) {
	if acct == nil {
		return false, "", nil
	}
	return s.isAccountKeyInUse(ctx, acct.PublicKey)
}

func (s *Service) existingFreeAccount(ctx context.Context, avoidPubKeys ...string) (*Account, error) {
	return s.existingAccountFor(ctx, "", "", avoidPubKeys...)
}

func (s *Service) existingAccountFor(ctx context.Context, excludeMode TargetMode, excludeID string, avoidPubKeys ...string) (*Account, error) {
	avoid := make(map[string]bool, len(avoidPubKeys))
	for _, k := range avoidPubKeys {
		if k = strings.TrimSpace(k); k != "" {
			avoid[k] = true
		}
	}
	for _, a := range []*Account{s.d.Store.InnerAccount(), s.d.Store.Account()} {
		if a == nil || a.PrivateKey == "" {
			continue
		}
		pub := strings.TrimSpace(a.PublicKey)
		if pub != "" && avoid[pub] {
			continue
		}
		inUse, _, err := s.isAccountKeyInUseExcluding(ctx, pub, excludeMode, excludeID)
		if err != nil {
			return nil, err
		}
		if inUse {
			continue
		}
		return a, nil
	}
	return nil, nil
}

// collectTunnelAccounts снимает учётные записи удалённых туннелей. Судит по
// списку записей управляемых туннелей — он читается с диска целиком или не
// читается вовсе. Системные туннели не трогает: их список приходит от
// роутера и при сбое бывает неполным, а ошибиться здесь значит оставить
// живой туннель без регистрации.
func (s *Service) collectTunnelAccounts(ctx context.Context) {
	accts := s.d.Store.TunnelAccounts()
	if len(accts) == 0 {
		return
	}
	list, err := s.d.Tunnels.List(ctx)
	if err != nil {
		return
	}
	alive := map[string]bool{}
	for _, t := range list {
		alive[string(t.Mode)+":"+t.ID] = true
	}
	for i := range accts {
		ta := accts[i]
		if ta.TunnelMode == TargetSystem || alive[string(ta.TunnelMode)+":"+ta.TunnelID] {
			continue
		}
		if err := s.d.Store.RemoveTunnelAccount(ta.PublicKey); err != nil {
			continue
		}
		// Если удалённый туннель создавался офлайн на ключе основного слота,
		// сам слот в Store и его регистрацию у Cloudflare сохраняем.
		if !s.isStoredSlotAccount(&ta.Account) {
			s.dropRegistration(&ta.Account)
		}
	}
}

// SetLicense привязывает ключ WARP+.
func (s *Service) SetLicense(ctx context.Context, license string) (*Account, error) {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	acct := s.d.Store.Account()
	if acct == nil {
		return nil, ErrNoAccount
	}
	if acct.ID == "" || acct.Token == "" || !acct.HasToken {
		return nil, fmt.Errorf("%w: импортированный аккаунт без токена Cloudflare не поддерживает привязку лицензии WARP+ — импортируйте JSON/TOML с токеном или перерегистрируйтесь", ErrInvalidInput)
	}
	if err := s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, base string) error {
		return applyLicenseWithBase(ctx, hc, acct, license, base)
	}); err != nil {
		return nil, err
	}
	if err := s.d.Store.SetAccount(acct); err != nil {
		return nil, err
	}
	s.logInfo("license", "warp", fmt.Sprintf("Лицензия применена: тип %s, WARP+=%v", acct.AccountType, acct.WarpPlus))
	return s.d.Store.Account(), nil
}

// SetAccountAddress задаёт локальный IPv4 туннеля для сканера и новых конфигов WARP.
func (s *Service) SetAccountAddress(address string) (*Account, error) {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	acct := s.d.Store.Account()
	if acct == nil {
		return nil, ErrNoAccount
	}
	norm, err := NormalizeTunnelIPv4(address)
	if err != nil {
		return nil, err
	}
	acct.AddressV4 = norm
	if err := s.d.Store.SetAccount(acct); err != nil {
		return nil, err
	}
	s.logInfo("address", "warp", "Задан IPv4 туннеля WARP: "+norm)
	return s.d.Store.Account(), nil
}

// DeleteAccountLocal забывает учётную запись (и снимает её у Cloudflare),
// а также очищает дополнительный слот WARP-in-WARP.
func (s *Service) DeleteAccountLocal(ctx context.Context) error {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	acct := s.d.Store.Account()
	inner := s.d.Store.InnerAccount()
	if inUse, owner, checkErr := s.accountKeyOwner(ctx, acct); checkErr != nil {
		return checkErr
	} else if inUse {
		return fmt.Errorf("%w: ключ текущего аккаунта используется живым туннелем %q — удаление снимет его регистрацию у Cloudflare; сначала удалите туннель", ErrInvalidInput, owner)
	}
	if inUse, owner, checkErr := s.accountKeyOwner(ctx, inner); checkErr != nil {
		return checkErr
	} else if inUse {
		return fmt.Errorf("%w: ключ дополнительного слота используется живым туннелем %q — сначала удалите туннель", ErrInvalidInput, owner)
	}
	if err := s.d.Store.SetAccount(nil); err != nil {
		return err
	}
	_ = s.d.Store.SetInnerAccount(nil)
	if acct != nil && acct.ID != "" && acct.Token != "" {
		_ = s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, base string) error {
			return deleteAccountWithBase(ctx, hc, acct, base)
		})
	}
	if inner != nil && inner.ID != "" && inner.Token != "" {
		_ = s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, base string) error {
			return deleteAccountWithBase(ctx, hc, inner, base)
		})
	}
	return nil
}

func endpointHost(ep string) string {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return ""
	}
	if host, _, ok := strings.Cut(ep, ":"); ok {
		return strings.TrimSpace(host)
	}
	return ep
}

// endpointIPv4 — адрес узла для exclude_endpoints сканера: тот принимает
// только ipv4[:port] и на имени или IPv6 отвергает весь конфиг скана.
func endpointIPv4(ep string) string {
	return bareIPHost(endpointHost(ep))
}

func bareIPHost(raw string) string {
	s := strings.TrimSpace(strings.SplitN(raw, "/", 2)[0])
	if ip, err := netip.ParseAddr(s); err == nil && ip.Is4() {
		return ip.String()
	}
	return ""
}

func (s *Service) resolveRouteTunnel(ctx context.Context, iface string) *TunnelRef {
	if iface == "" || s.d.Tunnels == nil {
		return nil
	}
	list, err := s.d.Tunnels.List(ctx)
	if err != nil {
		return nil
	}
	for i := range list {
		if list[i].Iface == iface && !list[i].Protected {
			c := list[i]
			return &c
		}
	}
	return nil
}

func (s *Service) pickNonCollidingIPv4(ctx context.Context, preferred string, avoid ...string) string {
	occupied := map[string]bool{}
	for _, a := range avoid {
		if h := bareIPHost(a); h != "" {
			occupied[h] = true
		}
	}
	if s.d.UsedIPv4 != nil {
		for _, ip := range s.d.UsedIPv4(ctx) {
			if h := bareIPHost(ip); h != "" {
				occupied[h] = true
			}
		}
	}
	if s.d.Tunnels != nil {
		if list, err := s.d.Tunnels.List(ctx); err == nil {
			for _, t := range list {
				if h := bareIPHost(t.AddressV4); h != "" {
					occupied[h] = true
				}
			}
		}
	}
	if h := bareIPHost(preferred); h != "" && !occupied[h] {
		return preferred
	}
	for sub := 0; sub <= 1; sub++ {
		for last := 2; last <= 254; last++ {
			cand := "172.16." + strconv.Itoa(sub) + "." + strconv.Itoa(last)
			if !occupied[cand] {
				return cand
			}
		}
	}
	return "172.16.0.3"
}

// ensureDistinctAccount возвращает учётную запись WARP, чей PublicKey не
// совпадает ни с одним из avoidPubKeys (иначе Cloudflare разрывает внешний
// туннель при появлении пакета с тем же ключом). При необходимости лениво
// регистрирует и шифрует второй слот InnerAccount.
func (s *Service) ensureDistinctAccount(ctx context.Context, routeIface string, avoidPubKeys ...string) (*Account, error) {
	avoid := map[string]bool{}
	for _, k := range avoidPubKeys {
		if k = strings.TrimSpace(k); k != "" {
			avoid[k] = true
		}
	}
	primary := s.d.Store.Account()
	if len(avoid) > 0 && primary != nil && primary.PrivateKey != "" && primary.PublicKey != "" && !avoid[primary.PublicKey] {
		inUse, _, err := s.isAccountKeyInUse(ctx, primary.PublicKey)
		if err != nil {
			return nil, err
		}
		if !inUse {
			return primary, nil
		}
	}

	// Тот же замок, что у Register и DeleteAccountLocal: иначе регистрация
	// слота, завершившаяся после удаления учётной записи, оставила бы слот,
	// который у Cloudflare уже никто не снимет.
	s.acctMu.Lock()
	defer s.acctMu.Unlock()

	if s.d.Store.Account() == nil {
		return nil, ErrNoAccount
	}
	if inner := s.d.Store.InnerAccount(); inner != nil && inner.PrivateKey != "" {
		if avoid[inner.PublicKey] {
			// Готовый слот не перезаписываем: на его ключе работают созданные
			// каскады, а прежняя регистрация осталась бы висеть у Cloudflare.
			return nil, fmt.Errorf("%w: обе учётные записи WARP совпадают с ключом внешнего туннеля", ErrInvalidInput)
		}
		inUse, owner, err := s.isAccountKeyInUse(ctx, inner.PublicKey)
		if err != nil {
			return nil, err
		}
		if inUse {
			return nil, fmt.Errorf("%w: ключ дополнительного слота уже используется живым туннелем %q", ErrInvalidInput, owner)
		}
		return inner, nil
	}

	var fresh *Account
	var firstErr error
	if routeIface != "" && s.d.HTTP != nil {
		if hc, err := s.d.HTTP(routeIface); err == nil && hc != nil {
			reg, rerr := RegisterAccount(ctx, hc)
			if rerr == nil {
				fresh = reg
			} else {
				firstErr = rerr
			}
		} else if err != nil {
			firstErr = err
		}
	}
	if fresh == nil && (firstErr == nil || shouldRetryFallback(firstErr)) {
		err := s.doAccountOpWithBase(ctx, RegisterOptions{}, func(hc *http.Client, base string) error {
			reg, rerr := registerAccountWithBase(ctx, hc, base)
			if rerr != nil {
				return rerr
			}
			fresh = reg
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if fresh == nil {
		return nil, firstErr
	}
	var avoidIPs []string
	if primary != nil && primary.AddressV4 != "" {
		avoidIPs = append(avoidIPs, primary.AddressV4)
	}
	fresh.AddressV4 = s.pickNonCollidingIPv4(ctx, "172.16.0.3", avoidIPs...)
	if err := s.d.Store.SetInnerAccount(fresh); err != nil {
		return nil, err
	}
	s.logInfo("register", "warp-in-warp", "Зарегистрирован дополнительный слот Cloudflare WARP")
	return s.d.Store.InnerAccount(), nil
}

// ── Параметры скана ──────────────────────────────────────────────

func (s *Service) buildParams(ctx context.Context, acct *Account, awg AWGParams, f Filters, maxHits, maxCandidates int, endpoints, exclude []string, ifnameOverride string, outer *TunnelRef) (ScanParams, error) {
	f = normalizeFilters(f)
	wan := s.resolveScanIface(ctx, ifnameOverride)
	tunIP := bareIPHost(acct.AddressV4)
	if tunIP == "" {
		tunIP = DefaultTunnelIPv4
	}
	if outer != nil && outer.IsWarp {
		if h := endpointIPv4(outer.Endpoint); h != "" {
			exclude = append(append([]string(nil), exclude...), h)
		}
		if outerIP := bareIPHost(outer.AddressV4); outerIP != "" && tunIP == outerIP {
			tunIP = s.pickNonCollidingIPv4(ctx, "172.16.0.3", outerIP)
		}
	}
	p := ScanParams{
		PrivateKey: acct.PrivateKey,
		// Реальные туннели (Keenetic NativeWG/System и Linux amneziawg) всегда шлют
		// reserved = [0, 0, 0] в заголовке WireGuard. Подстановка clientId при скане
		// позволяла обходить сигнатуры DPI только в сканере и давала ложноположительные узлы.
		PeerPublicKey:        acct.PeerPublicKey,
		TunnelIP:             tunIP,
		Reserved:             [3]int{0, 0, 0},
		Endpoints:            nonNil(endpoints),
		Prefixes:             DefaultPrefixes,
		Ports:                DefaultPorts,
		ExcludeEndpoints:     nonNil(exclude),
		MaxCandidates:        1500,
		Concurrency:          48,
		PPS:                  200,
		TimeoutMs:            1500,
		DeadlineMs:           int(scanDeadline / time.Millisecond),
		MaxHits:              maxHits,
		Ifname:               wan,
		PingIP:               "1.1.1.1",
		ProbeTrace:           true,
		TraceIP:              "104.16.132.229",
		TraceHost:            "cp.cloudflare.com",
		ProbeTG:              f.RequireTelegram,
		ExcludeColos:         f.ExcludeColos,
		AllowedCountries:     f.AllowedCountries,
		MaxRTTMs:             f.MaxRTTMs,
		RequireTrace:         true,
		RequireTG:            f.RequireTelegram,
		DurabilityPings:      8,
		DurabilityIntervalMs: 180,
	}
	if outer != nil && outer.IsWarp {
		// Внутри внешнего WARP-туннеля нет ТСПУ и почти 100% Anycast-IP
		// приземляются на один и тот же PoP Cloudflare. Высокая параллельность
		// с одним PrivateKey вызывает перезапись сессии на стороне PoP;
		// последовательный проход (Concurrency=1) даёт стабильные 100% хитов.
		p.Concurrency = 1
		p.PPS = 20
		p.DurabilityPings = 2
		p.DurabilityIntervalMs = 120
		p.MaxCandidates = 120
	}
	if p.PeerPublicKey == "" {
		p.PeerPublicKey = WarpPeerPublicKey
	}
	if len(f.Ports) > 0 {
		p.Ports = f.Ports
	}
	if maxCandidates > 0 {
		p.MaxCandidates = maxCandidates
	} else if f.MaxCandidates > 0 {
		p.MaxCandidates = f.MaxCandidates
	}
	if p.MaxCandidates > maxScanCandidates {
		p.MaxCandidates = maxScanCandidates
	}
	if p.MaxHits <= 0 {
		p.MaxHits = 10
	}
	if f.Protocol == ProtocolAWG {
		if outer != nil && outer.IsWarp {
			// Пакеты I1..I5 должны уместиться во внешний MTU 1280. Туннель
			// каскада получает ту же подгонку (Apply), иначе сканер проверял
			// бы не те пакеты, которые туннель потом пошлёт.
			var err error
			if awg, err = fitSignature(awg, nestedSignatureMax); err != nil {
				return p, err
			}
		}
		sig, err := signatureHex(awg)
		if err != nil {
			return p, err
		}
		p.AWG = EngineAWG{Jc: awg.Jc, Jmin: awg.Jmin, Jmax: awg.Jmax, I: sig}
	}
	return p, nil
}

// maxScanCandidates — потолок числа кандидатов скана; им же ограничен
// явный список endpoint'ов ручного скана.
const maxScanCandidates = 20000

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// awgForUse — параметры AWG с заполненной сигнатурой (генерируется один
// раз и сохраняется, чтобы созданные туннели и скан шли одинаково).
func (s *Service) awgForUse() AWGParams {
	cfg := s.d.Store.Config()
	if changed, err := EnsureSignature(&cfg.AWG); err == nil && changed {
		_ = s.d.Store.SetAWG(cfg.AWG)
	}
	return cfg.AWG
}

// ── Ручной скан ──────────────────────────────────────────────────

// StartScan запускает ручной скан в фоне.
func (s *Service) StartScan(req ScanRequest) error {
	if len(req.Endpoints) > maxScanCandidates {
		return fmt.Errorf("%w: в списке endpoint больше %d адресов", ErrInvalidInput, maxScanCandidates)
	}
	for _, ep := range req.Endpoints {
		if err := validEndpoint(ep); err != nil {
			return err
		}
	}
	acct := s.d.Store.Account()
	if acct == nil {
		return ErrNoAccount
	}
	if _, ok := s.d.Runner.Available(); !ok {
		return ErrEngineMissing
	}
	scanIface := s.resolveScanIface(s.rootCtx, req.Ifname)
	if outer := s.resolveRouteTunnel(s.rootCtx, scanIface); outer == nil || !outer.IsWarp {
		inUse, owner, checkErr := s.accountKeyOwner(s.rootCtx, acct)
		if checkErr != nil {
			return checkErr
		}
		if inUse {
			free, freeErr := s.existingFreeAccount(s.rootCtx, acct.PublicKey)
			if freeErr != nil {
				return freeErr
			}
			if free != nil {
				acct = free
			} else {
				return fmt.Errorf("%w: ключ аккаунта WARP используется живым туннелем %q — скан тем же ключом разорвёт его соединение; удалите туннель или импортируйте отдельный аккаунт для сканера", ErrInvalidInput, owner)
			}
		}
	}
	s.mu.Lock()
	if s.scan.Running || s.findJunk.Running {
		s.mu.Unlock()
		return ErrScanRunning
	}
	s.mu.Unlock()
	if !s.engineMu.TryLock() {
		return ErrEngineBusy
	}
	maxHits := req.MaxHits
	if maxHits <= 0 || maxHits > 100 {
		maxHits = 20
	}
	awg := s.awgForUse()
	params, err := s.buildParams(s.rootCtx, acct, awg, req.Filters, maxHits, req.MaxCandidates, req.Endpoints, nil, req.Ifname, nil)
	if err != nil {
		s.engineMu.Unlock()
		return err
	}
	// Зависший процесс сканера иначе держал бы engineMu, а автопилот сидел бы
	// в engine_busy до ручного «Стоп».
	ctx, cancel := context.WithTimeout(s.rootCtx, time.Duration(params.DeadlineMs)*time.Millisecond+scanKillGrace)
	s.mu.Lock()
	s.scan = ScanStatus{Running: true, Owner: "manual", StartedAt: s.d.Now(), Hits: []Hit{}}
	s.scanCancel = cancel
	s.mu.Unlock()
	s.publish(TopicScan, s.ScanSnapshot())
	s.logInfo("scan", "manual", fmt.Sprintf("Запуск скана WARP (протокол %s, исключить %v)", normalizeFilters(req.Filters).Protocol, params.ExcludeColos))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.engineMu.Unlock()
		defer cancel()
		var runErr error
		scanIface := params.Ifname
		outer := s.resolveRouteTunnel(ctx, scanIface)
		if outer != nil && outer.IsWarp {
			if !outer.Running {
				runErr = errors.New("внешний туннель WARP выключен — сначала запустите его")
			} else {
				var scanAcct *Account
				scanAcct, runErr = s.ensureDistinctAccount(ctx, outer.Iface, outer.LocalPublicKey)
				if runErr == nil {
					params, runErr = s.buildParams(ctx, scanAcct, awg, req.Filters, maxHits, req.MaxCandidates, req.Endpoints, nil, outer.Iface, outer)
				}
			}
		}
		lastPub := time.Time{}
		if runErr == nil {
			runErr = s.d.Runner.Run(ctx, params, func(ev EngineEvent) {
				s.mu.Lock()
				switch ev.Type {
				case "start":
					s.scan.Total = ev.Total
				case "progress":
					s.scan.Done, s.scan.Total = ev.Done, ev.Total
				case "hit":
					s.scan.Hits = append(s.scan.Hits, ev.Hit())
					sortHits(s.scan.Hits)
				case "done":
					s.scan.Done = ev.Scanned
					s.scan.Aborted = ev.Aborted
					s.scan.ElapsedMs = ev.ElapsedMs
				}
				s.mu.Unlock()
				if ev.Type == "hit" || time.Since(lastPub) > 700*time.Millisecond {
					lastPub = time.Now()
					s.publish(TopicScan, s.ScanSnapshot())
				}
			})
		}
		if runErr == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			runErr = errors.New("сканер не завершился за отведённое время и был остановлен")
		}
		s.mu.Lock()
		s.scan.Running = false
		s.scan.FinishedAt = s.d.Now()
		if ctx.Err() != nil {
			s.scan.Aborted = true
		}
		if runErr != nil {
			s.scan.Error = runErr.Error()
		}
		s.scanCancel = nil
		hits := len(s.scan.Hits)
		s.mu.Unlock()
		if runErr != nil {
			s.logWarn("scan", "manual", "Скан WARP завершился ошибкой: "+runErr.Error())
		} else {
			s.logInfo("scan", "manual", fmt.Sprintf("Скан WARP завершён: найдено %d узлов", hits))
		}
		s.publish(TopicScan, s.ScanSnapshot())
	}()
	return nil
}

// StopScan останавливает ручной скан.
func (s *Service) StopScan() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanCancel != nil {
		s.scanCancel()
	}
}

// ScanSnapshot — состояние ручного скана.
func (s *Service) ScanSnapshot() ScanStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.scan
	out.Hits = append([]Hit{}, s.scan.Hits...)
	return out
}

func sortHits(h []Hit) {
	// PingMs == 0 — замера нет: такой узел идёт последним, иначе автопилот
	// взял бы его как лучший.
	rank := func(ms int) int {
		if ms <= 0 {
			return math.MaxInt
		}
		return ms
	}
	sort.SliceStable(h, func(i, j int) bool {
		if pi, pj := rank(h[i].PingMs), rank(h[j].PingMs); pi != pj {
			return pi < pj
		}
		return h[i].HandshakeMs < h[j].HandshakeMs
	})
}

// ── Конфигурация и профили ───────────────────────────────────────

// SaveConfig валидирует и сохраняет конфигурацию, перезапуская сторожей.
func (s *Service) SaveConfig(ctx context.Context, in Config) (Config, error) {
	if cp, err := NormalizeContentPadding(in.AWG.ContentPadding); err != nil {
		return Config{}, err
	} else {
		in.AWG.ContentPadding = cp
	}
	cfg := normalizeConfig(cloneConfig(in))
	seen := map[string]bool{}
	targets := map[string]string{}
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		if p.ID == "" {
			p.ID = newID()
		}
		if seen[p.ID] {
			return Config{}, fmt.Errorf("%w: повтор id профиля %s", ErrInvalidInput, p.ID)
		}
		seen[p.ID] = true
		if !p.TargetMode.Valid() {
			return Config{}, fmt.Errorf("%w: профиль %q — неизвестный тип туннеля %q", ErrInvalidInput, p.Name, p.TargetMode)
		}
		if p.TargetID == "" {
			return Config{}, fmt.Errorf("%w: профиль %q — не выбран туннель", ErrInvalidInput, p.Name)
		}
		if p.Name == "" {
			p.Name = string(p.TargetMode) + ":" + p.TargetID
		}
		if !p.Enabled {
			continue
		}
		key := string(p.TargetMode) + "/" + p.TargetID
		if other, dup := targets[key]; dup {
			return Config{}, fmt.Errorf("%w: туннель %s уже привязан к профилю %q", ErrInvalidInput, p.TargetID, other)
		}
		targets[key] = p.Name
		if s.d.Tunnels != nil {
			ref, err := s.d.Tunnels.Get(ctx, p.TargetMode, p.TargetID)
			if err != nil {
				return Config{}, fmt.Errorf("профиль %q: %w", p.Name, err)
			}
			if err := checkTarget(ref); err != nil {
				return Config{}, fmt.Errorf("профиль %q: %w", p.Name, err)
			}
		}
	}
	if cfg.ProfileMode == ProfileModeSingle && cfg.ActiveProfileID != "" && !seen[cfg.ActiveProfileID] {
		cfg.ActiveProfileID = ""
	}
	for i, v := range []string{cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5} {
		// Значение ложится строкой в .conf: перевод строки дописал бы в него
		// посторонние параметры.
		if strings.ContainsAny(v, "\r\n\x00") {
			return Config{}, fmt.Errorf("%w: I%d содержит перевод строки", ErrInvalidInput, i+1)
		}
	}
	switch prof := cfg.AWG.SignatureProfile; prof {
	case "", "none":
		cfg.AWG.SignatureProfile = "none"
		cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = "", "", "", "", ""
		cfg.AWG.GeneratedDomain = ""
		if err := validateAndNormalizeAWGSignature(&cfg.AWG); err != nil {
			return Config{}, err
		}
	case SignatureCustom:
		cfg.AWG.GeneratedDomain = ""
		for idx, spec := range []string{cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5} {
			if _, err := CPSToRaw(spec); err != nil {
				return Config{}, fmt.Errorf("%w: сигнатура I%d: %v", ErrInvalidInput, idx+1, err)
			}
		}
	default:
		if err := validateAndNormalizeAWGSignature(&cfg.AWG); err != nil {
			return Config{}, err
		}
		old := s.d.Store.Config().AWG
		if old.SignatureProfile != prof ||
			normHostPreset(old.SignatureHostPreset) != normHostPreset(cfg.AWG.SignatureHostPreset) ||
			strings.TrimSpace(old.SignatureDomain) != strings.TrimSpace(cfg.AWG.SignatureDomain) {
			// Сменили профиль или пул/домен сигнатуры — перегенерируем пакеты.
			cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = "", "", "", "", ""
			cfg.AWG.GeneratedDomain = ""
		} else if cfg.AWG.I1 == "" {
			// Тот же пресет без пакетов (интерфейс их не шлёт): сигнатура
			// остаётся прежней, а не выдаётся заново на каждое сохранение.
			cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = old.I1, old.I2, old.I3, old.I4, old.I5
			cfg.AWG.GeneratedDomain = old.GeneratedDomain
		}
		if _, err := EnsureSignature(&cfg.AWG); err != nil {
			return Config{}, fmt.Errorf("%w: сигнатура: %v", ErrInvalidInput, err)
		}
		if err := checkSignaturePackets(cfg.AWG); err != nil {
			return Config{}, err
		}
	}
	if err := s.d.Store.SetConfig(cfg); err != nil {
		return Config{}, err
	}
	saved := s.d.Store.Config()
	s.reconcileWatchers(saved)
	if s.d.OnWatchChanged != nil {
		s.d.OnWatchChanged()
	}
	return saved, nil
}

// WatchedTunnels возвращает карту активных профилей автопилота,
// индексированную по ключу "<targetMode>:<targetId>".
func (s *Service) WatchedTunnels() map[string]TunnelWatchInfo {
	if s == nil || s.d.Store == nil {
		return nil
	}
	cfg := s.d.Store.Config()
	if len(cfg.Profiles) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]TunnelWatchInfo)
	for _, p := range cfg.Profiles {
		if !p.Enabled || p.TargetID == "" {
			continue
		}
		if cfg.ProfileMode == ProfileModeSingle && p.ID != cfg.ActiveProfileID {
			continue
		}
		state := WatchIdle
		msg := ""
		if ws := s.watch[p.ID]; ws != nil {
			if ws.State != "" {
				state = ws.State
			}
			msg = ws.Message
		}
		key := string(p.TargetMode) + ":" + p.TargetID
		out[key] = TunnelWatchInfo{
			ProfileID:   p.ID,
			ProfileName: p.Name,
			State:       state,
			Message:     msg,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func checkTarget(ref *TunnelRef) error {
	if ref == nil {
		return errors.New("туннель не найден")
	}
	if ref.Protected {
		if ref.ProtectReason != "" {
			return fmt.Errorf("%w: %s", ErrProtected, ref.ProtectReason)
		}
		return ErrProtected
	}
	if !ref.IsWarp {
		return ErrNotWarp
	}
	return nil
}

func newID() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return "wp" + hex.EncodeToString(b[:])
}

// ── Применение ───────────────────────────────────────────────────

func validateOverrideAWG(in AWGParams) (AWGParams, error) {
	if in.Jc < 0 {
		in.Jc = 0
	}
	if in.Jc > 128 {
		in.Jc = 128
	}
	if in.Jmin < 0 {
		in.Jmin = 0
	}
	if in.Jmax < in.Jmin {
		in.Jmax = in.Jmin
	}
	if in.Jmax > 1280 {
		in.Jmax = 1280
	}
	for idx, v := range []string{in.I1, in.I2, in.I3, in.I4, in.I5} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return AWGParams{}, fmt.Errorf("%w: I%d содержит перевод строки", ErrInvalidInput, idx+1)
		}
	}
	switch prof := strings.TrimSpace(in.SignatureProfile); prof {
	case "", "none":
		in.SignatureProfile = "none"
		in.I1, in.I2, in.I3, in.I4, in.I5 = "", "", "", "", ""
		in.GeneratedDomain = ""
		if err := validateAndNormalizeAWGSignature(&in); err != nil {
			return AWGParams{}, err
		}
	case SignatureCustom:
		in.SignatureProfile = SignatureCustom
		in.GeneratedDomain = ""
		for idx, spec := range []string{in.I1, in.I2, in.I3, in.I4, in.I5} {
			if _, err := CPSToRaw(spec); err != nil {
				return AWGParams{}, fmt.Errorf("%w: сигнатура I%d: %v", ErrInvalidInput, idx+1, err)
			}
		}
	default:
		in.SignatureProfile = prof
		if err := validateAndNormalizeAWGSignature(&in); err != nil {
			return AWGParams{}, err
		}
		if _, err := EnsureSignature(&in); err != nil {
			return AWGParams{}, fmt.Errorf("%w: сигнатура: %v", ErrInvalidInput, err)
		}
		// Готовые I1..I5 EnsureSignature не трогает, а они уходят в .conf.
		if err := checkSignaturePackets(in); err != nil {
			return AWGParams{}, err
		}
	}
	cp, err := NormalizeContentPadding(in.ContentPadding)
	if err != nil {
		return AWGParams{}, err
	}
	in.ContentPadding = cp
	return in, nil
}

func checkSignaturePackets(p AWGParams) error {
	for idx, spec := range []string{p.I1, p.I2, p.I3, p.I4, p.I5} {
		if _, err := CPSToRaw(spec); err != nil {
			return fmt.Errorf("%w: сигнатура I%d: %v", ErrInvalidInput, idx+1, err)
		}
	}
	return nil
}

// Apply создаёт туннель из найденного узла или подменяет endpoint.
func (s *Service) Apply(ctx context.Context, req ApplyRequest) (*ApplyResult, error) {
	if s.d.Tunnels == nil {
		return nil, errors.New("операции с туннелями недоступны")
	}
	if !req.Mode.Valid() {
		return nil, fmt.Errorf("%w: тип туннеля", ErrInvalidInput)
	}
	if err := validEndpoint(req.Endpoint); err != nil {
		return nil, err
	}
	var ref *TunnelRef
	var accountReused bool
	var createOpts CreateOptions
	switch req.Action {
	case "create":
		acct := s.d.Store.Account()
		if acct == nil {
			return nil, ErrNoAccount
		}
		protocol := req.Protocol
		if protocol != ProtocolWG {
			protocol = ProtocolAWG
		}
		// NativeWG-бэкенд без параметров AmneziaWG импортирует туннель, но не
		// запускает («конфиг не содержит параметров обфускации AWG» — стенд,
		// 13.11.2026), поэтому такое сочетание отвергается до создания.
		if req.Mode == TargetNative && protocol == ProtocolWG {
			return nil, fmt.Errorf("%w: тип Native WG требует протокол AmneziaWG — выберите протокол AmneziaWG или тип «Системный»", ErrInvalidInput)
		}
		awg := s.awgForUse()
		if req.AWG != nil {
			var err error
			awg, err = validateOverrideAWG(*req.AWG)
			if err != nil {
				return nil, err
			}
		}
		mtu := DefaultMTU
		viaID := strings.TrimSpace(req.ViaID)
		var outer *TunnelRef
		if viaID != "" {
			if req.Mode == TargetKernel {
				return nil, fmt.Errorf("%w: для WARP-in-WARP выберите тип Native WG или Системный", ErrInvalidInput)
			}
			viaMode := req.ViaMode
			if viaMode == "" {
				if list, lerr := s.d.Tunnels.List(ctx); lerr == nil {
					for _, t := range list {
						if t.ID == viaID {
							viaMode = t.Mode
							break
						}
					}
				}
			}
			if !viaMode.Valid() {
				return nil, fmt.Errorf("%w: не указан тип внешнего туннеля", ErrInvalidInput)
			}
			var err error
			outer, err = s.d.Tunnels.Get(ctx, viaMode, viaID)
			if err != nil {
				return nil, err
			}
			if err := checkTarget(outer); err != nil {
				return nil, err
			}
			if outer.ViaIface != "" {
				return nil, fmt.Errorf("%w: внешний туннель «%s» уже является каскадным WARP-in-WARP", ErrInvalidInput, outer.Name)
			}
			if epHost := endpointHost(req.Endpoint); epHost != "" && epHost == endpointHost(outer.Endpoint) {
				return nil, fmt.Errorf("%w: endpoint %s совпадает с IP внешнего туннеля (%s)", ErrInvalidInput, req.Endpoint, outer.Endpoint)
			}
			mtu = NestedWarpMTU
			if protocol != ProtocolWG {
				if awg, err = fitSignature(awg, nestedSignatureMax); err != nil {
					return nil, err
				}
			}
			createOpts = CreateOptions{ViaMode: outer.Mode, ViaID: outer.ID}
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			if viaID != "" {
				name = "WARP-in-WARP"
			} else {
				name = "WARP"
			}
		}
		// Адрес по умолчанию — заданный пользователем у основной записи;
		// проверяется до регистрации, чтобы негодный ввод её не тратил.
		addrV4 := acct.AddressV4
		if rawAddr := strings.TrimSpace(req.AddressV4); rawAddr != "" {
			norm, err := NormalizeTunnelIPv4(rawAddr)
			if err != nil {
				return nil, err
			}
			if outer != nil && bareIPHost(norm) != "" && bareIPHost(norm) == bareIPHost(outer.AddressV4) {
				return nil, fmt.Errorf("%w: IPv4 внутреннего туннеля совпадает с внешним туннелем (%s)", ErrInvalidInput, outer.AddressV4)
			}
			addrV4 = norm
		} else if outer != nil {
			addrV4 = s.pickNonCollidingIPv4(ctx, addrV4, outer.AddressV4)
		}
		s.collectTunnelAccounts(ctx)
		var (
			tunAcct       *Account
			reusedPrimary bool
		)
		if req.ReuseAccount {
			if acct.PrivateKey == "" {
				return nil, ErrNoAccount
			}
			if inUse, owner, checkErr := s.isAccountKeyInUse(ctx, acct.PublicKey); checkErr != nil {
				return nil, checkErr
			} else if inUse {
				return nil, fmt.Errorf("%w: ключ текущего аккаунта уже используется туннелем %q — для второго туннеля нужна отдельная регистрация или другой импорт", ErrInvalidInput, owner)
			}
			tunAcct = acct
			reusedPrimary = true
		} else {
			regAcct, regErr := s.registerTunnelAccount(ctx, acct)
			if regErr != nil {
				return nil, fmt.Errorf("учётная запись WARP для туннеля (если Cloudflare API недоступен, включите опцию «Использовать текущий аккаунт» в окне создания): %w", regErr)
			}
			tunAcct = regAcct
		}
		cfgAcct := *tunAcct
		cfgAcct.AddressV4 = addrV4
		includeAWG3 := req.Mode == TargetKernel || s.supportsASC3()
		includeAWG31 := (req.Mode == TargetKernel && s.supportsKernelAWG31()) || (req.Mode != TargetKernel && s.supportsASC3())
		conf := BuildConfWithOptions(&cfgAcct, awg, protocol, req.Endpoint, ConfOptions{
			MTU:            mtu,
			IncludeAWG3:    includeAWG3,
			IncludeAWG31:   includeAWG31,
			RangeKeepalive: req.Mode == TargetKernel,
		})
		created, err := s.d.Tunnels.Create(ctx, req.Mode, name, conf, createOpts)
		if err != nil {
			if !reusedPrimary {
				s.dropRegistration(tunAcct)
			}
			return nil, err
		}
		ref = created
		accountReused = reusedPrimary
		if err := s.d.Store.AddTunnelAccount(TunnelAccount{Account: *tunAcct, TunnelMode: created.Mode, TunnelID: created.ID}); err != nil {
			// Туннель работает; не сохранена лишь запись, по которой
			// регистрацию сняли бы после его удаления.
			s.logWarn("apply", name, "Учётная запись туннеля не сохранена: "+err.Error())
		}
		if viaID != "" {
			s.logInfo("apply", name, fmt.Sprintf("Создан WARP-in-WARP (%s) через %s с узлом %s", req.Mode, outer.Name, req.Endpoint))
		} else {
			s.logInfo("apply", name, fmt.Sprintf("Создан туннель WARP (%s) с узлом %s", req.Mode, req.Endpoint))
		}
	case "swap":
		protocol := req.Protocol
		if protocol == "" && req.Filters != nil && req.Filters.Protocol != "" {
			protocol = req.Filters.Protocol
		}
		if protocol != ProtocolWG {
			protocol = ProtocolAWG
		}
		if req.Mode == TargetNative && protocol == ProtocolWG {
			return nil, fmt.Errorf("%w: тип Native WG требует протокол AmneziaWG — выберите протокол AmneziaWG или тип «Системный»", ErrInvalidInput)
		}
		awg := s.awgForUse()
		if req.AWG != nil {
			var err error
			awg, err = validateOverrideAWG(*req.AWG)
			if err != nil {
				return nil, err
			}
		}
		cur, err := s.d.Tunnels.Get(ctx, req.Mode, req.TargetID)
		if err != nil {
			return nil, err
		}
		if err := checkTarget(cur); err != nil {
			return nil, err
		}
		swapOpts, err := buildSwapOptions(protocol, awg, cur.ViaIface != "")
		if err != nil {
			return nil, err
		}
		if err := s.d.Tunnels.SwapEndpoint(ctx, req.Mode, req.TargetID, req.Endpoint, swapOpts); err != nil {
			return nil, err
		}
		tunName := cur.Name
		if req.Colo != "" {
			tunName = s.syncTunnelColoName(ctx, req.Mode, req.TargetID, cur.Name, req.Colo)
		}
		s.noteManualSwap(req.Mode, req.TargetID, req.Endpoint)
		s.logInfo("apply", tunName, fmt.Sprintf("Endpoint заменён вручную: %s → %s", cur.Endpoint, req.Endpoint))
		ref, _ = s.d.Tunnels.Get(ctx, req.Mode, req.TargetID)
		if ref == nil {
			ref = cur
			ref.Name = tunName
		}
	default:
		return nil, fmt.Errorf("%w: action должен быть create или swap", ErrInvalidInput)
	}
	res := &ApplyResult{Tunnel: ref, AccountReused: accountReused}
	if req.EnableAutopilot && ref != nil {
		cfg := s.d.Store.Config()
		filters := DefaultFilters()
		if req.Filters != nil {
			filters = *req.Filters
		}
		if req.Protocol == ProtocolWG {
			filters.Protocol = ProtocolWG
		}
		p := Profile{ID: newID(), Name: ref.Name, Enabled: true, TargetMode: req.Mode, TargetID: ref.ID, Filters: filters}
		// Профиль на тот же туннель заменяем, а не дублируем.
		kept := cfg.Profiles[:0]
		hasOtherEnabled := false
		for _, old := range cfg.Profiles {
			if old.TargetMode == p.TargetMode && old.TargetID == p.TargetID {
				continue
			}
			if old.Enabled {
				hasOtherEnabled = true
			}
			kept = append(kept, old)
		}
		cfg.Profiles = append(kept, p)
		if cfg.ProfileMode == ProfileModeSingle {
			if createOpts.ViaID != "" && hasOtherEnabled {
				cfg.ProfileMode = ProfileModeMulti
				cfg.ActiveProfileID = ""
				s.logInfo("apply", ref.Name, "Автопилот переведён в режим нескольких профилей: каскаду нужен работающий автопилот внешнего туннеля")
			} else {
				cfg.ActiveProfileID = p.ID
			}
		}
		if _, err := s.SaveConfig(ctx, cfg); err != nil {
			// Туннель уже создан (endpoint уже подменён): ошибка в ответе
			// привела бы к повтору и дубликату туннеля. Пустой profileId в
			// ответе — признак, что автопилот не включён.
			s.logWarn("apply", ref.Name, "Туннель готов, но автопилот не включён: "+err.Error())
		} else {
			res.ProfileID = p.ID
		}
	}
	return res, nil
}

func (s *Service) syncTunnelColoName(ctx context.Context, mode TargetMode, id, currentName, colo string) string {
	if s.d.Store != nil && !s.d.Store.Config().TunnelAutoRename() {
		return currentName
	}
	newName, ok := DeriveWarpTunnelName(currentName, colo)
	if !ok || s.d.Tunnels == nil {
		return currentName
	}
	if err := s.d.Tunnels.Rename(ctx, mode, id, newName); err != nil {
		s.logWarn("rename", currentName, fmt.Sprintf("Не удалось переименовать туннель под %s: %v", strings.ToUpper(strings.TrimSpace(colo)), err))
		return currentName
	}
	if s.d.Store != nil {
		if _, err := s.d.Store.SyncProfileTargetName(mode, id, currentName, newName); err != nil {
			s.logWarn("rename", newName, "Имя профиля автопилота не сохранено: "+err.Error())
		}
	}
	s.mu.Lock()
	for _, w := range s.watchers {
		if w.p.TargetMode == mode && w.p.TargetID == id && w.p.Name == currentName {
			w.p.Name = newName
		}
	}
	s.mu.Unlock()
	s.logInfo("rename", newName, fmt.Sprintf("Название туннеля обновлено под колокейшн %s: %s → %s",
		strings.ToUpper(strings.TrimSpace(colo)), currentName, newName))
	return newName
}

func (s *Service) noteManualSwap(mode TargetMode, targetID, endpoint string) {
	now := s.d.Now()
	cfg := s.d.Store.Config()
	changed := false
	s.mu.Lock()
	for _, p := range cfg.Profiles {
		if p.TargetMode != mode || p.TargetID != targetID {
			continue
		}
		if ps, ok := s.stats[p.ID]; ok && ps.CurrentEndpoint != endpoint {
			ps.CurrentEndpoint = endpoint
			ps.EndpointSince = now
			changed = true
		}
		if ws, ok := s.watch[p.ID]; ok {
			ws.Endpoint = endpoint
		}
	}
	s.mu.Unlock()
	if changed {
		s.flushStats()
	}
}

// validEndpoint — строго ip:port (в скобках для IPv6), порт 1..65535. Узлы
// WARP приходят от сканера адресами; имя хоста, зона и любые лишние символы
// отвергаются — endpoint ложится строкой в .conf и в NDMS.
func validEndpoint(ep string) error {
	host, port, err := net.SplitHostPort(ep)
	if err == nil {
		var addr netip.Addr
		if addr, err = netip.ParseAddr(host); err == nil && addr.Zone() != "" {
			err = errors.New("zone")
		}
	}
	if err == nil {
		var n uint64
		if n, err = strconv.ParseUint(port, 10, 16); err == nil && n == 0 {
			err = errors.New("port")
		}
	}
	if err != nil {
		return fmt.Errorf("%w: endpoint должен быть вида ip:port", ErrInvalidInput)
	}
	return nil
}

// ExportConf собирает .conf для найденного эндпоинта без создания туннеля и изменения состояния.
func (s *Service) ExportConf(_ context.Context, req ExportRequest) (*ExportResult, error) {
	if err := validEndpoint(req.Endpoint); err != nil {
		return nil, err
	}
	acct := s.d.Store.Account()
	if acct == nil {
		return nil, ErrNoAccount
	}
	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	switch protocol {
	case "":
		protocol = ProtocolAWG
	case ProtocolAWG, ProtocolWG:
	default:
		return nil, fmt.Errorf("%w: неизвестный протокол %q", ErrInvalidInput, req.Protocol)
	}
	mtu := req.MTU
	if mtu == 0 {
		mtu = DefaultMTU
	}
	if mtu < 576 || mtu > 1500 {
		return nil, fmt.Errorf("%w: MTU должен быть в диапазоне 576–1500", ErrInvalidInput)
	}
	awg := s.awgForUse()
	if req.AWG != nil && protocol != ProtocolWG {
		ovr, err := validateOverrideAWG(*req.AWG)
		if err != nil {
			return nil, err
		}
		awg = ovr
	}
	cfgAcct := *acct
	content := BuildConfWithOptions(&cfgAcct, awg, protocol, strings.TrimSpace(req.Endpoint), ConfOptions{
		MTU:            mtu,
		IncludeAWG3:    true,
		IncludeAWG31:   true,
		RangeKeepalive: true,
	})
	filename := "warp-" + protocol + ".conf"
	if colo := strings.ToUpper(strings.TrimSpace(req.Colo)); isValidColoCode(colo) {
		filename = fmt.Sprintf("warp-%s-%s.conf", strings.ToLower(colo), protocol)
	}
	return &ExportResult{
		Filename: filename,
		Content:  content,
	}, nil
}
