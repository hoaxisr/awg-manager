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

func TestValidateUnknownOutboundInNestedRule(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"route":{"rules":[{"type":"logical","mode":"or","rules":[{"outbound":"ghost"}]}]}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !strings.Contains(res.Error(), "unknown-outbound") || !strings.Contains(res.Error(), "route.rules[0].rules[0]") {
		t.Errorf("missing nested unknown-outbound: %s", res.Error())
	}
}

func TestValidateUnknownOutboundInDetours(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{
		"route":{"rule_set":[{"tag":"geo","type":"remote","download_detour":"ghost-rs"}]},
		"dns":{"servers":[{"tag":"dns","detour":"ghost-dns"}]}
	}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !strings.Contains(res.Error(), "ghost-rs") || !strings.Contains(res.Error(), "route.rule_set[0=\"geo\"].download_detour") {
		t.Errorf("missing rule_set download_detour error: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "ghost-dns") || !strings.Contains(res.Error(), "dns.servers[0=\"dns\"].detour") {
		t.Errorf("missing dns detour error: %s", res.Error())
	}
}

func TestValidateUnknownRuleSetRefs(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{
		"route":{"rule_set":[{"tag":"known"}],"rules":[{"rule_set":["known","missing-route"]}]},
		"dns":{"rules":[{"rule_set":["missing-dns"]}]}
	}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !strings.Contains(res.Error(), "unknown-rule-set") {
		t.Fatalf("missing unknown-rule-set: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "missing-route") || !strings.Contains(res.Error(), "route.rules[0].rule_set") {
		t.Errorf("missing route rule_set error: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "missing-dns") || !strings.Contains(res.Error(), "dns.rules[0].rule_set") {
		t.Errorf("missing dns rule_set error: %s", res.Error())
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

func TestValidateUnknownDNSFinal(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"dns":{"servers":[{"tag":"real"}],"final":"ghost-dns"}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if res.Ok() {
		t.Fatalf("expected unknown-dns-server error, got ok")
	}
	if !strings.Contains(res.Error(), "unknown-dns-server") {
		t.Errorf("missing unknown-dns-server kind: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "ghost-dns") {
		t.Errorf("missing tag ghost-dns: %s", res.Error())
	}
}

func TestValidateUnknownDefaultDomainResolver(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"route":{"default_domain_resolver":{"server":"ghost-dns"}},"dns":{"servers":[{"tag":"real"}]}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if res.Ok() {
		t.Fatalf("expected unknown-dns-server error, got ok")
	}
	if !strings.Contains(res.Error(), "unknown-dns-server") {
		t.Errorf("missing unknown-dns-server kind: %s", res.Error())
	}
	if !strings.Contains(res.Error(), "ghost-dns") {
		t.Errorf("missing tag ghost-dns: %s", res.Error())
	}
}

func TestValidateKnownDNSRefsAccepted(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// Valid refs: dns.final and default_domain_resolver both point at declared server "real".
	writeSlot(t, dir, "20-router.json", `{"dns":{"servers":[{"tag":"real"}],"final":"real"},"route":{"default_domain_resolver":{"server":"real"}}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !res.Ok() {
		t.Errorf("known DNS refs should be accepted: %s", res.Error())
	}

	// Empty refs (no dns.final, no default_domain_resolver) must also be OK.
	writeSlot(t, dir, "20-router.json", `{"dns":{"servers":[{"tag":"real"}]}}`)
	res = o.Validate()
	if !res.Ok() {
		t.Errorf("omitted DNS refs should be accepted: %s", res.Error())
	}
}

func TestValidateKnownDNSRefsCrossSlot(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// SlotBase declares the "real" DNS server; SlotRouter references it in dns.final.
	writeSlot(t, dir, "00-base.json", `{"dns":{"servers":[{"tag":"real"}]}}`)
	writeSlot(t, dir, "20-router.json", `{"dns":{"final":"real"}}`)
	o.enabled[SlotBase] = true
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !res.Ok() {
		t.Errorf("cross-slot DNS ref should be accepted: %s", res.Error())
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

// 00-base.json carries default_domain_resolver as a BARE STRING (the server
// tag), not an object. The validator must accept both forms — a string failing
// to unmarshal previously failed parsing of the whole slot and silently skipped
// every reload (stand-caught 2026-06-18).
func TestValidateDefaultDomainResolverStringForm(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true})
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	// base: resolver as bare string referencing its own declared server.
	writeSlot(t, dir, "00-base.json", `{"dns":{"servers":[{"tag":"dns-bootstrap"}]},"route":{"default_domain_resolver":"dns-bootstrap"}}`)
	writeSlot(t, dir, "20-router.json", `{"route":{"final":"direct"}}`)
	o.enabled[SlotBase] = true
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if !res.Ok() {
		t.Fatalf("string-form default_domain_resolver must validate, got: %v", res.Error())
	}
}

// The string form is still checked as a DNS-tag reference: a bare-string
// resolver naming an undeclared server must fail.
func TestValidateDefaultDomainResolverStringForm_Unknown(t *testing.T) {
	o, dir := newTestOrch(t)
	_ = o.Register(SlotMeta{Slot: SlotRouter, Filename: "20-router.json"})
	if err := o.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	writeSlot(t, dir, "20-router.json", `{"dns":{"servers":[{"tag":"real"}]},"route":{"default_domain_resolver":"ghost-dns"}}`)
	o.enabled[SlotRouter] = true
	res := o.Validate()
	if res.Ok() || !strings.Contains(res.Error(), "unknown-dns-server") {
		t.Fatalf("bare-string resolver to unknown server must fail unknown-dns-server, got: %v", res.Error())
	}
}
