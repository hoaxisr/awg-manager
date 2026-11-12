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

// TunnelOps — операции над туннелями, которые автопилоту нужны от
// остального приложения. Реализация живёт в internal/api.
type TunnelOps interface {
	// List — все туннели, пригодные для показа в выборе цели.
	List(ctx context.Context) ([]TunnelRef, error)
	// Get — один туннель; ошибка, если его нет.
	Get(ctx context.Context, mode TargetMode, id string) (*TunnelRef, error)
	// Create создаёт туннель из .conf и возвращает его.
	Create(ctx context.Context, mode TargetMode, name, conf string) (*TunnelRef, error)
	// SwapEndpoint меняет endpoint пира на лету, не пересоздавая интерфейс.
	SwapEndpoint(ctx context.Context, mode TargetMode, id, endpoint string) error
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
	// Routes — доступные маршруты (WAN-интерфейсы и запущенные не-WARP туннели)
	// для выбора интерфейса сканера.
	Routes func(ctx context.Context) []RouteRef
	HTTP   HTTPClientFactory
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
	Engine            EngineInfo     `json:"engine"`
	Account           *Account       `json:"account"`
	AccountUnreadable bool           `json:"accountUnreadable"`
	DefaultWAN        string         `json:"defaultWan,omitempty"`
	Routes            []RouteRef     `json:"routes"`
	Config            Config         `json:"config"`
	Scan              ScanStatus     `json:"scan"`
	Watch             []WatchStatus  `json:"watch"`
	Stats             []ProfileStats `json:"stats"`
	History           []SwapEvent    `json:"history"`
}

// ApplyRequest — применить найденный эндпоинт.
type ApplyRequest struct {
	// Action: "create" — новый туннель, "swap" — подменить endpoint.
	Action          string     `json:"action"`
	Endpoint        string     `json:"endpoint"`
	Mode            TargetMode `json:"mode"`
	TargetID        string     `json:"targetId,omitempty"`
	Name            string     `json:"name,omitempty"`
	Protocol        string     `json:"protocol,omitempty"`
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
	acctMu sync.Mutex

	mu         sync.Mutex
	scan       ScanStatus
	scanCancel context.CancelFunc
	watch      map[string]*WatchStatus
	stats      map[string]*ProfileStats
	statsDirty bool // в stats есть изменения, не дошедшие до диска
	watchers   map[string]*watcher
	rootCtx    context.Context
	rootCancel context.CancelFunc
	wg         sync.WaitGroup
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
		watch: map[string]*WatchStatus{}, stats: statsMap, watchers: map[string]*watcher{},
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
	return State{
		Engine:            s.Engine(),
		Account:           s.d.Store.Account(),
		AccountUnreadable: s.d.Store.AccountUnreadable(),
		DefaultWAN:        defaultWAN,
		Routes:            routes,
		Config:            cfg,
		Scan:              scan,
		Watch:             watch,
		Stats:             stats,
		History:           s.d.Store.History(),
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
	if err := s.d.Store.SetAccount(acct); err != nil {
		return nil, err
	}
	if old != nil && old.ID != "" && old.ID != acct.ID && !oldNotFound && s.rootCtx.Err() == nil {
		// Старую регистрацию снимаем лучшим усилием под общим WaitGroup сервиса,
		// чтобы Stop дожидался завершения фонового запроса.
		s.wg.Add(1)
		go func(prev *Account) {
			defer s.wg.Done()
			dctx, cancel := context.WithTimeout(s.rootCtx, 20*time.Second)
			defer cancel()
			_ = s.doAccountOp(dctx, func(hc *http.Client) error {
				return DeleteAccount(dctx, hc, prev)
			})
		}(old)
	}
	s.logInfo("register", "warp", "Зарегистрирована учётная запись Cloudflare WARP")
	return s.d.Store.Account(), nil
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

// DeleteAccountLocal забывает учётную запись (и снимает её у Cloudflare).
func (s *Service) DeleteAccountLocal(ctx context.Context) error {
	s.acctMu.Lock()
	defer s.acctMu.Unlock()
	acct := s.d.Store.Account()
	if err := s.d.Store.SetAccount(nil); err != nil {
		return err
	}
	if acct != nil {
		_ = s.doAccountOp(ctx, func(hc *http.Client) error {
			return DeleteAccount(ctx, hc, acct)
		})
	}
	return nil
}

// ── Параметры скана ──────────────────────────────────────────────

func (s *Service) buildParams(ctx context.Context, acct *Account, awg AWGParams, f Filters, maxHits, maxCandidates int, endpoints, exclude []string, ifnameOverride string) (ScanParams, error) {
	f = normalizeFilters(f)
	wan := s.resolveScanIface(ctx, ifnameOverride)
	p := ScanParams{
		PrivateKey:       acct.PrivateKey,
		PeerPublicKey:    acct.PeerPublicKey,
		TunnelIP:         strings.SplitN(acct.AddressV4, "/", 2)[0],
		Reserved:         acct.Reserved(),
		Endpoints:        nonNil(endpoints),
		Prefixes:         DefaultPrefixes,
		Ports:            DefaultPorts,
		ExcludeEndpoints: nonNil(exclude),
		MaxCandidates:    1500,
		Concurrency:      48,
		PPS:              200,
		TimeoutMs:        1500,
		DeadlineMs:       int(scanDeadline / time.Millisecond),
		MaxHits:          maxHits,
		Ifname:           wan,
		PingIP:           "1.1.1.1",
		ProbeTrace:       true,
		TraceIP:          "104.16.132.229",
		TraceHost:        "cp.cloudflare.com",
		ProbeTG:          f.RequireTelegram,
		ExcludeColos:     f.ExcludeColos,
		AllowedCountries: f.AllowedCountries,
		MaxRTTMs:         f.MaxRTTMs,
		RequireTrace:     true,
		RequireTG:        f.RequireTelegram,
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
		_ = s.d.Store.SetConfig(cfg)
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
	if s.scan.Running {
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
	params, err := s.buildParams(s.rootCtx, acct, s.awgForUse(), req.Filters, maxHits, req.MaxCandidates, req.Endpoints, nil, req.Ifname)
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
		lastPub := time.Time{}
		runErr := s.d.Runner.Run(ctx, params, func(ev EngineEvent) {
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
	if prof := cfg.AWG.SignatureProfile; prof != "none" && prof != "" {
		old := s.d.Store.Config().AWG
		if old.SignatureProfile != prof {
			// Сменили профиль сигнатуры — перегенерируем пакеты.
			cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = "", "", "", "", ""
		}
		if _, err := EnsureSignature(&cfg.AWG); err != nil {
			return Config{}, fmt.Errorf("сигнатура: %w", err)
		}
	} else {
		cfg.AWG.I1, cfg.AWG.I2, cfg.AWG.I3, cfg.AWG.I4, cfg.AWG.I5 = "", "", "", "", ""
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
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = "WARP"
		}
		conf := BuildConf(acct, s.awgForUse(), protocol, req.Endpoint)
		created, err := s.d.Tunnels.Create(ctx, req.Mode, name, conf)
		if err != nil {
			return nil, err
		}
		ref = created
		s.logInfo("apply", name, fmt.Sprintf("Создан туннель WARP (%s) с узлом %s", req.Mode, req.Endpoint))
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
		s.noteManualSwap(req.Mode, req.TargetID, req.Endpoint)
		s.logInfo("apply", cur.Name, fmt.Sprintf("Endpoint заменён вручную: %s → %s", cur.Endpoint, req.Endpoint))
		ref, _ = s.d.Tunnels.Get(ctx, req.Mode, req.TargetID)
		if ref == nil {
			ref = cur
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
		for _, old := range cfg.Profiles {
			if old.TargetMode == p.TargetMode && old.TargetID == p.TargetID {
				continue
			}
			kept = append(kept, old)
		}
		cfg.Profiles = append(kept, p)
		if cfg.ProfileMode == ProfileModeSingle {
			cfg.ActiveProfileID = p.ID
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
