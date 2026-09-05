package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func newTestOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	old := appliedStatePath
	appliedStatePath = filepath.Join(t.TempDir(), "singbox-applied.json")
	t.Cleanup(func() { appliedStatePath = old })
	dir := t.TempDir()
	o := New(dir, nil) // nil ProcessController — Save/SetEnabled don't use it
	return o, dir
}

// Дефолт шва: New берёт package-var appliedStatePath (в проде — /var/run/...),
// иначе adopt после рестарта демона перестаёт работать молча. Пин доказывает
// «New читает package-var», а не сам прод-путь: переменная здесь перенаправлена
// в TempDir, чтобы тест не читал /var/run хоста.
func TestNew_UsesDefaultAppliedStatePath(t *testing.T) {
	old := appliedStatePath
	appliedStatePath = filepath.Join(t.TempDir(), "singbox-applied.json")
	t.Cleanup(func() { appliedStatePath = old })

	o := New(t.TempDir(), nil)
	if o.appliedPath != appliedStatePath {
		t.Fatalf("appliedPath = %q, want package default %q", o.appliedPath, appliedStatePath)
	}
	o2 := NewWithAppliedPath(t.TempDir(), nil, "/nonexistent/x.json")
	if o2.appliedPath != "/nonexistent/x.json" {
		t.Fatalf("NewWithAppliedPath ignored its argument: %q", o2.appliedPath)
	}
}

// newFakeOrch constructs an Orchestrator wired to a fakeProc, first
// redirecting the package-level applied-state seam (appliedStatePath) to
// a file scoped to this test's t.TempDir(). Without this, every
// fp-backed Reload test would share ONE /var/run/awg-manager/singbox-
// applied.json for the whole test binary run: besides writing outside
// the sandbox, several tests enable identical slot content, so one
// test's persisted hash could satisfy another's skip gate and suppress
// the Start/Reload/Stop call the test asserts on.
func newFakeOrch(t *testing.T, dir string, fp *fakeProc) *Orchestrator {
	t.Helper()
	appliedStatePath = filepath.Join(t.TempDir(), "singbox-applied.json")
	return New(dir, fp)
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

// startsN/stopsN/reloadsN — потокобезопасные читатели счётчиков: тесты
// читают их, пока отложенный debounce-таймер оркестратора (Save/SetEnabled)
// ещё может дёрнуть Start/Reload из своей горутины — прямое чтение полей
// ловится -race (предсуществовавшая гонка, всплыла на гейте #456).
func (p *fakeProc) startsN() int  { p.mu.Lock(); defer p.mu.Unlock(); return p.starts }
func (p *fakeProc) stopsN() int   { p.mu.Lock(); defer p.mu.Unlock(); return p.stops }
func (p *fakeProc) reloadsN() int { p.mu.Lock(); defer p.mu.Unlock(); return p.reloads }

func TestReloadDoesNotStartForAlwaysOnCatalogSlot(t *testing.T) {
	// Regression: tunnels + awg are AlwaysOn catalog slots. On a fresh
	// install with no router, no deviceproxy, no subscriptions and an
	// empty 10-tunnels.json, the daemon must NOT be started just because
	// these slots are enabled by virtue of being AlwaysOn.
	fp := &fakeProc{}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 0 {
		t.Errorf("expected 0 starts (no consumers, no tunnels), got %d", fp.startsN())
	}
}

func TestReloadStartsWhenAlwaysOnSlotHasContent(t *testing.T) {
	// As soon as the user defines at least one sing-box tunnel, the
	// AlwaysOn SlotTunnels HasContent flips true and the daemon must
	// be brought up — even if no other consumer slot is enabled.
	fp := &fakeProc{}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 1 {
		t.Errorf("expected 1 start (tunnel content present), got %d", fp.startsN())
	}
}

func TestReloadStartsWhenSlotEnabled(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 1 || fp.reloadsN() != 0 {
		t.Errorf("expected 1 start, 0 reloads; got starts=%d reloads=%d", fp.startsN(), fp.reloadsN())
	}
}

func TestReloadStopsWhenAllDisabled(t *testing.T) {
	fp := &fakeProc{running: true} // pretend already running
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// base is AlwaysOn, router is disabled. hasActiveWork = false.
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fp.stopsN() != 1 {
		t.Errorf("expected 1 stop, got %d", fp.stopsN())
	}
}

func TestReloadSighupsWhenAlreadyRunning(t *testing.T) {
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
		t.Fatal(err)
	}
	if fp.reloadsN() != 1 || fp.startsN() != 0 {
		t.Errorf("expected 1 reload, 0 starts; got reloads=%d starts=%d", fp.reloadsN(), fp.startsN())
	}
}

func TestReloadSkippedOnValidationError(t *testing.T) {
	fp := &fakeProc{}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 0 || fp.reloadsN() != 0 || fp.stopsN() != 0 {
		t.Errorf("no process action expected on invalid config; got starts=%d reloads=%d stops=%d",
			fp.startsN(), fp.reloadsN(), fp.stopsN())
	}
}

func TestDebouncerCoalescesMultipleSaves(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
	reloads := fp.reloadsN()
	starts := fp.startsN()
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
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 1 {
		t.Errorf("expected 1 start (both paths active), got %d", fp.startsN())
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

func TestBootstrap_SweepsStaleCheckDirs(t *testing.T) {
	dir := t.TempDir()
	// Pre-create leftovers from crashed Apply / CheckMerged / CheckSlotAlone runs.
	stale := []string{
		filepath.Join(dir, ".apply-check-abc123"),
		filepath.Join(dir, ".save-check-def456"),
		filepath.Join(dir, ".alone-check-ghi789"),
	}
	for _, s := range stale {
		if err := os.MkdirAll(s, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s, "20-router.json"), []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	o := New(dir, nil)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	for _, s := range stale {
		if _, err := os.Stat(s); !os.IsNotExist(err) {
			t.Errorf("stale check dir %s not swept: %v", filepath.Base(s), err)
		}
	}
}

func TestBootstrap_SweepsStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, disabledSubdir), 0755)
	_ = os.MkdirAll(filepath.Join(dir, "pending"), 0755)
	// Leftover AtomicWrite temp files from a crash between write and rename,
	// one in each dir slot writes can land in.
	staleTmps := []string{
		filepath.Join(dir, "20-router.json.tmp.1234.5678"),
		filepath.Join(dir, disabledSubdir, "40-subscriptions.json.tmp.1.2"),
		filepath.Join(dir, "pending", "90-user.json.tmp.9.9"),
	}
	for _, p := range staleTmps {
		if err := os.WriteFile(p, []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// A real slot file must survive the sweep.
	keep := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(keep, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	o := New(dir, nil)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	for _, p := range staleTmps {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale temp file not swept: %s (%v)", p, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("real slot file wrongly removed: %v", err)
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
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 0 {
		t.Errorf("cold-start must be suppressed; got starts=%d", fp.startsN())
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
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 1 {
		t.Errorf("expected 1 start, got %d", fp.startsN())
	}
}

// SIGHUP and stop branches must ignore shouldRun: the predicate only
// gates cold-start. Verified by enabling a slot, marking the proc as
// already running, calling Reload with shouldRun=false, and asserting
// the running daemon got SIGHUP rather than nothing.
func TestReloadShouldRunOnlyGatesColdStart(t *testing.T) {
	fp := &fakeProc{running: true} // already alive
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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
	if fp.reloadsN() != 1 {
		t.Errorf("SIGHUP must still fire when already running; reloads=%d", fp.reloadsN())
	}
	if fp.startsN() != 0 {
		t.Errorf("must not cold-start when already running; starts=%d", fp.startsN())
	}
}

// tunInboundConfig is a minimal slot config that declares a tun inbound,
// mirroring what the fakeip-tun feature writes.
const tunInboundConfig = `{"inbounds":[{"type":"tun","tag":"tun-in"}]}`

// altTunInboundConfig — тот же слот с ИЗМЕНЁННЫМ содержимым: skip-gate
// оркестратора сравнивает хеш, и применение без реального изменения он
// справедливо пропускает.
const altTunInboundConfig = `{"inbounds":[{"type":"tun","tag":"tun-in","mtu":1280}]}`

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
	o := newFakeOrch(t, dir, fp)
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
	if fp.reloadsN() != 0 {
		t.Errorf("must not SIGHUP on tun add; reloads=%d", fp.reloadsN())
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
	o := newFakeOrch(t, dir, fp)
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
	if fp.startsN() != 0 || fp.stopsN() != 0 {
		t.Errorf("must not restart when tun unchanged; starts=%d stops=%d", fp.startsN(), fp.stopsN())
	}
}

// TestHoldReloads_SuppressesUntilRelease: пока держится hold, записи слотов не
// дёргают процесс — иначе чужой продюсер перезапускал бы движок посреди
// перехода режима (при живом tun любой reload это Stop+Start). На release
// накопленное применяется одним reload'ом.
func TestHoldReloads_SuppressesUntilRelease(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	release := o.HoldReloads()
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * reloadDebounce)
	if got := fp.calls(); len(got) != 0 {
		t.Fatalf("под hold процесс трогать нельзя, получено %v", got)
	}

	release()
	deadline := time.Now().Add(2 * time.Second)
	for len(fp.calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := fp.calls(); len(got) == 0 {
		t.Fatal("после release накопленная запись обязана примениться")
	}
	// Ровно одно применение: старт (процесс уже жив → SIGHUP или restart по
	// toggle tun), а не по одному на каждую подавленную запись.
	time.Sleep(3 * reloadDebounce)
	if got := fp.calls(); len(got) > 2 {
		t.Errorf("ожидалось одно применение, получено %v", got)
	}
}

// TestHoldReloads_ReloadNowClearsPending: явный ReloadNow под hold применяет всё
// накопленное, поэтому release не обязан стрелять вторым reload'ом — иначе
// переход режима заканчивался бы лишним перезапуском движка.
func TestHoldReloads_ReloadNowClearsPending(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	release := o.HoldReloads()
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.ReloadNow(); err != nil {
		t.Fatalf("reload now: %v", err)
	}
	applied := len(fp.calls())
	if applied == 0 {
		t.Fatal("ReloadNow под hold обязан применить конфиг")
	}

	// Процесс на лишнем прогоне не пострадал бы — его съел бы skip-gate по
	// хешу, — поэтому ловим сам факт прогона по журналу оркестратора.
	var after []string
	o.SetLogger(func(_, msg string) { after = append(after, msg) })
	release()
	time.Sleep(3 * reloadDebounce)
	if len(after) != 0 {
		t.Errorf("release после ReloadNow не должен запускать reload повторно, журнал: %v", after)
	}
	if got := fp.calls(); len(got) != applied {
		t.Errorf("release после ReloadNow не должен применять повторно: было %d, стало %v", applied, got)
	}
}

// TestReload_LogNamesRestartWhenTunActive: при живом tun proc.Reload делает
// Stop+Start (SIGHUP пересоздал бы tun под удерживаемым fd → FATAL), поэтому
// строка журнала обязана называть рестарт. Прежняя формулировка «SIGHUP»
// сбивала с толку при разборе простоя: в журнале стоял SIGHUP, а движок в
// этот момент перезапускался.
func TestReload_LogNamesRestartWhenTunActive(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	var lines []string
	o.SetLogger(func(_, msg string) { lines = append(lines, msg) })
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
	o.prevHasTun = true // tun уже жив — реального SIGHUP не будет
	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "restarting sing-box (config changed, tun active)") {
		t.Errorf("журнал обязан назвать рестарт; строки: %v", lines)
	}
	if strings.Contains(joined, "SIGHUP") {
		t.Errorf("при живом tun строка SIGHUP лжёт; строки: %v", lines)
	}
}

// TestReload_RestartsWhenTunRemoved: leaving fakeip-tun. prevHasTun true,
// the new config has NO tun, but another slot keeps the daemon needed
// (needRunning stays true). Removing a tun inbound also cannot be done
// via SIGHUP => restart.
func TestReload_RestartsWhenTunRemoved(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
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

// TestSetEnabledReconcilesMapDiskDrift reproduces the live bug where a
// map↔disk drift left 20-router.json in BOTH config.d/ and config.d/disabled/
// while the in-memory enabled-map still said "disabled". The old no-op
// short-circuit (o.enabled[slot]==enabled → return nil) skipped renameForToggle,
// so the stray active file kept leaking into MergeDir (mixed FakeIP+TPROXY DNS).
// SetEnabled(false) must reconcile against disk and remove the stray active file.
func TestSetEnabledReconcilesMapDiskDrift(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	active := filepath.Join(dir, "20-router.json")
	disabled := filepath.Join(dir, "disabled", "20-router.json")

	// Drift: both copies present on disk, but the map still says disabled
	// (Bootstrap saw no active file, so o.enabled[SlotRouter] == false).
	if err := os.WriteFile(active, []byte(`{"dns":{"servers":[{"tag":"tproxy"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disabled, []byte(`{"dns":{"servers":[{"tag":"tproxy"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Disabling must remove the stray active file rather than no-op on the
	// stale map.
	if err := o.SetEnabled(SlotRouter, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Errorf("stray active file must be removed; stat err=%v", err)
	}
	if _, err := os.Stat(disabled); err != nil {
		t.Errorf("parked disabled copy must survive: %v", err)
	}
}

// TestReloadPrunesDanglingSelectorRefs reproduces the live FATAL
// "dependency[YC-FIN] not found for outbound[device-proxy-selector]": a selector
// keeps a member tag whose outbound was deleted from another slot. sing-box check
// does not catch it (like composite cycles, it only surfaces at "start service"),
// so the broken config reaches the daemon. Reload must prune dangling selector
// members (and a dangling default) from the slot file before applying.
func TestReloadPrunesDanglingSelectorRefs(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotDeviceProxy, Filename: "30-deviceproxy.json"})

	// Active slot: selector references "direct" (builtin, ok) + "YC-FIN" (gone).
	active := filepath.Join(dir, "30-deviceproxy.json")
	dp := `{"inbounds":[{"type":"mixed","tag":"device-proxy-in","listen_port":1090}],` +
		`"outbounds":[{"type":"selector","tag":"device-proxy-selector","outbounds":["direct","YC-FIN"],"default":"YC-FIN"}]}`
	if err := os.WriteFile(active, []byte(dp), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := o.Bootstrap(); err != nil { // file present → enabled[deviceproxy]=true
		t.Fatal(err)
	}

	if err := o.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	data, err := os.ReadFile(active)
	if err != nil {
		t.Fatalf("read pruned slot: %v", err)
	}
	var c slotConfig
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("unmarshal pruned slot: %v", err)
	}
	if len(c.Outbounds) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(c.Outbounds))
	}
	sel := c.Outbounds[0]
	for _, m := range sel.Outbounds {
		if m == "YC-FIN" {
			t.Errorf("dangling member YC-FIN must be pruned, got %v", sel.Outbounds)
		}
	}
	if sel.Default == "YC-FIN" {
		t.Errorf("dangling default YC-FIN must be cleared, got %q", sel.Default)
	}
	if len(sel.Outbounds) == 0 {
		t.Errorf("selector must keep surviving member \"direct\"")
	}
}

// TestScheduleReload_TimerForgetsItself: выстреливший debounce-таймер обязан
// обнулить указатель. Иначе HoldReloads видит «таймер взведён» там, где взводить
// нечего, помечает pendingReload и на release стреляет лишним reload'ом —
// посреди перехода это лишний Stop+Start движка.
func TestScheduleReload_TimerForgetsItself(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}

	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * reloadDebounce) // даём таймеру выстрелить

	o.mu.Lock()
	stale := o.reloadTimer != nil
	o.mu.Unlock()
	if stale {
		t.Fatal("выстреливший таймер обязан забыть себя")
	}

	// Прямое следствие: hold поверх отработавшего таймера не считает, что есть
	// что применять, и release не стреляет лишним reload'ом.
	before := len(fp.calls())
	release := o.HoldReloads()
	o.mu.Lock()
	pending := o.pendingReload
	o.mu.Unlock()
	release()
	time.Sleep(3 * reloadDebounce)
	if pending {
		t.Error("hold пометил pending по протухшему таймеру")
	}
	if got := fp.calls(); len(got) != before {
		t.Errorf("release без накопленных записей не должен применять, получено %v", got[before:])
	}
}

// TestSave_IdenticalBytesDoNotScheduleReload: продюсеры зовут Save
// идемпотентно (device-proxy переписывает свой слот по нескольку раз за один
// переход режима), а при живом tun каждый reload — полный Stop+Start движка.
// Байт-в-байт та же запись будить пайплайн не должна.
func TestSave_IdenticalBytesDoNotScheduleReload(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * reloadDebounce) // даём первому применению отработать
	before := len(fp.calls())

	// Повтор той же записи не должен даже ВЗВОДИТЬ таймер: процесс всё равно
	// уберёг бы скип-гейт по хешу в Reload, но ценой merged-мержа и
	// `sing-box check` — на mipsel это и есть дорогая часть, ради которой
	// гейт существует. Поэтому проверяем сам факт планирования.
	o.mu.Lock()
	o.reloadTimer, o.pendingReload = nil, false
	o.mu.Unlock()
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	scheduled := o.reloadTimer != nil || o.pendingReload
	o.mu.Unlock()
	if scheduled {
		t.Error("идентичная запись не должна планировать reload")
	}
	time.Sleep(3 * reloadDebounce)
	if got := fp.calls(); len(got) != before {
		t.Errorf("идентичная запись не должна трогать процесс, получено %v", got[before:])
	}

	// А изменившаяся — обязана.
	if err := o.Save(SlotRouter, []byte(altTunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(fp.calls()) == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(fp.calls()) == before {
		t.Error("изменившаяся запись обязана примениться")
	}
}

// Байт-гейт Save обязан быть fail-open: файла нет (первая запись слота) или
// он нечитаем — считаем «изменилось» и планируем reload. Пропустить нужный
// reload хуже лишнего: слот появился в merged, а движок о нём не узнал.
func TestSave_FirstWriteSchedulesReload(t *testing.T) {
	fp := &fakeProc{running: true}
	dir := t.TempDir()
	o := newFakeOrch(t, dir, fp)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEnabled(SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	o.reloadTimer, o.pendingReload = nil, false
	o.mu.Unlock()

	// Файла слота ещё нет — самая первая запись.
	if err := o.Save(SlotRouter, []byte(tunInboundConfig)); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	scheduled := o.reloadTimer != nil || o.pendingReload
	o.mu.Unlock()
	if !scheduled {
		t.Error("первая запись слота обязана планировать reload")
	}
}

// inodeOf — writeAtomic пишет через temp+rename, поэтому inode меняется на
// каждой записи. Это и есть наблюдаемая улика «файл переписан».
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("inode недоступен на этой платформе")
	}
	return uint64(st.Ino)
}

// Повторный Save теми же байтами не обязан трогать флеш: на роутере это
// лишний цикл записи, а на пути device-proxy такой Save случается на каждую
// подсказку инвалидации, меняющую состав туннелей.
func TestSave_UnchangedBytesDoNotRewriteFile(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true})
	data := []byte("{\n  \"a\": 1\n}")
	if err := o.Save(SlotTunnels, data); err != nil {
		t.Fatalf("первый Save: %v", err)
	}
	path := filepath.Join(dir, "10-tunnels.json")
	before := inodeOf(t, path)

	if err := o.Save(SlotTunnels, data); err != nil {
		t.Fatalf("повторный Save: %v", err)
	}
	if got := inodeOf(t, path); got != before {
		t.Errorf("файл переписан теми же байтами: inode %d → %d", before, got)
	}

	// Изменение байтов обязано доехать — гейт не должен запирать запись.
	if err := o.Save(SlotTunnels, []byte("{\n  \"a\": 2\n}")); err != nil {
		t.Fatalf("Save с другими байтами: %v", err)
	}
	if got := inodeOf(t, path); got == before {
		t.Error("изменённые байты не записаны")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "{\n  \"a\": 2\n}" {
		t.Errorf("на диске %q (err=%v)", got, err)
	}
}

// Тот же гейт у Mutate: мутатор вернул ровно то, что лежит на диске.
func TestMutate_UnchangedBytesDoNotRewriteFile(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true})
	data := []byte("{\n  \"a\": 1\n}")
	if err := o.Save(SlotTunnels, data); err != nil {
		t.Fatalf("первый Save: %v", err)
	}
	path := filepath.Join(dir, "10-tunnels.json")
	before := inodeOf(t, path)

	if err := o.Mutate(SlotTunnels, func(cur []byte, exists bool) ([]byte, error) {
		if !exists {
			t.Error("файл обязан существовать")
		}
		return append([]byte(nil), cur...), nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	if got := inodeOf(t, path); got != before {
		t.Errorf("файл переписан теми же байтами: inode %d → %d", before, got)
	}

	if err := o.Mutate(SlotTunnels, func([]byte, bool) ([]byte, error) {
		return []byte("{\n  \"a\": 3\n}"), nil
	}); err != nil {
		t.Fatalf("Mutate с другими байтами: %v", err)
	}
	if got := inodeOf(t, path); got == before {
		t.Error("изменённые байты не записаны")
	}
}
