package hydraroute

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// watchdogInterval — период наблюдения за процессом hrneo.
//
// Здесь опрос ЗАКОНЕН, и вот почему: смерть чужого процесса не порождает
// события ни у кого — ни хука NDMS, ни сигнала нам (демон hrneo запускается
// своим init-скриптом, а не нами, так что и wait() по нему не сделать).
// Наблюдение — единственный способ узнать.
//
// Но наблюдатель обязан быть ОДИН и в демоне. Раньше за процессом следила
// панель: стор `routing.hydrarouteStatus` опрашивал статус каждые 30 с из
// КАЖДОЙ открытой вкладки, по HTTP (F353). Теперь опрашивает демон, а панель
// получает событие.
//
// Стоимость одного прохода ничтожна: Detect() — это два os.Stat, чтение
// pid-файла и kill(pid, 0). Ни RCI, ни exec.
var watchdogInterval = 30 * time.Second

// Publisher — узкая часть шины, нужная сторожу.
type Publisher interface {
	PublishInvalidated(resource events.Resource, reason string)
}

// StartWatchdog наблюдает за живостью hrneo и публикует подсказку инвалидации
// на КАЖДОЙ смене состояния процесса. Возвращает функцию остановки.
//
// Публикуем только на смене: процесс либо жив, либо нет, и промежуточных
// значений у этого наблюдения нет — в отличие от проверок связности, где
// меняется ещё и время последней проверки.
func (s *Service) StartWatchdog(ctx context.Context, pub Publisher) func() {
	if pub == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(watchdogInterval)
		defer t.Stop()
		prev := s.processState()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cur := s.processState()
				if cur == prev {
					continue
				}
				prev = cur
				s.appLog.Info("watchdog", "", "состояние hrneo изменилось: "+string(cur))
				pub.PublishInvalidated(events.ResourceRoutingHydrarouteStatus, "watchdog")
			}
		}
	}()
	return cancel
}

// processState — наблюдаемая величина сторожа: только состояние процесса.
// Версия и прочие поля меняются лишь нашими действиями, о которых событие и
// так публикуется.
//
// probeForTest — шов: подменить наблюдение, не трогая файловую систему.
func (s *Service) processState() ProcessState {
	if s.probeForTest != nil {
		return s.probeForTest()
	}
	return Detect().ProcessState
}
