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
	settings, err := s.settings.Load()
	if err != nil || !settings.SingboxRouter.Enabled {
		return
	}
	if err := s.svc.Reconcile(ctx); err != nil {
		s.svc.appLog.Warn("scheduler-policy-sync", "", err.Error())
	}
}
