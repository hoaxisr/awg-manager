package updater

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const checkInterval = 24 * time.Hour

// Service manages periodic update checks and caches results.
type Service struct {
	version  string
	log      *logger.Logger
	settings *storage.SettingsStore
	mu       sync.RWMutex
	cached  *UpdateInfo
	stop    chan struct{}
	done    chan struct{}

	// Guard against concurrent upgrades
	upgrading bool
}

// New creates a new updater service.
func New(version string, settings *storage.SettingsStore, log *logger.Logger) *Service {
	return &Service{
		version:  version,
		log:      log,
		settings: settings,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins periodic update checks.
func (s *Service) Start() {
	go s.run()
}

// Stop stops the periodic checker.
func (s *Service) Stop() {
	close(s.stop)
	<-s.done
}

func (s *Service) run() {
	defer close(s.done)

	// Initial check after short delay (let the system settle)
	select {
	case <-time.After(5 * time.Minute):
	case <-s.stop:
		return
	}

	s.doCheck()

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.doCheck()
		case <-s.stop:
			return
		}
	}
}

func (s *Service) doCheck() {
	// Check if auto-updates are enabled
	if s.settings != nil {
		if st, err := s.settings.Get(); err == nil && !st.Updates.CheckEnabled {
			return
		}
	}

	s.mu.Lock()
	if s.cached == nil {
		s.cached = &UpdateInfo{CurrentVersion: s.version, Checking: true}
	} else {
		s.cached.Checking = true
	}
	s.mu.Unlock()

	ctx := context.Background()
	info := Check(ctx, s.version)

	s.mu.Lock()
	s.cached = info
	s.mu.Unlock()

	if info.Error != "" {
		s.log.Warn("Update check failed", map[string]interface{}{"error": info.Error})
	} else if info.Available {
		s.log.Info("Update available", map[string]interface{}{"latest": info.LatestVersion})
	}
}

// GetCached returns the last check result without triggering a new check.
func (s *Service) GetCached() *UpdateInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.cached == nil {
		return &UpdateInfo{
			CurrentVersion: s.version,
		}
	}
	return s.cached
}

// CheckNow triggers an immediate check and returns the result.
func (s *Service) CheckNow(ctx context.Context) *UpdateInfo {
	s.mu.Lock()
	if s.cached == nil {
		s.cached = &UpdateInfo{CurrentVersion: s.version, Checking: true}
	} else {
		s.cached.Checking = true
	}
	s.mu.Unlock()

	info := Check(ctx, s.version)

	s.mu.Lock()
	s.cached = info
	s.mu.Unlock()

	return info
}

// ApplyUpgrade starts the opkg upgrade process.
// Returns error if upgrade is already in progress.
func (s *Service) ApplyUpgrade(ctx context.Context) error {
	s.mu.Lock()
	if s.upgrading {
		s.mu.Unlock()
		return ErrUpgradeInProgress
	}
	s.upgrading = true
	s.mu.Unlock()

	return Upgrade(ctx)
}
