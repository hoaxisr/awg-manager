package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// autoInstallTick is how often the scheduler goroutine checks whether the
// configured auto-install slot has arrived. Mirrors the dnsroute scheduler's
// unified 15-minute cadence (internal/dnsroute/scheduler.go).
const autoInstallTick = 15 * time.Minute

// autoInstallMarkerFile is the marker filename inside dataDir.
const autoInstallMarkerFile = "update-autoinstall.json"

// SingboxUpdater lets the auto-install scheduler drive the managed sing-box
// binary without the updater package importing internal/singbox (avoids an
// import-direction inversion; implemented by a thin adapter in wiring around
// *singbox.Operator). Update must return an error satisfying
// errors.Is(err, ErrSingboxInstallInProgress) when another install/update is
// already running, so the scheduler can skip the slot without stamping the
// marker.
type SingboxUpdater interface {
	UpdateStatus(ctx context.Context) (installed, updateAvailable bool, current, required string)
	Update(ctx context.Context) error
}

// ErrSingboxInstallInProgress is the sentinel a SingboxUpdater.Update
// implementation must return (via errors.Is) when a sing-box install/update
// is already in flight — e.g. a manual update triggered from the UI.
var ErrSingboxInstallInProgress = errors.New("sing-box install already in progress")

// autoInstallMarker records the last scheduled auto-install attempt (either
// the awg-manager self-update or the managed sing-box binary — the
// scheduler only ever attempts one of the two per slot). Persisted at
// <dataDir>/update-autoinstall.json; missing or corrupt is treated as "no
// prior attempt" (first slot fires at the next HH:MM window, no N-day
// wait).
type autoInstallMarker struct {
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	FromVersion   string    `json:"fromVersion"`
	ToVersion     string    `json:"toVersion"`
	// Reported marks that the retrospective startup journal already logged
	// the outcome of this attempt once. Without it, every daemon restart
	// within the "fresh" window would re-log the same success/warn line.
	Reported bool `json:"reported,omitempty"`
}

// autoInstallDue is the pure slot-scheduling decision, kept side-effect
// free so it can be unit tested with fixed clocks.
//
//   - now.Year() < 2024 never fires — guards against the router's clock
//     still reading ~1970 before NTP has synced (a nil/zero marker plus an
//     unvalidated clock would otherwise look like "long overdue" and fire
//     immediately on cold boot).
//   - marker == nil (no prior attempt) fires at the very next HH:MM window,
//     without waiting intervalDays.
//   - otherwise fires once now is at/after today's HH:MM AND at least
//     intervalDays have elapsed since the last attempt.
func autoInstallDue(now time.Time, marker *autoInstallMarker, intervalDays int, hhmm string) bool {
	if now.Year() < 2024 {
		return false
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false
	}
	todayTarget := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if now.Before(todayTarget) {
		return false
	}
	if marker == nil {
		return true
	}
	// Anti-dup: already attempted in today's window — at most one attempt
	// per slot, regardless of interval.
	if !marker.LastAttemptAt.Before(todayTarget) {
		return false
	}
	if intervalDays < 1 {
		intervalDays = 1
	}
	interval := time.Duration(intervalDays) * 24 * time.Hour
	return now.Sub(marker.LastAttemptAt) >= interval
}

func autoInstallMarkerPath(dataDir string) string {
	return filepath.Join(dataDir, autoInstallMarkerFile)
}

// readAutoInstallMarker returns nil on a missing or corrupt marker file —
// both are treated identically to "no prior attempt" by autoInstallDue.
func readAutoInstallMarker(dataDir string) *autoInstallMarker {
	data, err := os.ReadFile(autoInstallMarkerPath(dataDir))
	if err != nil {
		return nil
	}
	var m autoInstallMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

func writeAutoInstallMarker(dataDir string, m *autoInstallMarker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(autoInstallMarkerPath(dataDir), data)
}

func (s *Service) readAutoInstallMarker() *autoInstallMarker {
	return readAutoInstallMarker(s.dataDir)
}

func (s *Service) writeAutoInstallMarker(m *autoInstallMarker) {
	if err := writeAutoInstallMarker(s.dataDir, m); err != nil {
		s.appLog.Warn("auto-install", "", "не удалось записать маркер: "+err.Error())
	}
}

// autoInstallSettings reads the current schedule from settings, applying
// the same defaults as the settings migration (V31) when a field is
// missing or invalid — this only affects the in-memory decision, it never
// writes settings.json (the demon is not the settings writer).
func (s *Service) autoInstallSettings() (enabled bool, intervalDays int, hhmm string) {
	intervalDays, hhmm = 7, "05:00"
	if s.settings == nil {
		return false, intervalDays, hhmm
	}
	st, err := s.settings.Get()
	if err != nil {
		return false, intervalDays, hhmm
	}
	if st.Updates.AutoInstallIntervalDays >= 1 {
		intervalDays = st.Updates.AutoInstallIntervalDays
	}
	if st.Updates.AutoInstallTime != "" {
		hhmm = st.Updates.AutoInstallTime
	}
	return st.Updates.AutoInstallEnabled, intervalDays, hhmm
}

// isUpgrading peeks the ApplyUpgrade busy flag without side effects, so the
// auto-install slot can decide to skip WITHOUT stamping the marker when a
// manual upgrade is already running (ApplyUpgrade itself can only report
// "busy" by way of actually being called, which would be too late to skip
// the pre-apply journal/marker write).
func (s *Service) isUpgrading() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.upgrading
}

// runAutoInstallSlot is the 15-minute tick handler: gates on settings and
// autoInstallDue, then delegates to runAutoInstallActions.
func (s *Service) runAutoInstallSlot() {
	enabled, intervalDays, hhmm := s.autoInstallSettings()
	if !enabled {
		return
	}
	if !autoInstallDue(time.Now(), s.readAutoInstallMarker(), intervalDays, hhmm) {
		return
	}
	s.runAutoInstallActions(context.Background())
}

// runAutoInstallActions performs the actual slot work: prefer an
// awg-manager self-update; only if none is available, fall back to the
// managed sing-box binary. At most one of the two is attempted per slot.
func (s *Service) runAutoInstallActions(ctx context.Context) {
	// 1. awg-manager self-update.
	info := s.CheckNow(ctx)
	if info != nil && info.Available && info.LatestVersion != "" {
		if s.isUpgrading() {
			// A manual apply is already running — retry next tick, do not
			// stamp the marker (no attempt was actually made).
			return
		}
		s.appLog.Info("auto-install", "", fmt.Sprintf("автообновление: awg-manager %s→%s", s.version, info.LatestVersion))
		// Stamped BEFORE calling ApplyUpgrade: on success ApplyUpgrade's
		// detached "opkg install" kills this process a couple of seconds
		// later, and the in-memory app log does not survive that restart.
		s.writeAutoInstallMarker(&autoInstallMarker{
			LastAttemptAt: time.Now(),
			FromVersion:   s.version,
			ToVersion:     info.LatestVersion,
		})
		if err := s.ApplyUpgrade(ctx); err != nil && !errors.Is(err, ErrUpgradeInProgress) {
			s.appLog.Warn("auto-install", "", "ApplyUpgrade: "+err.Error())
		}
		return
	}

	// 2. Managed sing-box binary.
	if s.singboxUpdater != nil {
		installed, updateAvailable, cur, req := s.singboxUpdater.UpdateStatus(ctx)
		if installed && updateAvailable {
			err := s.singboxUpdater.Update(ctx)
			if errors.Is(err, ErrSingboxInstallInProgress) {
				// A manual install/update is already running — skip without
				// stamping the marker.
				return
			}
			s.writeAutoInstallMarker(&autoInstallMarker{
				LastAttemptAt: time.Now(),
				FromVersion:   cur,
				ToVersion:     req,
				// A sing-box update does not restart this process, so the
				// outcome is already journaled live above (or the Warn
				// below) — mark it Reported so autoInstallRetrospective on
				// the next daemon restart does not re-log it.
				Reported: true,
			})
			if err != nil {
				s.appLog.Warn("auto-install", "", "sing-box Update: "+err.Error())
				return
			}
			_, _, newCur, _ := s.singboxUpdater.UpdateStatus(ctx)
			if newCur != "" && newCur != cur {
				s.appLog.Info("auto-install", "", fmt.Sprintf("автообновление sing-box: %s→%s", cur, newCur))
			} else {
				// Update() returned nil but the version did not actually
				// change (it already matched — Operator.Update no-ops
				// silently). Not a successful update.
				s.appLog.Warn("auto-install", "", fmt.Sprintf("автообновление sing-box: без изменений (%s)", cur))
			}
			return
		}
	}

	// 3. Nothing to install, and neither path was busy — the slot ran to
	// completion with no work to do. Stamp a lightweight "checked" marker
	// (empty From/ToVersion) so autoInstallDue's anti-dup + interval gate
	// (both keyed on LastAttemptAt) suppress re-checking on every 15-minute
	// tick until the next scheduled window. autoInstallRetrospective
	// ignores markers with an empty ToVersion, so this never gets journaled
	// as a real attempt.
	//
	// Exception: if the manager CheckNow itself failed (info.Error set —
	// e.g. the entware repo was unreachable), do NOT stamp — a failed check
	// is not "checked, nothing available", and stamping would defer an
	// actually-available update by up to intervalDays if the outage hit the
	// scheduled window. The next 15-minute tick retries instead.
	if info != nil && info.Error != "" {
		return
	}
	s.writeAutoInstallMarker(&autoInstallMarker{LastAttemptAt: time.Now()})
}

// autoInstallStartupCatchUp runs once per process, a few minutes after
// boot, to bring a managed sing-box binary that fell behind back in sync —
// independent of the daily HH:MM window (e.g. it was installed after the
// last scheduled slot ran, or awgm was down at the scheduled time).
func (s *Service) autoInstallStartupCatchUp(ctx context.Context) {
	if s.singboxUpdater == nil {
		return
	}
	enabled, _, _ := s.autoInstallSettings()
	if !enabled {
		return
	}
	installed, updateAvailable, cur, _ := s.singboxUpdater.UpdateStatus(ctx)
	if !installed || !updateAvailable {
		return
	}
	err := s.singboxUpdater.Update(ctx)
	if errors.Is(err, ErrSingboxInstallInProgress) {
		return
	}
	if err != nil {
		s.appLog.Warn("auto-install", "", "догон при старте sing-box Update: "+err.Error())
		return
	}
	_, _, newCur, _ := s.singboxUpdater.UpdateStatus(ctx)
	if newCur != "" && newCur != cur {
		s.appLog.Info("auto-install", "", fmt.Sprintf("автообновление sing-box (догон при старте): %s→%s", cur, newCur))
		s.writeAutoInstallMarker(&autoInstallMarker{
			LastAttemptAt: time.Now(),
			FromVersion:   cur,
			ToVersion:     newCur,
			// Already journaled just above in this same session — mark
			// Reported so autoInstallRetrospective on a later restart does
			// not re-log it (sing-box updates don't restart this process).
			Reported: true,
		})
	}
}

// autoInstallRetrospective reports, once per marker, the outcome of the
// last auto-install attempt recorded before this process started. The
// in-memory app log does not survive a restart, so this is the only way a
// self-update's own success ever reaches the visible journal.
func (s *Service) autoInstallRetrospective() {
	marker := s.readAutoInstallMarker()
	if marker == nil || marker.ToVersion == "" || marker.Reported {
		return
	}
	age := time.Since(marker.LastAttemptAt)
	if age < 0 {
		return
	}
	_, intervalDays, _ := s.autoInstallSettings()
	freshWindow := time.Duration(intervalDays) * 2 * 24 * time.Hour

	matched := marker.ToVersion == s.version
	label := "awg-manager"
	if !matched && s.singboxUpdater != nil {
		_, _, cur, _ := s.singboxUpdater.UpdateStatus(context.Background())
		if cur != "" && cur == marker.ToVersion {
			matched = true
			label = "sing-box"
		}
	}

	switch {
	case matched && age < freshWindow:
		s.appLog.Info("auto-install", "", fmt.Sprintf("автообновление успешно: %s %s→%s", label, marker.FromVersion, marker.ToVersion))
		marker.Reported = true
		s.writeAutoInstallMarker(marker)
	case !matched && age < 24*time.Hour:
		s.appLog.Warn("auto-install", "", fmt.Sprintf("автообновление не завершилось: %s→%s", marker.FromVersion, marker.ToVersion))
		marker.Reported = true
		s.writeAutoInstallMarker(marker)
	}
}

// nextDailyWindow returns the next occurrence (today if still ahead, else
// tomorrow) of clock t.
func nextDailyWindow(now, t time.Time) time.Time {
	target := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !now.Before(target) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// NextAutoInstallAt reports the next scheduled auto-install window and the
// last attempt timestamp, for the settings API (task #559-4). Returns a
// zero "next" when auto-install is disabled or the configured time is
// unparsable.
func (s *Service) NextAutoInstallAt() (next, last time.Time) {
	marker := s.readAutoInstallMarker()
	if marker != nil && marker.ToVersion != "" {
		last = marker.LastAttemptAt
	}
	enabled, intervalDays, hhmm := s.autoInstallSettings()
	if !enabled {
		return time.Time{}, last
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, last
	}
	now := time.Now()
	if marker == nil {
		return nextDailyWindow(now, t), last
	}
	base := time.Date(marker.LastAttemptAt.Year(), marker.LastAttemptAt.Month(), marker.LastAttemptAt.Day(), t.Hour(), t.Minute(), 0, 0, marker.LastAttemptAt.Location())
	next = base.AddDate(0, 0, intervalDays)
	if next.Before(now) {
		// Overdue — the actual next opportunity is the nearest future
		// window (the slot will fire on the very next tick).
		next = nextDailyWindow(now, t)
	}
	return next, last
}
