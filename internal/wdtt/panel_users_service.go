package wdtt

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ListServerPanelUsers returns the server clients: identities from wdtt.json,
// enriched with live counters from panel.db. Список не пустеет, даже если
// panel.db потеряна — это и есть смысл единственного источника правды.
func (s *Service) ListServerPanelUsers(serverID string) (PanelUsersStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	st, loadErr := loadPanelUsers(cfgDir, inst.Config.Password)
	st.PanelDBPath = panelDBPath(cfgDir)
	if loadErr != nil && s.appLog != nil {
		s.appLog.Warn("panel", serverID, "panel.db не прочитана: "+loadErr.Error())
	}
	clients := inst.Config.Clients
	if adopted := s.adoptServerClients(serverID, panelExtras(inst.Config, st.Users)); len(adopted) > 0 {
		clients = adopted
	}
	return mergePanelUsers(inst.Config.Password, clients, st), nil
}

// AddServerPanelUser registers a separate client password.
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
	client, err := addPanelUser(cfgDir, main, password, comment, vkHash)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	if err := s.putServerClient(serverID, client); err != nil {
		return PanelUsersStatus{}, err
	}
	s.notifyServerPanelUsersChanged(serverID)
	return s.ListServerPanelUsers(serverID)
}

// RemoveServerPanelUser deletes one non-main client.
func (s *Service) RemoveServerPanelUser(serverID, password string) (PanelUsersStatus, error) {
	inst, err := s.serverInstance(serverID)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	cfgDir, err := s.serverConfigDir(serverID, inst.Config)
	if err != nil {
		return PanelUsersStatus{}, err
	}
	if err := removePanelUser(cfgDir, inst.Config.Password, password); err != nil {
		return PanelUsersStatus{}, err
	}
	if err := s.dropServerClient(serverID, password); err != nil {
		return PanelUsersStatus{}, err
	}
	s.notifyServerPanelUsersChanged(serverID)
	return s.ListServerPanelUsers(serverID)
}

// restoreServerPanelUsers projects wdtt.json clients into panel.db before the
// server starts and adopts rows that only panel.db knows about.
func (s *Service) restoreServerPanelUsers(serverID, cfgDir string, cfg ServerConfig) {
	extra, err := restorePanelUsers(cfgDir, cfg.Password, cfg.Clients)
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("panel", serverID, "panel.db не восстановлена из конфига: "+err.Error())
		}
		return
	}
	s.adoptServerClients(serverID, extra)
}

// panelExtras returns panel.db rows that wdtt.json does not know yet.
func panelExtras(cfg ServerConfig, users []PanelUserEntry) []ServerClient {
	known := map[string]bool{strings.TrimSpace(cfg.Password): true}
	for _, c := range cfg.Clients {
		known[c.Password] = true
	}
	var extra []ServerClient
	for _, u := range users {
		// IsMain здесь — мнение самой panel.db: строка прежнего главного
		// пароля не клиент, её снимает setMainPassword.
		if !known[u.Password] && !u.IsMain {
			extra = append(extra, ServerClient{Password: u.Password, Comment: u.Comment, VkHash: u.VkHash})
		}
	}
	return extra
}

// mergePanelUsers builds the UI list: main password first, then wdtt.json
// clients, each overlaid with live panel.db counters.
func mergePanelUsers(mainPassword string, clients []ServerClient, st PanelUsersStatus) PanelUsersStatus {
	live := make(map[string]PanelUserEntry, len(st.Users))
	for _, u := range st.Users {
		live[u.Password] = u
	}
	out := PanelUsersStatus{PanelDBPath: st.PanelDBPath, Available: st.Available, Users: []PanelUserEntry{}}
	add := func(c ServerClient, isMain bool) {
		e, ok := live[c.Password]
		if !ok {
			e = PanelUserEntry{Password: c.Password, Comment: c.Comment, VkHash: c.VkHash}
		}
		if e.Comment == "" {
			e.Comment = c.Comment
		}
		if e.VkHash == "" {
			e.VkHash = c.VkHash
		}
		e.IsMain = isMain
		if isMain {
			// panel.db зовёт эту строку «ADMIN» — в UI подпись не должна
			// скакать в зависимости от того, запускался ли уже сервер.
			e.Comment = "Основной"
		}
		out.Users = append(out.Users, e)
	}
	if main := strings.TrimSpace(mainPassword); main != "" {
		add(ServerClient{Password: main}, true)
	}
	for _, c := range clients {
		if strings.TrimSpace(c.Password) == "" || c.Password == strings.TrimSpace(mainPassword) {
			continue
		}
		add(c, false)
	}
	return out
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
