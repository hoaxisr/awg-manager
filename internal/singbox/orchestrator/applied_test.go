package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppliedState_SaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "singbox-applied.json") // sub/ must be created by save
	want := appliedState{Hash: "abc123", HasTun: true}
	if err := saveAppliedState(path, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok := loadAppliedState(path)
	if !ok {
		t.Fatal("load: ok=false after save")
	}
	if got != want {
		t.Errorf("roundtrip mismatch: got %+v, want %+v", got, want)
	}
}

func TestLoadAppliedState_MissingFile(t *testing.T) {
	_, ok := loadAppliedState(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if ok {
		t.Error("expected ok=false for missing file")
	}
}

func TestLoadAppliedState_CorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "singbox-applied.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, ok := loadAppliedState(path)
	if ok {
		t.Error("expected ok=false for corrupt JSON")
	}
}

// enabledConfigHashLocked must be stable for unchanged content, change
// when the active bytes of an enabled slot change, and change again when
// the only enabled slot is disabled (an empty merged config is not the
// same as a non-empty one).
func TestEnabledConfigHashLocked_StableAndSensitive(t *testing.T) {
	o, _ := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}

	o.mu.Lock()
	h1 := o.enabledConfigHashLocked()
	h1Again := o.enabledConfigHashLocked()
	o.mu.Unlock()
	if h1 != h1Again {
		t.Errorf("hash not stable across calls: %q vs %q", h1, h1Again)
	}

	if err := o.Save(SlotRouter, []byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	h2 := o.enabledConfigHashLocked()
	o.mu.Unlock()
	if h1 == h2 {
		t.Errorf("hash did not change when active content changed")
	}

	if err := o.SetEnabled(SlotRouter, false); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	h3 := o.enabledConfigHashLocked()
	o.mu.Unlock()
	if h3 == h2 {
		t.Errorf("hash should change when the only enabled slot gets disabled")
	}
}

// TestReload_SkipsWhenHashUnchangedAndRunning: sing-box is already
// running the config we are about to (re)apply — the skip gate must
// fire and Reload must not touch the process at all (no second SIGHUP).
func TestReload_SkipsWhenHashUnchangedAndRunning(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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

	// First reload actually applies (SIGHUP, already running) and
	// persists the applied-state hash.
	if err := o.Reload(); err != nil {
		t.Fatalf("first reload: %v", err)
	}
	if fp.reloadsN() != 1 {
		t.Fatalf("expected 1 reload after first apply, got %d", fp.reloadsN())
	}

	// Second reload: config unchanged, sing-box still running — the skip
	// gate must fire and NOT call Reload/Start/Stop again.
	if err := o.Reload(); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if fp.reloadsN() != 1 || fp.startsN() != 0 || fp.stopsN() != 0 {
		t.Errorf("skip gate did not hold: reloads=%d starts=%d stops=%d",
			fp.reloadsN(), fp.startsN(), fp.stopsN())
	}
}

// TestReload_AppliesWhenHashChanged: same running/needRunning shape as
// the skip test, but the enabled slot's content changed in between — the
// skip gate must NOT fire and the normal SIGHUP path must run again.
func TestReload_AppliesWhenHashChanged(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
		t.Fatalf("first reload: %v", err)
	}
	if fp.reloadsN() != 1 {
		t.Fatalf("expected 1 reload after first apply, got %d", fp.reloadsN())
	}

	if err := o.Save(SlotRouter, []byte(`{"tag":"changed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("second reload: %v", err)
	}
	if fp.reloadsN() != 2 {
		t.Errorf("expected config change to trigger a second SIGHUP, got reloads=%d", fp.reloadsN())
	}
}

// TestReload_DaemonRestartAdoptsRunningSingbox reproduces the bug Task 3
// fixes: the Go process restarts (a fresh Orchestrator, prevHasTun back
// at its zero value false) while sing-box itself kept running with a tun
// inbound. Without the persisted applied-state seeding prevHasTun and the
// skip gate, comparing zero-value prevHasTun against the still-true
// newHasTun looks like a toggle and forces a needless (harmful)
// tun-restart of a daemon that never actually needed touching.
func TestReload_DaemonRestartAdoptsRunningSingbox(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	appliedStatePath = filepath.Join(t.TempDir(), "singbox-applied.json")

	o1 := New(dir, fp)
	t.Cleanup(o1.Close)
	_ = o1.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o1.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o1.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	if err := o1.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o1.Reload(); err != nil {
		t.Fatalf("o1 reload: %v", err)
	}
	// o1's in-memory prevHasTun started false, so this first apply IS a
	// real restart (tun newly present) — establishes the baseline.
	if got := fp.calls(); !equalStrs(got, []string{"stop", "start"}) {
		t.Fatalf("expected initial restart [stop start], got %v", got)
	}

	// Simulate the daemon restart: a brand-new Orchestrator over the SAME
	// config dir, talking to the SAME (still running, unaware of our
	// process restart) sing-box — seeded from the applied-state file o1
	// left behind.
	o2 := New(dir, fp)
	t.Cleanup(o2.Close)
	if !o2.CurrentHasTun() {
		t.Fatal("constructor must seed prevHasTun=true from persisted applied state")
	}
	if err := o2.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatal(err)
	}
	if err := o2.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o2.Reload(); err != nil {
		t.Fatalf("o2 reload: %v", err)
	}
	if got := fp.calls(); !equalStrs(got, []string{"stop", "start"}) {
		t.Errorf("daemon-restart reload must be a no-op (skip gate); process calls = %v", got)
	}
}

type variablePIDProc struct {
	fakeProc
	pid int
}

func (p *variablePIDProc) IsRunning() (bool, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return true, p.pid
	}
	return false, 0
}

func TestReload_SuppressAndStopInvalidatesGeneration(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	appliedPath := filepath.Join(t.TempDir(), "singbox-applied.json")
	o := NewWithAppliedPath(dir, fp, appliedPath)
	t.Cleanup(o.Close)

	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(`{"route":{"rules":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("initial reload: %v", err)
	}
	if fp.startsN() != 1 || fp.reloadsN() != 0 {
		t.Fatalf("unexpected initial calls: starts=%d reloads=%d", fp.startsN(), fp.reloadsN())
	}

	if err := o.SetEnabledSilent(SlotRouter, false); err != nil {
		t.Fatal(err)
	}
	if err := o.SuppressAndStop(fp.Stop); err != nil {
		t.Fatalf("SuppressAndStop: %v", err)
	}
	if err := fp.Start(); err != nil {
		t.Fatalf("external start after suppress: %v", err)
	}
	if err := o.SetEnabledSilent(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Reload(); err != nil {
		t.Fatalf("reload after external restart: %v", err)
	}
	if fp.reloadsN() < 1 {
		t.Fatalf("expected Reload() to reload new process generation after SuppressAndStop, got reloads=%d (calls=%v)",
			fp.reloadsN(), fp.calls())
	}
}

func TestReload_PIDChangeRefusesStaleAppliedHash(t *testing.T) {
	fp := &variablePIDProc{fakeProc: fakeProc{running: true}, pid: 1001}
	dir := t.TempDir()
	appliedPath := filepath.Join(t.TempDir(), "singbox-applied.json")

	o1 := NewWithAppliedPath(dir, fp, appliedPath)
	t.Cleanup(o1.Close)
	_ = o1.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o1.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o1.Save(SlotRouter, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := o1.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o1.Reload(); err != nil {
		t.Fatalf("o1 reload: %v", err)
	}
	if fp.reloadsN() != 1 {
		t.Fatalf("expected 1 reload, got %d", fp.reloadsN())
	}

	// Simulate external sing-box respawn under a new PID before awg-manager restarted.
	fp.mu.Lock()
	fp.pid = 2002
	fp.mu.Unlock()

	o2 := NewWithAppliedPath(dir, fp, appliedPath)
	t.Cleanup(o2.Close)
	_ = o2.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o2.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o2.Reload(); err != nil {
		t.Fatalf("o2 reload: %v", err)
	}
	if fp.reloadsN() != 2 {
		t.Fatalf("expected PID change (1001 -> 2002) to force reload instead of adopting stale hash; reloads=%d", fp.reloadsN())
	}
}

// F823: хук после reload зовётся и при применении, и в ветке «конфиг не
// изменился» — на рестарте демона Reload идёт именно ей.
func TestReload_AfterReloadHookOnApplyAndSkip(t *testing.T) {
	fp := &fakeProc{running: true}
	o := newFakeOrch(t, t.TempDir(), fp)
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
	calls := make(chan struct{}, 4)
	o.SetAfterReload(func() { calls <- struct{}{} })
	for i := 0; i < 2; i++ {
		if err := o.Reload(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatalf("reload %d: after-reload hook not called", i+1)
		}
	}
}
