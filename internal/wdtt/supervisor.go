package wdtt

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
			s.startBackoff.Forget(reconcileKey(c.ID))
			s.startBackoff.Forget(clientStallKey(c.ID))
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
				// не за что, на следующем тике клиент, скорее всего, уже поднят.
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
		ndmsBad := clientRawNDMSUnhealthy(c.Config, s.ifaceChecker, st, now)
		relayBad := clientRawRelayUnhealthy(ctx, c.Config, s.relayProbe, s.ifaceChecker, st, now)
		// I4/F6: пока у ЭТОГО клиента где-то идёт StartClientInstance
		// (bootstrap ждёт RAWCONF — VK-капча, может занять минуты), reconcile
		// не должен параллельно мутировать NDMS-интерфейс без лока. Per-client
		// guard — старт клиента A не должен глушить reconcile клиента B. Guard
		// до Allow(rKey) — не жжём backoff-окно на скип, страйк ниже всё равно
		// копится как обычно.
		if ndmsBad && !peerBad && !s.clientStartInFlight(c.ID) {
			// Reconcile — первая (дешёвая) попытка лечения: поднять OpkgTun без
			// рестарта wt-client. Рестарт — эскалация ниже, если интерфейс не
			// ожил. Сам reconcile — ~10-13 RCI-команд, поэтому гоняется под
			// собственным backoff (F1): вечно мёртвый OpkgTun не должен грузить
			// роутер RCI-вызовами каждый тик, а страйк в clientHealth всё равно
			// копится независимо от того, пропущен reconcile backoff'ом или нет.
			rKey := reconcileKey(c.ID)
			if s.startBackoff.Allow(rKey, now) {
				reconciled, rerr := s.reconcileClientRawNDMS(ctx, c.ID, c.Config)
				// Reconcile — блокирующая пачка RCI-команд: пользователь мог
				// нажать «стоп» за это время, и его решение важнее нашего
				// лечения (тот же паттерн, что в restartClientInstance,
				// health.go:106-110).
				if inst, err := s.clientInstance(c.ID); err == nil && !inst.Config.Enabled {
					continue
				}
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
				if ndmsBad {
					s.startBackoff.Fail(rKey, now)
				} else {
					s.startBackoff.Success(rKey)
					s.clientHealth.reset(c.ID)
					s.clientStall.reset(c.ID)
					continue
				}
			}
			// Не ожил (или reconcile сам пропущен backoff'ом) — страйк той же
			// механики, что peerBad/relayBad, ниже.
		}
		unhealthy := peerBad || ndmsBad || relayBad
		if s.clientHealth.note(c.ID, unhealthy) {
			healthKey := clientHealthKey(c.ID)
			if !s.startBackoff.Allow(healthKey, now) {
				continue
			}
			// F6: клиент сам мид-флайт стартует (StartClientInstance где-то
			// ещё в процессе — API или сам супервизор) — не гонять рестарт
			// параллельно тому же старту. Страйк уже учтён note() выше и
			// остаётся накопленным, backoff-окно не трогаем — переоценим на
			// следующем тике.
			if s.clientStartInFlight(c.ID) {
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
		stalled := clientRelayStalled(st, now)
		if s.clientStall.note(c.ID, stalled) {
			// (б): свой ключ, а не clientHealthKey — профиль застоя ловит и
			// живой простаивающий туннель (см. комментарий у clientRelayStalled
			// в health.go), это отдельный от «явно неисправен» повод и не
			// должен делить окно backoff с peerBad/ndmsBad/relayBad: страйк
			// одного не должен ни ускорять, ни тормозить эскалацию другого.
			stallKey := clientStallKey(c.ID)
			if !s.startBackoff.Allow(stallKey, now) {
				// F5: страйк-трекер clientStall тут намеренно НЕ трогаем —
				// note() уже вернул true (страйки на пороге), и при следующем
				// разрешённом тике, если застой не прошёл, рестарт случится
				// сразу, без повторного набора clientStallStrikes с нуля.
				continue
			}
			// F6: тот же per-client guard, что у health-эскалации выше —
			// не гонять рестарт по застою параллельно собственному старту.
			if s.clientStartInFlight(c.ID) {
				continue
			}
			s.startBackoff.Fail(stallKey, now)
			if err := s.restartClientInstance(c.ID); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("health", c.ID, "входящий трафик встал, перезапуск: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("health", c.ID, "клиент перезапущен: нет входящего трафика")
			}
			s.clientStall.reset(c.ID)
			continue
		}
		if !stalled && st.StartedAt != nil && now.Sub(*st.StartedAt) >= clientHealthGrace {
			s.startBackoff.Success(clientStallKey(c.ID))
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

// reconcileKey — свой backoff для самого вызова reconcileClientRawNDMS (F1):
// отдельно от clientHealthKey, потому что reconcile может пропускаться своим
// backoff'ом (дорогая серия RCI-команд), а страйк ndmsBad в clientHealth при
// этом всё равно обязан копиться и эскалировать в рестарт.
func reconcileKey(id string) string { return "reconcile-client:" + id }

// clientStallKey — свой backoff для рестарта по застою входящего трафика (б):
// отдельно от clientHealthKey, потому что признак застоя (health.go) сам
// признаёт неотличимость от живого простаивающего туннеля — смешивать его
// backoff-окно с окном явно неисправных peerBad/ndmsBad/relayBad не стоит.
func clientStallKey(id string) string { return "stall-client:" + id }
