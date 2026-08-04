package freeturn

import (
	"context"
	"time"
)

const supervisorInterval = 30 * time.Second

// StartSupervisor periodically restarts enabled client/server instances whose
// child process died unexpectedly. Respects Enabled==false (manual user stop).
func (s *Service) StartSupervisor(ctx context.Context) {
	go func() {
		s.superviseEnabled()
		ticker := time.NewTicker(supervisorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.superviseEnabled()
			}
		}
	}()
}

func (s *Service) superviseEnabled() {
	full, err := s.store.Load()
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("supervisor", "", "не удалось прочитать конфиг: "+err.Error())
		}
		return
	}
	now := time.Now()
	for _, c := range full.Clients {
		if !c.Config.Enabled {
			s.clientHealth.reset(c.ID)
			continue
		}
		proc := s.clientProcs.get(c.ID)
		running, _ := proc.IsRunning()
		if !running {
			if err := s.StartClientInstance(c.ID); err != nil && s.appLog != nil {
				s.appLog.Warn("supervisor", c.ID, "перезапуск клиента: "+err.Error())
			} else if s.appLog != nil {
				s.appLog.Info("supervisor", c.ID, "клиент перезапущен")
			}
			continue
		}
		st := proc.Status()
		if s.clientHealth.note(c.ID, clientPeerUnhealthy(st, now)) {
			if err := s.restartClientInstance(c.ID); err != nil && s.appLog != nil {
				s.appLog.Warn("health", c.ID, "peer недоступен, перезапуск: "+err.Error())
			} else if s.appLog != nil {
				s.appLog.Info("health", c.ID, "клиент перезапущен: нет активных DTLS-сессий")
			}
			s.clientHealth.reset(c.ID)
		}
	}
	for _, srv := range full.Servers {
		if !srv.Config.Enabled {
			continue
		}
		if running, _ := s.serverProcs.get(srv.ID).IsRunning(); running {
			continue
		}
		if err := s.StartServerInstance(srv.ID); err != nil && s.appLog != nil {
			s.appLog.Warn("supervisor", srv.ID, "перезапуск сервера: "+err.Error())
		} else if s.appLog != nil {
			s.appLog.Info("supervisor", srv.ID, "сервер перезапущен")
		}
	}
}
