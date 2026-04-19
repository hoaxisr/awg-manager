package dnsroute

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	schedulerInitialDelay = 2 * time.Minute
	schedulerTick         = 15 * time.Minute // unified tick for all modes
)

// Scheduler periodically refreshes DNS route subscriptions.
type Scheduler struct {
	svc         Service
	settings    *storage.SettingsStore
	log         *logger.Logger
	stop        chan struct{}
	done        chan struct{}
	lastRefresh time.Time // tracks last successful refresh
}

// NewScheduler creates a new subscription refresh scheduler.
func NewScheduler(svc Service, settings *storage.SettingsStore, log *logger.Logger) *Scheduler {
	return &Scheduler{
		svc:      svc,
		settings: settings,
		log:      log,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins the scheduler goroutine.
func (s *Scheduler) Start() {
	go s.run()
}

// Stop signals the scheduler to stop and waits for it to finish.
func (s *Scheduler) Stop() {
	close(s.stop)
	<-s.done
}

func (s *Scheduler) run() {
	defer close(s.done)

	// Initial delay — let the system settle after boot.
	select {
	case <-time.After(schedulerInitialDelay):
	case <-s.stop:
		return
	}

	for {
		if s.shouldRefresh() {
			s.doRefresh()
			s.lastRefresh = time.Now()
		}

		select {
		case <-time.After(schedulerTick):
		case <-s.stop:
			return
		}
	}
}

// shouldRefresh decides whether a refresh is needed based on current settings.
func (s *Scheduler) shouldRefresh() bool {
	st, err := s.settings.Get()
	if err != nil || !st.DNSRoute.AutoRefreshEnabled {
		return false
	}

	mode := st.DNSRoute.RefreshMode
	if mode == "" {
		mode = "interval"
	}

	switch mode {
	case "interval":
		hours := st.DNSRoute.RefreshIntervalHours
		if hours < 1 {
			return false
		}
		interval := time.Duration(hours) * time.Hour
		return s.lastRefresh.IsZero() || time.Since(s.lastRefresh) >= interval

	case "daily":
		return s.shouldRefreshDaily(st.DNSRoute.RefreshDailyTime)

	default:
		return false
	}
}

// shouldRefreshDaily returns true if current time is within the scheduler tick
// window of the configured daily time and no refresh has happened today yet.
func (s *Scheduler) shouldRefreshDaily(targetTime string) bool {
	if targetTime == "" {
		return false
	}
	now := time.Now()

	// Parse target HH:MM
	t, err := time.Parse("15:04", targetTime)
	if err != nil {
		return false
	}

	// Build today's target moment
	target := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())

	// Check if now is within [target, target+schedulerTick) window
	if now.Before(target) || now.After(target.Add(schedulerTick)) {
		return false
	}

	// Don't fire twice in the same window
	if !s.lastRefresh.IsZero() && s.lastRefresh.After(target) {
		return false
	}

	return true
}

func (s *Scheduler) doRefresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := s.svc.RefreshAllSubscriptions(ctx); err != nil {
		s.log.Warn("dns-route auto-refresh failed", map[string]interface{}{"error": err.Error()})
	} else {
		s.log.Info("dns-route auto-refresh completed", nil)
	}
}
