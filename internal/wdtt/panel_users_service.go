package wdtt

import (
	"fmt"
	"strings"
)

// ListServerPanelUsers returns WDTT client passwords from panel.db for a server instance.
func (s *Service) ListServerPanelUsers(serverID string) (PanelUsersStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	return loadPanelUsers(cfgDir, inst.Config.Password)
}

// AddServerPanelUser registers a separate client password in panel.db.
func (s *Service) AddServerPanelUser(serverID, password, comment, vkHash, mainPassword string) (PanelUsersStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	main := strings.TrimSpace(inst.Config.Password)
	if main == "" {
		main = strings.TrimSpace(mainPassword)
	}
	if main == "" {
		return PanelUsersStatus{}, fmt.Errorf("сначала задайте пароль сервера")
	}
	if strings.TrimSpace(inst.Config.Password) == "" {
		cfg := inst.Config
		cfg.Password = main
		if _, err := s.UpdateServerInstance(serverID, cfg); err != nil {
			return PanelUsersStatus{}, err
		}
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	st, err := addPanelUser(cfgDir, main, password, comment, vkHash)
	if err != nil {
		return st, err
	}
	s.notifyServerPanelUsersChanged(serverID)
	return st, nil
}

// RemoveServerPanelUser deletes one non-main client password from panel.db.
func (s *Service) RemoveServerPanelUser(serverID, password string) (PanelUsersStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	st, err := removePanelUser(cfgDir, inst.Config.Password, password)
	if err != nil {
		return st, err
	}
	s.notifyServerPanelUsersChanged(serverID)
	return st, nil
}

func (s *Service) notifyServerPanelUsersChanged(serverID string) {
	if !s.serverProcs.get(serverID).Status().Running {
		return
	}
	if err := s.serverProcs.get(serverID).Reload(); err != nil && s.appLog != nil {
		s.appLog.Warn("panel", serverID, "SIGHUP после panel.db: "+err.Error())
	}
}
