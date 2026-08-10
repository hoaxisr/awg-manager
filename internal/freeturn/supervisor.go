package freeturn

import (
	"context"
	"errors"
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
			s.startBackoff.Forget(clientHealthKey(c.ID))
			continue
		}
		proc := s.clientProcs.get(c.ID)
		st := proc.Status()
		// Живой процесс без StartedAt — осиротевший pid-файл, переживший
		// рестарт демона: лога и телеметрии по нему нет, health-надзор слеп.
		// Лечится обычным стартом — process.Start усыновляет такой процесс.
		if !st.Running || st.StartedAt == nil {
			// F6: StartClientInstance этого клиента уже идёт где-то ещё (API —
			// процесс мог не успеть пройти proc.Start, st.Running всё ещё false)
			// — не запускать параллельный старт.
			if s.clientStartInFlight(c.ID) {
				continue
			}
			if !s.startBackoff.Allow(key, now) {
				continue
			}
			if err := s.StartClientInstance(c.ID); err != nil {
				// ErrClientStartInFlight — TryLock проиграл гонку со стартом того
				// же клиента откуда-то ещё: это не провал старта, жечь backoff-окно
				// не за что.
				if errors.Is(err, ErrClientStartInFlight) {
					continue
				}
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
		relayBad := clientRelayUnhealthy(ctx, s.relayProbe, s.linkedTunnels, c.ID, st, now)
		unhealthy := peerBad || relayBad
		if s.clientHealth.note(c.ID, unhealthy) {
			healthKey := clientHealthKey(c.ID)
			if !s.startBackoff.Allow(healthKey, now) {
				continue
			}
			// F6: клиент сам мид-флайт стартует (StartClientInstance где-то ещё
			// в процессе — API или сам супервизор) — не гонять рестарт
			// параллельно тому же старту. Страйк уже учтён note() выше и
			// остаётся накопленным, backoff-окно не трогаем — переоценим на
			// следующем тике.
			if s.clientStartInFlight(c.ID) {
				continue
			}
			// Сам факт повторного выбивания порога после предыдущего
			// health-рестарта — уже неудача backoff'а, независимо от того,
			// стартует ли процесс технически: иначе рестарт при мёртвом
			// check-URL/серверной стороне повторялся бы каждые ~3.5 мин без
			// роста паузы, обрывая живые DTLS-потоки на ровном месте.
			s.startBackoff.Fail(healthKey, now)
			reason := "нет активных DTLS-сессий"
			if relayBad && !peerBad {
				reason = "linked-туннель не проходит проверку связи"
			}
			if err := s.restartClientInstance(c.ID); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, reason+", перезапуск: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("health", c.ID, "клиент перезапущен: "+reason)
			}
			s.clientHealth.reset(c.ID)
			continue
		}
		if !unhealthy && st.StartedAt != nil && now.Sub(*st.StartedAt) >= clientHealthGrace {
			// Health-предикаты чисты, и с последнего старта прошло достаточно,
			// чтобы relay-grace (короче) тоже успел дать реальный сигнал —
			// настоящее выздоровление, backoff можно обнулять.
			s.startBackoff.Success(clientHealthKey(c.ID))
		}
	}
	for _, srv := range full.Servers {
		if ctx.Err() != nil {
			return
		}
		key := serverKey(srv.ID)
		if !srv.Config.Enabled {
			s.startBackoff.Forget(key)
			s.startBackoff.Forget(serverHealthKey(srv.ID))
			s.serverHealth.reset(srv.ID)
			continue
		}
		proc := s.serverProcs.get(srv.ID)
		st := proc.Status()
		if !st.Running || st.StartedAt == nil {
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
			continue
		}
		s.startBackoff.Success(key)
		srvBad := serverPeerUnhealthy(st, now)
		if s.serverHealth.note(srv.ID, srvBad) {
			healthKey := serverHealthKey(srv.ID)
			if !s.startBackoff.Allow(healthKey, now) {
				continue
			}
			// Симметрично клиентскому I2: сам факт повторного выбивания порога
			// после предыдущего health-рестарта — уже неудача backoff'а. Мёртвый
			// backend WG на сервере иначе рестартовал бы раз в ~5 мин навсегда.
			s.startBackoff.Fail(healthKey, now)
			if err := s.restartServerInstance(srv.ID); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("health", srv.ID, "peer недоступен, перезапуск: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("health", srv.ID, "сервер перезапущен: handshake/peer недоступен")
			}
			s.serverHealth.reset(srv.ID)
			continue
		}
		if !srvBad && st.StartedAt != nil && now.Sub(*st.StartedAt) >= clientHealthGrace {
			// Настоящее выздоровление (симметрично клиентскому блоку выше) —
			// с последнего старта прошёл grace, и предикат чист не потому что
			// «ещё рано».
			s.startBackoff.Success(serverHealthKey(srv.ID))
		}
	}
}

func newStartBackoff() *proxysup.Backoff {
	return proxysup.NewBackoff(supervisorInterval, 15*time.Minute)
}

func clientKey(id string) string { return "client:" + id }
func serverKey(id string) string { return "server:" + id }

// clientHealthKey — отдельное пространство ключей backoff для health-рестартов
// (peerBad/relayBad), отдельное от clientKey: тот сбрасывается безусловно на
// каждом тике с живым процессом (см. Success(key) выше) и стёр бы
// health-backoff раньше, чем он успеет отработать.
func clientHealthKey(id string) string { return "health-client:" + id }

// serverHealthKey — отдельное пространство ключей backoff для серверных
// health-рестартов (а): отдельно от serverKey по той же причине, что и
// clientHealthKey от clientKey — serverKey безусловно получает Success() на
// каждом тике с живым процессом.
func serverHealthKey(id string) string { return "health-server:" + id }
