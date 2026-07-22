package freeturn

import (
	"fmt"
	"strings"
)

// ListServerAllowlist returns authorized client IDs for a server instance.
func (s *Service) ListServerAllowlist(serverID string) (AllowlistStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return AllowlistStatus{}, err
	}
	return loadAllowlistStatus(inst.Config.ClientsFile)
}

// AddServerAllowlistClient registers a client ID in the server's allowlist file.
// If allowlist was disabled, enables it by setting clientsFile in server config.
func (s *Service) AddServerAllowlistClient(serverID, clientID, comment string) (AddAllowlistResult, error) {
	full, err := s.store.Load()
	if err != nil {
		return AddAllowlistResult{}, err
	}
	idx := findServerIndex(full.Servers, serverID)
	if idx < 0 {
		return AddAllowlistResult{}, fmt.Errorf("сервер %q не найден", serverID)
	}

	cfg := full.Servers[idx].Config
	needsRestart := false
	path := strings.TrimSpace(cfg.ClientsFile)
	if path == "" {
		path = defaultAllowlistPath(s.dataDir, serverID)
		cfg.ClientsFile = path
		full.Servers[idx].Config = cfg
		if err := s.store.Save(full); err != nil {
			return AddAllowlistResult{}, err
		}
		needsRestart = true
	}

	if err := addAllowlistClient(path, clientID, comment); err != nil {
		return AddAllowlistResult{}, err
	}

	st, err := loadAllowlistStatus(path)
	if err != nil {
		return AddAllowlistResult{}, err
	}
	return AddAllowlistResult{AllowlistStatus: st, NeedsRestart: needsRestart}, nil
}

// RemoveServerAllowlistClient deletes one client ID from the allowlist file.
func (s *Service) RemoveServerAllowlistClient(serverID, clientID string) error {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(inst.Config.ClientsFile)
	if path == "" {
		return fmt.Errorf("allowlist не включён")
	}
	return removeAllowlistClient(path, clientID)
}

// DisableServerAllowlist turns off Client ID checking (clears clientsFile in config).
func (s *Service) DisableServerAllowlist(serverID string) error {
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, serverID)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", serverID)
	}
	if full.Servers[idx].Config.ClientsFile == "" {
		return nil
	}
	full.Servers[idx].Config.ClientsFile = ""
	return s.store.Save(full)
}
