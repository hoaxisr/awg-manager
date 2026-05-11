package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
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
