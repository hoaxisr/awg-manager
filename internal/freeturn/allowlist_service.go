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
	// Store Load-modify-Save под s.mu (как RMW-методы в service.go). Explicit
	// Lock/Unlock, а не defer: addAllowlistClient/loadAllowlistStatus ниже — это
	// ДРУГОЙ ресурс (allowlist-файл), их держать под s.mu не нужно, поэтому
	// Unlock до них. Каждый ранний return в блоке освобождает лок.
	s.mu.Lock()
	full, err := s.store.Load()
	if err != nil {
		s.mu.Unlock()
		return AddAllowlistResult{}, err
	}
	idx := findServerIndex(full.Servers, serverID)
	if idx < 0 {
		s.mu.Unlock()
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
			s.mu.Unlock()
			return AddAllowlistResult{}, err
		}
		needsRestart = true
	}
	s.mu.Unlock()

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
// Returns needsRestart, mirroring AddServerAllowlistClient: the path goes into the
// `-clients-file` start argument, so a running server keeps checking the allowlist
// until it is restarted. Already-disabled allowlist changes nothing — false.
func (s *Service) DisableServerAllowlist(serverID string) (bool, error) {
	// Весь метод — store Load-modify-Save, хвост функции: defer покрывает и
	// ранний return при пустом ClientsFile. Load/Save/findServerIndex s.mu не берут.
	s.mu.Lock()
	defer s.mu.Unlock()

	full, err := s.store.Load()
	if err != nil {
		return false, err
	}
	idx := findServerIndex(full.Servers, serverID)
	if idx < 0 {
		return false, fmt.Errorf("сервер %q не найден", serverID)
	}
	if full.Servers[idx].Config.ClientsFile == "" {
		return false, nil
	}
	full.Servers[idx].Config.ClientsFile = ""
	if err := s.store.Save(full); err != nil {
		return false, err
	}
	return true, nil
}
