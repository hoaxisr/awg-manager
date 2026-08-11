package freeturn

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/procport"
	"github.com/hoaxisr/awg-manager/internal/proxysup"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

// Service is the public facade consumed by the API layer (one instance per
// running awg-manager process — wired up in cmd/awg-manager/main.go the
// same way pingcheck.Service is).
type Service struct {
	store   *Store
	dataDir string

	clientBin string
	serverBin string

	clientProcs *processRegistry
	serverProcs *processRegistry

	// mu сериализует Load-modify-Save методы: без него два конкурентных
	// запроса теряют правки друг друга и могут выдать один listen-порт дважды.
	mu sync.Mutex

	versionPath string

	installSpecs *ArchSpecs
	downloader   childproc.Downloader
	installMu    sync.Mutex
	installing   bool

	appLog *logging.ScopedLogger

	listenPortChecker LocalListenPortChecker

	clientHealth *healthTracker
	serverHealth *healthTracker
	startBackoff *proxysup.Backoff

	relayProbe    RelayProbe
	linkedTunnels LinkedTunnelResolver

	// После listen-repair синхронизирует endpoint linked AWG-туннелей.
	linkedEndpointReconcile func() (int, error)

	// Кеш binariesMatchSpecs: сверка хеширует оба бинаря (~21 МБ), а
	// статус опрашивается раз в 2 секунды, пока открыта вкладка.
	matchMu  sync.Mutex
	matchKey string
	matchVal bool

	// clientStarts — per-client счётчик стартов в полёте (симметрично
	// wdtt.Service.clientStarts, F6): даёт супервизору дешёвый совещательный
	// fast-path, чтобы скипнуть тик без сжигания backoff-окна. Совещательный
	// (TOCTOU: окно между проверкой и стартом открыто) — жёсткая сериализация
	// самого старта обеспечивается clientStartLocks (TryLock внутри
	// StartClientInstance).
	clientStartMu    sync.Mutex
	clientStarts     map[string]int
	clientStartLocks map[string]*sync.Mutex
}

// ErrClientStartInFlight — StartClientInstance этого клиента уже выполняется
// где-то ещё (TryLock не взят); возвращается без запуска процесса.
var ErrClientStartInFlight = errors.New("старт клиента уже выполняется")

// tryLockClientStart — жёсткая per-client сериализация StartClientInstance:
// второй конкурентный вызов для того же id не блокируется, а сразу получает
// отказ. unlock должен вызываться defer'ом сразу после успешного захвата.
func (s *Service) tryLockClientStart(id string) (unlock func(), ok bool) {
	s.clientStartMu.Lock()
	if s.clientStartLocks == nil {
		s.clientStartLocks = make(map[string]*sync.Mutex)
	}
	l, exists := s.clientStartLocks[id]
	if !exists {
		l = &sync.Mutex{}
		s.clientStartLocks[id] = l
	}
	s.clientStartMu.Unlock()
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
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

// SetLogger wires the UI-visible journal (nil-safe scoped logger).
func (s *Service) SetLogger(appLogger logging.AppLogger) {
	s.appLog = logging.NewScopedLogger(appLogger, logging.GroupRouting, "freeturn")
}

// SetListenPortChecker wires external localhost listen ports (AWG tunnel endpoints, etc.).
func (s *Service) SetListenPortChecker(c LocalListenPortChecker) {
	s.listenPortChecker = c
}

func (s *Service) SetRelayProbe(p RelayProbe) {
	s.relayProbe = p
}

func (s *Service) SetLinkedTunnelResolver(r LinkedTunnelResolver) {
	s.linkedTunnels = r
}

// SetLinkedEndpointReconcile wires AWG tunnel endpoint sync after listen-repair.
func (s *Service) SetLinkedEndpointReconcile(fn func() (int, error)) {
	s.linkedEndpointReconcile = fn
}

func (s *Service) occupiedLocalListenPorts(selfClientID string) map[int]bool {
	if s.listenPortChecker == nil {
		return nil
	}
	used, err := s.listenPortChecker.OccupiedLocalListenPorts("", selfClientID)
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
	return out
}

// NewService wires up config storage and process managers per instance id.
func NewService(dataDir, runtimeDir, clientBin, serverBin string) *Service {
	return &Service{
		store:        NewStore(dataDir),
		dataDir:      dataDir,
		clientBin:    clientBin,
		serverBin:    serverBin,
		versionPath:  filepath.Join(dataDir, "freeturn-version.json"),
		clientProcs:  newProcessRegistry("client", clientBin, runtimeDir),
		serverProcs:  newProcessRegistry("server", serverBin, runtimeDir),
		clientHealth: newHealthTracker(),
		serverHealth: newHealthTracker(),
		startBackoff: newStartBackoff(),
	}
}

func (s *Service) GetConfig() (Config, error) {
	return s.store.Load()
}

func (s *Service) UpdateClientConfig(cfg ClientConfig) error {
	return s.UpdateClientInstance(DefaultInstanceID, cfg)
}

func (s *Service) UpdateServerConfig(cfg ServerConfig) error {
	return s.UpdateServerInstance(DefaultInstanceID, cfg)
}

func (s *Service) UpdateClientInstance(id string, cfg ClientConfig) error {
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
	listens := clientListenAddresses(full.Clients)
	cfg.Listen = ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(id), 9000, 9200)
	cfg.Platform = normalizePlatform(cfg.Platform)
	// Enabled — только Start/Stop; UI при сохранении часто шлёт stale false.
	cfg.Enabled = full.Clients[idx].Config.Enabled
	full.Clients[idx].Config = cfg
	// Правка конфига могла устранить причину отказа (порт, ключ, peer) —
	// не заставляем ждать окно backoff до следующей попытки супервизора.
	s.startBackoff.Forget(clientKey(id))
	s.startBackoff.Forget(clientHealthKey(id))
	return s.store.Save(full)
}

// UpdateServerInstance — публичный путь (вызывается API на PUT). В отличие от
// updateServerInstanceInternal, после успешного апдейта сбрасывает и стартовый,
// и health-backoff: пользователь чинит конфиг сервера руками — ждать
// оставшееся окно backoff (до 15 мин после серии эскалаций) до следующей
// попытки супервизора не нужно. У внутреннего пути такого Forget нет — см.
// его комментарий.
func (s *Service) UpdateServerInstance(id string, cfg ServerConfig) error {
	if err := s.updateServerInstanceInternal(id, cfg); err != nil {
		return err
	}
	s.startBackoff.Forget(serverKey(id))
	s.startBackoff.Forget(serverHealthKey(id))
	return nil
}

// updateServerInstanceInternal — тело апдейта без сброса backoff. Вызывается
// и публичным UpdateServerInstance, и StartServerInstance (тот зовёт его сам
// для нормализации listen на каждой попытке супервизора) — если бы backoff
// сбрасывался здесь, окно стиралось бы на каждой попытке и рост до 15 минут
// переставал работать ровно там, где он нужнее всего.
func (s *Service) updateServerInstanceInternal(id string, cfg ServerConfig) error {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("сервер %q не найден", id)
	}
	prevCfg := full.Servers[idx].Config
	listens := serverListenAddresses(full.Servers)
	cfg.Listen = ensureUniqueServerListenAddr(listens, idx, cfg.Listen, s.reservedServerPortsExcept(id), 56000, 56100)
	// Enabled — только Start/Stop; сохранение настроек не должно гасить автостарт.
	cfg.Enabled = prevCfg.Enabled
	full.Servers[idx].Config = cfg
	saveErr := s.store.Save(full)
	running := s.serverProcs.get(id).Status().Running
	s.mu.Unlock()
	if saveErr != nil {
		return saveErr
	}
	if running {
		s.syncServerListenFirewall(context.Background(), id, prevCfg, cfg)
	}
	return nil
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
	cfg.Platform = normalizePlatform(cfg.Platform)
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

func (s *Service) CreateServer(in CreateServerInput) (ServerInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ServerInstance{}, err
	}
	cfg := DefaultServerConfig()
	if in.Config != nil {
		cfg = *in.Config
	}
	cfg.Listen = nextServerListen(full.Servers, s.occupiedLocalListenPorts(""))
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("Сервер %d", len(full.Servers)+1)
	}
	inst := ServerInstance{ID: childproc.NewInstanceID(), Name: name, Config: cfg}
	full.Servers = append(full.Servers, inst)
	if err := s.store.Save(full); err != nil {
		return ServerInstance{}, err
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
	full.Clients = append(full.Clients[:idx], full.Clients[idx+1:]...)
	saveErr := s.store.Save(full)
	s.startBackoff.Forget(clientKey(id))
	s.startBackoff.Forget(clientHealthKey(id))
	s.clientHealth.reset(id)
	s.mu.Unlock()
	// Блокирующий Stop (kill до ~3с) — вне s.mu, чтобы не сериализовать
	// прочие RMW-методы и boot-ResumeEnabled на время убийства процесса.
	_ = s.clientProcs.get(id).Stop()
	return saveErr
}

func (s *Service) DeleteServer(id string) error {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("сервер %q не найден", id)
	}
	cfg := full.Servers[idx].Config
	full.Servers = append(full.Servers[:idx], full.Servers[idx+1:]...)
	saveErr := s.store.Save(full)
	s.startBackoff.Forget(serverKey(id))
	s.startBackoff.Forget(serverHealthKey(id))
	s.serverHealth.reset(id)
	s.mu.Unlock()
	// Блокирующий Stop (kill до ~3с) — вне s.mu, чтобы не сериализовать
	// прочие RMW-методы и boot-ResumeEnabled на время убийства процесса.
	_ = s.serverProcs.get(id).Stop()
	removeServerListenFirewall(context.Background(), cfg)
	return saveErr
}

func (s *Service) RenameClient(id, name string) error {
	name = trimName(name)
	if name == "" {
		return errors.New("укажите имя")
	}
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
	full.Clients[idx].Name = name
	return s.store.Save(full)
}

func (s *Service) RenameServer(id, name string) error {
	name = trimName(name)
	if name == "" {
		return errors.New("укажите имя")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	full.Servers[idx].Name = name
	return s.store.Save(full)
}

func trimName(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func findClientIndex(clients []ClientInstance, id string) int {
	for i, c := range clients {
		if c.ID == id {
			return i
		}
	}
	return -1
}

func findServerIndex(servers []ServerInstance, id string) int {
	for i, srv := range servers {
		if srv.ID == id {
			return i
		}
	}
	return -1
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

func (s *Service) serverInstance(id string) (ServerInstance, error) {
	full, err := s.store.Load()
	if err != nil {
		return ServerInstance{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return ServerInstance{}, fmt.Errorf("сервер %q не найден", id)
	}
	return full.Servers[idx], nil
}

// Status returns the local install/instance state without any network calls.
func (s *Service) Status() Status {
	return s.statusLocked()
}

func (s *Service) statusLocked() Status {
	cfg, err := s.store.Load()
	if err != nil {
		cfg = DefaultConfig()
	}
	version, available := s.InstallInfo()
	installedVersion, updateAvailable := s.installStatusFields(version)
	clock := routerclock.Get()
	st := Status{
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
		st.Clients = append(st.Clients, InstanceStatus{
			ID:     c.ID,
			Name:   c.Name,
			Status: ps,
		})
	}
	for _, srv := range cfg.Servers {
		ps := s.serverProcs.get(srv.ID).Status()
		proto := procport.ProtoUDP
		if strings.EqualFold(strings.TrimSpace(srv.Config.Mode), "tcp") {
			proto = procport.ProtoTCP
		}
		ps.LastError = procport.EnrichBindError(ps.LastError, srv.Config.Listen, proto)
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

func (s *Service) repairClientListenPort(id string) (ClientConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ClientConfig{}, err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return ClientConfig{}, fmt.Errorf("клиент %q не найден", id)
	}
	listens := clientListenAddresses(full.Clients)
	cfg := full.Clients[idx].Config
	next := ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(id), 9000, 9200)
	if next == cfg.Listen {
		return cfg, nil
	}
	cfg.Listen = next
	full.Clients[idx].Config = cfg
	if err := s.store.Save(full); err != nil {
		return ClientConfig{}, err
	}
	if s.appLog != nil {
		s.appLog.Info("listen-repair", id, "listen переназначен на "+next)
	}
	if s.linkedEndpointReconcile != nil {
		if n, err := s.linkedEndpointReconcile(); err != nil && s.appLog != nil {
			s.appLog.Warn("listen-repair", id, "sync linked endpoints: "+err.Error())
		} else if n > 0 && s.appLog != nil {
			s.appLog.Info("listen-repair", id, fmt.Sprintf("synced %d linked tunnel endpoint(s)", n))
		}
	}
	return cfg, nil
}

func (s *Service) StartClientInstance(id string) error {
	// Жёсткая сериализация (F6, симметрично wdtt): второй конкурентный старт
	// этого же id не гоняется с первым, а сразу отказывает.
	unlock, ok := s.tryLockClientStart(id)
	if !ok {
		return ErrClientStartInFlight
	}
	defer unlock()

	// Per-client in-flight guard (F6): супервизор проверяет clientStartInFlight
	// перед health-эскалацией, чтобы не гоняться со StartClientInstance этого
	// же клиента, запущенным откуда-то ещё (API, сам супервизор).
	s.beginClientStart(id)
	defer s.endClientStart(id)

	cfg, err := s.repairClientListenPort(id)
	if err != nil {
		return err
	}
	if cfg.Peer == "" {
		return errors.New("укажите адрес сервера (-peer)")
	}
	if cfg.Provider == "vk" && cfg.Links == "" {
		return errors.New("укажите ссылку(-и) VK Calls (-links) — обязательны для provider=vk")
	}
	if err := validateObfKey(cfg.ObfProfile, cfg.ObfKey); err != nil {
		return err
	}
	if err := s.clientProcs.get(id).Start(buildClientArgs(cfg)); err != nil {
		return err
	}
	// Enabled авторитетно = «пользователь запустил»; выставляем только по факту
	// успешного старта (fail-closed). Ошибку сохранения логируем — процесс уже жив.
	if err := s.setClientEnabled(id, true); err != nil && s.appLog != nil {
		s.appLog.Warn("start", id, "не удалось сохранить enabled: "+err.Error())
	}
	return nil
}

func (s *Service) StopClient() error {
	return s.StopClientInstance(DefaultInstanceID)
}

func (s *Service) StopClientInstance(id string) error {
	if _, err := s.clientInstance(id); err != nil {
		return err
	}
	err := s.clientProcs.get(id).Stop()
	// Явный пользовательский стоп снимает авторитетный Enabled, чтобы автостарт
	// на следующем бооте его не поднял. Stop() на выходе демона сюда не заходит.
	if e := s.setClientEnabled(id, false); e != nil && s.appLog != nil {
		s.appLog.Warn("stop", id, "не удалось сбросить enabled: "+e.Error())
	}
	return err
}

func (s *Service) StartServer() error {
	return s.StartServerInstance(DefaultInstanceID)
}

func (s *Service) StartServerInstance(id string) error {
	inst, err := s.serverInstance(id)
	if err != nil {
		return err
	}
	if err := s.updateServerInstanceInternal(id, inst.Config); err != nil {
		return err
	}
	inst, err = s.serverInstance(id)
	if err != nil {
		return err
	}
	if inst.Config.Connect == "" {
		return errors.New("укажите backend-адрес (-connect)")
	}
	if err := validateObfKey(inst.Config.ObfProfile, inst.Config.ObfKey); err != nil {
		return err
	}
	if err := s.serverProcs.get(id).Start(buildServerArgs(inst.Config)); err != nil {
		return err
	}
	if err := applyServerListenFirewall(context.Background(), inst.Config); err != nil && s.appLog != nil {
		s.appLog.Warn("firewall", id, "INPUT для listen-порта: "+err.Error())
	}
	if err := s.setServerEnabled(id, true); err != nil && s.appLog != nil {
		s.appLog.Warn("start", id, "не удалось сохранить enabled: "+err.Error())
	}
	return nil
}

func (s *Service) StopServer() error {
	return s.StopServerInstance(DefaultInstanceID)
}

func (s *Service) StopServerInstance(id string) error {
	inst, err := s.serverInstance(id)
	if err != nil {
		return err
	}
	removeServerListenFirewall(context.Background(), inst.Config)
	err = s.serverProcs.get(id).Stop()
	if e := s.setServerEnabled(id, false); e != nil && s.appLog != nil {
		s.appLog.Warn("stop", id, "не удалось сбросить enabled: "+e.Error())
	}
	return err
}

func (s *Service) ServerConfigForLink(id string) (ServerConfig, error) {
	inst, err := s.serverInstance(id)
	if err != nil {
		return ServerConfig{}, err
	}
	return inst.Config, nil
}

// Stop is wired into the app shutdown-hook chain in cmd/awg-manager/main.go.
func (s *Service) Stop() {
	full, _ := s.store.Load()
	for _, srv := range full.Servers {
		if s.serverProcs.get(srv.ID).Status().Running {
			removeServerListenFirewall(context.Background(), srv.Config)
		}
	}
	s.clientProcs.stopAll()
	s.serverProcs.stopAll()
}

// ResumeEnabled стартует всех клиентов и серверов с Enabled==true (авторитетный
// флаг «должен работать»). Вызывается на бооте в горутине. Ошибки логирует и
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
			s.appLog.Warn("resume", c.ID, "автостарт клиента не удался: "+err.Error())
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

// setClientEnabled — RMW-хелпер: под s.mu грузит конфиг, выставляет Enabled
// клиента и сохраняет. НЕ вызывать из уже-лоченного s.mu метода (дедлок);
// Start/StopClientInstance не лочены, вызов оттуда безопасен.
func (s *Service) setClientEnabled(id string, enabled bool) error {
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

func (s *Service) setServerEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	if full.Servers[idx].Config.Enabled == enabled {
		return nil
	}
	full.Servers[idx].Config.Enabled = enabled
	return s.store.Save(full)
}

// validateObfKey — ключ обфускации обязателен при профиле ≠ none и должен
// быть 64 hex-символа (32 байта); иначе бинарь падает с опак-ошибкой (#584).
func validateObfKey(profile, key string) error {
	if profile == "" || profile == "none" {
		return nil
	}
	if key == "" {
		return errors.New("сгенерируйте или укажите ключ обфускации (-obf-key) — обязателен при профиле ≠ none")
	}
	if len(key) != 64 {
		return errors.New("ключ обфускации (-obf-key) должен состоять из 64 hex-символов")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return errors.New("ключ обфускации (-obf-key) должен состоять из 64 hex-символов")
	}
	return nil
}

func buildClientArgs(c ClientConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	flag := func(name string, on bool) {
		if on {
			args = append(args, name)
		}
	}

	str("-listen", c.Listen)
	str("-peer", c.Peer)
	str("-provider", c.Provider)
	str("-links", c.Links)
	if c.Streams > 0 {
		args = append(args, "-n", strconv.Itoa(c.Streams))
	}
	str("-transport", c.Transport)
	str("-mode", c.Mode)
	flag("-bond", c.Bond)
	str("-turn", c.TurnHost)
	if c.TurnPort > 0 {
		args = append(args, "-port", strconv.Itoa(c.TurnPort))
	}
	str("-obf-profile", c.ObfProfile)
	str("-obf-key", c.ObfKey)
	if c.StreamsPerCred > 0 {
		args = append(args, "-streams-per-cred", strconv.Itoa(c.StreamsPerCred))
	}
	if p := normalizePlatform(c.Platform); p == "mobile" {
		str("-platform", p)
	}
	// awg-manager: только авто-капча; ручной fallback (:8765) не поддерживается в UI.
	str("-dns-mode", c.DNSMode)
	str("-dns-servers", c.DNSServers)
	str("-client-id", c.ClientID)
	str("-sub", c.Sub)
	flag("-debug", c.Debug)
	return args
}

func normalizePlatform(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "mobile":
		return "mobile"
	default:
		return "desktop"
	}
}

func buildServerArgs(c ServerConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	flag := func(name string, on bool) {
		if on {
			args = append(args, name)
		}
	}

	str("-listen", c.Listen)
	str("-connect", c.Connect)
	str("-mode", c.Mode)
	str("-obf-profile", c.ObfProfile)
	str("-obf-key", c.ObfKey)
	str("-clients-file", c.ClientsFile)
	flag("-debug", c.Debug)
	return args
}
