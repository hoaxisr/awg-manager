package router

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── Port allocation ───────────────────────────────────────────────

func TestQoSClassPorts_NoCollisions(t *testing.T) {
	reserved := map[int]string{
		TPROXYPort:   "TPROXYPort",
		RedirectPort: "RedirectPort",
	}
	seen := map[int]int{}
	for i := 0; i < MaxQoSClasses; i++ {
		tp, rp := QoSClassPorts(i)
		if owner, clash := reserved[tp]; clash {
			t.Errorf("QoSClassPorts(%d) tproxy=%d collides with %s", i, tp, owner)
		}
		if owner, clash := reserved[rp]; clash {
			t.Errorf("QoSClassPorts(%d) redirect=%d collides with %s", i, rp, owner)
		}
		if tp == rp {
			t.Errorf("QoSClassPorts(%d): tproxy and redirect port equal (%d)", i, tp)
		}
		if prev, dup := seen[tp]; dup {
			t.Errorf("tproxy port %d reused by classes %d and %d", tp, prev, i)
		}
		if prev, dup := seen[rp]; dup {
			t.Errorf("redirect port %d reused by classes %d and %d", rp, prev, i)
		}
		seen[tp] = i
		seen[rp] = i
	}
	// Deterministic bases documented for the frontend/iptables contract.
	if tp, rp := QoSClassPorts(0); tp != 51281 || rp != 51301 {
		t.Errorf("QoSClassPorts(0) = (%d, %d), want (51281, 51301)", tp, rp)
	}
}

func TestQoSTags(t *testing.T) {
	if qosTProxyTag(46) != "tproxy-qos-46" {
		t.Errorf("qosTProxyTag(46) = %q", qosTProxyTag(46))
	}
	if qosRedirectTag(46) != "redirect-qos-46" {
		t.Errorf("qosRedirectTag(46) = %q", qosRedirectTag(46))
	}
	for _, tag := range []string{"tproxy-qos-0", "redirect-qos-63"} {
		if !isQoSInboundTag(tag) {
			t.Errorf("isQoSInboundTag(%q) = false", tag)
		}
	}
	for _, tag := range []string{"tproxy-in", "redirect-in", "qos-46", ""} {
		if isQoSInboundTag(tag) {
			t.Errorf("isQoSInboundTag(%q) = true", tag)
		}
	}
}

// ── activeQoSClasses (defensive filtering + slot-derived ports) ──

func TestActiveQoSClasses_FiltersAndUsesSlotPorts(t *testing.T) {
	classes := []storage.SingboxQoSClass{
		{DSCP: 46, Name: "voip", Outbound: "vpn-a", Enabled: true, Slot: 3},
		{DSCP: 10, Outbound: "vpn-b", Enabled: false, Slot: 1},            // disabled → dropped
		{DSCP: 99, Outbound: "vpn-c", Enabled: true, Slot: 2},             // DSCP out of range → dropped
		{DSCP: 8, Outbound: "   ", Enabled: true, Slot: 4},                // empty outbound → dropped
		{DSCP: 46, Outbound: "vpn-dup", Enabled: true, Slot: 5},           // duplicate DSCP → dropped (first wins)
		{DSCP: 26, Outbound: "  vpn-d  ", Enabled: true, Slot: 0},         // trimmed; ports from ITS slot, not position
		{DSCP: -1, Outbound: "vpn-e", Enabled: true, Slot: 6},             // negative DSCP → dropped
		{DSCP: 20, Outbound: "vpn-f", Enabled: true, Slot: 3},             // duplicate slot → dropped (first wins)
		{DSCP: 21, Outbound: "vpn-g", Enabled: true, Slot: MaxQoSClasses}, // slot out of range → dropped
		{DSCP: 22, Outbound: "vpn-h", Enabled: true, Slot: -1},            // negative slot → dropped
	}
	got := activeQoSClasses(classes)
	want := []qosClass{
		{DSCP: 46, Outbound: "vpn-a", TProxyPort: 51284, RedirectPort: 51304},
		{DSCP: 26, Outbound: "vpn-d", TProxyPort: 51281, RedirectPort: 51301},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activeQoSClasses = %+v, want %+v", got, want)
	}
}

// TestActiveQoSClasses_PortsStableAcrossDisable is the FIX-3 invariant: a
// class keeps its slot ports when a preceding class is disabled or removed —
// positional assignment would shift them and RST/blackhole untouched flows.
func TestActiveQoSClasses_PortsStableAcrossDisable(t *testing.T) {
	all := []storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Enabled: true, Slot: 0},
		{DSCP: 20, Outbound: "b", Enabled: true, Slot: 1},
		{DSCP: 30, Outbound: "c", Enabled: true, Slot: 2},
	}
	before := activeQoSClasses(all)

	disabledB := append([]storage.SingboxQoSClass(nil), all...)
	disabledB[1].Enabled = false
	after := activeQoSClasses(disabledB)
	if len(after) != 2 {
		t.Fatalf("expected 2 active classes, got %+v", after)
	}
	if after[1].DSCP != 30 || after[1].TProxyPort != before[2].TProxyPort || after[1].RedirectPort != before[2].RedirectPort {
		t.Errorf("class C ports shifted after disabling B: before=%+v after=%+v", before[2], after[1])
	}

	removedB := []storage.SingboxQoSClass{all[0], all[2]}
	afterRemove := activeQoSClasses(removedB)
	if afterRemove[1].TProxyPort != before[2].TProxyPort {
		t.Errorf("class C ports shifted after removing B: %+v", afterRemove[1])
	}
}

func TestQoSIPTablesSpecs(t *testing.T) {
	if qosIPTablesSpecs(nil) != nil {
		t.Error("empty classes must project to nil specs")
	}
	specs := qosIPTablesSpecs([]qosClass{{DSCP: 46, Outbound: "vpn", TProxyPort: 51281, RedirectPort: 51301}})
	want := []QoSClassSpec{{DSCP: 46, TProxyPort: 51281, RedirectPort: 51301}}
	if !slices.Equal(specs, want) {
		t.Fatalf("specs = %+v, want %+v", specs, want)
	}
}

// ── Inbound reconcile ─────────────────────────────────────────────

func TestEnsureQoSInbounds_AddsCanonicalPairs(t *testing.T) {
	base := []Inbound{
		{Type: "redirect", Tag: "redirect-in", Listen: "0.0.0.0", ListenPort: RedirectPort, TCPFastOpen: true},
		{Type: "tproxy", Tag: "tproxy-in", Listen: "0.0.0.0", ListenPort: TPROXYPort, Network: "udp"},
	}
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn", Enabled: true},
	})
	got, changed := ensureQoSInbounds(base, classes, "10m0s")
	if !changed {
		t.Fatal("expected changed=true when adding class inbounds")
	}
	// Main inbounds untouched, in order.
	if got[0].Tag != "redirect-in" || got[1].Tag != "tproxy-in" {
		t.Fatalf("main inbounds disturbed: %+v", got)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 inbounds, got %d: %+v", len(got), got)
	}
	tp := got[2]
	if tp.Type != "tproxy" || tp.Tag != "tproxy-qos-46" || tp.ListenPort != 51281 ||
		tp.Network != "udp" || !tp.UDPFragment || tp.UDPTimeout != "10m0s" || tp.Listen != "127.0.0.1" {
		t.Errorf("qos tproxy inbound not canonical: %+v", tp)
	}
	rd := got[3]
	if rd.Type != "redirect" || rd.Tag != "redirect-qos-46" || rd.ListenPort != 51301 ||
		!rd.TCPFastOpen || rd.Listen != "0.0.0.0" {
		t.Errorf("qos redirect inbound not canonical: %+v", rd)
	}
}

func TestEnsureQoSInbounds_RemovesStaleAndConverges(t *testing.T) {
	// Stale artifacts: old class 10 removed, class 46 drifted port/timeout.
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", ListenPort: TPROXYPort},
		{Type: "tproxy", Tag: "tproxy-qos-10", ListenPort: 51282},
		{Type: "redirect", Tag: "redirect-qos-10", ListenPort: 51302},
		{Type: "tproxy", Tag: "tproxy-qos-46", ListenPort: 59999, UDPTimeout: "1m0s"},
	}
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn", Enabled: true},
	})
	got, changed := ensureQoSInbounds(in, classes, "")
	if !changed {
		t.Fatal("expected changed=true")
	}
	tags := make([]string, 0, len(got))
	for _, i := range got {
		tags = append(tags, i.Tag)
	}
	want := []string{"tproxy-in", "tproxy-qos-46", "redirect-qos-46"}
	if !slices.Equal(tags, want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	if got[1].ListenPort != 51281 || got[1].UDPTimeout != DefaultUDPTimeout {
		t.Errorf("drifted qos inbound not healed: %+v", got[1])
	}

	// Second pass over the converged result: no change.
	again, changed2 := ensureQoSInbounds(got, classes, "")
	if changed2 {
		t.Errorf("expected idempotent second pass, got change: %+v", again)
	}
}

func TestEnsureQoSInbounds_NoClasses_RemovesAll(t *testing.T) {
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in"},
		{Type: "tproxy", Tag: "tproxy-qos-46"},
		{Type: "redirect", Tag: "redirect-qos-46"},
	}
	got, changed := ensureQoSInbounds(in, nil, "")
	if !changed {
		t.Fatal("expected changed=true when removing stale qos inbounds")
	}
	if len(got) != 1 || got[0].Tag != "tproxy-in" {
		t.Fatalf("got %+v", got)
	}

	// Empty in, no classes → no phantom change.
	if _, changed := ensureQoSInbounds(nil, nil, ""); changed {
		t.Error("nil inbounds + no classes must not report change")
	}
	if _, changed := ensureQoSInbounds([]Inbound{}, nil, ""); changed {
		t.Error("empty inbounds + no classes must not report change")
	}
}

// ── Slot assignment (normalize + PUT re-association) ─────────────

func TestNormalizeQoSClasses_PreservesAndRepairsSlots(t *testing.T) {
	got := normalizeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Slot: 2},             // valid → preserved
		{DSCP: 20, Outbound: "b", Slot: 2},             // duplicate → first free (0)
		{DSCP: 30, Outbound: "c", Slot: MaxQoSClasses}, // out of range → first free (1)
		{DSCP: 40, Outbound: "d", Slot: -3},            // negative → first free (3)
	})
	wantSlots := []int{2, 0, 1, 3}
	for i, w := range wantSlots {
		if got[i].Slot != w {
			t.Errorf("class %d slot = %d, want %d (%+v)", i, got[i].Slot, w, got)
		}
	}
	// Idempotent: re-normalizing the repaired slice is a fixed point.
	again := normalizeQoSClasses(got)
	if !reflect.DeepEqual(got, again) {
		t.Errorf("normalize not idempotent: %+v vs %+v", got, again)
	}
}

func TestReassociateQoSSlots_CarriesSlotsByDSCP(t *testing.T) {
	stored := []storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Enabled: true, Slot: 0},
		{DSCP: 20, Outbound: "b", Enabled: true, Slot: 1},
		{DSCP: 30, Outbound: "c", Enabled: true, Slot: 2},
	}
	// Frontend PUTs the whole array WITHOUT slots (all zero), reordered and
	// with class 20 removed + a new class 40 added.
	incoming := []storage.SingboxQoSClass{
		{DSCP: 30, Outbound: "c", Enabled: true},
		{DSCP: 40, Outbound: "d", Enabled: true},
		{DSCP: 10, Outbound: "a", Enabled: false}, // disabled but keeps its slot
	}
	got := reassociateQoSSlots(incoming, stored)
	if got[0].Slot != 2 {
		t.Errorf("class 30 must keep slot 2, got %d", got[0].Slot)
	}
	if got[2].Slot != 0 {
		t.Errorf("class 10 must keep slot 0 even while disabled, got %d", got[2].Slot)
	}
	// New DSCP gets the FREED slot (1, vacated by removed class 20).
	if got[1].Slot != 1 {
		t.Errorf("new class 40 must get freed slot 1, got %d", got[1].Slot)
	}
}

func TestReassociateQoSSlots_DisableReEnableKeepsPorts(t *testing.T) {
	stored := []storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Enabled: true, Slot: 0},
		{DSCP: 20, Outbound: "b", Enabled: true, Slot: 1},
	}
	// PUT 1: disable class 10 (no slots on the wire).
	put1 := reassociateQoSSlots([]storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Enabled: false},
		{DSCP: 20, Outbound: "b", Enabled: true},
	}, stored)
	// PUT 2: re-enable it.
	put2 := reassociateQoSSlots([]storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Enabled: true},
		{DSCP: 20, Outbound: "b", Enabled: true},
	}, put1)
	if put2[0].Slot != 0 || put2[1].Slot != 1 {
		t.Errorf("slots drifted across disable/re-enable: %+v", put2)
	}
	tp, _ := QoSClassPorts(put2[1].Slot)
	if tp != 51282 {
		t.Errorf("class 20 tproxy port = %d, want 51282", tp)
	}
}

func TestReassociateQoSSlots_IgnoresIncomingSlots(t *testing.T) {
	stored := []storage.SingboxQoSClass{{DSCP: 10, Outbound: "a", Enabled: true, Slot: 3}}
	// A client echoing GET data back sends slot values — they must be
	// ignored in favour of the stored association.
	got := reassociateQoSSlots([]storage.SingboxQoSClass{
		{DSCP: 10, Outbound: "a", Enabled: true, Slot: 7},
		{DSCP: 20, Outbound: "b", Enabled: true, Slot: 3},
	}, stored)
	if got[0].Slot != 3 {
		t.Errorf("class 10 must keep stored slot 3, got %d", got[0].Slot)
	}
	if got[1].Slot == 3 || got[1].Slot < 0 || got[1].Slot >= MaxQoSClasses {
		t.Errorf("new class must get a free valid slot, got %d", got[1].Slot)
	}
}

// ── QoS routes slot (18-qos-routes.json) ──────────────────────────

func TestBuildQoSRouteRules_CanonicalAndDeterministic(t *testing.T) {
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
		{DSCP: 26, Outbound: "vpn-b", Enabled: true, Slot: 1},
	})
	rules := buildQoSRouteRules(classes, storage.SingboxRouterSettings{})
	// +1 for the leading udp_timeout route-options rule (see the builder).
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %+v", rules)
	}
	q0 := rules[1]
	if !slices.Equal(q0.Inbound, []string{"tproxy-qos-46", "redirect-qos-46"}) ||
		q0.Action != "route" || q0.Outbound != "vpn-a" {
		t.Errorf("qos rule 0 wrong: %+v", q0)
	}
	if q0.AwgmManaged != "" {
		t.Errorf("qos rule must not carry awgm_managed (sing-box rejects unknown rule fields), got %q", q0.AwgmManaged)
	}
	if buildQoSRouteRules(nil, storage.SingboxRouterSettings{}) != nil {
		t.Error("no classes must yield nil rules")
	}
	// Deterministic marshalling — syncQoSRoutesSlot byte-compares the slot.
	first, err := marshalQoSRoutesSlot(rules)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again, err := marshalQoSRoutesSlot(buildQoSRouteRules(classes, storage.SingboxRouterSettings{}))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(first, again) {
			t.Fatalf("marshal %d differs:\n%s\nvs\n%s", i, first, again)
		}
	}
}

// #554: the system route-options rule lives in 20-router.json, but sing-box
// merges config.d in filename order — 18-qos-routes.json comes first, so a
// QoS class's final `route` rule ends evaluation before the timeout rule is
// ever reached. The slot must carry its own leading copy.
func TestBuildQoSRouteRules_LeadingUDPTimeoutRule(t *testing.T) {
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
	})
	rules := buildQoSRouteRules(classes, storage.SingboxRouterSettings{UDPTimeout: "1h0m0s"})
	if len(rules) != 2 {
		t.Fatalf("expected timeout rule + 1 class rule, got %+v", rules)
	}
	if !isSystemUDPTimeoutRule(rules[0]) || rules[0].UDPTimeout != "1h0m0s" {
		t.Errorf("rule 0 must be the udp_timeout route-options rule: %+v", rules[0])
	}
	// Scoped to the class UDP inbounds — a matcher-less rule would precede the
	// system hijack-dns rule in the merge and stretch 10s DNS sessions.
	if !slices.Equal(rules[0].Inbound, []string{"tproxy-qos-46"}) {
		t.Errorf("timeout rule must match only the class tproxy inbounds: %+v", rules[0].Inbound)
	}
	if rules[1].Action != "route" {
		t.Errorf("class rule must follow the timeout rule: %+v", rules[1])
	}
	// Unset timeout falls back to the same default the router slot uses.
	fallback := buildQoSRouteRules(classes, storage.SingboxRouterSettings{})
	if fallback[0].UDPTimeout != DefaultUDPTimeout {
		t.Errorf("unset timeout = %q, want %q", fallback[0].UDPTimeout, DefaultUDPTimeout)
	}
	// No classes → no slot at all, timeout notwithstanding (an enabled slot
	// holding only the timeout rule would duplicate 20-router.json's copy).
	if buildQoSRouteRules(nil, storage.SingboxRouterSettings{UDPTimeout: "1h0m0s"}) != nil {
		t.Error("no classes must yield nil rules")
	}
}

// The system sniff rule sits in 20-router.json, i.e. behind the class rules in
// the merge — QoS traffic would never be sniffed and would show up without
// protocol/domain. The slot carries its own copy, gated on the same toggle.
func TestBuildQoSRouteRules_SniffRule(t *testing.T) {
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
	})
	on := buildQoSRouteRules(classes, storage.SingboxRouterSettings{SnifferEnabled: true})
	if len(on) != 3 {
		t.Fatalf("expected sniff + timeout + class rule, got %+v", on)
	}
	if on[0].Action != "sniff" {
		t.Fatalf("sniff must come first (before the final route action): %+v", on[0])
	}
	// Both class inbounds: TCP hostnames come from the redirect one.
	if !slices.Equal(on[0].Inbound, []string{"tproxy-qos-46", "redirect-qos-46"}) {
		t.Errorf("sniff rule inbounds = %v", on[0].Inbound)
	}
	off := buildQoSRouteRules(classes, storage.SingboxRouterSettings{})
	for _, r := range off {
		if r.Action == "sniff" {
			t.Fatalf("sniffer off must emit no sniff rule: %+v", off)
		}
	}
}

func TestSyncQoSRoutesSlot_UDPTimeoutChangeRewritesSlot(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn-a")
	ctx := context.Background()
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
	})
	if _, err := svc.syncQoSRoutesSlot(ctx, classes, storage.SingboxRouterSettings{UDPTimeout: "5m0s"}); err != nil {
		t.Fatal(err)
	}
	// Same classes, new timeout → slot must be rewritten (#554: a changed
	// setting on a running engine must not stay stale until an on/off cycle).
	changed, err := svc.syncQoSRoutesSlot(ctx, classes, storage.SingboxRouterSettings{UDPTimeout: "1h0m0s"})
	if err != nil || !changed {
		t.Fatalf("timeout change: changed=%v err=%v", changed, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "18-qos-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"1h0m0s"`) {
		t.Errorf("slot must carry the new timeout:\n%s", raw)
	}
}

func TestMarshalQoSRoutesSlot_RouteOnly(t *testing.T) {
	data, err := marshalQoSRoutesSlot(nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"outbounds", "inbounds", "dns"} {
		if _, ok := m[forbidden]; ok {
			t.Fatalf("slot JSON must not contain %q: %s", forbidden, data)
		}
	}
	if !strings.Contains(string(data), `"rules"`) {
		t.Fatalf("slot JSON must carry route.rules: %s", data)
	}
}

// newQoSSlotTestService wires a ServiceImpl with a real orchestrator in a
// temp dir, both router + qos-routes slots registered, and a 20-router.json
// active config carrying the given outbounds (so isKnownOutboundTag can
// resolve them at emit time).
func newQoSSlotTestService(t *testing.T, outbounds ...string) (*ServiceImpl, string) {
	t.Helper()
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotQoSRoutes, Filename: "18-qos-routes.json"}); err != nil {
		t.Fatal(err)
	}
	cfg := NewEmptyConfig()
	for _, ob := range outbounds {
		cfg.Outbounds = append(cfg.Outbounds, Outbound{Type: "selector", Tag: ob, Outbounds: []string{"direct"}})
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := orch.SetEnabledSilent(orchestrator.SlotRouter, true); err != nil {
		t.Fatal(err)
	}
	if err := orch.SaveSilent(orchestrator.SlotRouter, data); err != nil {
		t.Fatal(err)
	}
	return &ServiceImpl{deps: Deps{Orch: orch, Singbox: &fakeSingbox{dir: dir}}}, dir
}

func TestSyncQoSRoutesSlot_EnableDisableLifecycle(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn-a")
	ctx := context.Background()
	activePath := filepath.Join(dir, "18-qos-routes.json")
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
	})

	// First sync: slot written + enabled, changed=true.
	changed, err := svc.syncQoSRoutesSlot(ctx, classes, storage.SingboxRouterSettings{})
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v", changed, err)
	}
	raw, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("active slot file missing: %v", err)
	}
	if !strings.Contains(string(raw), "tproxy-qos-46") || !strings.Contains(string(raw), `"vpn-a"`) {
		t.Errorf("slot content wrong:\n%s", raw)
	}
	if strings.Contains(string(raw), "awgm_managed") {
		t.Errorf("slot must stay sing-box-parseable (no awgm_managed):\n%s", raw)
	}

	// Identical classes → byte-equal slot → changed=false (no reload).
	changed, err = svc.syncQoSRoutesSlot(ctx, classes, storage.SingboxRouterSettings{})
	if err != nil || changed {
		t.Fatalf("no-op sync must report changed=false, got changed=%v err=%v", changed, err)
	}

	// Different class set → changed again.
	changedClasses := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 26, Outbound: "vpn-a", Enabled: true, Slot: 0},
	})
	changed, err = svc.syncQoSRoutesSlot(ctx, changedClasses, storage.SingboxRouterSettings{})
	if err != nil || !changed {
		t.Fatalf("diff sync: changed=%v err=%v", changed, err)
	}

	// No classes → slot cleared (active file parked under disabled/), changed=true.
	changed, err = svc.syncQoSRoutesSlot(ctx, nil, storage.SingboxRouterSettings{})
	if err != nil || !changed {
		t.Fatalf("disable sync: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(activePath); !os.IsNotExist(err) {
		t.Errorf("active slot file must be gone after disable, stat err=%v", err)
	}

	// Still disabled + still no classes → no-op.
	changed, err = svc.syncQoSRoutesSlot(ctx, nil, storage.SingboxRouterSettings{})
	if err != nil || changed {
		t.Fatalf("steady disabled sync must be changed=false, got %v err=%v", changed, err)
	}
}

func TestSyncQoSRoutesSlot_SkipsUnknownOutbounds(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn-known")
	ctx := context.Background()
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-known", Enabled: true, Slot: 0},
		{DSCP: 26, Outbound: "vpn-ghost", Enabled: true, Slot: 1},
	})
	if _, err := svc.syncQoSRoutesSlot(ctx, classes, storage.SingboxRouterSettings{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "18-qos-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "tproxy-qos-46") {
		t.Errorf("known-outbound class missing from slot:\n%s", raw)
	}
	// A rule routing to a nonexistent outbound would take the whole merged
	// config down at sing-box load — it must never be emitted.
	if strings.Contains(string(raw), "vpn-ghost") || strings.Contains(string(raw), "tproxy-qos-26") {
		t.Errorf("unknown-outbound class must be skipped at emit time:\n%s", raw)
	}

	// ALL outbounds unknown → slot cleared entirely.
	ghostOnly := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 26, Outbound: "vpn-ghost", Enabled: true, Slot: 1},
	})
	if _, err := svc.syncQoSRoutesSlot(ctx, ghostOnly, storage.SingboxRouterSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "18-qos-routes.json")); !os.IsNotExist(err) {
		t.Errorf("slot must be cleared when every class outbound is unknown, stat err=%v", err)
	}
}

func TestSyncQoSRoutesSlot_ResolvesOutboundsAgainstAppliedNotDraft(t *testing.T) {
	// The emit decision must reflect what sing-box actually merges: an
	// outbound that only exists in the PENDING draft must not unlock the
	// class (the user may still discard the draft).
	svc, dir := newQoSSlotTestService(t) // no outbounds in the APPLIED config
	draft := NewEmptyConfig()
	draft.Outbounds = append(draft.Outbounds, Outbound{Type: "selector", Tag: "vpn-staged", Outbounds: []string{"direct"}})
	data, _ := json.MarshalIndent(draft, "", "  ")
	if err := svc.deps.Orch.SaveDraft(orchestrator.SlotRouter, data); err != nil {
		t.Fatal(err)
	}
	classes := activeQoSClasses([]storage.SingboxQoSClass{
		{DSCP: 46, Outbound: "vpn-staged", Enabled: true, Slot: 0},
	})
	if _, err := svc.syncQoSRoutesSlot(context.Background(), classes, storage.SingboxRouterSettings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "18-qos-routes.json")); !os.IsNotExist(err) {
		t.Errorf("draft-only outbound must not be emitted (staging gate), stat err=%v", err)
	}
}

// ── Rule model: Inbound matcher + validation ──────────────────────

func TestRuleInbound_HasAnyMatcherAndValidation(t *testing.T) {
	cfg := NewEmptyConfig()
	// Inbound-only rule counts as having a matcher (non-reserved tag).
	if err := cfg.AddRule(Rule{Inbound: []string{"tproxy-in"}, Action: "route", Outbound: "v"}); err != nil {
		t.Fatalf("inbound-only rule rejected: %v", err)
	}
	// Empty inbound tag is invalid.
	if err := cfg.AddRule(Rule{Inbound: []string{"  "}, Action: "route", Outbound: "v"}); err == nil {
		t.Fatal("expected error for blank inbound tag")
	}
	// Round-trips through JSON as sing-box's native `inbound` field.
	data, err := json.Marshal(Rule{Inbound: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"inbound":["a","b"]`) {
		t.Errorf("inbound field serialization: %s", data)
	}
}

// TestRuleInbound_ReservedQoSNamespaceRejected guards FIX «reserved
// namespace»: with the managed rules living in 18-qos-routes.json (merged
// BEFORE the user slot), a user rule on a qos-* inbound tag could never
// match — it would only sit in the UI as an inert shadow rule, so
// AddRule/UpdateRule must reject it with the dedicated 400-mapped error.
func TestRuleInbound_ReservedQoSNamespaceRejected(t *testing.T) {
	cfg := NewEmptyConfig()
	for _, tag := range []string{"tproxy-qos-46", "redirect-qos-0"} {
		err := cfg.AddRule(Rule{Inbound: []string{tag}, Action: "route", Outbound: "v"})
		if !errors.Is(err, ErrReservedInboundTag) {
			t.Errorf("AddRule(%q) err = %v, want ErrReservedInboundTag", tag, err)
		}
	}
	// UpdateRule takes the same path.
	if err := cfg.AddRule(Rule{DomainSuffix: []string{".x.com"}, Action: "route", Outbound: "v"}); err != nil {
		t.Fatal(err)
	}
	err := cfg.UpdateRule(len(cfg.Route.Rules)-1, Rule{Inbound: []string{"tproxy-qos-1"}, Action: "route", Outbound: "v"})
	if !errors.Is(err, ErrReservedInboundTag) {
		t.Errorf("UpdateRule err = %v, want ErrReservedInboundTag", err)
	}
	// Reserved tags hidden inside a nested logical rule are caught too.
	err = cfg.AddRule(Rule{
		Type: "logical", Mode: "or", Action: "route", Outbound: "v",
		Rules: []Rule{{Inbound: []string{"redirect-qos-46"}}},
	})
	if !errors.Is(err, ErrReservedInboundTag) {
		t.Errorf("nested reserved tag err = %v, want ErrReservedInboundTag", err)
	}
}

// ── Settings validation / normalize ───────────────────────────────

func TestValidateQoSClasses_Table(t *testing.T) {
	mk := func(classes ...storage.SingboxQoSClass) storage.SingboxRouterSettings {
		return storage.SingboxRouterSettings{
			PolicyName:    "Policy0",
			WANAutoDetect: true,
			QoSClasses:    classes,
		}
	}
	cases := []struct {
		name    string
		sr      storage.SingboxRouterSettings
		wantErr string // substring; "" = valid
	}{
		{"empty ok", mk(), ""},
		{"valid class", mk(storage.SingboxQoSClass{DSCP: 46, Name: "VoIP", Outbound: "vpn", Enabled: true}), ""},
		{"disabled class still validated ok", mk(storage.SingboxQoSClass{DSCP: 0, Outbound: "vpn"}), ""},
		{"dscp too big", mk(storage.SingboxQoSClass{DSCP: 64, Outbound: "vpn"}), "DSCP должен быть 0-63"},
		{"dscp negative", mk(storage.SingboxQoSClass{DSCP: -1, Outbound: "vpn"}), "DSCP должен быть 0-63"},
		{"duplicate dscp", mk(
			storage.SingboxQoSClass{DSCP: 46, Outbound: "a"},
			storage.SingboxQoSClass{DSCP: 46, Outbound: "b"},
		), "дублирующийся DSCP"},
		{"duplicate across disabled", mk(
			storage.SingboxQoSClass{DSCP: 46, Outbound: "a", Enabled: false},
			storage.SingboxQoSClass{DSCP: 46, Outbound: "b", Enabled: true},
		), "дублирующийся DSCP"},
		{"empty outbound", mk(storage.SingboxQoSClass{DSCP: 46, Outbound: "  "}), "outbound обязателен"},
		{"name too long", mk(storage.SingboxQoSClass{DSCP: 46, Name: strings.Repeat("я", 33), Outbound: "vpn"}), "32"},
		{"name exactly 32 ok", mk(storage.SingboxQoSClass{DSCP: 46, Name: strings.Repeat("я", 32), Outbound: "vpn"}), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NormalizeSingboxRouterSettings(c.sr)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
			if !errors.Is(err, ErrQoSClassesInvalid) {
				t.Errorf("error %v must wrap ErrQoSClassesInvalid for the 400 mapping", err)
			}
		})
	}
}

func TestValidateQoSClasses_LimitExceeded(t *testing.T) {
	classes := make([]storage.SingboxQoSClass, 0, MaxQoSClasses+1)
	for d := 0; d <= MaxQoSClasses; d++ {
		classes = append(classes, storage.SingboxQoSClass{DSCP: d, Outbound: "vpn"})
	}
	err := validateQoSClasses(classes)
	if err == nil || !strings.Contains(err.Error(), "превышен лимит классов (8)") {
		t.Fatalf("expected class-limit error, got %v", err)
	}
}

func TestNormalizeSingboxRouterSettings_TrimsQoSFields(t *testing.T) {
	sr := storage.SingboxRouterSettings{
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Name: "  VoIP  ", Outbound: "  vpn  ", Enabled: true},
		},
	}
	got, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		t.Fatal(err)
	}
	if got.QoSClasses[0].Name != "VoIP" || got.QoSClasses[0].Outbound != "vpn" {
		t.Fatalf("fields not trimmed: %+v", got.QoSClasses[0])
	}
}

// ── Reconcile wiring ──────────────────────────────────────────────

func TestReconcile_QoSClassesChanged_Reinstalls(t *testing.T) {
	stubListeningProbe(t, func() bool { return true })
	restoreCalls := 0
	var lastRestore string
	ipt := newStubIPTables(func(_ context.Context, input string) error {
		restoreCalls++
		lastRestore = input
		return nil
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            singbox,
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("expected 1 Install due to QoS class change, got %d", restoreCalls)
	}
	if !strings.Contains(lastRestore, "-m dscp --dscp 46") {
		t.Errorf("restore input missing dscp rule:\n%s", lastRestore)
	}
	want := []QoSClassSpec{{DSCP: 46, TProxyPort: 51281, RedirectPort: 51301}}
	if !slices.Equal(appliedQoSOf(svc), want) {
		t.Errorf("применённые классы QoS не обновились: %+v", appliedQoSOf(svc))
	}
}

// TestReconcile_QoSPortChange_HealsConfigBeforeInstall guards the FIX-5
// ordering: when the QoS port set changes, the sing-box config heal (which
// makes the new per-class inbounds listen) plus its reload must complete
// BEFORE the iptables Install starts dispatching to the new ports —
// otherwise marked traffic blackholes onto ports nothing listens on. This
// is the same config→wait→install order Enable uses.
func TestReconcile_QoSPortChange_HealsConfigBeforeInstall(t *testing.T) {
	stubListeningProbe(t, func() bool { return true })
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	var sequence []string
	// The recorder snapshots the persisted config AT INSTALL TIME: the heal
	// must already have provisioned the class inbounds by then.
	installedWithInbounds := false
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		sequence = append(sequence, "install")
		if cfg, err := LoadConfig(filepath.Join(singbox.dir, "20-router.json")); err == nil {
			for _, in := range cfg.Inbounds {
				if in.Tag == "tproxy-qos-46" {
					installedWithInbounds = true
				}
			}
		}
		return nil
	})
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            singbox,
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if len(sequence) != 1 || sequence[0] != "install" {
		t.Fatalf("expected exactly one install, got %v", sequence)
	}
	if !installedWithInbounds {
		t.Error("Install ran BEFORE the config heal provisioned the class inbounds — blackhole window (FIX-5 regression)")
	}
}

func TestReconcile_QoSClassesSame_NoReinstall(t *testing.T) {
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	svc := &ServiceImpl{
		deps: Deps{
			Policies:       &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:       ipt,
			WANIPCollector: &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			// Движок ЖИВОЙ: с мёртвым установку глушит гейт готовности и «ноль
			// Install» не доказывал бы совпадения классов.
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		appliedSpec: &RestoreInputSpec{
			PolicyMark: "0xffffaaa",
			WANIPs:     []string{"203.0.113.207/32"},
			QoSClasses: []QoSClassSpec{{DSCP: 46, TProxyPort: 51281, RedirectPort: 51301}},
		},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("reconcileInstalled err: %v", err)
	}
	if restoreCalls != 0 {
		t.Errorf("expected no Install when QoS classes unchanged, got %d", restoreCalls)
	}
}

func TestReconcile_QoSXtDscpUnavailable_DegradesWithoutFailing(t *testing.T) {
	// xt_dscp missing: reconcile must neither fail nor churn — the desired
	// dispatch set degrades to empty, which equals the installed state.
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	origEnsure := ensureKernelModuleFn
	ensureKernelModuleFn = func(_ context.Context, _ string) error { return ErrNetfilterComponentMissing }
	t.Cleanup(func() { ensureKernelModuleFn = origEnsure })

	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            newTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return false },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	if err := svc.reconcileInstalled(context.Background(), storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("reconcileInstalled must not fail on missing xt_dscp: %v", err)
	}
	if restoreCalls != 0 {
		t.Errorf("expected no Install churn while xt_dscp unavailable, got %d", restoreCalls)
	}
	if appliedQoSOf(svc) != nil {
		t.Errorf("применённые классы QoS обязаны остаться пустыми, got %+v", appliedQoSOf(svc))
	}
}

// ── Enable wiring (tproxy path) ───────────────────────────────────

func TestEnable_Tproxy_QoSClasses_WiresConfigAndIPTables(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Name: "VoIP", Outbound: "vpn-a", Enabled: true},
			{DSCP: 10, Name: "off", Outbound: "vpn-b", Enabled: false},
		},
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubListeningProbe(t, func() bool { return true })

	var restoreInput string
	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           newStubIPTables(func(_ context.Context, input string) error { restoreInput = input; return nil }),
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
		XtDscpProbe:        func(context.Context) bool { return true },
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable (tproxy, qos): %v", err)
	}

	// iptables side: dispatch rules for the ENABLED class only.
	if !strings.Contains(restoreInput, "-m dscp --dscp 46 -j TPROXY --on-port 51281") {
		t.Errorf("restore input missing UDP dscp dispatch:\n%s", restoreInput)
	}
	if !strings.Contains(restoreInput, "-m dscp --dscp 46 -j REDIRECT --to-ports 51301") {
		t.Errorf("restore input missing TCP dscp dispatch:\n%s", restoreInput)
	}
	if strings.Contains(restoreInput, "--dscp 10") {
		t.Errorf("disabled class must not be dispatched:\n%s", restoreInput)
	}
	want := []QoSClassSpec{{DSCP: 46, TProxyPort: 51281, RedirectPort: 51301}}
	if !slices.Equal(appliedQoSOf(svc), want) {
		t.Errorf("применённые классы QoS = %+v, want %+v", appliedQoSOf(svc), want)
	}

	// sing-box side: persisted config carries the class inbound pair. The
	// managed route rule does NOT live here anymore — it goes to the
	// 18-qos-routes.json slot (invisible in the rules UI); with no
	// orchestrator wired in this legacy-mode test the slot sync is a no-op.
	cfg, err := LoadConfig(svc.routerConfigPath())
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	var tags []string
	for _, in := range cfg.Inbounds {
		tags = append(tags, in.Tag)
	}
	for _, wantTag := range []string{"tproxy-in", "redirect-in", "tproxy-qos-46", "redirect-qos-46"} {
		if !slices.Contains(tags, wantTag) {
			t.Errorf("persisted inbounds missing %q: %v", wantTag, tags)
		}
	}
	if slices.Contains(tags, "tproxy-qos-10") {
		t.Errorf("disabled class inbound persisted: %v", tags)
	}
	// No managed rules may leak into the user-visible rules file — that was
	// the ListRules churn loop (users saw anonymous rows, deleted them, the
	// heal re-added them).
	for _, r := range cfg.Route.Rules {
		for _, tag := range r.Inbound {
			if isQoSInboundTag(tag) {
				t.Errorf("managed qos rule leaked into 20-router.json: %+v", r)
			}
		}
	}
	// The persisted JSON must stay sing-box-parseable: no awgm_managed key.
	raw, _ := os.ReadFile(svc.routerConfigPath())
	if strings.Contains(string(raw), "awgm_managed") {
		t.Errorf("persisted config contains awgm_managed (sing-box rejects unknown rule fields):\n%s", raw)
	}
}

// TestEnable_Tproxy_QoS_WritesRoutesSlot is the orchestrator-backed variant:
// Enable must materialize the managed rules into 18-qos-routes.json and keep
// 20-router.json free of them.
func TestEnable_Tproxy_QoS_WritesRoutesSlot(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn-a")
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Name: "VoIP", Outbound: "vpn-a", Enabled: true, Slot: 0},
		},
	})
	singbox := &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	stubListeningProbe(t, func() bool { return true })
	svc.deps.Settings = settingsStore
	svc.deps.Singbox = singbox
	svc.deps.Policies = &fakeAccessPolicyProvider{}
	svc.deps.IPTables = newStubIPTables(func(_ context.Context, _ string) error { return nil })
	svc.deps.WANIPCollector = &fakeWANIPCollector{}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	slotRaw, err := os.ReadFile(filepath.Join(dir, "18-qos-routes.json"))
	if err != nil {
		t.Fatalf("qos routes slot not written: %v", err)
	}
	if !strings.Contains(string(slotRaw), "tproxy-qos-46") || !strings.Contains(string(slotRaw), `"vpn-a"`) {
		t.Errorf("slot content wrong:\n%s", slotRaw)
	}
	routerRaw, err := os.ReadFile(filepath.Join(dir, "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(routerRaw), "\"inbound\": [\n        \"tproxy-qos-46\"") {
		t.Errorf("managed rule leaked into 20-router.json:\n%s", routerRaw)
	}
	// ListRules (user-visible) must not surface the managed rules.
	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		for _, tag := range r.Inbound {
			if isQoSInboundTag(tag) {
				t.Errorf("ListRules leaked a managed qos rule: %+v", r)
			}
		}
	}

	// Disable parks the slot with the router.
	if err := svc.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "18-qos-routes.json")); !os.IsNotExist(err) {
		t.Errorf("qos routes slot must be parked on Disable, stat err=%v", err)
	}
}

func TestEnable_Tproxy_QoSXtDscpMissing_DegradesWithoutFailing(t *testing.T) {
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn-a", Enabled: true},
		},
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	stubListeningProbe(t, func() bool { return true })
	origEnsure := ensureKernelModuleFn
	ensureKernelModuleFn = func(_ context.Context, _ string) error { return ErrNetfilterComponentMissing }
	t.Cleanup(func() { ensureKernelModuleFn = origEnsure })

	var restoreInput string
	svc := newTestService(t, Deps{
		Settings:           settingsStore,
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           newStubIPTables(func(_ context.Context, input string) error { restoreInput = input; return nil }),
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
		XtDscpProbe:        func(context.Context) bool { return false },
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable must not fail when xt_dscp is unavailable: %v", err)
	}
	if strings.Contains(restoreInput, "-m dscp") {
		t.Errorf("dscp rules must be skipped when xt_dscp unavailable:\n%s", restoreInput)
	}
	if appliedQoSOf(svc) != nil {
		t.Errorf("применённые классы QoS обязаны остаться пустыми при деградации, got %+v", appliedQoSOf(svc))
	}
}

// ── healQoSConfig (reconcile self-heal) ───────────────────────────

func TestHealQoSConfig_ConvergesAndNoopsWhenClean(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "fresh-outbound")

	// Seed the active config with a stale class-10 inbound pair on top of the
	// catalog config newQoSSlotTestService wrote.
	seed, err := svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	seed.Inbounds = []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", Listen: "0.0.0.0", ListenPort: TPROXYPort, Network: "udp", UDPFragment: true, UDPTimeout: DefaultUDPTimeout},
		{Type: "tproxy", Tag: "tproxy-qos-10", ListenPort: 51281},
	}
	if err := svc.persistConfigDirect(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	// Seed a stale slot file for a class that no longer exists.
	staleRules := buildQoSRouteRules([]qosClass{{DSCP: 10, Outbound: "old", TProxyPort: 51281, RedirectPort: 51301}}, storage.SingboxRouterSettings{})
	staleData, _ := marshalQoSRoutesSlot(staleRules)
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotQoSRoutes, staleData); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotQoSRoutes, true); err != nil {
		t.Fatal(err)
	}

	sr := storage.SingboxRouterSettings{
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "fresh-outbound", Enabled: true, Slot: 0},
		},
	}
	changed, err := svc.healQoSConfig(context.Background(), sr)
	if err != nil {
		t.Fatalf("healQoSConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on a drifted config")
	}
	cfg, err := svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, in := range cfg.Inbounds {
		tags = append(tags, in.Tag)
	}
	wantTags := []string{"tproxy-in", "tproxy-qos-46", "redirect-qos-46"}
	if !slices.Equal(tags, wantTags) {
		t.Fatalf("healed inbound tags = %v, want %v", tags, wantTags)
	}
	slotRaw, err := os.ReadFile(filepath.Join(dir, "18-qos-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(slotRaw), "tproxy-qos-46") || strings.Contains(string(slotRaw), "tproxy-qos-10") {
		t.Fatalf("healed slot content wrong:\n%s", slotRaw)
	}

	// Steady state: second heal must not rewrite either file and must report
	// changed=false (no sing-box reload).
	beforeCfg, _ := os.Stat(filepath.Join(dir, "20-router.json"))
	beforeSlot, _ := os.Stat(filepath.Join(dir, "18-qos-routes.json"))
	changed, err = svc.healQoSConfig(context.Background(), sr)
	if err != nil {
		t.Fatalf("second healQoSConfig: %v", err)
	}
	if changed {
		t.Error("steady-state heal must report changed=false")
	}
	afterCfg, _ := os.Stat(filepath.Join(dir, "20-router.json"))
	afterSlot, _ := os.Stat(filepath.Join(dir, "18-qos-routes.json"))
	if !afterCfg.ModTime().Equal(beforeCfg.ModTime()) {
		t.Error("steady-state heal must not rewrite 20-router.json")
	}
	if !afterSlot.ModTime().Equal(beforeSlot.ModTime()) {
		t.Error("steady-state heal must not rewrite 18-qos-routes.json")
	}
}

// TestHealQoSConfig_DoesNotApplyPendingDraft guards the staging gate: the
// heal derives from the APPLIED config (LoadApplied), never the pending
// draft — the old LoadEffective-based heal silently applied staged edits to
// active on every reconcile tick.
func TestHealQoSConfig_DoesNotApplyPendingDraft(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn-a")

	// Stage a draft with a user rule the user has NOT applied yet.
	draft, err := svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	draft.Route.Rules = append(draft.Route.Rules, Rule{DomainSuffix: []string{".staged.example"}, Action: "route", Outbound: "vpn-a"})
	draftData, _ := json.MarshalIndent(draft, "", "  ")
	if err := svc.deps.Orch.SaveDraft(orchestrator.SlotRouter, draftData); err != nil {
		t.Fatal(err)
	}

	sr := storage.SingboxRouterSettings{
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
		},
	}
	if _, err := svc.healQoSConfig(context.Background(), sr); err != nil {
		t.Fatalf("healQoSConfig: %v", err)
	}
	activeRaw, err := os.ReadFile(filepath.Join(dir, "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(activeRaw), ".staged.example") {
		t.Errorf("heal leaked the pending draft into the active config (staging gate bypassed):\n%s", activeRaw)
	}
	// And the draft itself must still be pending.
	if !svc.deps.Orch.HasDraft(orchestrator.SlotRouter) {
		t.Error("pending draft vanished during heal")
	}
}

// ── Outbound lifecycle (FIX-4) ────────────────────────────────────

// noopNotInstalledIPTables reports "nothing installed" so Reconcile at the
// end of UpdateSettings is a clean no-op for a disabled router.
func noopNotInstalledIPTables() *IPTables {
	return &IPTables{
		restoreNoflush: func(_ context.Context, _ string) error { return nil },
		runIPTables:    func(_ context.Context, _ ...string) error { return errors.New("chain missing") },
		runIPTablesOut: func(_ context.Context, _ ...string) (string, error) { return "", nil },
		runIP:          func(_ context.Context, _ ...string) error { return nil },
		runIPOut:       func(_ context.Context, _ ...string) (string, error) { return "", nil },
		persistRules:   func(_, _, _ string) error { return nil },
		persistHook:    func(bool) error { return nil },
		cleanupHook:    func() {},

		persistPolicyTunDNSHook: func(string) error { return nil },
		cleanupPolicyTunDNSHook: func() {},
		persistBlackhole:        func(string) error { return nil },
		cleanupBlackhole:        func() {},
		runCtClean:              func(context.Context) {},
	}
}

func newQoSLifecycleService(t *testing.T, sr storage.SingboxRouterSettings, outbounds ...string) *ServiceImpl {
	t.Helper()
	settingsStore := newTestSettingsStore(t, sr)
	singbox := newTestSingbox(t)
	svc := newTestService(t, Deps{
		Settings: settingsStore,
		Singbox:  singbox,
		IPTables: noopNotInstalledIPTables(),
		Policies: &fakeAccessPolicyProvider{},
	})
	cfg := NewEmptyConfig()
	// Атомарный член, на который ссылаются сеяные композиты: с валидацией
	// членов (#567) плейсхолдер обязан существовать в каталоге.
	cfg.Outbounds = append(cfg.Outbounds, Outbound{Type: "socks", Tag: "member-x", Server: "10.0.0.1"})
	for _, ob := range outbounds {
		cfg.Outbounds = append(cfg.Outbounds, Outbound{Type: "selector", Tag: ob, Outbounds: []string{"member-x"}})
	}
	if err := SaveConfig(svc.routerConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestUpdateSettings_ReassociatesQoSSlotsByDSCP(t *testing.T) {
	svc := newQoSLifecycleService(t, storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 10, Outbound: "vpn-a", Enabled: true, Slot: 0},
			{DSCP: 20, Outbound: "vpn-b", Enabled: true, Slot: 1},
			{DSCP: 30, Outbound: "vpn-c", Enabled: true, Slot: 2},
		},
	}, "vpn-a", "vpn-b", "vpn-c", "vpn-d")

	// Frontend PUT: no slots on the wire, class 20 removed, class 40 added,
	// class 30 disabled.
	err := svc.UpdateSettings(context.Background(), storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 30, Outbound: "vpn-c", Enabled: false},
			{DSCP: 10, Outbound: "vpn-a", Enabled: true},
			{DSCP: 40, Outbound: "vpn-d", Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	all, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	got := all.SingboxRouter.QoSClasses
	bySlot := map[int]int{}
	for _, c := range got {
		bySlot[c.DSCP] = c.Slot
	}
	if bySlot[30] != 2 || bySlot[10] != 0 {
		t.Errorf("existing classes must keep their slots: %+v", got)
	}
	if bySlot[40] != 1 {
		t.Errorf("new class must reuse the freed slot 1, got %d (%+v)", bySlot[40], got)
	}
}

func TestUpdateSettings_RejectsUnknownQoSOutbound(t *testing.T) {
	svc := newQoSLifecycleService(t, storage.SingboxRouterSettings{WANAutoDetect: true}, "vpn-a")

	err := svc.UpdateSettings(context.Background(), storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn-ghost", Enabled: true},
		},
	})
	if !errors.Is(err, ErrQoSClassesInvalid) {
		t.Fatalf("expected ErrQoSClassesInvalid for unknown enabled outbound, got %v", err)
	}
	if !strings.Contains(err.Error(), "vpn-ghost") {
		t.Errorf("error must name the offending outbound: %v", err)
	}

	// A DISABLED class may keep an unknown outbound (force-delete leaves the
	// class around disabled; a verbatim re-PUT must not 400).
	err = svc.UpdateSettings(context.Background(), storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn-ghost", Enabled: false},
			{DSCP: 26, Outbound: "vpn-a", Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("disabled class with unknown outbound must be storable: %v", err)
	}
}

func TestUpdateCompositeOutbound_RenameFollowsQoSClasses(t *testing.T) {
	svc := newQoSLifecycleService(t, storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn-a", Enabled: true, Slot: 0},
		},
	}, "vpn-a")

	err := svc.UpdateCompositeOutbound(context.Background(), "vpn-a",
		Outbound{Type: "selector", Tag: "vpn-renamed", Outbounds: []string{"member-x"}})
	if err != nil {
		t.Fatalf("UpdateCompositeOutbound: %v", err)
	}
	all, _ := svc.deps.Settings.Load()
	if got := all.SingboxRouter.QoSClasses[0].Outbound; got != "vpn-renamed" {
		t.Errorf("QoS class outbound = %q, want vpn-renamed", got)
	}
}

func TestRenameExternalOutboundTag_FollowsQoSClasses(t *testing.T) {
	svc := newQoSLifecycleService(t, storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "tunnel-old", Enabled: true, Slot: 0},
		},
	})

	if err := svc.RenameExternalOutboundTag(context.Background(), "tunnel-old", "tunnel-new"); err != nil {
		t.Fatalf("RenameExternalOutboundTag: %v", err)
	}
	all, _ := svc.deps.Settings.Load()
	if got := all.SingboxRouter.QoSClasses[0].Outbound; got != "tunnel-new" {
		t.Errorf("QoS class outbound = %q, want tunnel-new", got)
	}
}

func TestDeleteCompositeOutbound_QoSClassGuardAndForceDisable(t *testing.T) {
	svc := newQoSLifecycleService(t, storage.SingboxRouterSettings{
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Name: "VoIP", Outbound: "vpn-a", Enabled: true, Slot: 0},
		},
	}, "vpn-a")

	// Non-force: refused while a QoS class references the outbound.
	err := svc.DeleteCompositeOutbound(context.Background(), "vpn-a", false)
	if !errors.Is(err, ErrOutboundReferenced) {
		t.Fatalf("expected ErrOutboundReferenced, got %v", err)
	}

	// Force: outbound removed, class kept but DISABLED (never silently
	// deleted — the UI must show it off).
	if err := svc.DeleteCompositeOutbound(context.Background(), "vpn-a", true); err != nil {
		t.Fatalf("force delete: %v", err)
	}
	all, _ := svc.deps.Settings.Load()
	classes := all.SingboxRouter.QoSClasses
	if len(classes) != 1 {
		t.Fatalf("class must survive force-delete: %+v", classes)
	}
	if classes[0].Enabled {
		t.Error("class must be disabled after force-deleting its outbound")
	}
	if classes[0].Outbound != "vpn-a" || classes[0].Name != "VoIP" || classes[0].Slot != 0 {
		t.Errorf("class data must be preserved: %+v", classes[0])
	}
}

// ── GetStatus: qos-outbound-missing issue (FIX-4c) ────────────────

func TestGetStatus_ReportsQoSOutboundMissing(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:       true,
		PolicyName:    "Policy0",
		WANAutoDetect: true,
		QoSClasses: []storage.SingboxQoSClass{
			{DSCP: 46, Outbound: "vpn-known", Enabled: true, Slot: 0},
			{DSCP: 26, Outbound: "vpn-ghost", Enabled: true, Slot: 1},
			{DSCP: 30, Outbound: "vpn-off-ghost", Enabled: false, Slot: 2}, // disabled → no issue
		},
	})
	fe := &fakeExec{}
	singbox := newTestSingbox(t)
	svc := newTestService(t, Deps{
		Settings:    settingsStore,
		Policies:    &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables:    newTestIPTables(fe),
		Singbox:     singbox,
		XtDscpProbe: func(context.Context) bool { return true },
	})
	cfg := NewEmptyConfig()
	cfg.Outbounds = append(cfg.Outbounds, Outbound{Type: "selector", Tag: "vpn-known", Outbounds: []string{"member-x"}})
	if err := SaveConfig(svc.routerConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	var hits []Issue
	for _, is := range st.Issues {
		if is.Kind == "qos-outbound-missing" {
			hits = append(hits, is)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("expected exactly 1 qos-outbound-missing issue, got %+v", st.Issues)
	}
	if hits[0].Tag != "vpn-ghost" || !strings.Contains(hits[0].Message, "vpn-ghost") {
		t.Errorf("issue must name the missing outbound: %+v", hits[0])
	}
}

// appliedQoSOf — применённые классы QoS из снимка; в проде это читается
// инлайном в одном месте, тестам нужен короткий доступ.
func appliedQoSOf(s *ServiceImpl) []QoSClassSpec {
	if s.appliedSpec == nil {
		return nil
	}
	return s.appliedSpec.QoSClasses
}

// F20: провал Install оставлял appliedSpec и netfilterStateKnown=true
// нетронутыми. iptables-restore коммитит по таблицам, поэтому часть правил
// могла примениться — а если желаемый спек затем возвращался к применённому,
// тик не переустанавливал ничего, и частично закоммиченный netfilter жил до
// случайного расхождения спеков.
//
// Сценарий: applied = спек A → тик хочет B, Install падает → тик снова хочет A.
// Старый код: спеки равны, джампы на месте → no-op. Новый: known=false → force.
func TestReconcile_FailedInstallForcesReinstallOnRevertedSpec(t *testing.T) {
	stubListeningProbe(t, func() bool { return true })
	installOK := false
	restoreCalls := 0
	ipt := newStubIPTables(func(context.Context, string) error {
		restoreCalls++
		if !installOK {
			return errors.New("injected: iptables-restore")
		}
		return nil
	})
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            singbox,
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	specA := storage.SingboxRouterSettings{Enabled: true, PolicyName: "Policy0", WANAutoDetect: true}
	specB := specA
	specB.QoSClasses = []storage.SingboxQoSClass{{DSCP: 46, Outbound: "vpn", Enabled: true}}

	// Тик 1: желаемый спек B, Install падает.
	if err := svc.reconcileInstalled(context.Background(), specB); err == nil {
		t.Fatal("ожидался провал Install")
	}
	if restoreCalls != 1 {
		t.Fatalf("Install вызван %d раз на первом тике, want 1", restoreCalls)
	}

	// Тик 2: желаемый спек снова A — совпадает с applied, джампы живы.
	installOK = true
	if err := svc.reconcileInstalled(context.Background(), specA); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 2 {
		t.Fatalf("Install вызван %d раз всего, want 2 (снимок после провала обязан считаться неизвестным)", restoreCalls)
	}
}

// F21: reconcilePolicyTunQoS, снимая netfilter при исчезнувших классах, сбрасывал
// три поля группы из четырёх — состав geoip-тегов оставался. Как баг сегодня
// недостижимо (теги к этому моменту уже пусты), но дисциплина «четыре поля
// сбрасываются вместе», объявленная в policytun_disable.go, была нарушена
// ровно здесь.
//
// Мутация для пина: убрать `s.currentBypassGeoIPTags = nil` из ветки want==nil —
// тест краснеет.
func TestReconcilePolicyTunQoS_UninstallResetsWholeGroup(t *testing.T) {
	ipt := newStubIPTables(func(context.Context, string) error { return nil })
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            singbox,
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		appliedSpec:            &RestoreInputSpec{PolicyMark: "0xffffaaa"},
		appliedBlackhole:       &RestoreInputSpec{},
		currentBypassGeoIPTags: []string{"ru"},
		netfilterStateKnown:    true,
	}

	// Классов QoS нет → want == nil → Uninstall и сброс группы.
	svc.reconcilePolicyTunQoS(context.Background(), storage.SingboxRouterSettings{
		Enabled: true, PolicyName: "Policy0", WANAutoDetect: true,
	})

	if svc.appliedSpec != nil {
		t.Errorf("appliedSpec обязан обнулиться: %+v", svc.appliedSpec)
	}
	if svc.appliedBlackhole != nil {
		t.Error("appliedBlackhole обязан обнулиться")
	}
	if svc.currentBypassGeoIPTags != nil {
		t.Errorf("состав geoip-тегов — четвёртый член группы, обязан обнулиться: %v", svc.currentBypassGeoIPTags)
	}
}
