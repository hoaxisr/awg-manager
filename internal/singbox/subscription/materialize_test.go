package subscription

import (
	"encoding/json"
	"testing"
)

func TestBuildSelector(t *testing.T) {
	memberTags := []string{"sub-abc-1111", "sub-abc-2222", "sub-abc-3333"}
	sel := BuildSelector("sub-abc", memberTags, "sub-abc-1111")
	var ob map[string]any
	json.Unmarshal(sel, &ob)
	if ob["type"] != "selector" || ob["tag"] != "sub-abc" {
		t.Errorf("selector wrong: %+v", ob)
	}
	if def, _ := ob["default"].(string); def != "sub-abc-1111" {
		t.Errorf("default=%v", def)
	}
	outs := ob["outbounds"].([]any)
	if len(outs) != 3 {
		t.Errorf("outbounds len=%d", len(outs))
	}
}

func TestBuildSelector_DefaultsToFirstWhenEmpty(t *testing.T) {
	memberTags := []string{"sub-abc-1111", "sub-abc-2222"}
	sel := BuildSelector("sub-abc", memberTags, "")
	var ob map[string]any
	json.Unmarshal(sel, &ob)
	if def, _ := ob["default"].(string); def != "sub-abc-1111" {
		t.Errorf("default should fall back to first member, got %v", def)
	}
}

func TestBuildMixedInbound(t *testing.T) {
	mb := BuildMixedInbound("sub-abc-in", 11080)
	var ob map[string]any
	json.Unmarshal(mb, &ob)
	if ob["type"] != "mixed" || ob["tag"] != "sub-abc-in" {
		t.Errorf("inbound wrong: %+v", ob)
	}
	if ob["listen"] != "127.0.0.1" || ob["listen_port"] != float64(11080) {
		t.Errorf("listen wrong: %+v", ob)
	}
}

func TestBuildRouteRule(t *testing.T) {
	rr := BuildRouteRule("sub-abc-in", "sub-abc")
	var ob map[string]any
	json.Unmarshal(rr, &ob)
	if ob["inbound"] != "sub-abc-in" || ob["outbound"] != "sub-abc" {
		t.Errorf("route rule wrong: %+v", ob)
	}
}

func TestBuildURLTest(t *testing.T) {
	memberTags := []string{"sub-abc-1111", "sub-abc-2222"}
	cfg := URLTestConfig{URL: "https://probe.example", IntervalSec: 30, ToleranceMs: 75}
	out := BuildURLTest("sub-abc", memberTags, cfg)
	var ob map[string]any
	if err := json.Unmarshal(out, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["type"] != "urltest" || ob["tag"] != "sub-abc" {
		t.Errorf("urltest header wrong: %+v", ob)
	}
	if ob["url"] != "https://probe.example" {
		t.Errorf("url=%v", ob["url"])
	}
	if ob["interval"] != "30s" {
		t.Errorf("interval=%v (want 30s)", ob["interval"])
	}
	if ob["tolerance"] != float64(75) {
		t.Errorf("tolerance=%v (want 75)", ob["tolerance"])
	}
	if outs, _ := ob["outbounds"].([]any); len(outs) != 2 {
		t.Errorf("outbounds len=%d", len(outs))
	}
	if _, ok := ob["default"]; ok {
		t.Errorf("urltest must not emit `default`: %+v", ob)
	}
}

func TestBuildURLTest_AppliesDefaultsForZeroValues(t *testing.T) {
	out := BuildURLTest("sub-abc", []string{"m"}, URLTestConfig{})
	var ob map[string]any
	json.Unmarshal(out, &ob)
	def := DefaultURLTestConfig()
	if ob["url"] != def.URL {
		t.Errorf("url default not applied: %v", ob["url"])
	}
	if ob["interval"] != "60s" {
		t.Errorf("interval default not applied: %v", ob["interval"])
	}
}

func TestBuildURLTest_AcceptsZeroToleranceAsMeaningful(t *testing.T) {
	// Sing-box treats tolerance=0 as "always switch on any RTT
	// advantage". Negative is the sentinel for "use default 50ms".
	out := BuildURLTest("sub-x", []string{"m"}, URLTestConfig{
		URL:         "https://probe.example",
		IntervalSec: 30,
		ToleranceMs: 0,
	})
	var ob map[string]any
	json.Unmarshal(out, &ob)
	if _, ok := ob["tolerance"]; ok {
		t.Errorf("zero tolerance must be omitted from JSON, got %v", ob["tolerance"])
	}
	out2 := BuildURLTest("sub-x", []string{"m"}, URLTestConfig{
		URL:         "https://probe.example",
		IntervalSec: 30,
		ToleranceMs: -1,
	})
	var ob2 map[string]any
	json.Unmarshal(out2, &ob2)
	if ob2["tolerance"] != float64(50) {
		t.Errorf("negative tolerance must apply default 50, got %v", ob2["tolerance"])
	}
}

func TestBuildGroupOutbound_DispatchesByMode(t *testing.T) {
	members := []string{"m1", "m2"}
	subSel := Subscription{SelectorTag: "sub-x", Mode: ModeSelector}
	subUT := Subscription{SelectorTag: "sub-x", Mode: ModeURLTest}
	subEmpty := Subscription{SelectorTag: "sub-x"} // back-compat: "" → selector

	check := func(t *testing.T, raw json.RawMessage, wantType string) {
		t.Helper()
		var ob map[string]any
		if err := json.Unmarshal(raw, &ob); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ob["type"] != wantType {
			t.Errorf("type=%v, want %v", ob["type"], wantType)
		}
	}
	check(t, BuildGroupOutbound(subSel, members, "m1"), "selector")
	check(t, BuildGroupOutbound(subUT, members, "m1"), "urltest")
	check(t, BuildGroupOutbound(subEmpty, members, "m1"), "selector")

	subFB := Subscription{SelectorTag: "sub-x", Mode: ModeFallback}
	check(t, BuildGroupOutbound(subFB, members, "m1"), "fallback")
}

func TestBuildFallback(t *testing.T) {
	members := []string{"m1", "m2"}
	raw := BuildFallback("sub-fb", members, URLTestConfig{
		URL:         "https://example.com/check",
		IntervalSec: 3600,
	})
	var ob map[string]any
	if err := json.Unmarshal(raw, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ob["type"] != "fallback" || ob["tag"] != "sub-fb" {
		t.Errorf("wrong fallback outbound: %+v", ob)
	}
	if ob["interrupt_exist_connections"] != true {
		t.Errorf("expected interrupt_exist_connections=true, got %v", ob["interrupt_exist_connections"])
	}
	if ob["idle_timeout"] != "7200s" {
		t.Errorf("expected safe idle_timeout=7200s, got %v", ob["idle_timeout"])
	}
}
