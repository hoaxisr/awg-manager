package router

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	schedulerInitialDelay = 2 * time.Minute
	schedulerTick         = 30 * time.Second
	dailyTickWindow       = 30 * time.Second
)

type Scheduler struct {
	svc      *ServiceImpl
	settings *storage.SettingsStore
	log      *logger.Logger
	stop     chan struct{}
	done     chan struct{}

	lastRefresh time.Time
}

func NewScheduler(svc *ServiceImpl, settings *storage.SettingsStore, log *logger.Logger) *Scheduler {
	return &Scheduler{
		svc:      svc,
		settings: settings,
		log:      log,
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
		s.tickRuleSetRefresh(ctx)
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
	if err := s.svc.Reconcile(ctx); err != nil && s.log != nil {
		s.log.Warnf("router scheduler: policy sync: %v", err)
	}
}

func (s *Scheduler) tickRuleSetRefresh(ctx context.Context) {
	settings, err := s.settings.Load()
	if err != nil || !settings.SingboxRouter.Enabled {
		return
	}
	sr := settings.SingboxRouter
	if sr.RefreshMode == "" {
		sr.RefreshMode = "interval"
	}
	now := time.Now()
	var should bool
	switch sr.RefreshMode {
	case "interval":
		should = shouldRefreshInterval(now, s.lastRefresh, sr.RefreshInterval)
	case "daily":
		should = shouldRefreshDaily(now, s.lastRefresh, sr.RefreshDaily)
	}
	if !should {
		return
	}
	if s.svc.deps.Singbox != nil {
		if err := s.svc.deps.Singbox.Reload(); err != nil {
			if s.log != nil {
				s.log.Warnf("router scheduler: reload for refresh: %v", err)
			}
			return
		}
	}
	s.lastRefresh = now
}

func shouldRefreshInterval(now, last time.Time, intervalHours int) bool {
	if intervalHours < 1 {
		return false
	}
	if last.IsZero() {
		return true
	}
	return now.Sub(last) >= time.Duration(intervalHours)*time.Hour
}

func shouldRefreshDaily(now, last time.Time, targetTime string) bool {
	if targetTime == "" {
		return false
	}
	t, err := time.Parse("15:04", targetTime)
	if err != nil {
		return false
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if now.Before(target) || now.After(target.Add(dailyTickWindow)) {
		return false
	}
	if !last.IsZero() && last.After(target) {
		return false
	}
	return true
}
