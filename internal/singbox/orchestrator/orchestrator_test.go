package orchestrator

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil) // nil ProcessController — Save/SetEnabled don't use it
	return o, dir
}

func TestRegisterAndBootstrap(t *testing.T) {
	o, dir := newTestOrch(t)
	if err := o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	if err := o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatalf("register router: %v", err)
	}
	if err := o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"}); err == nil {
		t.Errorf("expected ErrSlotAlreadyRegistered on duplicate")
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// disabled/ subdir must exist
	if _, err := os.Stat(filepath.Join(dir, "disabled")); err != nil {
		t.Errorf("disabled subdir missing: %v", err)
	}
	snap := o.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	// base AlwaysOn → enabled, no file yet → Present=false
	if !snap[0].Enabled {
		t.Errorf("base should be enabled (AlwaysOn)")
	}
	if snap[0].Present {
		t.Errorf("base file should not exist on fresh dir")
	}
}

func TestSaveWritesActivePathWhenEnabled(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{"x":1}`)); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "00-base.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != `{"x":1}` {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled", "00-base.json")); !os.IsNotExist(err) {
		t.Errorf("disabled copy should not exist")
	}
}

func TestSaveWritesDisabledPathWhenDisabled(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Slot is disabled by default (not AlwaysOn, no file yet).
	if err := o.Save(SlotRouter, []byte(`{"y":2}`)); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "disabled", "20-router.json"))
	if err != nil {
		t.Fatalf("read disabled: %v", err)
	}
	if string(data) != `{"y":2}` {
		t.Errorf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("active copy should not exist")
	}
}

func TestSetEnabledRenamesFile(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{"y":2}`)); err != nil {
		t.Fatal(err)
	}
	// Lives in disabled/.
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "20-router.json")); err != nil {
		t.Errorf("expected active path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled", "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("disabled path should be empty")
	}
	// Idempotent.
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Errorf("idempotent enable failed: %v", err)
	}
	// Disable.
	if err := o.SetEnabled(SlotRouter, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled", "20-router.json")); err != nil {
		t.Errorf("expected disabled path: %v", err)
	}
}

func TestSetEnabledSilentRenamesWithoutSchedulingReload(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotDownloadProxy, Filename: "35-download-proxy.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.SaveSilent(SlotDownloadProxy, []byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabledSilent(SlotDownloadProxy, true); err != nil {
		t.Fatalf("enable silent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "35-download-proxy.json")); err != nil {
		t.Fatalf("expected active file after silent enable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled", "35-download-proxy.json")); !os.IsNotExist(err) {
		t.Fatalf("expected disabled copy removed, err=%v", err)
	}
	if o.reloadTimer != nil {
		t.Fatal("SetEnabledSilent should not schedule debounce reload timer")
	}
}

func TestSetEnabledRejectsAlwaysOn(t *testing.T) {
	o, _ := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotBase, false); err != ErrSlotAlwaysOn {
		t.Errorf("disable always-on should error, got %v", err)
	}
}

func TestSaveUnknownSlot(t *testing.T) {
	o, _ := newTestOrch(t)
	if err := o.Save(SlotRouter, []byte(`{}`)); err != ErrUnknownSlot {
		t.Errorf("expected ErrUnknownSlot, got %v", err)
	}
}

// fakeProc records lifecycle calls for tests.
type fakeProc struct {
	mu       sync.Mutex
	running  bool
	starts   int
	stops    int
	reloads  int
	startErr error
	order    []string // sequence of "start"/"stop"/"reload" calls
}

// calls returns a copy of the recorded call sequence.
func (p *fakeProc) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.order...)
}

func (p *fakeProc) IsRunning() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return true, 12345
	}
	return false, 0
}
func (p *fakeProc) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	p.order = append(p.order, "start")
	if p.startErr != nil {
		return p.startErr
	}
	p.running = true
	return nil
}
func (p *fakeProc) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	p.order = append(p.order, "stop")
	p.running = false
	return nil
}
func (p *fakeProc) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reloads++
	p.order = append(p.order, "reload")
	return nil
}

func TestReloadDoesNotStartForAlwaysOnCatalogSlot(t *testing.T) {
	// Regression: tunnels + awg are AlwaysOn catalog slots. On a fresh
	// install with no router, no deviceproxy, no subscriptions and an
	// empty 10-tunnels.json, the daemon must NOT be started just because
	// these slots are enabled by virtue of being AlwaysOn.
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{
		Slot:       SlotTunnels,
		Filename:   "10-tunnels.json",
		AlwaysOn:   true,
		HasContent: func() bool { return false }, // empty tunnels file
	})
	_ = o.Register(SlotMeta{Slot: SlotAwg, Filename: "15-awg.json", AlwaysOn: true})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotTunnels, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotAwg, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.starts != 0 {
		t.Errorf("expected 0 starts (no consumers, no tunnels), got %d", fp.starts)
	}
}

func TestReloadStartsWhenAlwaysOnSlotHasContent(t *testing.T) {
	// As soon as the user defines at least one sing-box tunnel, the
	// AlwaysOn SlotTunnels HasContent flips true and the daemon must
	// be brought up — even if no other consumer slot is enabled.
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{
		Slot:       SlotTunnels,
		Filename:   "10-tunnels.json",
		AlwaysOn:   true,
		HasContent: func() bool { return true },
	})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotTunnels, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.starts != 1 {
		t.Errorf("expected 1 start (tunnel content present), got %d", fp.starts)
	}
}

func TestReloadStartsWhenSlotEnabled(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.starts != 1 || fp.reloads != 0 {
		t.Errorf("expected 1 start, 0 reloads; got starts=%d reloads=%d", fp.starts, fp.reloads)
	}
}

func TestReloadStopsWhenAllDisabled(t *testing.T) {
	fp := &fakeProc{running: true} // pretend already running
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// base is AlwaysOn, router is disabled. hasActiveWork = false.
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.stops != 1 {
		t.Errorf("expected 1 stop, got %d", fp.stops)
	}
}

func TestReloadSighupsWhenAlreadyRunning(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatal(err)
	}
	if fp.reloads != 1 || fp.starts != 0 {
		t.Errorf("expected 1 reload, 0 starts; got reloads=%d starts=%d", fp.reloads, fp.starts)
	}
}

func TestReloadSkippedOnValidationError(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Write a config with a dangling outbound reference.
	if err := o.Save(SlotRouter, []byte(`{"route":{"rules":[{"outbound":"ghost"}]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	err := o.Reload()
	if err == nil {
		t.Errorf("expected validation error from Reload")
	}
	if fp.starts != 0 || fp.reloads != 0 || fp.stops != 0 {
		t.Errorf("no process action expected on invalid config; got %+v", fp)
	}
}

func TestDebouncerCoalescesMultipleSaves(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	// Three rapid saves within the debounce window.
	for i := 0; i < 3; i++ {
		if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	// Wait past debounce.
	time.Sleep(reloadDebounce + 100*time.Millisecond)
	fp.mu.Lock()
	reloads := fp.reloads
	starts := fp.starts
	fp.mu.Unlock()
	if reloads+starts > 2 {
		// SetEnabled fires once; the 3 saves coalesce into at most one
		// additional reload. Tolerate <=2 total.
		t.Errorf("debouncer didn't coalesce; reloads=%d starts=%d", reloads, starts)
	}
	if reloads+starts == 0 {
		t.Errorf("expected at least one reload to fire")
	}
}

func TestBootstrapPromotesAlwaysOnSlotFromDisabled(t *testing.T) {
	// Migration scenario: an earlier build that treated SlotTunnels as
	// non-AlwaysOn parked 10-tunnels.json under disabled/. The new
	// AlwaysOn registration must promote it back to active/ on Bootstrap
	// so sing-box's -C (non-recursive) sees the file again, and the
	// in-memory enabled map matches the AlwaysOn invariant.
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true})
	if err := os.MkdirAll(filepath.Join(dir, "disabled"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disabled", "10-tunnels.json"), []byte(`{"stale":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled", "10-tunnels.json")); !os.IsNotExist(err) {
		t.Errorf("file should have been moved out of disabled/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "10-tunnels.json")); err != nil {
		t.Errorf("file should now be in active/: %v", err)
	}
	snap := o.Snapshot()
	if len(snap) != 1 || !snap[0].Enabled {
		t.Errorf("AlwaysOn tunnels slot should be enabled after bootstrap: %+v", snap)
	}
}

func TestReloadStartsForBothAlwaysOnContentAndConsumerSlot(t *testing.T) {
	// Composition: an AlwaysOn slot with HasContent=true AND a
	// non-AlwaysOn slot enabled both contribute "active work" — neither
	// path should shadow the other.
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{
		Slot:       SlotTunnels,
		Filename:   "10-tunnels.json",
		AlwaysOn:   true,
		HasContent: func() bool { return true },
	})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotTunnels, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.starts != 1 {
		t.Errorf("expected 1 start (both paths active), got %d", fp.starts)
	}
}

func TestPendingPath_ReturnsExpectedPath(t *testing.T) {
	o := New("/tmp/cfg", nil)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	meta := o.slots[SlotRouter]
	got := o.pendingPath(meta)
	want := "/tmp/cfg/pending/20-router.json"
	if got != want {
		t.Errorf("pendingPath: got %q want %q", got, want)
	}
}

func TestEnsureDirs_CreatesPendingSubdir(t *testing.T) {
	dir := t.TempDir()
	o := New(dir, nil)
	if err := o.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "pending"))
	if err != nil {
		t.Fatalf("pending dir missing: %v", err)
	}
	if !st.IsDir() {
		t.Errorf("pending exists but is not a dir")
	}
}

func TestBootstrapResolvesBothLocationsConflict(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	// Pre-seed BOTH locations.
	if err := os.MkdirAll(filepath.Join(dir, "disabled"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(`{"active":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disabled", "20-router.json"), []byte(`{"stale":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "disabled", "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("disabled stale copy should be removed")
	}
	snap := o.Snapshot()
	if len(snap) != 1 || !snap[0].Enabled {
		t.Errorf("router should be enabled after both-locations resolution: %+v", snap)
	}
}

func TestBootstrap_SweepsStaleApplyCheckDirs(t *testing.T) {
	dir := t.TempDir()
	// Pre-create a leftover from a crashed Apply.
	stale := filepath.Join(dir, ".apply-check-abc123")
	if err := os.MkdirAll(stale, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "20-router.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	o := New(dir, nil)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .apply-check-* dir not swept: %v", err)
	}
}

func TestBootstrap_LeavesPendingFileIntact(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "pending"), 0755)
	pendingFile := filepath.Join(dir, "pending", "20-router.json")
	bytes := []byte(`{"draft":"survives"}`)
	if err := os.WriteFile(pendingFile, bytes, 0644); err != nil {
		t.Fatal(err)
	}

	o := New(dir, nil)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(pendingFile)
	if err != nil {
		t.Fatalf("pending file lost: %v", err)
	}
	if string(got) != string(bytes) {
		t.Errorf("pending bytes mutated: got %s", got)
	}
	if !o.HasDraft(SlotRouter) {
		t.Errorf("HasDraft should be true after Bootstrap")
	}
}

// shouldRun=false must suppress the cold-start branch of Reload — this
// is what makes a user-pressed Stop "sticky" against reload triggers
// from slot writes. SIGHUP and stop branches remain unaffected; only
// `needRunning && !running` is gated.
func TestReloadColdStartSuppressedByShouldRun(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	o.SetShouldRun(func() bool { return false }) // sticky-stop intent

	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.starts != 0 {
		t.Errorf("cold-start must be suppressed; got starts=%d", fp.starts)
	}
	if fp.running {
		t.Errorf("daemon must remain stopped")
	}
}

// When shouldRun returns true, the legacy cold-start path runs — proves
// the predicate doesn't break the happy path.
func TestReloadColdStartProceedsWhenShouldRunTrue(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	o.SetShouldRun(func() bool { return true })

	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.starts != 1 {
		t.Errorf("expected 1 start, got %d", fp.starts)
	}
}

// SIGHUP and stop branches must ignore shouldRun: the predicate only
// gates cold-start. Verified by enabling a slot, marking the proc as
// already running, calling Reload with shouldRun=false, and asserting
// the running daemon got SIGHUP rather than nothing.
func TestReloadShouldRunOnlyGatesColdStart(t *testing.T) {
	fp := &fakeProc{running: true} // already alive
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotBase, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	o.SetShouldRun(func() bool { return false }) // would block cold-start

	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.reloads != 1 {
		t.Errorf("SIGHUP must still fire when already running; reloads=%d", fp.reloads)
	}
	if fp.starts != 0 {
		t.Errorf("must not cold-start when already running; starts=%d", fp.starts)
	}
}

// tunInboundConfig is a minimal slot config that declares a tun inbound,
// mirroring what the fakeip-tun feature writes.
const tunInboundConfig = `{"inbounds":[{"type":"tun","tag":"tun-in"}]}`

// equalStrs reports slice equality for call-order assertions.
func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestReload_RestartsWhenTunAdded: transitioning INTO fakeip-tun (tun
// inbound newly present, prevHasTun starting false) while sing-box is
// already running must trigger a full restart (Stop THEN Start), NOT a
// SIGHUP — sing-box cannot bring up a freshly-added tun inbound via HUP.
func TestReload_RestartsWhenTunAdded(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	// prevHasTun starts false (zero value) — this is the add toggle.
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := fp.calls(); !equalStrs(got, []string{"stop", "start"}) {
		t.Errorf("expected restart [stop start], got %v", got)
	}
	if fp.reloads != 0 {
		t.Errorf("must not SIGHUP on tun add; reloads=%d", fp.reloads)
	}
	if !o.prevHasTun {
		t.Errorf("prevHasTun must be true after applying a tun config")
	}
}

// TestReload_SighupWhenTunStillPresent: tun was already present in the
// prior reload (prevHasTun true) and the new config still has the tun —
// only other things changed. No toggle => SIGHUP, not restart.
func TestReload_SighupWhenTunStillPresent(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	o.prevHasTun = true // tun was present in the prior applied config
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := fp.calls(); !equalStrs(got, []string{"reload"}) {
		t.Errorf("expected SIGHUP [reload], got %v", got)
	}
	if fp.starts != 0 || fp.stops != 0 {
		t.Errorf("must not restart when tun unchanged; starts=%d stops=%d", fp.starts, fp.stops)
	}
}

// TestReload_RestartsWhenTunRemoved: leaving fakeip-tun. prevHasTun true,
// the new config has NO tun, but another slot keeps the daemon needed
// (needRunning stays true). Removing a tun inbound also cannot be done
// via SIGHUP => restart.
func TestReload_RestartsWhenTunRemoved(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// No tun inbound in the new config, but the slot is enabled so the
	// daemon is still needed.
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	o.prevHasTun = true // tun WAS present previously
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := fp.calls(); !equalStrs(got, []string{"stop", "start"}) {
		t.Errorf("expected restart [stop start] on tun removal, got %v", got)
	}
	if o.prevHasTun {
		t.Errorf("prevHasTun must be false after applying a tun-less config")
	}
}

// TestReload_SighupWhenNoTunEither: no tun before, no tun now, running —
// the unchanged classic SIGHUP path. Guards against regressing normal
// (tproxy/router) reloads into a full restart.
func TestReload_SighupWhenNoTunEither(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := New(dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	// prevHasTun stays false (zero value).
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := fp.calls(); !equalStrs(got, []string{"reload"}) {
		t.Errorf("expected SIGHUP [reload], got %v", got)
	}
}

func TestKnownSlots_FakeIP(t *testing.T) {
	var fi *SlotMeta
	slots := KnownSlots()
	for i := range slots {
		if slots[i].Slot == SlotFakeIP {
			fi = &slots[i]
		}
	}
	if fi == nil || fi.Filename != "21-fakeip.json" || fi.AlwaysOn {
		t.Fatalf("SlotFakeIP missing/wrong: %+v", fi)
	}
}
