package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// setupAttrOrch registers tunnels (10-, two outbounds) and subscriptions
// (40-) slots; tunnels is enabled with an active file so the CheckMerged
// snapshot has outbounds lexically before the target slot.
func setupAttrOrch(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	o := New(dir, nil)
	if err := o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true}); err != nil {
		t.Fatalf("register tunnels: %v", err)
	}
	if err := o.Register(SlotMeta{Slot: SlotSubscriptions, Filename: "13-subscriptions.json"}); err != nil {
		t.Fatalf("register subscriptions: %v", err)
	}
	if err := o.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	o.enabled[SlotTunnels] = true
	tunnels := []byte(`{"outbounds":[{"type":"direct","tag":"d1"},{"type":"direct","tag":"d2"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "10-tunnels.json"), tunnels, 0o644); err != nil {
		t.Fatalf("write tunnels: %v", err)
	}
	return o, dir
}

// subBytes is a target slot with two outbounds (local indexes 0 and 1).
var subBytes = []byte(`{"outbounds":[{"type":"vless","tag":"s0"},{"type":"vless","tag":"s1"}]}`)

func checkMergedWithError(t *testing.T, msg string) ValidationResult {
	t.Helper()
	o, _ := setupAttrOrch(t)
	o.SetValidator(&fakeValidator{err: errors.New(msg)})
	res, err := o.CheckMerged(SlotSubscriptions, subBytes)
	if err != nil {
		t.Fatalf("CheckMerged: %v", err)
	}
	if res.Ok() || len(res.Errors) != 1 {
		t.Fatalf("expected exactly 1 validation error, got %+v", res.Errors)
	}
	return res
}

func TestCheckMerged_AttributesInitializeIndexToTargetSlot(t *testing.T) {
	// Merged outbounds: [d1, d2, s0, s1] — sing-box reports the index in
	// the merged array (lexical filename order), so outbound[2] is s0,
	// local index 0 of the subscriptions slot.
	res := checkMergedWithError(t,
		"sing-box check failed: FATAL[0000] initialize outbound[2]: uTLS is required by reality client: exit status 1")
	e := res.Errors[0]
	if e.OutboundIndex == nil {
		t.Fatal("expected attributed OutboundIndex, got nil")
	}
	if e.OutboundSlot != SlotSubscriptions || *e.OutboundIndex != 0 {
		t.Errorf("attributed to (%s, %d), want (subscriptions, 0)", e.OutboundSlot, *e.OutboundIndex)
	}
}

func TestCheckMerged_AttributesInitializeIndexToSiblingSlot(t *testing.T) {
	// outbound[1] is d2 — an outbound of the tunnels slot, not ours.
	res := checkMergedWithError(t,
		"sing-box check failed: FATAL[0000] initialize outbound[1]: whatever: exit status 1")
	e := res.Errors[0]
	if e.OutboundIndex == nil {
		t.Fatal("expected attributed OutboundIndex, got nil")
	}
	if e.OutboundSlot != SlotTunnels || *e.OutboundIndex != 1 {
		t.Errorf("attributed to (%s, %d), want (tunnels, 1)", e.OutboundSlot, *e.OutboundIndex)
	}
}

func TestCheckMerged_AttributesDecodeIndex(t *testing.T) {
	// Decode errors carry the slot filename and a per-file index — no
	// offset translation. Message shape captured from issue #350.
	res := checkMergedWithError(t,
		"sing-box check failed: FATAL[0000] decode config at /tmp/.save-check-1/13-subscriptions.json: outbounds[1].tls.certificate_public_key_sha256: (illegal base64 data at input byte 4 | json: cannot unmarshal string into Go value of type [][]uint8)\n: exit status 1")
	e := res.Errors[0]
	if e.OutboundIndex == nil {
		t.Fatal("expected attributed OutboundIndex, got nil")
	}
	if e.OutboundSlot != SlotSubscriptions || *e.OutboundIndex != 1 {
		t.Errorf("attributed to (%s, %d), want (subscriptions, 1)", e.OutboundSlot, *e.OutboundIndex)
	}
}

func TestCheckMerged_AttributesDecodeIndexToSiblingFile(t *testing.T) {
	res := checkMergedWithError(t,
		"sing-box check failed: FATAL[0000] decode config at /tmp/.save-check-1/10-tunnels.json: outbounds[0].type: unknown type\n: exit status 1")
	e := res.Errors[0]
	if e.OutboundIndex == nil {
		t.Fatal("expected attributed OutboundIndex, got nil")
	}
	if e.OutboundSlot != SlotTunnels || *e.OutboundIndex != 0 {
		t.Errorf("attributed to (%s, %d), want (tunnels, 0)", e.OutboundSlot, *e.OutboundIndex)
	}
}

func TestCheckMerged_NoAttributionOnUnknownMessage(t *testing.T) {
	res := checkMergedWithError(t,
		"sing-box check failed: FATAL[0000] something completely different: exit status 1")
	if res.Errors[0].OutboundIndex != nil {
		t.Errorf("expected nil OutboundIndex, got %d in slot %s",
			*res.Errors[0].OutboundIndex, res.Errors[0].OutboundSlot)
	}
}

func TestCheckMerged_NoAttributionOnOutOfRangeIndex(t *testing.T) {
	// Merged array has 4 outbounds; index 9 fits no slot.
	res := checkMergedWithError(t,
		"sing-box check failed: FATAL[0000] initialize outbound[9]: whatever: exit status 1")
	if res.Errors[0].OutboundIndex != nil {
		t.Errorf("expected nil OutboundIndex, got %d", *res.Errors[0].OutboundIndex)
	}
}
