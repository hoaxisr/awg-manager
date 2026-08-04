package freeturn

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxysup"
)

const supervisorInterval = 30 * time.Second

// StartSupervisor periodically restarts enabled client/server instances whose
// child process died unexpectedly. Respects Enabled==false (manual user stop).
//
// ready гейтит тики: демон поднимает супервизор до boot-последовательности, а
// автостарт прокси намеренно отложен до готовности NDMS/WAN и DNS (иначе
// VK-авторизация бьётся о мёртвый резолвер) и подавлен маркером post-restore.
// Прохода «сразу при старте» нет по той же причине — первый шанс на тике.
func (s *Service) StartSupervisor(ctx context.Context, ready func() bool) {
	go func() {
		ticker := time.NewTicker(supervisorInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ready != nil && !ready() {
					continue
				}
				s.superviseEnabled(ctx)
			}
		}
	}()
}

func (s *Service) superviseEnabled(ctx context.Context) {
	full, err := s.store.Load()
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("supervisor", "", "не удалось прочитать конфиг: "+err.Error())
		}
		return
	}
	now := time.Now()
	for _, c := range full.Clients {
		if ctx.Err() != nil {
			return
		}
		key := clientKey(c.ID)
		if !c.Config.Enabled {
			s.clientHealth.reset(c.ID)
			s.startBackoff.Forget(key)
			continue
		}
		proc := s.clientProcs.get(c.ID)
		running, _ := proc.IsRunning()
		if !running {
			if !s.startBackoff.Allow(key, now) {
				continue
			}
			if err := s.StartClientInstance(c.ID); err != nil {
				s.startBackoff.Fail(key, now)
				if s.appLog != nil {
					s.appLog.Warn("supervisor", c.ID, "перезапуск клиента: "+err.Error())
				}
			} else {
				s.startBackoff.Success(key)
				if s.appLog != nil {
					s.appLog.Info("supervisor", c.ID, "клиент перезапущен")
				}
			}
			continue
		}
		s.startBackoff.Success(key)
		st := proc.Status()
		if s.clientHealth.note(c.ID, clientPeerUnhealthy(st, now)) {
			if err := s.restartClientInstance(c.ID); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, "peer недоступен, перезапуск: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("health", c.ID, "клиент перезапущен: нет активных DTLS-сессий")
			}
			s.clientHealth.reset(c.ID)
		}
	}
	for _, srv := range full.Servers {
		if ctx.Err() != nil {
			return
		}
		key := serverKey(srv.ID)
		if !srv.Config.Enabled {
			s.startBackoff.Forget(key)
			continue
		}
		if running, _ := s.serverProcs.get(srv.ID).IsRunning(); running {
			s.startBackoff.Success(key)
			continue
		}
		if !s.startBackoff.Allow(key, now) {
			continue
		}
		if err := s.StartServerInstance(srv.ID); err != nil {
			s.startBackoff.Fail(key, now)
			if s.appLog != nil {
				s.appLog.Warn("supervisor", srv.ID, "перезапуск сервера: "+err.Error())
			}
		} else {
			s.startBackoff.Success(key)
			if s.appLog != nil {
				s.appLog.Info("supervisor", srv.ID, "сервер перезапущен")
			}
		}
	}
}

func newStartBackoff() *proxysup.Backoff {
	return proxysup.NewBackoff(supervisorInterval, 15*time.Minute)
}

func clientKey(id string) string { return "client:" + id }
func serverKey(id string) string { return "server:" + id }
