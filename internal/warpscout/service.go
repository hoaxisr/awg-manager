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

// TunnelOps — операции над туннелями, которые автопилоту нужны от
// остального приложения. Реализация живёт в internal/api.
type TunnelOps interface {
	// List — все туннели, пригодные для показа в выборе цели.
	List(ctx context.Context) ([]TunnelRef, error)
	// Get — один туннель; ошибка, если его нет.
	Get(ctx context.Context, mode TargetMode, id string) (*TunnelRef, error)
	// Create создаёт туннель из .conf и возвращает его.
	Create(ctx context.Context, mode TargetMode, name, conf string, opts CreateOptions) (*TunnelRef, error)
	// SwapEndpoint меняет endpoint пира на лету, не пересоздавая интерфейс.
	SwapEndpoint(ctx context.Context, mode TargetMode, id, endpoint string) error
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
	// HTTPFallbacks — резервные HTTP-клиенты (прокси/туннель) на случай
	// блокировки API регистрации Cloudflare на прямом WAN.
	HTTPFallbacks func(ctx context.Context) []*http.Client
	Bus           Publisher
	Log           Logger
	// Now/Sleep — для тестов.
	Now func() time.Time
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
	Engine             EngineInfo          `json:"engine"`
	Account            *Account            `json:"account"`
	HasInnerAccount    bool                `json:"hasInnerAccount"`
	AccountUnreadable  bool                `json:"accountUnreadable"`
	DefaultWAN         string              `json:"defaultWan,omitempty"`
	Routes             []RouteRef          `json:"routes"`
	ObfuscationSources []ObfuscationSource `json:"obfuscationSources,omitempty"`
	UsedIPv4           []string            `json:"usedIpv4,omitempty"`
	Config             Config              `json:"config"`
	Scan               ScanStatus          `json:"scan"`
	FindJunk           FindJunkStatus      `json:"findJunk"`
	Watch              []WatchStatus       `json:"watch"`
	Stats              []ProfileStats      `json:"stats"`
	History            []SwapEvent         `json:"history"`
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
}

// ApplyResult — итог применения.
type ApplyResult struct {
	Tunnel    *TunnelRef `json:"tunnel"`
	ProfileID string     `json:"profileId,omitempty"`
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
	return State{
		Engine:             s.Engine(),
		Account:            s.d.Store.Account(),
		HasInnerAccount:    s.d.Store.InnerAccount() != nil,
		AccountUnreadable:  s.d.Store.AccountUnreadable(),
		DefaultWAN:         defaultWAN,
		Routes:             routes,
		ObfuscationSources: obfSources,
		UsedIPv4:           usedIPv4,
		Config:             cfg,
		Scan:               scan,
		FindJunk:           findJunk,
		Watch:              watch,
		Stats:              stats,
		History:            s.d.Store.History(),
	}
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

// Targets — туннели для выбора цели.
func (s *Service) Targets(ctx context.Context) ([]TunnelRef, error) {
	if s.d.Tunnels == nil {
		return []TunnelRef{}, nil
	}
	list, err := s.d.Tunnels.List(ctx)
	if err != nil {
		return nil, err
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
// при сетевой блокировке ТСПУ перебирает резервные выходы (прокси/туннели).
func (s *Service) doAccountOp(ctx context.Context, fn func(*http.Client) error) error {
	var firstErr error
	if hc, err := s.httpClient(ctx); err == nil && hc != nil {
		err = fn(hc)
		if err == nil || !shouldRetryFallback(err) {
			return err
		}
		firstErr = err
	} else if err != nil {
		firstErr = err
	}
	if s.d.HTTPFallbacks != nil {
		for _, fb := range s.d.HTTPFallbacks(ctx) {
			if fb == nil || ctx.Err() != nil {
				continue
			}
			err := fn(fb)
			if err == nil || !shouldRetryFallback(err) {
				return err
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return errors.New("HTTP-клиент не настроен")
}

// Register регистрирует (или перевыпускает ключи) учётную запись WARP.
func (s *Service) Register(ctx context.Context) (*Account, error) {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	old := s.d.Store.Account()
	var acct *Account
	var oldNotFound bool
	err := s.doAccountOp(ctx, func(hc *http.Client) error {
		if old != nil && old.ID != "" && old.Token != "" {
			rotated, rotErr := RotateAccountKey(ctx, hc, old)
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
		fresh, regErr := RegisterAccount(ctx, hc)
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
	if old != nil && old.ID != "" && old.ID != acct.ID && !oldNotFound {
		s.dropRegistration(old)
	}
	s.logInfo("register", "warp", "Зарегистрирована учётная запись Cloudflare WARP")
	return s.d.Store.Account(), nil
}

// dropRegistration снимает регистрацию у Cloudflare лучшим усилием, в фоне,
// под общим WaitGroup сервиса, чтобы Stop дожидался запроса.
func (s *Service) dropRegistration(prev *Account) {
	if prev == nil || prev.ID == "" || s.rootCtx.Err() != nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		dctx, cancel := context.WithTimeout(s.rootCtx, 20*time.Second)
		defer cancel()
		_ = s.doAccountOp(dctx, func(hc *http.Client) error {
			return DeleteAccount(dctx, hc, prev)
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
	if err := s.doAccountOp(ctx, func(hc *http.Client) error {
		reg, err := RegisterAccount(ctx, hc)
		if err != nil {
			return err
		}
		fresh = reg
		return nil
	}); err != nil {
		return nil, err
	}
	if primary != nil && primary.License != "" {
		if err := s.doAccountOp(ctx, func(hc *http.Client) error {
			return ApplyLicense(ctx, hc, fresh, primary.License)
		}); err != nil {
			s.logWarn("register", "warp", "Лицензия WARP+ к учётной записи туннеля не применена: "+err.Error())
		}
	}
	return fresh, nil
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
		s.dropRegistration(&ta.Account)
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
	if err := s.doAccountOp(ctx, func(hc *http.Client) error {
		return ApplyLicense(ctx, hc, acct, license)
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
	if err := s.d.Store.SetAccount(nil); err != nil {
		return err
	}
	_ = s.d.Store.SetInnerAccount(nil)
	if acct != nil {
		_ = s.doAccountOp(ctx, func(hc *http.Client) error {
			return DeleteAccount(ctx, hc, acct)
		})
	}
	if inner != nil {
		_ = s.doAccountOp(ctx, func(hc *http.Client) error {
			return DeleteAccount(ctx, hc, inner)
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

// existingAccountAvoiding — уже имеющаяся учётная запись, чей ключ отличается
// от avoidKey; второй слот в приоритете. Сети не касается: автоподмена
// работает без пользователя и регистрировать слот сама не должна.
func (s *Service) existingAccountAvoiding(avoidKey string) *Account {
	for _, a := range []*Account{s.d.Store.InnerAccount(), s.d.Store.Account()} {
		if a != nil && a.PrivateKey != "" && a.PublicKey != avoidKey {
			return a
		}
	}
	return nil
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
		return primary, nil
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
		if !avoid[inner.PublicKey] {
			return inner, nil
		}
		// Готовый слот не перезаписываем: на его ключе работают созданные
		// каскады, а прежняя регистрация осталась бы висеть у Cloudflare.
		return nil, fmt.Errorf("%w: обе учётные записи WARP совпадают с ключом внешнего туннеля", ErrInvalidInput)
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
		err := s.doAccountOp(ctx, func(hc *http.Client) error {
			reg, rerr := RegisterAccount(ctx, hc)
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
		scanIface := s.resolveScanIface(ctx, req.Ifname)
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
	case SignatureCustom:
		for idx, spec := range []string{cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5} {
			if _, err := CPSToRaw(spec); err != nil {
				return Config{}, fmt.Errorf("%w: сигнатура I%d: %v", ErrInvalidInput, idx+1, err)
			}
		}
	default:
		old := s.d.Store.Config().AWG
		if old.SignatureProfile != prof {
			// Сменили профиль сигнатуры — перегенерируем пакеты.
			cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = "", "", "", "", ""
		} else if cfg.AWG.I1 == "" {
			// Тот же пресет без пакетов (интерфейс их не шлёт): сигнатура
			// остаётся прежней, а не выдаётся заново на каждое сохранение.
			cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = old.I1, old.I2, old.I3, old.I4, old.I5
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
	return saved, nil
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
	case SignatureCustom:
		in.SignatureProfile = SignatureCustom
		for idx, spec := range []string{in.I1, in.I2, in.I3, in.I4, in.I5} {
			if _, err := CPSToRaw(spec); err != nil {
				return AWGParams{}, fmt.Errorf("%w: сигнатура I%d: %v", ErrInvalidInput, idx+1, err)
			}
		}
	default:
		in.SignatureProfile = prof
		if _, err := EnsureSignature(&in); err != nil {
			return AWGParams{}, fmt.Errorf("%w: сигнатура: %v", ErrInvalidInput, err)
		}
		// Готовые I1..I5 EnsureSignature не трогает, а они уходят в .conf.
		if err := checkSignaturePackets(in); err != nil {
			return AWGParams{}, err
		}
	}
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
			if awg, err = fitSignature(awg, nestedSignatureMax); err != nil {
				return nil, err
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
		tunAcct, err := s.registerTunnelAccount(ctx, acct)
		if err != nil {
			return nil, fmt.Errorf("учётная запись WARP для туннеля: %w", err)
		}
		cfgAcct := *tunAcct
		cfgAcct.AddressV4 = addrV4
		conf := BuildConfWithMTU(&cfgAcct, awg, protocol, req.Endpoint, mtu)
		created, err := s.d.Tunnels.Create(ctx, req.Mode, name, conf, createOpts)
		if err != nil {
			s.dropRegistration(tunAcct)
			return nil, err
		}
		ref = created
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
		cur, err := s.d.Tunnels.Get(ctx, req.Mode, req.TargetID)
		if err != nil {
			return nil, err
		}
		if err := checkTarget(cur); err != nil {
			return nil, err
		}
		if err := s.d.Tunnels.SwapEndpoint(ctx, req.Mode, req.TargetID, req.Endpoint); err != nil {
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
	res := &ApplyResult{Tunnel: ref}
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
