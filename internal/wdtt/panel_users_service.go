package wdtt

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
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

// serverAdminAddr — дефолтный -admin-addr у wdtt-server; мы его не переопределяем.
const serverAdminAddr = "127.0.0.1:2861"

func (s *Service) notifyServerPanelUsersChanged(serverID string) {
	if !s.serverProcs.get(serverID).Status().Running {
		return
	}
	if err := reloadServerPanelDB(); err != nil && s.appLog != nil {
		s.appLog.Warn("panel", serverID, "перечитывание panel.db: "+err.Error())
	}
}

// reloadServerPanelDB просит запущенный wdtt-server перечитать panel.db.
//
// SIGHUP для этого не годится: сервер подписан только на SIGTERM и SIGINT
// (server/server.go в апстриме), а необработанный SIGHUP в Go сохраняет
// дефолтную диспозицию и убивает процесс. Штатный путь — admin-ручка на
// localhost; токен не нужен, пока session_key в panel.db пуст (headless).
func reloadServerPanelDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+serverAdminAddr+"/admin/reload", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("admin/reload: %s", resp.Status)
	}
	return nil
}
