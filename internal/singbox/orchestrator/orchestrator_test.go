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
	p.running = false
	return nil
}
func (p *fakeProc) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reloads++
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
