package wdtt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

func (s *Service) UpdateServerConfig(cfg ServerConfig) error {
	_, err := s.UpdateServerInstance(DefaultInstanceID, cfg)
	return err
}

// UpdateServerInstance сохраняет конфиг и возвращает его фактическое состояние
// после нормализации и авто-починки listen — его и отдаёт API.
func (s *Service) UpdateServerInstance(id string, cfg ServerConfig) (ServerConfig, error) {
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return ServerConfig{}, err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		s.mu.Unlock()
		return ServerConfig{}, fmt.Errorf("сервер %q не найден", id)
	}
	prevCfg := full.Servers[idx].Config
	listens := serverListenAddresses(full.Servers)
	reserved := s.reservedServerPortsExcept(id)
	cfg.Listen = ensureUniqueServerListenAddr(listens, idx, cfg.Listen, reserved, 56000, 56100)
	cfg = normalizeServerConfig(cfg)
	cfg.WgPort = ensureUniqueWgPort(cfg.WgPort, cfg.Listen, reserved, 56000, 56100)
	full.Servers[idx].Config = cfg
	saveErr := s.store.Save(full)
	running := s.serverProcs.get(id).Status().Running
	savedCfg := full.Servers[idx].Config
	s.mu.Unlock()
	if saveErr != nil {
		return ServerConfig{}, saveErr
	}
	if pwd := strings.TrimSpace(savedCfg.Password); pwd != "" {
		if cfgDir, dirErr := s.serverConfigDir(id, savedCfg); dirErr == nil {
			go func(dir, pass string) {
				_ = syncPanelMainPassword(dir, pass)
			}(cfgDir, pwd)
		}
	}
	if running {
		go func(prev, saved ServerConfig) {
			ctx := context.Background()
			if err := s.applyServerAccess(ctx, id, saved); err != nil && s.appLog != nil {
				s.appLog.Warn("access", id, "фоновое применение NAT/LAN: "+err.Error())
			}
			s.syncServerListenFirewall(ctx, id, prev, saved)
		}(prevCfg, savedCfg)
	}
	return savedCfg, nil
}

func (s *Service) CreateServer(in CreateServerInput) (ServerInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return ServerInstance{}, err
	}
	// Один инстанс: wdtt-server всегда создаёт интерфейс wdtt0 с адресом
	// 10.66.66.1 — второй параллельный сервер поднять физически нельзя.
	if len(full.Servers) > 0 {
		return ServerInstance{}, errors.New("wdtt-server поддерживает один инстанс: интерфейс wdtt0 общий")
	}
	cfg := DefaultServerConfig()
	if in.Config != nil {
		cfg = *in.Config
	}
	reserved := s.occupiedLocalListenPorts()
	cfg = normalizeServerConfig(cfg)
	cfg.Listen = nextServerListen(full.Servers, reserved)
	cfg.WgPort = ensureUniqueWgPort(cfg.WgPort, cfg.Listen, reserved, 56000, 56100)
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
	full.Servers = append(full.Servers[:idx], full.Servers[idx+1:]...)
	saveErr := s.store.Save(full)
	s.mu.Unlock()
	_ = s.serverProcs.get(id).Stop()
	return saveErr
}

func (s *Service) RenameServer(id, name string) error {
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
	full.Servers[idx].Name = strings.TrimSpace(name)
	return s.store.Save(full)
}

func (s *Service) StartServer() error {
	return s.StartServerInstance(DefaultInstanceID)
}

func (s *Service) StartServerInstance(id string) error {
	inst, err := s.serverInstance(id)
	if err != nil {
		return err
	}
	saved, err := s.UpdateServerInstance(id, inst.Config)
	if err != nil {
		return err
	}
	if strings.TrimSpace(saved.Password) == "" {
		return errors.New("укажите пароль подключения (-password)")
	}
	cfgDir, err := s.serverConfigDir(id, saved)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return fmt.Errorf("config-dir: %w", err)
	}
	cfg := saved
	cfg.ConfigDir = cfgDir
	if err := s.serverProcs.get(id).Start(buildServerArgs(cfg)); err != nil {
		return err
	}
	waitForInterface(s.ifaceChecker, DefaultWdttIface, 8*time.Second)
	if err := s.applyServerAccess(context.Background(), id, cfg); err != nil {
		_ = s.serverProcs.get(id).Stop()
		return err
	}
	if err := applyServerListenFirewall(context.Background(), cfg); err != nil && s.appLog != nil {
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
	removeEntwareNAT(context.Background(), DefaultWdttIface)
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

func (s *Service) serverConfigDir(id string, cfg ServerConfig) (string, error) {
	if dir := strings.TrimSpace(cfg.ConfigDir); dir != "" {
		return dir, nil
	}
	return filepath.Join(s.dataDir, "wdtt", "server", id), nil
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

func normalizeServerConfig(cfg ServerConfig) ServerConfig {
	if cfg.Listen == "" {
		cfg.Listen = DefaultServerConfig().Listen
	}
	if cfg.WgPort <= 0 {
		cfg.WgPort = DefaultServerConfig().WgPort
	}
	cfg.Password = strings.TrimSpace(cfg.Password)
	cfg.AdminID = strings.TrimSpace(cfg.AdminID)
	cfg.BotToken = strings.TrimSpace(cfg.BotToken)
	cfg.ConfigDir = strings.TrimSpace(cfg.ConfigDir)
	if cfg.NatMode == "" {
		cfg.NatMode = DefaultServerConfig().NatMode
	}
	if cfg.Policy == "" {
		cfg.Policy = DefaultServerConfig().Policy
	}
	return cfg
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
	if c.WgPort > 0 {
		args = append(args, "-wg-port", strconv.Itoa(c.WgPort))
	}
	str("-config-dir", c.ConfigDir)
	str("-password", c.Password)
	str("-admin", c.AdminID)
	str("-bot-token", c.BotToken)
	flag("-no-nat", true)
	str("-nat-if", c.NatIface)
	flag("-debug", c.Debug)
	return args
}
