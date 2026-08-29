package updater

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func mustParseTime(t *testing.T, layout, value string) time.Time {
	t.Helper()
	tm, err := time.Parse(layout, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return tm
}

func TestAutoInstallDue_ClockNotYetValid(t *testing.T) {
	// 1970-clock guard: before NTP sync the router's clock reads ~1970,
	// which must never be treated as "the scheduled window arrived".
	now := time.Date(1970, 1, 1, 12, 0, 0, 0, time.UTC)
	if autoInstallDue(now, nil, 7, "05:00") {
		t.Fatal("expected false for year < 2024, got true")
	}
}

func TestAutoInstallDue_NilMarker_BeforeWindow(t *testing.T) {
	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	if autoInstallDue(now, nil, 7, "05:00") {
		t.Fatal("expected false before HH:MM with nil marker")
	}
}

func TestAutoInstallDue_NilMarker_AfterWindow(t *testing.T) {
	// No marker yet — due at the very next HH:MM window, no N-day wait.
	now := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	if !autoInstallDue(now, nil, 7, "05:00") {
		t.Fatal("expected true after HH:MM with nil marker")
	}
}

func TestAutoInstallDue_FreshMarker_NotDue(t *testing.T) {
	now := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	marker := &autoInstallMarker{LastAttemptAt: now.AddDate(0, 0, -1)} // yesterday
	if autoInstallDue(now, marker, 7, "05:00") {
		t.Fatal("expected false: last attempt only 1 day ago, interval is 7")
	}
}

func TestAutoInstallDue_StaleMarker_AfterWindow_Due(t *testing.T) {
	now := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	marker := &autoInstallMarker{LastAttemptAt: now.AddDate(0, 0, -8)}
	if !autoInstallDue(now, marker, 7, "05:00") {
		t.Fatal("expected true: last attempt 8 days ago, interval is 7, now after HH:MM")
	}
}

func TestAutoInstallDue_StaleMarker_BeforeWindow_NotDue(t *testing.T) {
	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	marker := &autoInstallMarker{LastAttemptAt: now.AddDate(0, 0, -8)}
	if autoInstallDue(now, marker, 7, "05:00") {
		t.Fatal("expected false: now is before today's HH:MM window")
	}
}

func TestAutoInstallDue_AntiDup_SameSlotNotFiredTwice(t *testing.T) {
	// Marker was stamped today, at/after the window — must not fire again
	// today even later in the day, regardless of interval.
	fired := time.Date(2026, 7, 17, 5, 5, 0, 0, time.UTC)
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	marker := &autoInstallMarker{LastAttemptAt: fired}
	if autoInstallDue(now, marker, 1, "05:00") {
		t.Fatal("expected false: already attempted in today's window")
	}
}

func TestAutoInstallDue_InvalidHHMM(t *testing.T) {
	now := time.Date(2026, 7, 17, 6, 0, 0, 0, time.UTC)
	if autoInstallDue(now, nil, 7, "not-a-time") {
		t.Fatal("expected false for unparsable HH:MM")
	}
}

func TestAutoInstallMarker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &autoInstallMarker{
		LastAttemptAt: mustParseTime(t, time.RFC3339, "2026-07-17T05:00:00Z").UTC(),
		FromVersion:   "2.12.0",
		ToVersion:     "2.12.1",
	}
	if err := writeAutoInstallMarker(dir, want); err != nil {
		t.Fatalf("writeAutoInstallMarker: %v", err)
	}
	got := readAutoInstallMarker(dir)
	if got == nil {
		t.Fatal("readAutoInstallMarker returned nil after successful write")
	}
	if !got.LastAttemptAt.Equal(want.LastAttemptAt) || got.FromVersion != want.FromVersion || got.ToVersion != want.ToVersion {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestAutoInstallMarker_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if got := readAutoInstallMarker(dir); got != nil {
		t.Fatalf("expected nil for missing marker file, got %+v", got)
	}
}

func TestAutoInstallMarker_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(autoInstallMarkerPath(dir), []byte("{not json"), 0644); err != nil {
		t.Fatalf("write corrupt marker: %v", err)
	}
	if got := readAutoInstallMarker(dir); got != nil {
		t.Fatalf("expected nil for corrupt marker file, got %+v", got)
	}
}

// --- fakeSingboxUpdater: exercises the slot / catch-up / retrospective logic. ---

type fakeSingboxUpdater struct {
	installed       bool
	updateAvailable bool
	current         string
	required        string
	updateErr       error
	updateCalls     int
	// versionAfterUpdate, if set, is what UpdateStatus reports as current
	// once Update has been called at least once (simulates a real swap).
	versionAfterUpdate string
}

func (f *fakeSingboxUpdater) UpdateStatus(ctx context.Context) (installed, updateAvailable bool, current, required string) {
	cur := f.current
	if f.updateCalls > 0 && f.versionAfterUpdate != "" {
		cur = f.versionAfterUpdate
	}
	return f.installed, f.updateAvailable, cur, f.required
}

func (f *fakeSingboxUpdater) Update(ctx context.Context) error {
	f.updateCalls++
	return f.updateErr
}

// failingDownloader simulates "no network" for CheckNow so the manager
// self-update path in runAutoInstallActions reliably falls through to the
// sing-box path in tests, without ever making a real HTTP request.
func failingDownloader() Downloader {
	return &fakeDownloader{
		readAllFn: func(context.Context, downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			return nil, downloader.ResponseMeta{}, errors.New("no network in test")
		},
	}
}

func newTestUpdateService(t *testing.T, su SingboxUpdater) *Service {
	t.Helper()
	dir := t.TempDir()
	dl := failingDownloader()
	return &Service{
		version:        "2.12.0",
		dataDir:        dir,
		singboxUpdater: su,
		downloader:     dl,
		changelog:      newChangelogFetcher(changelogURLForChannel(channelStable), 10*time.Minute, dl),
	}
}

func TestRunAutoInstallSlot_SingboxUpdateAvailable_WritesMarkerAndUpdates(t *testing.T) {
	su := &fakeSingboxUpdater{
		installed:          true,
		updateAvailable:    true,
		current:            "1.11.0",
		required:           "1.11.1",
		versionAfterUpdate: "1.11.1",
	}
	svc := newTestUpdateService(t, su)
	// No manager update available: CheckNow will hit the fake repo URL and
	// fail (no network in tests) — that's fine, info.Available stays false.
	svc.runAutoInstallSlotForced()

	if su.updateCalls != 1 {
		t.Fatalf("Update calls = %d, want 1", su.updateCalls)
	}
	marker := svc.readAutoInstallMarker()
	if marker == nil {
		t.Fatal("expected marker to be written")
	}
	if marker.FromVersion != "1.11.0" || marker.ToVersion != "1.11.1" {
		t.Fatalf("marker = %+v, want from=1.11.0 to=1.11.1", marker)
	}
	// A sing-box update never restarts this process, so unlike the manager
	// self-update marker, this one must already be Reported: the outcome was
	// journaled live in this same session — a later daemon restart within
	// freshWindow must not re-journal it (autoInstallRetrospective).
	if !marker.Reported {
		t.Fatalf("expected sing-box marker.Reported = true, got %+v", marker)
	}
}

func TestRunAutoInstallSlot_SingboxNoUpdateAvailable_StampsCheckedMarker(t *testing.T) {
	su := &fakeSingboxUpdater{installed: true, updateAvailable: false, current: "1.11.1", required: "1.11.1"}
	svc := &Service{
		version:        "2.12.0",
		dataDir:        t.TempDir(),
		singboxUpdater: su,
		// A downloader that reports a SUCCESSFUL check with nothing newer
		// (not a failure) — the checked-marker must only be stamped for a
		// genuine "nothing to do" outcome, not a failed check.
		downloader: managerUpdateDownloader(t, "2.12.0", nil),
	}
	svc.changelog = newChangelogFetcher(changelogURLForChannel(channelStable), 10*time.Minute, svc.downloader)
	svc.runAutoInstallSlotForced()

	if su.updateCalls != 0 {
		t.Fatalf("Update calls = %d, want 0", su.updateCalls)
	}
	// Nothing was available to install — the slot still stamps a
	// lightweight "checked" marker (empty From/ToVersion) so the 15-minute
	// ticker does not re-poll CheckNow/UpdateStatus on every tick until the
	// next scheduled window (see runAutoInstallActions step 3).
	marker := svc.readAutoInstallMarker()
	if marker == nil {
		t.Fatal("expected checked-marker to be stamped when nothing is available")
	}
	if marker.FromVersion != "" || marker.ToVersion != "" {
		t.Fatalf("expected empty From/ToVersion on checked-marker, got %+v", marker)
	}
	if marker.LastAttemptAt.IsZero() {
		t.Fatal("expected LastAttemptAt to be set on checked-marker")
	}
}

// TestRunAutoInstallSlot_NothingAvailable_SuppressesImmediateRepoll is the
// regression test for the steady-state 15-min repoll bug: a slot that ran to
// completion with nothing to install must stamp a marker that makes the very
// next autoInstallDue check (same slot) return false.
func TestRunAutoInstallSlot_NothingAvailable_SuppressesImmediateRepoll(t *testing.T) {
	su := &fakeSingboxUpdater{installed: true, updateAvailable: false, current: "1.11.1", required: "1.11.1"}
	svc := &Service{
		version:        "2.12.0",
		dataDir:        t.TempDir(),
		singboxUpdater: su,
		// Genuine "checked, nothing available" — not a failed check.
		downloader: managerUpdateDownloader(t, "2.12.0", nil),
	}
	svc.changelog = newChangelogFetcher(changelogURLForChannel(channelStable), 10*time.Minute, svc.downloader)
	svc.runAutoInstallSlotForced()

	marker := svc.readAutoInstallMarker()
	if marker == nil {
		t.Fatal("expected checked-marker to be stamped")
	}
	// hhmm="00:00" makes today's target window trivially <= any stamp time
	// on the same day, isolating the anti-dup gate (marker.LastAttemptAt
	// not before todayTarget) from time-of-day flakiness.
	if autoInstallDue(marker.LastAttemptAt, marker, 7, "00:00") {
		t.Fatal("expected autoInstallDue=false immediately after a checked-marker stamp (same slot must not repoll)")
	}
}

// TestRunAutoInstallSlot_ManagerCheckFailed_DoesNotStampCheckedMarker is the
// regression test for the outage bug: a failed CheckNow (info.Error set, e.g.
// the entware repo was unreachable) must NOT be treated as "checked, nothing
// available" — stamping the checked-marker in that case would defer an
// actually-available update by up to intervalDays if the outage hit the
// scheduled window. No marker means the next tick retries.
func TestRunAutoInstallSlot_ManagerCheckFailed_DoesNotStampCheckedMarker(t *testing.T) {
	svc := newTestUpdateService(t, nil) // failingDownloader, no sing-box updater
	svc.runAutoInstallSlotForced()

	if marker := svc.readAutoInstallMarker(); marker != nil {
		t.Fatalf("expected no marker stamped when the manager check itself failed, got %+v", marker)
	}
	// No marker at all => autoInstallDue still fires on the next tick.
	if !autoInstallDue(time.Now(), svc.readAutoInstallMarker(), 7, "00:00") {
		t.Fatal("expected autoInstallDue=true so the next tick retries the failed check")
	}
}

func TestRunAutoInstallSlot_SingboxBusy_SkipsWithoutStamp(t *testing.T) {
	su := &fakeSingboxUpdater{
		installed:       true,
		updateAvailable: true,
		current:         "1.11.0",
		required:        "1.11.1",
		updateErr:       ErrSingboxInstallInProgress,
	}
	svc := newTestUpdateService(t, su)
	svc.runAutoInstallSlotForced()

	if su.updateCalls != 1 {
		t.Fatalf("Update calls = %d, want 1", su.updateCalls)
	}
	if marker := svc.readAutoInstallMarker(); marker != nil {
		t.Fatalf("expected no marker stamped when install already in progress, got %+v", marker)
	}
}

func TestRunAutoInstallSlot_SingboxNoOp_NotJournaledAsUpdate(t *testing.T) {
	// Update() returns nil but the version never actually changed
	// (no-space / MatchesRequired no-op case) — must not be reported as
	// a successful update, but the attempt is still stamped.
	su := &fakeSingboxUpdater{
		installed:       true,
		updateAvailable: true,
		current:         "1.11.0",
		required:        "1.11.1",
		// versionAfterUpdate left empty => UpdateStatus keeps reporting "1.11.0"
	}
	svc := newTestUpdateService(t, su)
	svc.runAutoInstallSlotForced()

	marker := svc.readAutoInstallMarker()
	if marker == nil {
		t.Fatal("expected marker to be written even on no-op")
	}
	if marker.ToVersion != "1.11.1" {
		t.Fatalf("marker.ToVersion = %q, want required version stamped as attempted", marker.ToVersion)
	}
}

// --- manager (awg-manager self-update) branch of runAutoInstallActions ---
//
// All the tests above use failingDownloader, so info.Available is always
// false and the manager branch always falls through to sing-box. These two
// exercise the manager branch itself with a downloader stubbed to report an
// available update via the real Packages.gz parsing path (checkWithDownloader
// / CheckNow are not mockable through an interface, so the fake plugs in one
// level down at the Downloader).

func managerUpdateDownloader(t *testing.T, newVersion string, downloadFileFn func(context.Context, downloader.FileRequest) (downloader.FileResult, error)) *fakeDownloader {
	t.Helper()
	arch := archSuffix()
	ipkName := "awg-manager_" + newVersion + "_" + arch + "-kn.ipk"
	packages := "Package: awg-manager\nVersion: " + newVersion + "\nFilename: " + ipkName + "\n"
	return &fakeDownloader{
		readAllFn: func(_ context.Context, _ downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			return gzipBytes(t, packages), downloader.ResponseMeta{}, nil
		},
		downloadFileFn: downloadFileFn,
	}
}

func TestRunAutoInstallSlot_ManagerUpdateAvailable_StampsMarkerBeforeApply(t *testing.T) {
	dl := managerUpdateDownloader(t, "9.9.9", func(context.Context, downloader.FileRequest) (downloader.FileResult, error) {
		// Force ApplyUpgrade to fail its IPK download so the test never
		// shells out to a real detached "opkg install". If the marker is
		// still stamped with the correct from/to versions despite the
		// downstream failure, that proves the write happens before (and
		// independent of) ApplyUpgrade's own outcome.
		return downloader.FileResult{}, errors.New("no download in test")
	})
	svc := &Service{
		version:    "2.12.0",
		dataDir:    t.TempDir(),
		downloader: dl,
		changelog:  newChangelogFetcher(changelogURLForChannel(channelStable), 10*time.Minute, dl),
	}

	svc.runAutoInstallSlotForced()

	marker := svc.readAutoInstallMarker()
	if marker == nil {
		t.Fatal("expected marker to be stamped for the manager self-update attempt")
	}
	if marker.FromVersion != "2.12.0" || marker.ToVersion != "9.9.9" {
		t.Fatalf("marker = %+v, want from=2.12.0 to=9.9.9", marker)
	}
	// Unlike the sing-box paths, the manager self-update marker must NOT be
	// Reported: ApplyUpgrade's detached "opkg install" kills this process a
	// couple of seconds later, so the outcome can only ever be journaled
	// retrospectively on the next daemon restart.
	if marker.Reported {
		t.Fatalf("expected manager marker.Reported = false, got %+v", marker)
	}
}

func TestRunAutoInstallSlot_ManagerBusy_SkipsWithoutStamp(t *testing.T) {
	dl := managerUpdateDownloader(t, "9.9.9", nil)
	svc := &Service{
		version:    "2.12.0",
		dataDir:    t.TempDir(),
		downloader: dl,
		changelog:  newChangelogFetcher(changelogURLForChannel(channelStable), 10*time.Minute, dl),
		upgrading:  true, // simulates a manual apply already running
	}

	svc.runAutoInstallSlotForced()

	if marker := svc.readAutoInstallMarker(); marker != nil {
		t.Fatalf("expected no marker when a manual apply is already in progress, got %+v", marker)
	}
}

func TestAutoInstallStartupCatchUp_UpdatesOnceOnMismatch(t *testing.T) {
	su := &fakeSingboxUpdater{
		installed:          true,
		updateAvailable:    true,
		current:            "1.11.0",
		required:           "1.11.1",
		versionAfterUpdate: "1.11.1",
	}
	svc := newTestUpdateService(t, su)
	svc.settings = newAutoInstallTestSettings(t, true)

	svc.autoInstallStartupCatchUp(context.Background())

	if su.updateCalls != 1 {
		t.Fatalf("Update calls = %d, want exactly 1", su.updateCalls)
	}
	marker := svc.readAutoInstallMarker()
	if marker == nil {
		t.Fatal("expected marker to be written by the startup catch-up")
	}
	// Journaled live above in this same session — must be Reported so a
	// later daemon restart within freshWindow does not re-journal it.
	if !marker.Reported {
		t.Fatalf("expected startup catch-up marker.Reported = true, got %+v", marker)
	}
}

func TestAutoInstallStartupCatchUp_DisabledDoesNothing(t *testing.T) {
	su := &fakeSingboxUpdater{installed: true, updateAvailable: true, current: "1.11.0", required: "1.11.1"}
	svc := newTestUpdateService(t, su)
	svc.settings = newAutoInstallTestSettings(t, false)

	svc.autoInstallStartupCatchUp(context.Background())

	if su.updateCalls != 0 {
		t.Fatalf("Update calls = %d, want 0 when auto-install disabled", su.updateCalls)
	}
}

func TestAutoInstallRetrospective_SuccessReportedOnce(t *testing.T) {
	svc := newTestUpdateService(t, nil)
	svc.settings = newAutoInstallTestSettings(t, true)
	marker := &autoInstallMarker{
		LastAttemptAt: time.Now().Add(-time.Hour),
		FromVersion:   "2.11.9",
		ToVersion:     "2.12.0", // matches svc.version
	}
	if err := writeAutoInstallMarker(svc.dataDir, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	svc.autoInstallRetrospective()

	got := svc.readAutoInstallMarker()
	if got == nil || !got.Reported {
		t.Fatalf("expected marker.Reported = true after retrospective, got %+v", got)
	}

	// Second run must not re-log (Reported already true) — no panic/second
	// write side effect beyond idempotence.
	svc.autoInstallRetrospective()
	got2 := svc.readAutoInstallMarker()
	if got2 == nil || !got2.Reported {
		t.Fatalf("expected marker to remain reported, got %+v", got2)
	}
}

func TestAutoInstallRetrospective_MismatchRecentWarns(t *testing.T) {
	svc := newTestUpdateService(t, nil)
	svc.settings = newAutoInstallTestSettings(t, true)
	marker := &autoInstallMarker{
		LastAttemptAt: time.Now().Add(-time.Hour),
		FromVersion:   "2.11.9",
		ToVersion:     "2.99.0", // does not match svc.version, recent attempt
	}
	if err := writeAutoInstallMarker(svc.dataDir, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	svc.autoInstallRetrospective()

	got := svc.readAutoInstallMarker()
	if got == nil || !got.Reported {
		t.Fatalf("expected marker.Reported = true after mismatch retrospective, got %+v", got)
	}
}

func TestAutoInstallRetrospective_StaleMarker_NotJournaled(t *testing.T) {
	// intervalDays=7 => freshWindow = 14 days. An attempt older than that is
	// neither a fresh success (age < freshWindow) nor a recent mismatch
	// (age < 24h) — it must be silently ignored, not journaled either way.
	svc := newTestUpdateService(t, nil)
	svc.settings = newAutoInstallTestSettings(t, true)
	marker := &autoInstallMarker{
		LastAttemptAt: time.Now().Add(-20 * 24 * time.Hour),
		FromVersion:   "2.11.9",
		ToVersion:     "2.99.0", // deliberately does not match svc.version
	}
	if err := writeAutoInstallMarker(svc.dataDir, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	svc.autoInstallRetrospective()

	got := svc.readAutoInstallMarker()
	if got == nil {
		t.Fatal("expected marker to still exist")
	}
	if got.Reported {
		t.Fatalf("expected Reported to remain false for a stale marker (neither branch matches), got %+v", got)
	}
}

// TestAutoInstallRetrospective_SingboxMarker_NotReJournaled is the regression
// test for the sing-box marker fix: a sing-box update marker written with
// Reported: true (as the slot / startup catch-up now do) must make
// autoInstallRetrospective a no-op on the next daemon restart, even though
// ToVersion matches the live sing-box version and the attempt is well within
// freshWindow — without the fix this would re-log "автообновление успешно"
// on every restart within the window.
func TestAutoInstallRetrospective_SingboxMarker_NotReJournaled(t *testing.T) {
	su := &fakeSingboxUpdater{current: "1.11.1"}
	svc := newTestUpdateService(t, su)
	recorder := &recordingAppLogger{}
	svc.appLog = logging.NewScopedLogger(recorder, logging.GroupSystem, logging.SubUpdate)
	svc.settings = newAutoInstallTestSettings(t, true)
	marker := &autoInstallMarker{
		LastAttemptAt: time.Now().Add(-time.Hour),
		FromVersion:   "1.11.0",
		ToVersion:     "1.11.1", // matches su.current, would otherwise match+journal
		Reported:      true,
	}
	if err := writeAutoInstallMarker(svc.dataDir, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	svc.autoInstallRetrospective()

	if len(recorder.entries) != 0 {
		t.Fatalf("expected no journal entries for an already-Reported marker, got %+v", recorder.entries)
	}
}

// TestNextAutoInstallAt_CheckedMarker_LastIsZero is the regression test for
// the UI bug: a checked-marker (empty ToVersion — "checked, nothing to
// install") must not be reported as "last auto-install" in the settings API,
// or the UI shows «Последняя автоустановка: <date>» when nothing was ever
// actually installed.
func TestNextAutoInstallAt_CheckedMarker_LastIsZero(t *testing.T) {
	svc := newTestUpdateService(t, nil)
	svc.settings = newAutoInstallTestSettings(t, true)
	marker := &autoInstallMarker{LastAttemptAt: time.Now().Add(-time.Hour)} // empty From/ToVersion
	if err := writeAutoInstallMarker(svc.dataDir, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	_, last := svc.NextAutoInstallAt()

	if !last.IsZero() {
		t.Fatalf("expected last to be zero for a checked-marker, got %v", last)
	}
}

// TestNextAutoInstallAt_RealInstallMarker_LastIsSet is the sanity companion:
// a marker that recorded an actual install (ToVersion set) must still report
// its LastAttemptAt as "last".
func TestNextAutoInstallAt_RealInstallMarker_LastIsSet(t *testing.T) {
	svc := newTestUpdateService(t, nil)
	svc.settings = newAutoInstallTestSettings(t, true)
	attemptedAt := time.Now().Add(-time.Hour)
	marker := &autoInstallMarker{LastAttemptAt: attemptedAt, FromVersion: "2.11.9", ToVersion: "2.12.0"}
	if err := writeAutoInstallMarker(svc.dataDir, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	_, last := svc.NextAutoInstallAt()

	if !last.Equal(attemptedAt) {
		t.Fatalf("expected last = %v, got %v", attemptedAt, last)
	}
}

func newAutoInstallTestSettings(t *testing.T, enabled bool) *storage.SettingsStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.Updates.AutoInstallEnabled = enabled
	settings.Updates.AutoInstallIntervalDays = 7
	settings.Updates.AutoInstallTime = "05:00"
	if err := store.Update(func(cur *storage.Settings) error { *cur = *settings; return nil }); err != nil {
		t.Fatal(err)
	}
	return store
}

// runAutoInstallSlotForced runs the slot action unconditionally, bypassing
// the autoInstallDue gate (settings not wired in these unit tests) so the
// action sequencing itself can be exercised in isolation.
func (s *Service) runAutoInstallSlotForced() {
	s.runAutoInstallActions(context.Background())
}
