package updater

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const checkInterval = 24 * time.Hour

// Service manages periodic update checks and caches results.
type Service struct {
	version   string
	log       *logger.Logger
	appLog    *logging.ScopedLogger
	settings  *storage.SettingsStore
	changelog *changelogFetcher
	mu        sync.RWMutex
	cached    *UpdateInfo
	stop      chan struct{}
	done      chan struct{}

	// Guard against concurrent upgrades
	upgrading bool
}

// New creates a new updater service.
func New(version string, settings *storage.SettingsStore, log *logger.Logger, appLogger logging.AppLogger) *Service {
	s := &Service{
		version:  version,
		log:      log,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubUpdate),
		settings: settings,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	s.changelog = newChangelogFetcher(defaultChangelogURL, 10*time.Minute)
	return s
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

	s.appLog.Debug("check", "", "Checking for updates")

	ctx := context.Background()
	info := Check(ctx, s.version)

	s.mu.Lock()
	s.cached = info
	s.mu.Unlock()

	if info.Error != "" {
		s.log.Warn("Update check failed", map[string]interface{}{"error": info.Error})
		s.appLog.Warn("check", "", "Update check failed: "+info.Error)
	} else if info.Available {
		s.log.Info("Update available", map[string]interface{}{"latest": info.LatestVersion})
		s.appLog.Info("check", "", fmt.Sprintf("Update available: %s → %s", s.version, info.LatestVersion))
	} else {
		s.appLog.Debug("check", "", fmt.Sprintf("Up to date (%s)", s.version))
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

	// A user-forced refresh should also invalidate the changelog cache so
	// the next "Что нового" click hits the repo server for fresh content.
	s.changelog.Invalidate()

	s.mu.Lock()
	s.cached = info
	s.mu.Unlock()

	return info
}

// ApplyUpgrade downloads and installs the update from the entware repo.
// Returns error if upgrade is already in progress or no download URL cached.
func (s *Service) ApplyUpgrade(ctx context.Context) error {
	s.mu.Lock()
	if s.upgrading {
		s.mu.Unlock()
		return ErrUpgradeInProgress
	}

	var downloadURL string
	if s.cached != nil {
		downloadURL = s.cached.DownloadURL
	}
	s.upgrading = true
	s.mu.Unlock()

	if downloadURL == "" {
		return fmt.Errorf("no download URL available, run check first")
	}

	return Upgrade(ctx, downloadURL)
}

// GetChangelog fetches the monolithic CHANGELOG.md from the repo server,
// parses it, and returns the slice of entries strictly newer than fromVer
// and no newer than toVer. Result is sorted newest-first.
func (s *Service) GetChangelog(ctx context.Context, fromVer, toVer string) ([]Entry, error) {
	entries, err := s.changelog.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	return Slice(entries, fromVer, toVer), nil
}
