package freeturn

import (
	"encoding/hex"
	"errors"
	"strconv"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// Service is the public facade consumed by the API layer (one instance per
// running awg-manager process — wired up in cmd/awg-manager/main.go the
// same way pingcheck.Service is).
type Service struct {
	store *Store

	clientBin string
	serverBin string

	clientProc *process
	serverProc *process

	// One-click install (install.go): pinned specs for this arch + shared
	// downloader; nil = install unavailable (UI keeps the manual hint).
	installSpecs *ArchSpecs
	downloader   Downloader
	installMu    sync.Mutex
	installing   bool

	appLog *logging.ScopedLogger
}

// SetLogger wires the UI-visible journal (nil-safe scoped logger).
func (s *Service) SetLogger(appLogger logging.AppLogger) {
	s.appLog = logging.NewScopedLogger(appLogger, logging.GroupRouting, "freeturn")
}

// NewService wires up config storage and the two process managers.
//
//   - dataDir:    e.g. /opt/etc/awg-manager — where freeturn.json lives.
//   - runtimeDir: e.g. filepath.Join(dataDir, "run") — where PID files live.
//   - clientBin / serverBin: paths to the freeturn client/server binaries.
//     The upstream project ships them as two separate binaries (see
//     docs/quickstart.md) — e.g. /opt/bin/freeturn-client, /opt/bin/freeturn-server.
//     Adjust to match whatever the install script actually drops on the router.
func NewService(dataDir, runtimeDir, clientBin, serverBin string) *Service {
	return &Service{
		store:      NewStore(dataDir),
		clientBin:  clientBin,
		serverBin:  serverBin,
		clientProc: newProcess("client", clientBin, runtimeDir),
		serverProc: newProcess("server", serverBin, runtimeDir),
	}
}

func (s *Service) GetConfig() (Config, error) {
	return s.store.Load()
}

func (s *Service) UpdateClientConfig(cfg ClientConfig) error {
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	full.Client = cfg
	return s.store.Save(full)
}

func (s *Service) UpdateServerConfig(cfg ServerConfig) error {
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	full.Server = cfg
	return s.store.Save(full)
}

func (s *Service) Status() Status {
	version, available := s.InstallInfo()
	return Status{
		Client:           s.clientProc.Status(),
		Server:           s.serverProc.Status(),
		InstallAvailable: available,
		InstallVersion:   version,
		Installing:       s.Installing(),
	}
}

// StartClient validates the required flags (-peer always; -link when
// provider=vk, per docs/flags.md) before spawning, so the API can return a
// clear Russian-language error instead of an opaque process-exit message.
func (s *Service) StartClient() error {
	cfg, err := s.store.Load()
	if err != nil {
		return err
	}
	if cfg.Client.Peer == "" {
		return errors.New("укажите адрес сервера (-peer)")
	}
	if cfg.Client.Provider == "vk" && cfg.Client.Links == "" {
		return errors.New("укажите ссылку(-и) VK Calls (-links) — обязательны для provider=vk")
	}
	if err := validateObfKey(cfg.Client.ObfProfile, cfg.Client.ObfKey); err != nil {
		return err
	}
	return s.clientProc.Start(buildClientArgs(cfg.Client))
}

func (s *Service) StopClient() error {
	return s.clientProc.Stop()
}

func (s *Service) StartServer() error {
	cfg, err := s.store.Load()
	if err != nil {
		return err
	}
	if cfg.Server.Connect == "" {
		return errors.New("укажите backend-адрес (-connect)")
	}
	if err := validateObfKey(cfg.Server.ObfProfile, cfg.Server.ObfKey); err != nil {
		return err
	}
	return s.serverProc.Start(buildServerArgs(cfg.Server))
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

func (s *Service) StopServer() error {
	return s.serverProc.Stop()
}

// Stop is wired into the app shutdown-hook chain in cmd/awg-manager/main.go
// (srv.AddShutdownHook(freeturnService.Stop)), same pattern as pingCheckService.
func (s *Service) Stop() {
	_ = s.clientProc.Stop()
	_ = s.serverProc.Stop()
}

// buildClientArgs / buildServerArgs translate Config into the exact CLI
// flags from free-turn-proxy's docs/flags.md. Only non-zero-value fields
// are emitted explicitly, so the binary's own defaults stay in effect for
// anything left unset — saves users from having to know every default.
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
