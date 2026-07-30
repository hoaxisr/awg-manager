package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #633: CheckMerged must predict the outcome of a real Reload. Reload
// self-heals dangling selector members/defaults in non-user slots via
// pruneDanglingSelectorRefsLocked BEFORE validating — so a sibling slot
// still referencing a tag the candidate just removed (device-proxy selector
// → deleted subscription outbound) must not fail CheckMerged. Without the
// virtual prune, subscription delete 500s with "could not isolate outbound"
// and the subscription becomes undeletable.
func setupPruneOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil)
	for _, m := range []SlotMeta{
		{Slot: SlotDeviceProxy, Filename: "30-deviceproxy.json"},
		{Slot: SlotSubscriptions, Filename: "40-subscriptions.json"},
		{Slot: SlotUser, Filename: "90-user.json"},
	} {
		if err := o.Register(m); err != nil {
			t.Fatalf("register %s: %v", m.Slot, err)
		}
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return o, dir
}

func writeSlotFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestCheckMerged_DanglingSiblingSelectorMemberIsPruned(t *testing.T) {
	o, dir := setupPruneOrch(t)
	o.enabled[SlotDeviceProxy] = true
	// Device-proxy selector references sub-x (a subscription outbound) as
	// member AND default — the reporter's exact shape.
	writeSlotFile(t, dir, "30-deviceproxy.json",
		`{"outbounds":[{"type":"selector","tag":"proxy-out","outbounds":["direct","sub-x"],"default":"sub-x"}]}`)

	// Candidate: subscriptions slot after deleting the sub-x subscription.
	res, err := o.CheckMerged(SlotSubscriptions, []byte(`{"outbounds":[{"type":"vless","tag":"sub-y"}]}`))
	if err != nil {
		t.Fatalf("CheckMerged: %v", err)
	}
	if !res.Ok() {
		t.Fatalf("expected Ok — Reload would prune the dangling member; got %+v", res.Errors)
	}
}

func TestCheckMerged_AllDanglingSelectorStillFails(t *testing.T) {
	// Prune never empties a selector (surviving-member guard) — a selector
	// whose EVERY member is dangling stays broken on a real Reload too, so
	// CheckMerged must keep reporting it.
	o, dir := setupPruneOrch(t)
	o.enabled[SlotDeviceProxy] = true
	writeSlotFile(t, dir, "30-deviceproxy.json",
		`{"outbounds":[{"type":"selector","tag":"proxy-out","outbounds":["sub-x"]}]}`)

	res, err := o.CheckMerged(SlotSubscriptions, []byte(`{"outbounds":[{"type":"vless","tag":"sub-y"}]}`))
	if err != nil {
		t.Fatalf("CheckMerged: %v", err)
	}
	if res.Ok() {
		t.Fatal("expected failure: all-dangling selector is not prunable, real Reload fails too")
	}
}

func TestCheckMerged_UserSlotDanglingRefStillFails(t *testing.T) {
	// 90-user.json is never pruned (user edits are not silently mutated) —
	// a dangling ref there fails the real Reload, so CheckMerged keeps it.
	o, dir := setupPruneOrch(t)
	o.enabled[SlotUser] = true
	writeSlotFile(t, dir, "90-user.json",
		`{"outbounds":[{"type":"selector","tag":"my-sel","outbounds":["direct","sub-x"]}]}`)

	res, err := o.CheckMerged(SlotSubscriptions, []byte(`{"outbounds":[{"type":"vless","tag":"sub-y"}]}`))
	if err != nil {
		t.Fatalf("CheckMerged: %v", err)
	}
	if res.Ok() {
		t.Fatal("expected failure: user slot is never pruned")
	}
}

func TestCheckMerged_DanglingRefInCandidateStillReported(t *testing.T) {
	// The candidate itself is validated as-is: the subscription flush relies
	// on unknown-outbound errors for ITS slot to self-heal dangling group
	// members (cleanCrossSlotUnknownRefs). Virtual prune must not hide them.
	o, _ := setupPruneOrch(t)

	res, err := o.CheckMerged(SlotSubscriptions,
		[]byte(`{"outbounds":[{"type":"selector","tag":"agg","outbounds":["direct","gone"]}]}`))
	if err != nil {
		t.Fatalf("CheckMerged: %v", err)
	}
	if res.Ok() {
		t.Fatal("expected unknown-outbound for the candidate's own dangling ref")
	}
	found := false
	for _, e := range res.Errors {
		if e.Slot == SlotSubscriptions && e.Kind == "unknown-outbound" && e.Tag == "gone" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected candidate-slot unknown-outbound for 'gone', got %+v", res.Errors)
	}
}

func TestCheckMerged_SiblingRouteRuleRefStillFails(t *testing.T) {
	// Prune heals only selector members/defaults. A route rule in a sibling
	// slot pointing at the removed tag is NOT prunable — real Reload fails,
	// so CheckMerged must too.
	o, dir := setupPruneOrch(t)
	o.enabled[SlotDeviceProxy] = true
	writeSlotFile(t, dir, "30-deviceproxy.json",
		`{"route":{"rules":[{"inbound":["px-in"],"outbound":"sub-x"}]}}`)

	res, err := o.CheckMerged(SlotSubscriptions, []byte(`{"outbounds":[{"type":"vless","tag":"sub-y"}]}`))
	if err != nil {
		t.Fatalf("CheckMerged: %v", err)
	}
	if res.Ok() {
		t.Fatal("expected failure: route-rule refs are not prunable")
	}
}
