package router

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	schedulerInitialDelay = 2 * time.Minute
	schedulerTick         = 30 * time.Second
)

type Scheduler struct {
	svc      *ServiceImpl
	settings *storage.SettingsStore
	stop     chan struct{}
	done     chan struct{}
}

func NewScheduler(svc *ServiceImpl, settings *storage.SettingsStore) *Scheduler {
	return &Scheduler{
		svc:      svc,
		settings: settings,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (s *Scheduler) Start() { go s.run() }

func (s *Scheduler) Stop() {
	close(s.stop)
	<-s.done
}

func (s *Scheduler) run() {
	defer close(s.done)

	select {
	case <-time.After(schedulerInitialDelay):
	case <-s.stop:
		return
	}

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		s.tickPolicySync(ctx)
		cancel()

		select {
		case <-time.After(schedulerTick):
		case <-s.stop:
			return
		}
	}
}

func (s *Scheduler) tickPolicySync(ctx context.Context) {
	// Get, а не Load: Load читает файл с флеша и берёт ПИШУЩИЙ лок стора, и
	// так — дважды за тик (здесь и внутри Reconcile). Тик самолечения ходит
	// раз в 30 секунд круглосуточно; читать ради него файл незачем, кэш стора
	// обновляется на каждой записи настроек.
	//
	// Живой объект тут безопасен: читаем поле-структуру SingboxRouter, а
	// оговорка стора (settings.go: «Get возвращает ЖИВОЙ объект») — про
	// параллельное чтение его map-полей, которых мы не касаемся.
	settings, err := s.settings.Get()
	if err != nil {
		return
	}
	if !settings.SingboxRouter.Enabled {
		// Движок выключен — Reconcile не гоняем, но reap fakeip-сирот обязан
		// работать и здесь: runtime-сирота (провал delete при disable)
		// возникает именно в выключенном состоянии. В steady-state дёшево —
		// скан читает кэш InterfaceStore. При включённом движке reap делает
		// сам Reconcile.
		if err := s.svc.ReapOrphanedFakeIPTun(ctx); err != nil {
			s.svc.appLog.Warn("fakeip-reap", "", err.Error())
		}
		return
	}
	if err := s.svc.Reconcile(ctx); err != nil {
		s.svc.appLog.Warn("scheduler-policy-sync", "", err.Error())
	}
}
