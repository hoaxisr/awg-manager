package hydraroute

import (
	"context"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const (
	geoSchedulerInitialDelay = 2 * time.Minute
	geoSchedulerTick         = 15 * time.Minute
	geoRefreshTimeout        = 5 * time.Minute
)

// resolveClientFn returns an HTTP client for the configured global download
// route, a human label, and a cleanup func that MUST be called (lease close).
type resolveClientFn func(ctx context.Context) (*http.Client, string, func(), error)

// GeoRefreshScheduler periodically re-downloads non-external geo files.
// Mirrors internal/dnsroute.Scheduler; dnsroute is intentionally untouched.
type GeoRefreshScheduler struct {
	store       *GeoDataStore
	settings    *storage.SettingsStore
	resolve     resolveClientFn
	appLog      *logging.ScopedLogger
	stop        chan struct{}
	done        chan struct{}
	lastRefresh time.Time
}

func NewGeoRefreshScheduler(store *GeoDataStore, settings *storage.SettingsStore, appLogger logging.AppLogger, resolve resolveClientFn) *GeoRefreshScheduler {
	return &GeoRefreshScheduler{
		store:    store,
		settings: settings,
		resolve:  resolve,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubHrNeo),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func (s *GeoRefreshScheduler) Start() { go s.run() }

func (s *GeoRefreshScheduler) Stop() {
	close(s.stop)
	<-s.done
}

func (s *GeoRefreshScheduler) run() {
	defer close(s.done)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	select {
	case <-time.After(geoSchedulerInitialDelay):
	case <-s.stop:
		return
	}
	for {
		if s.shouldRefresh() {
			// Stamp only successful runs: on cold boot the clock may still be
			// at 1970 until NTP syncs, every TLS fetch fails "not yet valid",
			// and stamping the failure would silently delay the retry by the
			// full interval instead of the next tick.
			if s.doRefresh(ctx) {
				s.lastRefresh = time.Now()
			}
		}
		select {
		case <-time.After(geoSchedulerTick):
		case <-s.stop:
			return
		}
	}
}

func (s *GeoRefreshScheduler) shouldRefresh() bool {
	st, err := s.settings.Get()
	if err != nil || !st.GeoFile.AutoRefreshEnabled {
		return false
	}
	mode := st.GeoFile.RefreshMode
	if mode == "" {
		mode = "interval"
	}
	switch mode {
	case "interval":
		hours := st.GeoFile.RefreshIntervalHours
		if hours < 1 {
			return false
		}
		return s.lastRefresh.IsZero() || time.Since(s.lastRefresh) >= time.Duration(hours)*time.Hour
	case "daily":
		return s.shouldRefreshDaily(st.GeoFile.RefreshDailyTime)
	default:
		return false
	}
}

func (s *GeoRefreshScheduler) shouldRefreshDaily(targetTime string) bool {
	if targetTime == "" {
		return false
	}
	now := time.Now()
	t, err := time.Parse("15:04", targetTime)
	if err != nil {
		return false
	}
	target := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if now.Before(target) || now.After(target.Add(geoSchedulerTick)) {
		return false
	}
	if !s.lastRefresh.IsZero() && s.lastRefresh.After(target) {
		return false
	}
	return true
}

func (s *GeoRefreshScheduler) doRefresh(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, geoRefreshTimeout)
	defer cancel()

	client, label, closeFn, err := s.resolve(ctx)
	if err != nil {
		// Stale/removed route: log and skip — do NOT silently fall back to direct.
		s.appLog.Warn("geo-auto-refresh", "", "resolve download route: "+err.Error())
		return false
	}
	defer closeFn()

	if _, err := s.store.UpdateAllWithClientVia(ctx, client, label); err != nil {
		s.appLog.Warn("geo-auto-refresh", "", "update via "+label+": "+err.Error())
		return false
	}
	s.appLog.Info("geo-auto-refresh", "", "completed via "+label)
	return true
}
