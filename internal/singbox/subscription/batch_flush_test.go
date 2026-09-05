package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

type countingValidator struct{ calls int }

func (c *countingValidator) Validate(_ context.Context, _ string) error { c.calls++; return nil }

func validVlessJSON(server string) []byte {
	j, _ := json.Marshal(map[string]any{
		"type":        "vless",
		"server":      server,
		"server_port": float64(443),
		"uuid":        "3a3b1c2e-9999-4321-aaaa-1234567890ab",
	})
	return j
}

// #331: materialising N members must run the sing-box validation (and the
// disk save / reload) ONCE at commit, not once per member. Per-member flush
// was O(N^2) sing-box checks — 15 min + pinned CPU for a 199-server sub.
func TestOperatorAdapter_BatchesValidationToOneFlush(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	cv := &countingValidator{}
	orch.SetValidator(cv)
	adapter := NewOperatorAdapter(orch, nil, nil)

	const n = 10
	for i := 0; i < n; i++ {
		if err := adapter.AddOutbound(fmt.Sprintf("sub-x-%d", i), validVlessJSON(fmt.Sprintf("10.0.0.%d", i+1))); err != nil {
			t.Fatalf("AddOutbound %d: %v", i, err)
		}
	}
	if cv.calls != 0 {
		t.Errorf("no validation should run during adds, got %d", cv.calls)
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatalf("Reload (commit): %v", err)
	}
	if cv.calls != 1 {
		t.Errorf("want exactly 1 validation for %d adds, got %d", n, cv.calls)
	}
	if got := len(adapter.DeclaredOutboundTags()); got != n {
		t.Errorf("committed outbounds = %d, want %d", got, n)
	}
}

// Rollback discards an uncommitted batch, restoring the previously committed
// in-memory config so a failed Create can't leave a partial that the next
// operation would commit.
func TestOperatorAdapter_RollbackRestoresCommitted(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	adapter := NewOperatorAdapter(orch, nil, nil)

	if err := adapter.AddOutbound("sub-x-0", validVlessJSON("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	// New uncommitted batch.
	if err := adapter.AddOutbound("sub-x-1", validVlessJSON("10.0.0.2")); err != nil {
		t.Fatal(err)
	}
	if got := len(adapter.DeclaredOutboundTags()); got != 2 {
		t.Fatalf("pending batch should show 2 in-memory, got %d", got)
	}
	adapter.Rollback()
	if got := len(adapter.DeclaredOutboundTags()); got != 1 {
		t.Errorf("after rollback want 1 (committed), got %d", got)
	}
}

// #331 regression: deleting the last subscription removes every outbound,
// emptying the slot. Committing that teardown must SUCCEED — the "no valid
// outbounds left" guard is for an additive Create/Refresh where every server
// was dropped as invalid, NOT for a deliberate emptying. If commit errors,
// deleteLocked's Reload-error check blocks store.Delete AND Reload restores the
// just-deleted config, making the last subscription undeletable.
func TestOperatorAdapter_CommitEmptySlotOnDelete(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	orch.SetValidator(&countingValidator{})
	adapter := NewOperatorAdapter(orch, nil, nil)

	// Materialise the only subscription member and commit it.
	if err := adapter.AddOutbound("sub-x-0", validVlessJSON("10.0.0.1")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	// Delete it: remove the only outbound, then commit the teardown.
	if err := adapter.RemoveOutbound("sub-x-0"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatalf("delete-to-empty commit must succeed, got: %v", err)
	}
	if got := len(adapter.DeclaredOutboundTags()); got != 0 {
		t.Errorf("slot must be empty after delete, got %d outbound(s) (config resurrected?)", got)
	}
}

// offsetValidator simulates `sing-box check -C`: it walks config files in
// lexical filename order (sing-box merge order) and FATALs with the index
// in the MERGED outbounds array while an outbound tagged "bad" is present.
type offsetValidator struct{}

func (offsetValidator) Validate(_ context.Context, dir string) error {
	entries, err := os.ReadDir(dir) // ReadDir sorts by filename
	if err != nil {
		return err
	}
	idx := 0
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		var c struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		for _, ob := range c.Outbounds {
			if ob["tag"] == "bad" {
				return fmt.Errorf("sing-box check failed: FATAL[0000] initialize outbound[%d]: uTLS is required by reality client: exit status 1", idx)
			}
			idx++
		}
	}
	return nil
}

// Issue #350 follow-up: sing-box initialize errors index the MERGED
// outbounds array, so with outbounds in a lexically-earlier slot (15-awg
// catalog, sing-box tunnels) the raw index pointed past or at the wrong
// outbound of our slot — Pass 2 dropped valid servers until out-of-range
// failed the whole subscription. orchestrator.CheckMerged now attributes
// the index to (slot, local index); flush must drop exactly the bad one.
func TestFlush_DropsOutboundByMergedInitializeIndex(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.NewWithAppliedPath(dir, nil, filepath.Join(t.TempDir(), "singbox-applied.json"))
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Two outbounds in 10-tunnels.json — lexically before 40-subscriptions.json,
	// shifting merged indexes of our outbounds by 2.
	if err := orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true,
	}); err != nil {
		t.Fatal(err)
	}
	tunnels := []byte(`{"outbounds":[{"type":"direct","tag":"d1"},{"type":"direct","tag":"d2"}]}`)
	if err := orch.Save(orchestrator.SlotTunnels, tunnels); err != nil {
		t.Fatal(err)
	}
	orch.SetValidator(offsetValidator{})
	adapter := NewOperatorAdapter(orch, nil, nil)

	for i, tag := range []string{"s0", "bad", "s2"} {
		if err := adapter.AddOutbound(tag, validVlessJSON(fmt.Sprintf("192.0.2.%d", i+1))); err != nil {
			t.Fatalf("AddOutbound %s: %v", tag, err)
		}
	}
	if err := adapter.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	tags := adapter.DeclaredOutboundTags()
	if len(tags) != 2 || tags[0] != "s0" || tags[1] != "s2" {
		t.Errorf("kept tags = %v, want [s0 s2] (only the attributed outbound dropped)", tags)
	}
}
