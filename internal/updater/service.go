package updater

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

const checkInterval = 24 * time.Hour

// Service manages periodic update checks and caches results.
type Service struct {
	version    string
	appLog     *logging.ScopedLogger
	settings   *storage.SettingsStore
	downloader Downloader
	changelog  *changelogFetcher
	mu         sync.RWMutex
	cached     *UpdateInfo
	stop       chan struct{}
	done       chan struct{}

	// Guard against concurrent upgrades
	upgrading bool

	// dataDir is where the auto-install marker file lives.
	dataDir string
	// singboxUpdater lets the auto-install scheduler drive the managed
	// sing-box binary. nil when not wired (e.g. plain unit tests) — the
	// sing-box auto-install path is then simply skipped.
	singboxUpdater SingboxUpdater
	// features — источник флагов для анонимной статистики (stats.go); под mu.
	features func() Features
	// instanceID — ID установки, прочитанный или заведённый первой проверкой; под mu.
	instanceID string
	// snapshot снимает каталог данных перед установкой обновления; spare —
	// сколько места оставить под установку пакета. В тестах подменяется.
	snapshot func(spare int64) (backup.Snapshot, error)
	// bus — SSE-шина: список снимков меняется без участия страницы
	// (автообновление, срок жизни). nil в тестах.
	bus *events.Bus
}

// New creates a new updater service. dataDir is used for the auto-install
// marker file; singboxUpdater may be nil if sing-box auto-install is not
// wired (the scheduler then only handles awg-manager self-updates).
func New(version string, settings *storage.SettingsStore, appLogger logging.AppLogger, dataDir string, singboxUpdater SingboxUpdater) *Service {
	s := &Service{
		version:        version,
		appLog:         logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubUpdate),
		settings:       settings,
		dataDir:        dataDir,
		singboxUpdater: singboxUpdater,
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	s.snapshot = func(spare int64) (backup.Snapshot, error) {
		return backup.TakeUpdateSnapshot(dataDir, version, time.Now(), spare)
	}
	s.downloader = newLoggingDownloader(newDefaultDownloader(), s.appLog)
	s.changelog = newChangelogFetcher(changelogURLForChannel(channelStable), 10*time.Minute, s.downloader)
	return s
}

// channel returns the configured update channel, defaulting to stable.
func (s *Service) channel() string {
	if s.settings != nil {
		if st, err := s.settings.Get(); err == nil && st.Updates.Channel != "" {
			return st.Updates.Channel
		}
	}
	return channelStable
}

func (s *Service) SetDownloader(dl Downloader) {
	if dl == nil {
		dl = newDefaultDownloader()
	}
	s.downloader = newLoggingDownloader(dl, s.appLog)
	if s.changelog != nil {
		s.changelog.downloader = s.downloader
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

	// Report the outcome of any auto-install attempt made before this
	// process started (the in-memory app log does not survive a restart).
	s.autoInstallRetrospective()
	s.pruneExpiredSnapshots()

	s.doCheck()

	// One-shot catch-up for a managed sing-box binary that fell behind
	// while auto-install was enabled (e.g. it was installed after the
	// last scheduled slot, or awgm was down at the scheduled time).
	s.autoInstallStartupCatchUp(context.Background())

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	autoTicker := time.NewTicker(autoInstallTick)
	defer autoTicker.Stop()

	for {
		select {
		case <-ticker.C:
			s.doCheck()
		case <-autoTicker.C:
			s.runAutoInstallSlot()
			s.pruneExpiredSnapshots()
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
	ch := s.channel()
	s.changelog.SetURL(changelogURLForChannel(ch))
	info := checkWithDownloader(ctx, s.version, ch, s.downloader, s.statsHeaders())

	s.mu.Lock()
	s.cached = info
	s.mu.Unlock()

	if info.Error != "" {
		s.appLog.Warn("check", "", "Update check failed: "+info.Error)
	} else if info.Available {
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

	ch := s.channel()
	s.changelog.SetURL(changelogURLForChannel(ch))
	info := checkWithDownloader(ctx, s.version, ch, s.downloader, s.statsHeaders())

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
	// Восстановление и opkg с его postinst боролись бы за одни и те же данные.
	if backup.Restoring() {
		return ErrRestoreInProgress
	}
	s.mu.Lock()
	if s.upgrading {
		s.mu.Unlock()
		return ErrUpgradeInProgress
	}

	var downloadURL, wantSHA256 string
	if s.cached != nil {
		downloadURL = s.cached.DownloadURL
		wantSHA256 = s.cached.SHA256
	}
	if downloadURL == "" {
		s.mu.Unlock()
		return fmt.Errorf("no download URL available, run check first")
	}
	s.upgrading = true
	s.mu.Unlock()
	if err := upgradeWithDownloader(ctx, downloadURL, wantSHA256, s.downloader, s.snapshotBeforeInstall); err != nil {
		s.mu.Lock()
		s.upgrading = false
		s.mu.Unlock()
		return err
	}
	// On success opkg restarts this daemon, so the flag never needs manual
	// clearing. But if the detached install fails silently (opkg lock, disk
	// full), the flag would otherwise stay set forever and every later apply
	// would return ErrUpgradeInProgress until a manual restart. Give the
	// install a generous window, then re-allow retries.
	time.AfterFunc(10*time.Minute, func() {
		s.mu.Lock()
		s.upgrading = false
		s.mu.Unlock()
	})
	return nil
}

// SetEventBus подключает SSE-шину для подсказки «список снимков изменился».
func (s *Service) SetEventBus(bus *events.Bus) { s.bus = bus }

// IsUpgrading сообщает, идёт ли установка обновления: восстановление данных
// в это время запрещено.
func (s *Service) IsUpgrading() bool { return s.isUpgrading() }

// snapshotSpare — сколько места оставить после снимка под установку пакета
// ipkPath. Оценка: opkg распаковывает пакет на тот же раздел и до замены
// держит старые файлы рядом с новыми, а сжатый бинарь Go распаковывается
// примерно в два-три раза. Не меньше backup.MinSnapshotSpare.
func snapshotSpare(ipkPath string) int64 {
	info, err := os.Stat(ipkPath)
	if err != nil {
		return backup.MinSnapshotSpare
	}
	return max(3*info.Size(), backup.MinSnapshotSpare)
}

// pruneExpiredSnapshots удаляет снимки старше backup.SnapshotTTL.
func (s *Service) pruneExpiredSnapshots() {
	if s.dataDir == "" {
		return
	}
	if n := backup.PruneExpiredSnapshots(s.dataDir, time.Now()); n > 0 {
		s.appLog.Info("snapshot", "", fmt.Sprintf("удалено просроченных снимков перед обновлением: %d", n))
		s.bus.PublishInvalidated(events.ResourceUpdateSnapshots, "expired")
	}
}

// snapshotBeforeInstall снимает каталог данных перед установкой пакета
// ipkPath. Неудача не останавливает обновление: снимок — страховка, а не
// условие установки.
//
// Дочерние процессы не останавливаются, в отличие от ручного экспорта:
// обновление по замыслу не трогает туннели (prerm при upgrade останавливает
// только демон), и остановка ради снимка добавила бы обрыв, которого само
// обновление не делает. Файлы каталога данных пишутся атомарно.
func (s *Service) snapshotBeforeInstall(ipkPath string) {
	if s.snapshot == nil {
		return
	}
	if s.settings != nil {
		if st, err := s.settings.Get(); err == nil && st.Updates.SnapshotDisabled {
			s.appLog.Info("snapshot", "", "снимок настроек перед обновлением отключён в настройках")
			return
		}
	}
	snap, err := s.snapshot(snapshotSpare(ipkPath))
	if err != nil {
		s.appLog.Warn("snapshot", "", "снимок настроек перед обновлением не сохранён: "+err.Error())
		return
	}
	s.appLog.Info("snapshot", snap.ID, fmt.Sprintf("снимок настроек перед обновлением сохранён (%d КБ)", snap.Size>>10))
	s.bus.PublishInvalidated(events.ResourceUpdateSnapshots, "created")
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

// GetChangelogMinor returns all CHANGELOG entries for the same major.minor
// as version up to and including that release (e.g. 2.11.0–2.11.2 on 2.11.2+r70).
func (s *Service) GetChangelogMinor(ctx context.Context, version string) ([]Entry, error) {
	entries, err := s.changelog.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	return MinorLine(entries, version), nil
}
