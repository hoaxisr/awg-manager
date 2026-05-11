package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSlot(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

func TestValidateOk(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json"})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "10-tunnels.json", `{"outbounds":[{"tag":"vpn1"}]}`)
	writeSlot(t, dir, "20-router.json", `{"outbounds":[{"tag":"sel","outbounds":["vpn1","direct"],"default":"vpn1"}],"route":{"rules":[{"outbound":"sel"}],"final":"direct"}}`)
	o.enabled[SlotTunnels] = true
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !res.Ok() {
		t.Errorf("expected ok, got: %v", res.Error())
	}
}

func TestValidateDuplicateOutbound(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json"})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "10-tunnels.json", `{"outbounds":[{"tag":"vpn1"}]}`)
	writeSlot(t, dir, "20-router.json", `{"outbounds":[{"tag":"vpn1"}]}`)
	o.enabled[SlotTunnels] = true
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if res.Ok() {
		t.Fatalf("expected dup error")
	}
	if !strings.Contains(res.Error(), "duplicate-outbound") {
		t.Errorf("missing duplicate-outbound: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "vpn1") {
		t.Errorf("missing tag in error: %s", res.Error())
	}
}

func TestValidateDuplicateInbound(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	_ = o.Register(SlotMeta{Slot: SlotDeviceProxy, Filename: "30-deviceproxy.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"inbounds":[{"tag":"tproxy-in"}]}`)
	writeSlot(t, dir, "30-deviceproxy.json", `{"inbounds":[{"tag":"tproxy-in"}]}`)
	o.enabled[SlotRouter] = true
	o.enabled[SlotDeviceProxy] = true
	res := o.Validate()
	if !strings.Contains(res.Error(), "duplicate-inbound") {
		t.Errorf("missing duplicate-inbound: %s", res.Error())
	}
}

func TestValidateUnknownOutboundInRule(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"route":{"rules":[{"outbound":"ghost"}]}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !strings.Contains(res.Error(), "unknown-outbound") {
		t.Errorf("missing unknown-outbound: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "ghost") {
		t.Errorf("missing tag: %s", res.Error())
	}
}

func TestValidateBuiltinOutboundsAccepted(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"route":{"rules":[{"outbound":"direct"},{"outbound":"block"},{"outbound":"dns"}]}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !res.Ok() {
		t.Errorf("builtins should be accepted: %s", res.Error())
	}
}

func TestValidateDisabledSlotsIgnored(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotTunnels, Filename: "10-tunnels.json"})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Both files have "vpn1", but tunnels is in disabled/ → skipped.
	writeSlot(t, filepath.Join(dir, "disabled"), "10-tunnels.json", `{"outbounds":[{"tag":"vpn1"}]}`)
	writeSlot(t, dir, "20-router.json", `{"outbounds":[{"tag":"vpn1"}]}`)
	o.enabled[SlotRouter] = true
	// SlotTunnels stays disabled (default).
	res := o.Validate()
	if !res.Ok() {
		t.Errorf("disabled slot should not contribute: %s", res.Error())
	}
}

func TestValidateSelectorDefaultUnknown(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"outbounds":[{"tag":"sel","outbounds":["direct"],"default":"missing"}]}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !strings.Contains(res.Error(), "unknown-outbound") {
		t.Errorf("expected unknown-outbound for default: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "missing") {
		t.Errorf("missing tag: %s", res.Error())
	}
}

func TestValidateDraftLocked_SwapsTargetSlot(t *testing.T) {
	dir := t.TempDir()
	o := New(dir, nil)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	o.enabled[SlotBase] = true
	o.enabled[SlotRouter] = true

	// Active 20-router.json declares outbound tag "live-X"
	active := []byte(`{"outbounds":[{"tag":"live-X","type":"direct"}]}`)
	_ = os.WriteFile(filepath.Join(dir, "20-router.json"), active, 0644)
	// 00-base declares "direct"
	base := []byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`)
	_ = os.WriteFile(filepath.Join(dir, "00-base.json"), base, 0644)

	// Draft replaces 20-router content with one referring to a new tag "draft-Y"
	// and a route.final referencing it.
	draft := []byte(`{"outbounds":[{"tag":"draft-Y","type":"direct"}],"route":{"final":"draft-Y"}}`)

	o.mu.Lock()
	res := o.validateDraftLocked(SlotRouter, draft)
	o.mu.Unlock()

	if !res.Ok() {
		t.Fatalf("draft validation should be ok (draft-Y is self-defined), got: %s", res.Error())
	}

	// Negative: draft references ghost tag.
	badDraft := []byte(`{"route":{"final":"ghost"}}`)
	o.mu.Lock()
	res = o.validateDraftLocked(SlotRouter, badDraft)
	o.mu.Unlock()

	if res.Ok() {
		t.Fatalf("draft validation should fail on ghost ref, got ok")
	}
	found := false
	for _, e := range res.Errors {
		if e.Kind == "unknown-outbound" && e.Tag == "ghost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown-outbound 'ghost', got: %s", res.Error())
	}
}

func TestValidateDraftLocked_DetectsDuplicateAcrossSlots(t *testing.T) {
	dir := t.TempDir()
	o := New(dir, nil)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	_ = o.Bootstrap()
	o.enabled[SlotBase] = true
	o.enabled[SlotRouter] = true

	_ = os.WriteFile(filepath.Join(dir, "00-base.json"),
		[]byte(`{"outbounds":[{"tag":"direct","type":"direct"}]}`), 0644)

	// Draft tries to introduce another "direct" outbound. Collision.
	draft := []byte(`{"outbounds":[{"tag":"direct","type":"direct","bind_interface":"eth0"}]}`)

	o.mu.Lock()
	res := o.validateDraftLocked(SlotRouter, draft)
	o.mu.Unlock()

	if res.Ok() {
		t.Fatalf("expected duplicate-outbound, got ok")
	}
	found := false
	for _, e := range res.Errors {
		if e.Kind == "duplicate-outbound" && e.Tag == "direct" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected duplicate-outbound 'direct', got: %s", res.Error())
	}
}
