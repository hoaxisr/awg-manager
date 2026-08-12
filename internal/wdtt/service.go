package wdtt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/proxysup"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

type Service struct {
	store       *Store
	dataDir     string
	clientBin   string
	serverBin   string
	clientProcs *processRegistry
	serverProcs *processRegistry

	// mu сериализует Load-modify-Save методы: без него два конкурентных
	// запроса теряют правки друг друга и могут выдать один listen-порт дважды.
	mu sync.Mutex

	versionPath     string
	installSpecs    *ArchSpecs
	downloader      childproc.Downloader
	installMu       sync.Mutex
	installing      bool
	appLog          *logging.ScopedLogger
	listenChecker   LocalListenPortChecker
	clientHealth    *healthTracker
	clientStall     *healthTracker
	startBackoff    *proxysup.Backoff
	relayProbe      RelayProbe
	accessMgr       AccessManager
	ifaceChecker    InterfaceChecker
	ndmsIfaces      NDMSOpkgTunCommands
	opkgIndices     OpkgTunIndexLister
	opkgExist       OpkgTunExistChecker
	opkgScan        func(ctx context.Context, description string) ([]string, error)
	routerReconcile RouterReconciler
	clientRoutes    ClientRouteHooks
	policyPermit    NDMSPolicyPermitter
	policyList      NDMSPolicyLister
	policyTables    NDMSPolicyTableGetter
	policyMarks     NDMSPolicyMarkGetter
	ingressEnsurer  IngressRefEnsurer

	// После listen-repair доводит новый listen до Endpoint linked AWG-туннелей
	// (не только в хранилище, но и на живом интерфейсе — см. api.SyncLinkedTunnelEndpoints).
	linkedEndpointSync func(clientID, listen string) (int, error)

	// natIdleSwept — снос entware-NAT в холостом тике уже сделан. Без лока:
	// пишется и читается только из горутины natReconcileLoop.
	natIdleSwept bool

	wgIfaceMu        sync.Mutex
	wgIfaceFlagKnown bool
	wgIfaceFlagOK    bool

	// Кеш сверки бинарей с пином (см. binariesMatchSpecs).
	matchMu  sync.Mutex
	matchKey string
	matchVal bool

	// opkgStarts — счётчик стартов серверов в полёте. Между созданием
	// OpkgTun и запуском процесса reap увидел бы «владельца нет» и снёс
	// интерфейс из-под старта.
	opkgStartMu sync.Mutex
	opkgStarts  int

	// clientStarts — per-client счётчик стартов в полёте (StartClientInstance
	// целиком, супервизор и API учитываются одинаково). В отличие от
	// opkgStarts (глобальный, для reap/сервера) — не даёт старту одного
	// клиента глушить reconcile/эскалацию у другого. Совещательный fast-path
	// (TOCTOU: окно между проверкой и стартом открыто) — жёсткая сериализация
	// самого старта обеспечивается clientStartLocks (TryLock внутри
	// StartClientInstance).
	clientStartMu    sync.Mutex
	clientStarts     map[string]int
	clientStartLocks map[string]*sync.Mutex

	// serverStartLocks — то же для серверов: старт тянет NDMS/RCI и живёт
	// секунды, а флаг Enabled оба пути пишут последним действием.
	serverStartMu    sync.Mutex
	serverStartLocks map[string]*sync.Mutex
}

// ErrClientStartInFlight — StartClientInstance этого клиента уже выполняется
// где-то ещё (TryLock не взят); возвращается без какой-либо RCI-работы.
var ErrClientStartInFlight = errors.New("старт клиента уже выполняется")

// tryLockClientStart — жёсткая per-client сериализация StartClientInstance:
// второй конкурентный вызов для того же id не блокируется, а сразу получает
// отказ (в отличие от clientStartInFlight — совещательной проверки для
// супервизора). unlock должен вызываться defer'ом сразу после успешного
// захвата.
func (s *Service) tryLockClientStart(id string) (unlock func(), ok bool) {
	l := s.clientStartLock(id)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

// lockClientStart — тот же лок, но с ожиданием: для остановки, которая обязана
// состояться, а не отступить. Без него стоп, попавший в окно идущего старта,
// снимал Enabled раньше, чем старт дописывал его обратно в true, — клиент
// оставался «включённым» вопреки решению пользователя, и супервизор поднимал
// его снова.
func (s *Service) lockClientStart(id string) (unlock func()) {
	l := s.clientStartLock(id)
	l.Lock()
	return l.Unlock
}

func (s *Service) clientStartLock(id string) *sync.Mutex {
	s.clientStartMu.Lock()
	defer s.clientStartMu.Unlock()
	if s.clientStartLocks == nil {
		s.clientStartLocks = make(map[string]*sync.Mutex)
	}
	l, exists := s.clientStartLocks[id]
	if !exists {
		l = &sync.Mutex{}
		s.clientStartLocks[id] = l
	}
	return l
}

// ErrServerStartInFlight — StartServerInstance этого сервера уже выполняется
// где-то ещё; возвращается без какой-либо RCI-работы.
var ErrServerStartInFlight = errors.New("старт сервера уже выполняется")

// tryLockServerStart / lockServerStart — серверный аналог клиентских локов:
// старт отступает (супервизор попробует на следующем тике), стоп ждёт.
func (s *Service) tryLockServerStart(id string) (unlock func(), ok bool) {
	l := s.serverStartLock(id)
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

func (s *Service) lockServerStart(id string) (unlock func()) {
	l := s.serverStartLock(id)
	l.Lock()
	return l.Unlock
}

func (s *Service) serverStartLock(id string) *sync.Mutex {
	s.serverStartMu.Lock()
	defer s.serverStartMu.Unlock()
	if s.serverStartLocks == nil {
		s.serverStartLocks = make(map[string]*sync.Mutex)
	}
	l, exists := s.serverStartLocks[id]
	if !exists {
		l = &sync.Mutex{}
		s.serverStartLocks[id] = l
	}
	return l
}

func (s *Service) beginOpkgStart() {
	s.opkgStartMu.Lock()
	s.opkgStarts++
	s.opkgStartMu.Unlock()
}

func (s *Service) endOpkgStart() {
	s.opkgStartMu.Lock()
	s.opkgStarts--
	s.opkgStartMu.Unlock()
}

func (s *Service) opkgStartsInFlight() bool {
	s.opkgStartMu.Lock()
	defer s.opkgStartMu.Unlock()
	return s.opkgStarts > 0
}

func (s *Service) beginClientStart(id string) {
	s.clientStartMu.Lock()
	if s.clientStarts == nil {
		s.clientStarts = make(map[string]int)
	}
	s.clientStarts[id]++
	s.clientStartMu.Unlock()
}

func (s *Service) endClientStart(id string) {
	s.clientStartMu.Lock()
	if s.clientStarts[id] > 0 {
		s.clientStarts[id]--
		if s.clientStarts[id] == 0 {
			delete(s.clientStarts, id)
		}
	}
	s.clientStartMu.Unlock()
}

func (s *Service) clientStartInFlight(id string) bool {
	s.clientStartMu.Lock()
	defer s.clientStartMu.Unlock()
	return s.clientStarts[id] > 0
}

func NewService(dataDir, runtimeDir, clientBin, serverBin string) *Service {
	return &Service{
		store:        NewStore(dataDir),
		dataDir:      dataDir,
		clientBin:    clientBin,
		serverBin:    serverBin,
		versionPath:  filepath.Join(dataDir, "wdtt-version.json"),
		clientProcs:  newProcessRegistry("client", clientBin, runtimeDir),
		serverProcs:  newProcessRegistry("server", serverBin, runtimeDir),
		clientHealth: newHealthTracker(clientHealthStrikes),
		clientStall:  newHealthTracker(clientStallStrikes),
		startBackoff: newStartBackoff(),
	}
}

func (s *Service) SetLogger(appLogger logging.AppLogger) {
	s.appLog = logging.NewScopedLogger(appLogger, logging.GroupRouting, "wdtt")
}

func (s *Service) SetListenPortChecker(c LocalListenPortChecker) {
	s.listenChecker = c
}

func (s *Service) SetAccessManager(m AccessManager) {
	s.accessMgr = m
}

func (s *Service) SetInterfaceChecker(c InterfaceChecker) {
	s.ifaceChecker = c
}

func (s *Service) SetRelayProbe(p RelayProbe) {
	s.relayProbe = p
}

// SetLinkedEndpointSync wires AWG tunnel endpoint sync after listen-repair.
func (s *Service) SetLinkedEndpointSync(fn func(clientID, listen string) (int, error)) {
	s.linkedEndpointSync = fn
}

func (s *Service) SetInstallSpecs(specs ArchSpecs) {
	s.installSpecs = &specs
}

func (s *Service) SetDownloader(dl childproc.Downloader) {
	s.downloader = dl
}

func (s *Service) occupiedLocalListenPorts(selfClientID string) map[int]bool {
	if s.listenChecker == nil {
		return nil
	}
	used, err := s.listenChecker.OccupiedLocalListenPorts(selfClientID, "")
	if err != nil || len(used) == 0 {
		return nil
	}
	return used
}

func (s *Service) reservedServerPortsExcept(id string) map[int]bool {
	used := s.occupiedLocalListenPorts("")
	if len(used) == 0 {
		return used
	}
	inst, err := s.serverInstance(id)
	if err != nil {
		return used
	}
	out := map[int]bool{}
	for port, v := range used {
		if v {
			out[port] = true
		}
	}
	if port, err := listenPort(inst.Config.Listen); err == nil {
		delete(out, port)
	}
	if inst.Config.WgPort > 0 {
		delete(out, inst.Config.WgPort)
	}
	return out
}

func (s *Service) GetConfig() (Config, error) {
	return s.store.Load()
}

func (s *Service) UpdateClientConfig(cfg ClientConfig) error {
	return s.UpdateClientInstance(DefaultInstanceID, cfg)
}

func (s *Service) UpdateClientInstance(id string, cfg ClientConfig) error {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("клиент %q не найден", id)
	}
	listens := clientListenAddresses(full.Clients)
	cfg.Listen = ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(id), 9000, 9200)
	cfg = normalizeClientConfig(cfg)
	cfg.Enabled = full.Clients[idx].Config.Enabled
	cfg.NdmsIface = full.Clients[idx].Config.NdmsIface
	cfg.RawIface = full.Clients[idx].Config.RawIface
	cfg.RawClientIP = full.Clients[idx].Config.RawClientIP
	cfg.RawClientMTU = full.Clients[idx].Config.RawClientMTU
	cfg.DeviceID = full.Clients[idx].Config.DeviceID
	full.Clients[idx].Config = cfg
	s.startBackoff.Forget(clientKey(id))
	s.startBackoff.Forget(clientHealthKey(id))
	s.startBackoff.Forget(reconcileKey(id))
	s.startBackoff.Forget(clientStallKey(id))
	saveErr := s.store.Save(full)
	s.mu.Unlock()
	return saveErr
}

func (s *Service) CreateClient(in CreateClientInput) (ClientInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ClientInstance{}, err
	}
	cfg := DefaultClientConfig()
	if in.Config != nil {
		cfg = *in.Config
	}
	cfg = normalizeClientConfig(cfg)
	cfg.Listen = nextClientListen(full.Clients, s.occupiedLocalListenPorts(""))
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("Клиент %d", len(full.Clients)+1)
	}
	inst := ClientInstance{ID: childproc.NewInstanceID(), Name: name, Config: cfg}
	full.Clients = append(full.Clients, inst)
	if err := s.store.Save(full); err != nil {
		return ClientInstance{}, err
	}
	return inst, nil
}

func (s *Service) DeleteClient(id string) error {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("клиент %q не найден", id)
	}
	inst := full.Clients[idx]
	full.Clients = append(full.Clients[:idx], full.Clients[idx+1:]...)
	saveErr := s.store.Save(full)
	s.startBackoff.Forget(clientKey(id))
	s.startBackoff.Forget(clientHealthKey(id))
	s.startBackoff.Forget(reconcileKey(id))
	s.startBackoff.Forget(clientStallKey(id))
	s.clientHealth.reset(id)
	s.clientStall.reset(id)
	s.mu.Unlock()
	_ = s.clientProcs.get(id).Stop()
	if !inst.Config.UsesWireGuard() {
		_ = s.teardownClientOpkgTun(context.Background(), inst.Config)
	}
	return saveErr
}

func (s *Service) RenameClient(id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", id)
	}
	full.Clients[idx].Name = strings.TrimSpace(name)
	if err := s.store.Save(full); err != nil {
		return err
	}
	inst := full.Clients[idx]
	if !inst.Config.UsesWireGuard() && inst.Config.usesNDMSOpkgTun() &&
		s.clientProcs.get(id).Status().Running && s.ndmsIfaces != nil {
		s.syncClientOpkgNDMSDescription(context.Background(), id, inst.Config)
	}
	return nil
}

func (s *Service) ImportLink(id, link string) (ClientInstance, ImportPayload, error) {
	payload, err := DecodeImport(link)
	if err != nil {
		return ClientInstance{}, ImportPayload{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ClientInstance{}, payload, err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return ClientInstance{}, payload, fmt.Errorf("клиент %q не найден", id)
	}
	cfg := ApplyImport(full.Clients[idx].Config, payload)
	listens := clientListenAddresses(full.Clients)
	cfg.Listen = ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(id), 9000, 9200)
	if name := strings.TrimSpace(payload.Name); name != "" {
		full.Clients[idx].Name = name
	}
	full.Clients[idx].Config = cfg
	s.startBackoff.Forget(clientKey(id))
	s.startBackoff.Forget(clientHealthKey(id))
	s.startBackoff.Forget(reconcileKey(id))
	s.startBackoff.Forget(clientStallKey(id))
	if err := s.store.Save(full); err != nil {
		return ClientInstance{}, payload, err
	}
	return full.Clients[idx], payload, nil
}

func (s *Service) DecodeLink(link string) (LinkDecodeResult, error) {
	return DecodeLink(link)
}

func (s *Service) Status() Status {
	cfg, err := s.store.Load()
	if err != nil {
		cfg = DefaultConfig()
	}
	version, available := s.InstallInfo()
	installedVersion, updateAvailable := s.installStatusFields(version)
	clock := routerclock.Get()
	st := Status{
		ServerSupported:  s.installSpecs.serverSupported() || binaryPresent(s.serverBin),
		InstallAvailable: available,
		InstallVersion:   version,
		InstalledVersion: installedVersion,
		UpdateAvailable:  updateAvailable,
		Installing:       s.Installing(),
		RouterClock:      clock.Now.Format("2006-01-02 15:04:05") + " " + clock.ZoneName,
	}
	for _, c := range cfg.Clients {
		ps := s.clientProcs.get(c.ID).Status()
		ps.LastError = procport.EnrichBindError(ps.LastError, c.Config.Listen, procport.ProtoUDP)
		if c.Config.usesNDMSOpkgTun() {
			ps.NdmsIface = c.Config.ndmsAccessIface()
			ps.RawIface = c.Config.kernelRawIface()
		}
		st.Clients = append(st.Clients, InstanceStatus{
			ID:     c.ID,
			Name:   c.Name,
			Status: ps,
		})
	}
	for _, srv := range cfg.Servers {
		ps := s.serverProcs.get(srv.ID).Status()
		ps.LastError = procport.EnrichBindErrorMulti(ps.LastError, srv.Config.ServerListenAddrs(), procport.ProtoUDP)
		st.Servers = append(st.Servers, InstanceStatus{
			ID:     srv.ID,
			Name:   srv.Name,
			Status: ps,
		})
	}
	if cs := instanceStatusByID(st.Clients, DefaultInstanceID); cs != nil {
		st.Client = *cs
	} else if len(st.Clients) > 0 {
		st.Client = st.Clients[0].Status
	}
	if ss := instanceStatusByID(st.Servers, DefaultInstanceID); ss != nil {
		st.Server = *ss
	} else if len(st.Servers) > 0 {
		st.Server = st.Servers[0].Status
	}
	return st
}

func instanceStatusByID(list []InstanceStatus, id string) *ProcessStatus {
	for _, item := range list {
		if item.ID == id {
			st := item.Status
			return &st
		}
	}
	return nil
}

func (s *Service) StartClient() error {
	return s.StartClientInstance(DefaultInstanceID)
}

// repairClientListenPort разводит конфликтующие listen-порты клиентов.
// changed говорит вызывающему, что порт сменился и linked-туннели надо
// довести до нового endpoint — сам sync делается ВНЕ s.mu (он ходит в
// хранилище туннелей и в ядро, держать под сервисным мьютексом нельзя).
func (s *Service) repairClientListenPort(id string) (cfg ClientConfig, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ClientConfig{}, false, err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return ClientConfig{}, false, fmt.Errorf("клиент %q не найден", id)
	}
	listens := clientListenAddresses(full.Clients)
	cfg = full.Clients[idx].Config
	next := ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(id), 9000, 9200)
	if next == cfg.Listen {
		return cfg, false, nil
	}
	cfg.Listen = next
	full.Clients[idx].Config = cfg
	if err := s.store.Save(full); err != nil {
		return ClientConfig{}, false, err
	}
	if s.appLog != nil {
		s.appLog.Info("listen-repair", id, "listen переназначен на "+next)
	}
	return cfg, true, nil
}

// syncLinkedEndpoints доводит listen клиента до Endpoint linked AWG-туннелей.
// Вызывать только вне s.mu.
func (s *Service) syncLinkedEndpoints(id, listen string) {
	if s.linkedEndpointSync == nil {
		return
	}
	n, err := s.linkedEndpointSync(id, listen)
	if s.appLog == nil {
		return
	}
	if err != nil {
		s.appLog.Warn("listen-repair", id, "sync linked endpoints: "+err.Error())
		return
	}
	if n > 0 {
		s.appLog.Info("listen-repair", id, fmt.Sprintf("synced %d linked tunnel endpoint(s)", n))
	}
}

func (s *Service) StartClientInstance(id string) error {
	// Жёсткая сериализация (L4): второй конкурентный старт этого же id не
	// гоняется с первым, а сразу отказывает — без RCI-работы.
	unlock, ok := s.tryLockClientStart(id)
	if !ok {
		return ErrClientStartInFlight
	}
	defer unlock()

	// Per-client in-flight guard (F6): супервизор проверяет clientStartInFlight
	// перед reconcile/эскалацией, чтобы не гоняться со StartClientInstance
	// этого же клиента, запущенным откуда-то ещё (API, сам супервизор).
	s.beginClientStart(id)
	defer s.endClientStart(id)
	cfg, listenChanged, err := s.repairClientListenPort(id)
	if err != nil {
		return err
	}
	if listenChanged {
		s.syncLinkedEndpoints(id, cfg.Listen)
	}
	prevWorkers := cfg.Workers
	cfg = normalizeClientConfig(cfg)
	if cfg.Workers != prevWorkers {
		if err := s.persistClientConfig(id, cfg); err != nil && s.appLog != nil {
			s.appLog.Warn("start", id, "workers нормализованы, но конфиг не сохранён: "+err.Error())
		} else if s.appLog != nil && !cfg.UsesWireGuard() {
			s.appLog.Info("start", id, fmt.Sprintf("raw workers: %d → %d", prevWorkers, cfg.Workers))
		}
	}
	if cfg.Peer == "" {
		return errors.New("укажите адрес сервера (-peer)")
	}
	if strings.TrimSpace(cfg.VKHashes) == "" {
		return errors.New("укажите VK-хеши (-vk)")
	}
	if strings.TrimSpace(cfg.Password) == "" {
		return errors.New("укажите пароль подключения (-password)")
	}

	isRaw := !cfg.UsesWireGuard()
	ctx := context.Background()
	if isRaw {
		var devErr error
		cfg, devErr = s.ensureClientDeviceID(id, cfg)
		if devErr != nil {
			return devErr
		}
		s.beginOpkgStart()
		defer s.endOpkgStart()
		var opkgErr error
		cfg, opkgErr = s.ensureClientOpkgIndex(ctx, id, cfg)
		if opkgErr != nil {
			return opkgErr
		}
		if err := s.prepareClientNDMSOpkgTun(ctx, id, cfg); err != nil {
			_ = s.teardownClientOpkgTun(ctx, cfg)
			return err
		}
	}

	var tunFdSock string
	if isRaw && cfg.usesNDMSOpkgTun() {
		tunFdSock = s.clientTunFdSockPath(id)
	}

	if isRaw && cfg.usesNDMSOpkgTun() {
		st := s.clientProcs.get(id).Status()
		if st.Running {
			recycle := st.StartedAt == nil || !rawClientNDMSReady(cfg, s.ifaceChecker)
			if !recycle {
				if _, ok := s.clientProcs.get(id).lastRawConf(); !ok {
					recycle = true
				}
			}
			if recycle {
				_ = s.clientProcs.get(id).Stop()
				if rawClientNDMSReady(cfg, s.ifaceChecker) {
					_ = s.teardownClientOpkgTun(ctx, cfg)
					// teardown снёс OpkgTun целиком; prepare выше отработал ДО
					// него — без повторного вызова bootstrapRawClient ждёт
					// интерфейс, который больше некому создать (I1).
					if err := s.prepareClientNDMSOpkgTun(ctx, id, cfg); err != nil {
						_ = s.teardownClientOpkgTun(ctx, cfg)
						return err
					}
				}
			}
		}
	}

	if err := s.clientProcs.get(id).Start(buildClientArgs(cfg, tunFdSock)); err != nil {
		if isRaw {
			_ = s.teardownClientOpkgTun(ctx, cfg)
		}
		return err
	}

	if isRaw {
		_, bootstrapped := s.clientProcs.get(id).lastRawConf()
		if bootstrapped && rawClientNDMSReady(cfg, s.ifaceChecker) {
			if err := s.applyClientRawIface(ctx, id, cfg); err != nil {
				_ = s.clientProcs.get(id).Stop()
				_ = s.teardownClientOpkgTun(ctx, cfg)
				return err
			}
			s.notifyClientRouteStart(ctx, id, cfg.kernelRawIface())
			s.restoreOpkgPolicyPermits(ctx, id, cfg)
		} else if err := s.bootstrapRawClient(ctx, id, cfg, tunFdSock); err != nil {
			return err
		}
	}

	if err := s.setEnabled(id, true); err != nil && s.appLog != nil {
		s.appLog.Warn("start", id, "не удалось сохранить enabled: "+err.Error())
	}
	return nil
}

func (s *Service) bootstrapRawClient(ctx context.Context, id string, cfg ClientConfig, tunFdSock string) error {
	kernelIface := cfg.kernelRawIface()
	if !waitForInterface(s.ifaceChecker, kernelIface, 20*time.Second) {
		_ = s.clientProcs.get(id).Stop()
		_ = s.teardownClientOpkgTun(ctx, cfg)
		return fmt.Errorf("интерфейс %s не появился (NDMS OpkgTun)", kernelIface)
	}
	if tunFdSock != "" {
		if err := sendTunFD(ctx, tunFdSock, kernelIface); err != nil {
			_ = s.clientProcs.get(id).Stop()
			_ = s.teardownClientOpkgTun(ctx, cfg)
			return fmt.Errorf("передача TUN fd: %w", err)
		}
		if s.appLog != nil {
			s.appLog.Info("start", id, "TUN fd передан клиенту через "+tunFdSock)
		}
	}
	rawConf, ok := s.waitForClientRawConf(id, 90*time.Second)
	if !ok {
		_ = s.clientProcs.get(id).Stop()
		_ = s.teardownClientOpkgTun(ctx, cfg)
		return fmt.Errorf("RAWCONF не получен от wt-client")
	}
	if err := s.activateClientNDMSOpkgTun(ctx, id, cfg, rawConf); err != nil {
		_ = s.clientProcs.get(id).Stop()
		_ = s.teardownClientOpkgTun(ctx, cfg)
		return err
	}
	cfg.RawClientIP = rawConf.ClientIP
	cfg.RawClientMTU = rawConf.MTU
	_ = s.persistClientConfig(id, cfg)
	if err := s.applyClientRawIface(ctx, id, cfg); err != nil {
		_ = s.clientProcs.get(id).Stop()
		_ = s.teardownClientOpkgTun(ctx, cfg)
		return err
	}
	s.notifyClientRouteStart(ctx, id, cfg.kernelRawIface())
	s.restoreOpkgPolicyPermits(ctx, id, cfg)
	if s.ifaceChecker != nil && !s.ifaceChecker.InterfaceOperUp(kernelIface) {
		_ = s.clientProcs.get(id).Stop()
		_ = s.teardownClientOpkgTun(ctx, cfg)
		return fmt.Errorf("интерфейс %s operstate down после bootstrap", kernelIface)
	}
	return nil
}

func (s *Service) StopClient() error {
	return s.StopClientInstance(DefaultInstanceID)
}

func (s *Service) StopClientInstance(id string) error {
	// Ждём идущий старт этого клиента: иначе он допишет Enabled=true после нас.
	unlock := s.lockClientStart(id)
	defer unlock()
	inst, err := s.clientInstance(id)
	if err != nil {
		return err
	}
	cfg := inst.Config
	ctx := context.Background()
	err = s.clientProcs.get(id).Stop()
	if !cfg.UsesWireGuard() && cfg.usesNDMSOpkgTun() {
		s.notifyClientRouteStop(ctx, id)
	}
	if !cfg.UsesWireGuard() {
		_ = s.teardownClientOpkgTun(ctx, cfg)
	}
	if e := s.setEnabled(id, false); e != nil && s.appLog != nil {
		s.appLog.Warn("stop", id, "не удалось сбросить enabled: "+e.Error())
	}
	return err
}

func (s *Service) Stop() {
	full, _ := s.store.Load()
	var wasRunning []ServerConfig
	var wasRawClients []ClientConfig
	for _, srv := range full.Servers {
		if s.serverProcs.get(srv.ID).Status().Running {
			wasRunning = append(wasRunning, srv.Config)
			removeServerListenFirewall(context.Background(), srv.Config)
		}
	}
	for _, cl := range full.Clients {
		if !cl.Config.UsesWireGuard() && s.clientProcs.get(cl.ID).Status().Running {
			wasRawClients = append(wasRawClients, cl.Config)
		}
	}
	s.clientProcs.stopAll()
	s.serverProcs.stopAll()
	for _, cfg := range wasRawClients {
		_ = s.teardownClientOpkgTun(context.Background(), cfg)
	}
	// Только те, что реально работали: снимать NDMS-интерфейс у сервера,
	// который в этой сессии не поднимался, — лишние RCI на каждом рестарте.
	for _, cfg := range wasRunning {
		removeEntwareNATForServer(context.Background(), cfg)
		_ = s.teardownServerOpkgTun(context.Background(), cfg)
	}
}

// ResumeEnabled стартует всех клиентов с Enabled==true (авторитетный флаг
// «должен работать»). Вызывается на бооте в горутине. Ошибки логирует и
// продолжает; идемпотентно (повторный старт живого процесса — no-op).
func (s *Service) ResumeEnabled() {
	full, err := s.store.Load()
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("resume", "", "не удалось прочитать конфиг: "+err.Error())
		}
		return
	}
	for _, c := range full.Clients {
		if !c.Config.Enabled {
			continue
		}
		if err := s.StartClientInstance(c.ID); err != nil && s.appLog != nil {
			s.appLog.Warn("resume", c.ID, "автостарт не удался: "+err.Error())
		}
	}
	for _, srv := range full.Servers {
		if !srv.Config.Enabled {
			continue
		}
		if err := s.StartServerInstance(srv.ID); err != nil && s.appLog != nil {
			s.appLog.Warn("resume", srv.ID, "автостарт сервера не удался: "+err.Error())
		}
	}
}

// setEnabled — RMW-хелпер: под s.mu грузит конфиг, выставляет Enabled инстанса
// и сохраняет. НЕ вызывать из уже-лоченного s.mu метода (дедлок); Start/Stop-
// ClientInstance не лочены, вызов оттуда безопасен.
func (s *Service) setEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", id)
	}
	if full.Clients[idx].Config.Enabled == enabled {
		return nil
	}
	full.Clients[idx].Config.Enabled = enabled
	return s.store.Save(full)
}

func (s *Service) clientInstance(id string) (ClientInstance, error) {
	full, err := s.store.Load()
	if err != nil {
		return ClientInstance{}, err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return ClientInstance{}, fmt.Errorf("клиент %q не найден", id)
	}
	return full.Clients[idx], nil
}

func normalizeClientConfig(cfg ClientConfig) ClientConfig {
	if cfg.Listen == "" {
		cfg.Listen = DefaultClientConfig().Listen
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultClientConfig().Workers
	}
	if cfg.Obfs == "" {
		cfg.Obfs = DefaultClientConfig().Obfs
	}
	if cfg.Fingerprint == "" {
		cfg.Fingerprint = DefaultClientConfig().Fingerprint
	}
	if cfg.CaptchaMode == "" {
		cfg.CaptchaMode = DefaultClientConfig().CaptchaMode
	}
	if cfg.VKAuthMode == "" {
		cfg.VKAuthMode = DefaultClientConfig().VKAuthMode
	}
	cfg.ConnMode = normalizeConnMode(cfg.ConnMode)
	return normalizePeers(cfg)
}

func buildClientArgs(c ClientConfig, tunFdSock string) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	str("-listen", c.Listen)
	str("-peer", c.Peer)
	str("-password", c.Password)
	str("-vk", c.VKHashes)
	if c.Workers > 0 {
		args = append(args, "-n", strconv.Itoa(c.Workers))
	}
	str("-obfs", c.Obfs)
	str("-fingerprint", c.Fingerprint)
	str("-device-id", c.DeviceID)
	str("-captcha-mode", normalizeCaptchaMode(c.CaptchaMode))
	appendVkAuthArgs(&args, c.VKAuthMode)
	if mode := normalizeConnMode(c.ConnMode); mode == ConnModeRaw {
		args = append(args, "-mode", "rawtun")
		str("-tun-fd-sock", tunFdSock)
		if iface := strings.TrimSpace(c.RawIface); iface != "" {
			args = append(args, "-tun-name", iface)
		}
	}
	return args
}

// appendVkAuthArgs мапит vkAuthMode awg-manager на -vk-auth-mode wt-client.
// Старые бинари (mips/mipsel без qWDTT 1.4 patch) не знают -vk-auth/-vk-anon-path;
// patched arm64 принимает -vk-auth-mode как alias (flags_compat.go).
func appendVkAuthArgs(args *[]string, vkAuthMode string) {
	mode := strings.ToLower(strings.TrimSpace(vkAuthMode))
	if mode == "" {
		mode = "vkcalls"
	}
	*args = append(*args, "-vk-auth-mode", mode)
}

func normalizeCaptchaMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "rjs", "wv":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "rjs"
	}
}

func (s *Service) InstallBinaries(ctx context.Context) error {
	if s.installSpecs == nil || s.downloader == nil {
		return fmt.Errorf("установка недоступна: для этой архитектуры нет закреплённой сборки wdtt")
	}
	s.installMu.Lock()
	if s.installing {
		s.installMu.Unlock()
		return fmt.Errorf("установка уже выполняется")
	}
	s.installing = true
	s.installMu.Unlock()
	defer func() {
		s.installMu.Lock()
		s.installing = false
		s.installMu.Unlock()
	}()

	specs := *s.installSpecs
	if err := s.installOne(ctx, s.clientBin, specs.Client); err != nil {
		return fmt.Errorf("клиент: %w", err)
	}
	if specs.serverSupported() {
		if err := s.installOne(ctx, s.serverBin, specs.Server); err != nil {
			return fmt.Errorf("сервер: %w", err)
		}
	}
	if err := s.writeInstalledVersion(installVersionLabel(specs)); err != nil && s.appLog != nil {
		s.appLog.Warn("install", "version-file", err.Error())
	}
	if s.appLog != nil {
		s.appLog.Info("install", specs.Client.Version,
			fmt.Sprintf("wdtt установлен: %s, %s", s.clientBin, s.serverBin))
	}
	return nil
}

func (s *Service) InstallInfo() (version string, available bool) {
	if s.installSpecs == nil || s.downloader == nil {
		return "", false
	}
	return installVersionLabel(*s.installSpecs), true
}

func (s *Service) Installing() bool {
	s.installMu.Lock()
	defer s.installMu.Unlock()
	return s.installing
}
