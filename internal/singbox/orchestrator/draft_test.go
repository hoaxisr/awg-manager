package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil)
	if err := o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	o.enabled[SlotRouter] = true
	return o, dir
}

func TestSaveDraft_WritesToPendingDir(t *testing.T) {
	o, dir := setupOrch(t)
	bytes := []byte(`{"outbounds":[]}`)
	if err := o.SaveDraft(SlotRouter, bytes); err != nil {
		t.Fatalf("SaveDraft: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil {
		t.Fatalf("pending file missing: %v", err)
	}
	if string(got) != string(bytes) {
		t.Errorf("pending bytes mismatch: got %s", got)
	}
	// active must be untouched (not exist or empty).
	if _, err := os.Stat(filepath.Join(dir, "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("active file should not exist yet, got: %v", err)
	}
}

func TestLoadEffective_PrefersPending(t *testing.T) {
	o, dir := setupOrch(t)
	_ = os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(`{"active":true}`), 0644)
	_ = o.SaveDraft(SlotRouter, []byte(`{"draft":true}`))
	got, err := o.LoadEffective(SlotRouter)
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if string(got) != `{"draft":true}` {
		t.Errorf("want draft bytes, got %s", got)
	}
}

func TestLoadEffective_FallsBackToActive(t *testing.T) {
	o, dir := setupOrch(t)
	_ = os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(`{"active":true}`), 0644)
	got, err := o.LoadEffective(SlotRouter)
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if string(got) != `{"active":true}` {
		t.Errorf("want active bytes, got %s", got)
	}
}

func TestLoadEffective_ReturnsNilWhenBothMissing(t *testing.T) {
	o, _ := setupOrch(t)
	got, err := o.LoadEffective(SlotRouter)
	if err != nil {
		t.Fatalf("LoadEffective: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %s", got)
	}
}

func TestHasDraft_TrueAfterSave_FalseAfterDiscard(t *testing.T) {
	o, _ := setupOrch(t)
	if o.HasDraft(SlotRouter) {
		t.Fatal("HasDraft true before any SaveDraft")
	}
	_ = o.SaveDraft(SlotRouter, []byte(`{}`))
	if !o.HasDraft(SlotRouter) {
		t.Fatal("HasDraft false after SaveDraft")
	}
	if err := o.DiscardDraft(SlotRouter); err != nil {
		t.Fatalf("DiscardDraft: %v", err)
	}
	if o.HasDraft(SlotRouter) {
		t.Fatal("HasDraft true after DiscardDraft")
	}
}

func TestDiscardDraft_Idempotent(t *testing.T) {
	o, _ := setupOrch(t)
	if err := o.DiscardDraft(SlotRouter); err != nil {
		t.Errorf("first discard (no pending): %v", err)
	}
	if err := o.DiscardDraft(SlotRouter); err != nil {
		t.Errorf("second discard: %v", err)
	}
}

func TestDraftInfo_ReturnsMtime(t *testing.T) {
	o, dir := setupOrch(t)
	if info := o.DraftInfo(SlotRouter); info.HasDraft {
		t.Fatal("DraftInfo says HasDraft when no pending file exists")
	}
	_ = o.SaveDraft(SlotRouter, []byte(`{}`))
	info := o.DraftInfo(SlotRouter)
	if !info.HasDraft {
		t.Fatal("DraftInfo says !HasDraft after SaveDraft")
	}
	st, _ := os.Stat(filepath.Join(dir, "pending", "20-router.json"))
	if !info.DraftedAt.Equal(st.ModTime()) {
		t.Errorf("DraftedAt mismatch: got %v want %v", info.DraftedAt, st.ModTime())
	}
}

func TestSaveDraft_DoesNotScheduleReload(t *testing.T) {
	o, _ := setupOrch(t)
	o.reloadTimer = nil
	_ = o.SaveDraft(SlotRouter, []byte(`{}`))
	if o.reloadTimer != nil {
		t.Errorf("SaveDraft armed reload timer (it must not)")
	}
}

func TestSaveDraft_UnknownSlot(t *testing.T) {
	o, _ := setupOrch(t)
	err := o.SaveDraft(Slot("never-registered"), []byte(`{}`))
	if !errors.Is(err, ErrUnknownSlot) {
		t.Errorf("want ErrUnknownSlot, got %v", err)
	}
}

type fakeValidator struct {
	calls int
	err   error
}

func (f *fakeValidator) Validate(ctx context.Context, dir string) error {
	f.calls++
	return f.err
}

func TestApplyDraft_HappyPath(t *testing.T) {
	o, dir := setupOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	o.enabled[SlotBase] = true
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "20-router.json"),
		[]byte(`{"outbounds":[]}`), 0644)
	_ = o.SaveDraft(SlotRouter,
		[]byte(`{"outbounds":[],"route":{"final":"direct"}}`))

	fv := &fakeValidator{}
	o.SetValidator(fv)

	res, err := o.ApplyDraft(SlotRouter)
	if err != nil {
		t.Fatalf("ApplyDraft: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("ApplyDraft validation: %s", res.Error())
	}
	if fv.calls != 1 {
		t.Errorf("validator should be called once, got %d", fv.calls)
	}
	// pending gone
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("pending file should be gone, got: %v", err)
	}
	// active updated
	got, _ := os.ReadFile(filepath.Join(dir, "20-router.json"))
	if string(got) != `{"outbounds":[],"route":{"final":"direct"}}` {
		t.Errorf("active not updated: %s", got)
	}
}

func TestApplyDraft_NoDraft(t *testing.T) {
	o, _ := setupOrch(t)
	res, err := o.ApplyDraft(SlotRouter)
	if !errors.Is(err, ErrNoDraft) {
		t.Errorf("want ErrNoDraft, got %v", err)
	}
	if !res.Ok() {
		t.Errorf("ZeroResult should be Ok() when err is ErrNoDraft")
	}
}

func TestApplyDraft_CrossSlotValidationFail(t *testing.T) {
	o, dir := setupOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	o.enabled[SlotBase] = true
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	// Draft references a ghost outbound.
	_ = o.SaveDraft(SlotRouter,
		[]byte(`{"route":{"final":"ghost-tag"}}`))

	res, err := o.ApplyDraft(SlotRouter)
	if err != nil {
		t.Fatalf("ApplyDraft: %v (validation failure should be in res, not err)", err)
	}
	if res.Ok() {
		t.Fatalf("expected validation failure")
	}
	// Pending preserved.
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); err != nil {
		t.Errorf("pending should still exist: %v", err)
	}
	// Active untouched.
	if _, err := os.Stat(filepath.Join(dir, "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("active should not exist: %v", err)
	}
}

func TestApplyDraft_SingboxCheckFail(t *testing.T) {
	o, dir := setupOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	o.enabled[SlotBase] = true
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	_ = o.SaveDraft(SlotRouter,
		[]byte(`{"route":{"final":"direct"}}`))

	o.SetValidator(&fakeValidator{err: errors.New("simulated sb-check failure")})

	res, err := o.ApplyDraft(SlotRouter)
	if err == nil {
		t.Fatalf("want sb-check error, got nil; res=%v", res)
	}
	if !res.Ok() {
		t.Errorf("res should be ZeroResult on sb-check failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); err != nil {
		t.Errorf("pending should still exist: %v", err)
	}
}

func tmpDirHasApplyCheck(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read configDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".apply-check-") {
			return true
		}
	}
	return false
}

func TestApplyDraft_CleansTmpdir_OnSuccess(t *testing.T) {
	o, dir := setupOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	o.enabled[SlotBase] = true
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	_ = o.SaveDraft(SlotRouter, []byte(`{"route":{"final":"direct"}}`))
	o.SetValidator(&fakeValidator{})
	_, _ = o.ApplyDraft(SlotRouter)
	if tmpDirHasApplyCheck(t, dir) {
		t.Errorf("tmpdir not cleaned up after success")
	}
}

func TestApplyDraft_CleansTmpdir_OnSbCheckFail(t *testing.T) {
	o, dir := setupOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	o.enabled[SlotBase] = true
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	_ = o.SaveDraft(SlotRouter, []byte(`{"route":{"final":"direct"}}`))
	o.SetValidator(&fakeValidator{err: errors.New("nope")})
	_, _ = o.ApplyDraft(SlotRouter)
	if tmpDirHasApplyCheck(t, dir) {
		t.Errorf("tmpdir not cleaned up after sb-check fail")
	}
}

// slowValidator blocks until gate is closed.
// ready is closed immediately on entry so callers can synchronise on goroutine #1
// actually being inside Validate (i.e. holding the orchestrator mu).
type slowValidator struct {
	ready chan struct{} // closed when Validate is entered
	gate  chan struct{}
	err   error
}

func (s *slowValidator) Validate(ctx context.Context, dir string) error {
	close(s.ready)
	<-s.gate
	return s.err
}

func TestApplyDraft_ConcurrentSecondCallReturnsNoDraft(t *testing.T) {
	o, dir := setupOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	o.enabled[SlotBase] = true
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)
	_ = o.SaveDraft(SlotRouter, []byte(`{"route":{"final":"direct"}}`))

	sv := &slowValidator{ready: make(chan struct{}), gate: make(chan struct{})}
	o.SetValidator(sv)

	type result struct {
		res ValidationResult
		err error
	}
	first := make(chan result, 1)
	go func() {
		r, e := o.ApplyDraft(SlotRouter)
		first <- result{r, e}
	}()

	// Wait until goroutine #1 is actually inside Validate (holding mu).
	<-sv.ready
	// Release first's validator so it can finish and proceed.
	close(sv.gate)
	r1 := <-first
	if r1.err != nil {
		t.Fatalf("first call: %v", r1.err)
	}

	// Now second call: pending is gone.
	r2, err := o.ApplyDraft(SlotRouter)
	if !errors.Is(err, ErrNoDraft) {
		t.Errorf("second call want ErrNoDraft, got %v (res=%v)", err, r2)
	}
}
