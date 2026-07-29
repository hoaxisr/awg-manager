package subscription

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Test fixtures use RFC 5737 documentation IPs (192.0.2.x) and
// RFC 2606 example domains. UUIDs / keys / short_ids are pattern-
// only — formatted to match the validator's expectations but contain
// no real credentials. Structural defects (reality-without-utls,
// malformed uuid) mirror real subscriptions from issue #221.

// === Pass 1: preFilterOutbounds =========================================

func TestPreFilterOutbounds_AcceptsValidVless(t *testing.T) {
	ob := map[string]any{
		"type":        "vless",
		"tag":         "good-vless",
		"server":      "192.0.2.10",
		"server_port": float64(443),
		"uuid":        "11111111-2222-3333-4444-555555555555",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "example.com",
			"reality": map[string]any{
				"enabled":    true,
				"public_key": "REDACTED_PUBKEY",
				"short_id":   "01ab",
			},
			"utls": map[string]any{
				"enabled":     true,
				"fingerprint": "chrome",
			},
		},
	}
	kept, dropped := preFilterOutbounds([]any{ob}, nil)
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("expected 1 kept / 0 dropped, got %d / %d (reasons=%v)", len(kept), len(dropped), dropped)
	}
}

func TestPreFilterOutbounds_DropsRealityWithoutUTLSBlock(t *testing.T) {
	// Exactly the issue #221 shape: reality.enabled=true, no utls block.
	ob := map[string]any{
		"type":        "vless",
		"tag":         "bad-reality-no-utls",
		"server":      "192.0.2.20",
		"server_port": float64(443),
		"uuid":        "22222222-3333-4444-5555-666666666666",
		"transport":   map[string]any{"type": "grpc", "service_name": "x"},
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "example.org",
			"reality": map[string]any{
				"enabled":    true,
				"public_key": "REDACTED_PUBKEY",
				"short_id":   "02cd",
			},
		},
	}
	kept, dropped := preFilterOutbounds([]any{ob}, nil)
	if len(kept) != 0 {
		t.Fatalf("expected 0 kept, got %d", len(kept))
	}
	if len(dropped) != 1 {
		t.Fatalf("expected 1 dropped, got %d", len(dropped))
	}
	if dropped[0].Tag != "bad-reality-no-utls" {
		t.Errorf("tag = %q, want bad-reality-no-utls", dropped[0].Tag)
	}
	if !strings.Contains(dropped[0].Reason, "utls") {
		t.Errorf("reason = %q, want it to mention utls", dropped[0].Reason)
	}
}

func TestPreFilterOutbounds_DropsRealityWithUTLSDisabled(t *testing.T) {
	ob := map[string]any{
		"type":        "vless",
		"tag":         "bad-reality-utls-off",
		"server":      "192.0.2.21",
		"server_port": float64(443),
		"uuid":        "33333333-4444-5555-6666-777777777777",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "example.com",
			"reality":     map[string]any{"enabled": true, "public_key": "K", "short_id": "00"},
			"utls":        map[string]any{"enabled": false, "fingerprint": "chrome"},
		},
	}
	_, dropped := preFilterOutbounds([]any{ob}, nil)
	if len(dropped) != 1 {
		t.Fatalf("expected 1 dropped, got %d", len(dropped))
	}
	if !strings.Contains(dropped[0].Reason, "utls.enabled=true") {
		t.Errorf("reason = %q, want it to mention utls.enabled=true", dropped[0].Reason)
	}
}

func TestPreFilterOutbounds_AcceptsPlainTLSNoReality(t *testing.T) {
	// Plain TLS without reality should NOT require utls.
	ob := map[string]any{
		"type":        "vless",
		"tag":         "tls-no-reality",
		"server":      "192.0.2.30",
		"server_port": float64(443),
		"uuid":        "44444444-5555-6666-7777-888888888888",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "example.org",
		},
	}
	kept, dropped := preFilterOutbounds([]any{ob}, nil)
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("expected 1 kept / 0 dropped, got %d / %d (reasons=%v)", len(kept), len(dropped), dropped)
	}
}

func TestPreFilterOutbounds_DropsInvalidUUID(t *testing.T) {
	ob := map[string]any{
		"type":        "vless",
		"tag":         "bad-uuid",
		"server":      "192.0.2.40",
		"server_port": float64(443),
		"uuid":        "Telegram:@SomeHandle", // pattern from issue #221 JSON
	}
	_, dropped := preFilterOutbounds([]any{ob}, nil)
	if len(dropped) != 1 || !strings.Contains(dropped[0].Reason, "uuid") {
		t.Fatalf("expected 1 drop with uuid reason, got %v", dropped)
	}
}

func TestPreFilterOutbounds_DropsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		ob   map[string]any
		want string
	}{
		{
			name: "missing type",
			ob:   map[string]any{"tag": "no-type", "server": "192.0.2.50", "server_port": float64(443)},
			want: "missing type",
		},
		{
			name: "missing server",
			ob: map[string]any{
				"type": "vless", "tag": "no-server", "server_port": float64(443),
				"uuid": "11111111-2222-3333-4444-555555555555",
			},
			want: "missing server",
		},
		{
			name: "invalid port",
			ob: map[string]any{
				"type": "vless", "tag": "bad-port", "server": "192.0.2.60",
				"server_port": float64(0),
				"uuid":        "11111111-2222-3333-4444-555555555555",
			},
			want: "server_port",
		},
		{
			name: "hysteria2 missing password",
			ob: map[string]any{
				"type": "hysteria2", "tag": "hy2-no-pw",
				"server": "192.0.2.70", "server_port": float64(443),
			},
			want: "hysteria2 password",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dropped := preFilterOutbounds([]any{tc.ob}, nil)
			if len(dropped) != 1 {
				t.Fatalf("expected 1 dropped, got %d", len(dropped))
			}
			if !strings.Contains(dropped[0].Reason, tc.want) {
				t.Errorf("reason = %q, want it to contain %q", dropped[0].Reason, tc.want)
			}
		})
	}
}

func TestPreFilterOutbounds_SkipsGroupOutbounds(t *testing.T) {
	// selector / urltest don't take server fields; Pass 1 should let
	// them through and let cascade cleanup handle empty groups.
	sel := map[string]any{
		"type":      "selector",
		"tag":       "sel",
		"outbounds": []any{"member-a"},
	}
	urltest := map[string]any{
		"type":      "urltest",
		"tag":       "ut",
		"outbounds": []any{"member-a"},
		"url":       "https://example.com/204",
	}
	kept, dropped := preFilterOutbounds([]any{sel, urltest}, nil)
	if len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("expected 2 kept / 0 dropped, got %d / %d (reasons=%v)", len(kept), len(dropped), dropped)
	}
}

func TestPreFilterOutbounds_DropsNonObject(t *testing.T) {
	_, dropped := preFilterOutbounds([]any{"not an object", 42}, nil)
	if len(dropped) != 2 {
		t.Fatalf("expected 2 dropped non-objects, got %d", len(dropped))
	}
	for _, d := range dropped {
		if d.Tag != "" || !strings.Contains(d.Reason, "non-object") {
			t.Errorf("got drop %+v, want empty tag and non-object reason", d)
		}
	}
}

// === sing-box error attribution ==========================================
//
// Index parsing/attribution itself lives in orchestrator.CheckMerged (it
// knows the snapshot composition) and is tested there; here we only check
// that flush's helper takes attributed indexes of OUR slot and nothing else.

func TestSubscriptionsOutboundIndex_OwnSlot(t *testing.T) {
	idx := 7
	res := orchestrator.ValidationResult{Errors: []orchestrator.ValidationError{{
		Slot: orchestrator.SlotSubscriptions, Kind: "sing-box check",
		OutboundSlot: orchestrator.SlotSubscriptions, OutboundIndex: &idx,
	}}}
	got, ok := subscriptionsOutboundIndex(res)
	if !ok || got != 7 {
		t.Errorf("got (%d, %v), want (7, true)", got, ok)
	}
}

func TestSubscriptionsOutboundIndex_ForeignSlotOrUnattributed(t *testing.T) {
	idx := 3
	foreign := orchestrator.ValidationResult{Errors: []orchestrator.ValidationError{{
		Slot: orchestrator.SlotSubscriptions, Kind: "sing-box check",
		OutboundSlot: orchestrator.SlotTunnels, OutboundIndex: &idx,
	}}}
	if _, ok := subscriptionsOutboundIndex(foreign); ok {
		t.Error("index attributed to another slot must not be used")
	}
	unattributed := orchestrator.ValidationResult{Errors: []orchestrator.ValidationError{{
		Slot: orchestrator.SlotSubscriptions, Kind: "sing-box check", Message: "opaque",
	}}}
	if _, ok := subscriptionsOutboundIndex(unattributed); ok {
		t.Error("unattributed error must not yield an index")
	}
}

// === Reference cleanup ===================================================

func TestDropOutboundAndCleanRefs_CascadeToSelectorAndUrltest(t *testing.T) {
	cfg := &slotConfig{
		Outbounds: []any{
			map[string]any{"type": "vless", "tag": "a", "server": "192.0.2.1", "server_port": float64(443), "uuid": "11111111-2222-3333-4444-555555555555"},
			map[string]any{"type": "vless", "tag": "b", "server": "192.0.2.2", "server_port": float64(443), "uuid": "22222222-3333-4444-5555-666666666666"},
			map[string]any{"type": "selector", "tag": "sel", "outbounds": []any{"a", "b"}, "default": "a"},
			map[string]any{"type": "urltest", "tag": "ut", "outbounds": []any{"a", "b"}},
		},
		Route: map[string]any{
			"rules": []any{
				map[string]any{"inbound": "in", "outbound": "a"},
				map[string]any{"inbound": "in", "outbound": "b"},
			},
			"final": "a",
		},
	}
	tag, err := dropOutboundAndCleanRefs(cfg, 0) // drop "a"
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tag != "a" {
		t.Errorf("tag = %q, want a", tag)
	}
	// "a" is gone from outbounds.
	for _, ob := range cfg.Outbounds {
		if outboundTag(ob.(map[string]any)) == "a" {
			t.Error("outbound 'a' still present")
		}
	}
	// selector.outbounds = ["b"], default re-aimed to "b".
	sel := findByTag(cfg.Outbounds, "sel")
	if !reflect.DeepEqual(sel["outbounds"], []any{"b"}) {
		t.Errorf("selector outbounds = %v, want [b]", sel["outbounds"])
	}
	if sel["default"] != "b" {
		t.Errorf("selector default = %v, want b", sel["default"])
	}
	// urltest.outbounds = ["b"].
	ut := findByTag(cfg.Outbounds, "ut")
	if !reflect.DeepEqual(ut["outbounds"], []any{"b"}) {
		t.Errorf("urltest outbounds = %v, want [b]", ut["outbounds"])
	}
	// route.rules — only the "b" rule survives.
	rules, _ := cfg.Route["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("expected 1 route rule, got %d", len(rules))
	}
	if rules[0].(map[string]any)["outbound"] != "b" {
		t.Errorf("surviving rule outbound = %v, want b", rules[0])
	}
	// route.final pointed at "a" → cleared.
	if _, present := cfg.Route["final"]; present {
		t.Errorf("route.final should have been removed, got %v", cfg.Route["final"])
	}
}

func TestCleanReferencesToTag_DropsEmptyGroups(t *testing.T) {
	cfg := &slotConfig{
		Outbounds: []any{
			map[string]any{"type": "vless", "tag": "leaf", "server": "192.0.2.1", "server_port": float64(443), "uuid": "11111111-2222-3333-4444-555555555555"},
			map[string]any{"type": "selector", "tag": "single-member-sel", "outbounds": []any{"leaf"}, "default": "leaf"},
		},
	}
	// Drop the only leaf — selector should cascade-disappear too.
	if _, err := dropOutboundAndCleanRefs(cfg, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Outbounds) != 0 {
		t.Fatalf("expected outbounds empty after cascade, got %d (%v)", len(cfg.Outbounds), cfg.Outbounds)
	}
}

func TestPreFilterOutboundsFeatureGate(t *testing.T) {
	mieru := map[string]any{"type": "mieru", "tag": "m", "server": "1.2.3.4", "server_port": 443}

	for _, tc := range []struct {
		name     string
		features []string
		wantKept bool
	}{
		{"нет фичи — отбраковываем", []string{"with_quic"}, false},
		{"фича есть — оставляем", []string{"with_mieru_outbound"}, true},
		{"features неизвестны — не трогаем", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kept, dropped := preFilterOutbounds([]any{mieru}, tc.features)
			if tc.wantKept {
				if len(kept) != 1 || len(dropped) != 0 {
					t.Fatalf("ожидали сохранение, got kept=%d dropped=%v", len(kept), dropped)
				}
				return
			}
			if len(kept) != 0 || len(dropped) != 1 {
				t.Fatalf("ожидали отбраковку, got kept=%d dropped=%v", len(kept), dropped)
			}
			if !strings.Contains(dropped[0].Reason, "with_mieru_outbound") {
				t.Fatalf("причина без имени тега: %q", dropped[0].Reason)
			}
		})
	}
}

// findByTag is a test helper — returns the first outbound matching tag, or nil.
func findByTag(outs []any, tag string) map[string]any {
	for _, raw := range outs {
		if ob, ok := raw.(map[string]any); ok && outboundTag(ob) == tag {
			return ob
		}
	}
	return nil
}
