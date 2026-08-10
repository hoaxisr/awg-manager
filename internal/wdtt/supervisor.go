package wdtt

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
// автостарт прокси намеренно отложен до готовности NDMS/WAN и DNS (vkcalls
// бьётся о мёртвый 127.0.0.1:53) и подавлен маркером post-restore. Старт
// WDTT-сервера к тому же тянет NDMS/RCI — тем более не «сразу при старте».
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
			s.clientStall.reset(c.ID)
			s.startBackoff.Forget(key)
			s.startBackoff.Forget(clientHealthKey(c.ID))
			continue
		}
		proc := s.clientProcs.get(c.ID)
		st := proc.Status()
		// Живой процесс без StartedAt — осиротевший pid-файл, переживший
		// рестарт демона: лога и телеметрии по нему нет, health-надзор слеп.
		// Лечится обычным стартом — process.Start усыновляет такой процесс.
		if !st.Running || st.StartedAt == nil {
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
		peerBad := clientPeerUnhealthy(st, now)
		ndmsBad := clientRawNDMSUnhealthy(c.Config, s.ifaceChecker, st, now)
		relayBad := clientRawRelayUnhealthy(ctx, c.Config, s.relayProbe, s.ifaceChecker, st, now)
		if ndmsBad && !peerBad {
			// Reconcile — первая (дешёвая) попытка лечения: поднять OpkgTun без
			// рестарта wt-client. Рестарт — эскалация ниже, если интерфейс не
			// ожил.
			reconciled, rerr := s.reconcileClientRawNDMS(ctx, c.ID, c.Config)
			switch {
			case rerr != nil:
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, "OpkgTun reconcile: "+rerr.Error())
				}
			case !reconciled:
				// Пустой RawClientIP и т.п. — reconcile ничего не сделал; это
				// не должно молча сходить за успех.
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, "OpkgTun reconcile: пропущен, нет условий для восстановления")
				}
			}
			st = proc.Status()
			ndmsBad = clientRawNDMSUnhealthy(c.Config, s.ifaceChecker, st, now)
			if !ndmsBad {
				s.clientHealth.reset(c.ID)
				s.clientStall.reset(c.ID)
				continue
			}
			// Не ожил — страйк той же механики, что peerBad/relayBad, ниже.
		}
		unhealthy := peerBad || ndmsBad || relayBad
		if s.clientHealth.note(c.ID, unhealthy) {
			healthKey := clientHealthKey(c.ID)
			if !s.startBackoff.Allow(healthKey, now) {
				continue
			}
			// Порог страйков выбит повторно после предыдущего health-рестарта —
			// само по себе это значит, что лечение не удержалось. Считаем это
			// неудачей backoff'а независимо от того, стартует ли процесс
			// технически: иначе рестарт при неисправимой причине (мёртвый
			// check-URL, окончательно упавший OpkgTun) повторялся бы каждые
			// ~3.5 мин без роста паузы.
			s.startBackoff.Fail(healthKey, now)
			reason := "peer недоступен"
			switch {
			case relayBad && !peerBad && !ndmsBad:
				reason = "raw-туннель не проходит проверку связи"
			case ndmsBad && !peerBad:
				reason = "OpkgTun интерфейс down"
			}
			if err := s.restartClientInstance(c.ID); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, reason+", перезапуск: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("health", c.ID, "клиент перезапущен: "+reason)
			}
			s.clientHealth.reset(c.ID)
			s.clientStall.reset(c.ID)
			continue
		}
		if !unhealthy && st.StartedAt != nil && now.Sub(*st.StartedAt) >= clientHealthGrace {
			// Health-предикаты чисты, и с последнего старта прошло достаточно,
			// чтобы все они (NDMS/relay grace короче) дали реальный сигнал, а
			// не молчали «ещё рано». Это настоящее выздоровление — обнулять
			// backoff можно.
			s.startBackoff.Success(clientHealthKey(c.ID))
		}
		if s.clientStall.note(c.ID, clientRelayStalled(st, now)) {
			if err := s.restartClientInstance(c.ID); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, "входящий трафик встал, перезапуск: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("health", c.ID, "клиент перезапущен: нет входящего трафика")
			}
			s.clientStall.reset(c.ID)
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

// clientHealthKey — отдельное пространство ключей backoff для health-рестартов
// (peerBad/ndmsBad-эскалация/relayBad), отдельное от clientKey: тот сбрасывается
// безусловно на каждом тике с живым процессом (см. Success(key) выше) и стёр бы
// health-backoff раньше, чем он успеет отработать.
func clientHealthKey(id string) string { return "health-client:" + id }
