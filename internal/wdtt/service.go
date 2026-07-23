package wdtt

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/routerclock"
)

type Service struct {
	store     *Store
	dataDir   string
	clientBin string
	procs     *processRegistry

	// mu сериализует Load-modify-Save методы: без него два конкурентных
	// запроса теряют правки друг друга и могут выдать один listen-порт дважды.
	mu sync.Mutex

	versionPath  string
	installSpec  *BinarySpec
	downloader   childproc.Downloader
	installMu    sync.Mutex
	installing   bool
	appLog       *logging.ScopedLogger
	listenChecker LocalListenPortChecker
}

func NewService(dataDir, runtimeDir, clientBin string) *Service {
	return &Service{
		store:       NewStore(dataDir),
		dataDir:     dataDir,
		clientBin:   clientBin,
		versionPath: filepath.Join(dataDir, "wdtt-version.json"),
		procs:       newProcessRegistry(clientBin, runtimeDir),
	}
}

func (s *Service) SetLogger(appLogger logging.AppLogger) {
	s.appLog = logging.NewScopedLogger(appLogger, logging.GroupRouting, "wdtt")
}

func (s *Service) SetListenPortChecker(c LocalListenPortChecker) {
	s.listenChecker = c
}

func (s *Service) SetInstallSpec(spec BinarySpec) {
	s.installSpec = &spec
}

func (s *Service) SetDownloader(dl childproc.Downloader) {
	s.downloader = dl
}

func (s *Service) occupiedLocalListenPorts() map[int]bool {
	if s.listenChecker == nil {
		return nil
	}
	used, err := s.listenChecker.OccupiedLocalListenPorts()
	if err != nil || len(used) == 0 {
		return nil
	}
	return used
}

func (s *Service) GetConfig() (Config, error) {
	return s.store.Load()
}

func (s *Service) UpdateClientConfig(cfg ClientConfig) error {
	return s.UpdateClientInstance(DefaultInstanceID, cfg)
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
	cfg.Listen = ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(), 9000, 9200)
	cfg = normalizeClientConfig(cfg)
	full.Clients[idx].Config = cfg
	return s.store.Save(full)
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
	cfg.Listen = nextClientListen(full.Clients, s.occupiedLocalListenPorts())
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
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, id)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", id)
	}
	_ = s.procs.get(id).Stop()
	full.Clients = append(full.Clients[:idx], full.Clients[idx+1:]...)
	return s.store.Save(full)
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
	return s.store.Save(full)
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
	cfg.Listen = ensureUniqueListenAddr(listens, idx, cfg.Listen, s.occupiedLocalListenPorts(), 9000, 9200)
	if name := strings.TrimSpace(payload.Name); name != "" {
		full.Clients[idx].Name = name
	}
	full.Clients[idx].Config = cfg
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
		InstallAvailable: available,
		InstallVersion:   version,
		InstalledVersion: installedVersion,
		UpdateAvailable:  updateAvailable,
		Installing:       s.Installing(),
		RouterClock:      clock.Now.Format("2006-01-02 15:04:05") + " " + clock.ZoneName,
	}
	for _, c := range cfg.Clients {
		st.Clients = append(st.Clients, InstanceStatus{
			ID:     c.ID,
			Name:   c.Name,
			Status: s.procs.get(c.ID).Status(),
		})
	}
	if cs := instanceStatusByID(st.Clients, DefaultInstanceID); cs != nil {
		st.Client = *cs
	} else if len(st.Clients) > 0 {
		st.Client = st.Clients[0].Status
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
	if strings.TrimSpace(inst.Config.VKHashes) == "" {
		return errors.New("укажите VK-хеши (-vk)")
	}
	if strings.TrimSpace(inst.Config.Password) == "" {
		return errors.New("укажите пароль подключения (-password)")
	}
	if err := s.procs.get(id).Start(buildClientArgs(inst.Config)); err != nil {
		return err
	}
	// Enabled авторитетно = «пользователь запустил»; выставляем только по факту
	// успешного старта (fail-closed). Ошибку сохранения логируем — процесс уже жив.
	if err := s.setEnabled(id, true); err != nil && s.appLog != nil {
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
	err := s.procs.get(id).Stop()
	// Явный пользовательский стоп снимает авторитетный Enabled, чтобы автостарт
	// на следующем бооте его не поднял. Stop() на выходе демона сюда не заходит.
	if e := s.setEnabled(id, false); e != nil && s.appLog != nil {
		s.appLog.Warn("stop", id, "не удалось сбросить enabled: "+e.Error())
	}
	return err
}

func (s *Service) Stop() {
	s.procs.stopAll()
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
	return cfg
}

func buildClientArgs(c ClientConfig) []string {
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
	str("-vk-auth-mode", c.VKAuthMode)
	return args
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
	if s.installSpec == nil || s.downloader == nil {
		return fmt.Errorf("установка недоступна: для этой архитектуры нет закреплённой сборки wdtt-client")
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

	spec := *s.installSpec
	if err := s.installOne(ctx, s.clientBin, spec); err != nil {
		return err
	}
	if err := s.writeInstalledVersion(spec.Version); err != nil && s.appLog != nil {
		s.appLog.Warn("install", "version-file", err.Error())
	}
	if s.appLog != nil {
		s.appLog.Info("install", spec.Version, fmt.Sprintf("wdtt-client v%s установлен: %s", spec.Version, s.clientBin))
	}
	return nil
}

func (s *Service) InstallInfo() (version string, available bool) {
	if s.installSpec == nil || s.downloader == nil {
		return "", false
	}
	return s.installSpec.Version, true
}

func (s *Service) Installing() bool {
	s.installMu.Lock()
	defer s.installMu.Unlock()
	return s.installing
}
