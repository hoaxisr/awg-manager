package freeturn

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
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

	versionPath string

	installSpecs *ArchSpecs
	downloader   Downloader
	installMu    sync.Mutex
	installing   bool

	archKey   string
	remoteMu  sync.RWMutex
	remoteCache *remoteReleaseCache

	appLog *logging.ScopedLogger
}

// SetLogger wires the UI-visible journal (nil-safe scoped logger).
func (s *Service) SetLogger(appLogger logging.AppLogger) {
	s.appLog = logging.NewScopedLogger(appLogger, logging.GroupRouting, "freeturn")
}

// NewService wires up config storage and process managers per instance id.
func NewService(dataDir, runtimeDir, clientBin, serverBin string) *Service {
	return &Service{
		store:       NewStore(dataDir),
		dataDir:     dataDir,
		clientBin:   clientBin,
		serverBin:   serverBin,
		versionPath: filepath.Join(dataDir, "freeturn-version.json"),
		clientProcs: newProcessRegistry("client", clientBin, runtimeDir),
		serverProcs: newProcessRegistry("server", serverBin, runtimeDir),
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
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", id)
	}
	listens := clientListenAddresses(full.Clients)
	if err := validateUniqueListens(listens, idx, cfg.Listen); err != nil {
		return err
	}
	full.Clients[idx].Config = cfg
	return s.store.Save(full)
}

func (s *Service) UpdateServerInstance(id string, cfg ServerConfig) error {
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	listens := serverListenAddresses(full.Servers)
	if err := validateUniqueListens(listens, idx, cfg.Listen); err != nil {
		return err
	}
	full.Servers[idx].Config = cfg
	return s.store.Save(full)
}

func (s *Service) CreateClient(in CreateClientInput) (ClientInstance, error) {
	full, err := s.store.Load()
	if err != nil {
		return ClientInstance{}, err
	}
	cfg := DefaultClientConfig()
	if in.Config != nil {
		cfg = *in.Config
	}
	cfg.Listen = nextClientListen(full.Clients)
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("Клиент %d", len(full.Clients)+1)
	}
	inst := ClientInstance{ID: newInstanceID(), Name: name, Config: cfg}
	full.Clients = append(full.Clients, inst)
	if err := s.store.Save(full); err != nil {
		return ClientInstance{}, err
	}
	return inst, nil
}

func (s *Service) CreateServer(in CreateServerInput) (ServerInstance, error) {
	full, err := s.store.Load()
	if err != nil {
		return ServerInstance{}, err
	}
	cfg := DefaultServerConfig()
	if in.Config != nil {
		cfg = *in.Config
	}
	cfg.Listen = nextServerListen(full.Servers)
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("Сервер %d", len(full.Servers)+1)
	}
	inst := ServerInstance{ID: newInstanceID(), Name: name, Config: cfg}
	full.Servers = append(full.Servers, inst)
	if err := s.store.Save(full); err != nil {
		return ServerInstance{}, err
	}
	return inst, nil
}

func (s *Service) DeleteClient(id string) error {
	if id == DefaultInstanceID {
		return errors.New("нельзя удалить экземпляр по умолчанию")
	}
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", id)
	}
	_ = s.clientProcs.get(id).Stop()
	full.Clients = append(full.Clients[:idx], full.Clients[idx+1:]...)
	return s.store.Save(full)
}

func (s *Service) DeleteServer(id string) error {
	if id == DefaultInstanceID {
		return errors.New("нельзя удалить экземпляр по умолчанию")
	}
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	_ = s.serverProcs.get(id).Stop()
	full.Servers = append(full.Servers[:idx], full.Servers[idx+1:]...)
	return s.store.Save(full)
}

func (s *Service) RenameClient(id, name string) error {
	name = trimName(name)
	if name == "" {
		return errors.New("укажите имя")
	}
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

func (s *Service) Status() Status {
	go s.refreshRemote(context.Background(), false)
	return s.statusLocked()
}

func (s *Service) StatusForceRemote(ctx context.Context) Status {
	s.refreshRemote(ctx, true)
	return s.statusLocked()
}

func (s *Service) statusLocked() Status {
	cfg, err := s.store.Load()
	if err != nil {
		cfg = DefaultConfig()
	}
	version, available := s.InstallInfo()
	installedVersion, updateAvailable := s.installStatusFields(version)
	remoteVersion, remoteErr := s.remoteStatus()
	st := Status{
		InstallAvailable: available,
		InstallVersion:   version,
		InstalledVersion: installedVersion,
		UpdateAvailable:  updateAvailable,
		RemoteVersion:    remoteVersion,
		RemoteCheckError: remoteErr,
		Installing:       s.Installing(),
	}
	for _, c := range cfg.Clients {
		st.Clients = append(st.Clients, InstanceStatus{
			ID:     c.ID,
			Name:   c.Name,
			Status: s.clientProcs.get(c.ID).Status(),
		})
	}
	for _, srv := range cfg.Servers {
		st.Servers = append(st.Servers, InstanceStatus{
			ID:     srv.ID,
			Name:   srv.Name,
			Status: s.serverProcs.get(srv.ID).Status(),
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

func (s *Service) StartClientInstance(id string) error {
	inst, err := s.clientInstance(id)
	if err != nil {
		return err
	}
	if inst.Config.Peer == "" {
		return errors.New("укажите адрес сервера (-peer)")
	}
	if inst.Config.Provider == "vk" && inst.Config.Links == "" {
		return errors.New("укажите ссылку(-и) VK Calls (-links) — обязательны для provider=vk")
	}
	if err := validateObfKey(inst.Config.ObfProfile, inst.Config.ObfKey); err != nil {
		return err
	}
	return s.clientProcs.get(id).Start(buildClientArgs(inst.Config))
}

func (s *Service) StopClient() error {
	return s.StopClientInstance(DefaultInstanceID)
}

func (s *Service) StopClientInstance(id string) error {
	if _, err := s.clientInstance(id); err != nil {
		return err
	}
	return s.clientProcs.get(id).Stop()
}

func (s *Service) StartServer() error {
	return s.StartServerInstance(DefaultInstanceID)
}

func (s *Service) StartServerInstance(id string) error {
	inst, err := s.serverInstance(id)
	if err != nil {
		return err
	}
	if inst.Config.Connect == "" {
		return errors.New("укажите backend-адрес (-connect)")
	}
	if err := validateObfKey(inst.Config.ObfProfile, inst.Config.ObfKey); err != nil {
		return err
	}
	return s.serverProcs.get(id).Start(buildServerArgs(inst.Config))
}

func (s *Service) StopServer() error {
	return s.StopServerInstance(DefaultInstanceID)
}

func (s *Service) StopServerInstance(id string) error {
	if _, err := s.serverInstance(id); err != nil {
		return err
	}
	return s.serverProcs.get(id).Stop()
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
	s.clientProcs.stopAll()
	s.serverProcs.stopAll()
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
	str("-browser", c.Browser)
	flag("-manual-captcha", c.ManualCaptcha)
	str("-dns-mode", c.DNSMode)
	str("-dns-servers", c.DNSServers)
	str("-client-id", c.ClientID)
	str("-sub", c.Sub)
	flag("-debug", c.Debug)
	return args
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
