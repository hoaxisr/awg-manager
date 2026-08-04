package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/deviceproxy"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
)

// writeSlot creates a slot file in dir. Fails the test on error.
func writeSlot(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func callInbounds(t *testing.T, h *SingboxInboundsHandler) SingboxInboundsResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/inbounds", nil)
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Success bool                    `json:"success"`
		Data    SingboxInboundsResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if !env.Success {
		t.Fatalf("expected success=true: %s", rec.Body.String())
	}
	return env.Data
}

func entryByTag(t *testing.T, resp SingboxInboundsResponse, tag string) SingboxInboundEntry {
	t.Helper()
	for _, e := range resp.Inbounds {
		if e.Tag == tag {
			return e
		}
	}
	t.Fatalf("inbound %q not found in %+v", tag, resp.Inbounds)
	return SingboxInboundEntry{}
}

func TestSingboxInboundsHandler_AttributionPerSlot(t *testing.T) {
	dir := t.TempDir()
	writeSlot(t, dir, "10-tunnels.json",
		`{"inbounds":[{"type":"mixed","tag":"my-vless-in","listen":"127.0.0.1","listen_port":1081}],
		  "route":{"rules":[{"inbound":"my-vless-in","outbound":"my-vless"}]}}`)
	writeSlot(t, dir, "18-qos-routes.json",
		`{"inbounds":[{"type":"tproxy","tag":"tproxy-qos-0","listen":"127.0.0.1","listen_port":51281}]}`)
	writeSlot(t, dir, "21-routing.json",
		`{"inbounds":[{"type":"tproxy","tag":"tproxy-in","listen":"127.0.0.1","listen_port":51280},{"type":"redirect","tag":"redirect-in","listen":"127.0.0.1","listen_port":51300}]}`)
	writeSlot(t, dir, "20-fakeip.json",
		`{"inbounds":[{"type":"tun","tag":"tun-in"}]}`)
	writeSlot(t, dir, "30-deviceproxy.json",
		`{"inbounds":[{"type":"mixed","tag":"device-proxy-abc-in","listen":"0.0.0.0","listen_port":1099}]}`)
	// route.rules: строковая и массивная формы "inbound" — обе валидны для
	// sing-box, парсер должен принимать обе.
	writeSlot(t, dir, "40-subscriptions.json",
		`{"inbounds":[
			{"type":"mixed","tag":"sub-11112222-in","listen":"127.0.0.1","listen_port":1200},
			{"type":"mixed","tag":"agg-33334444-in","listen":"127.0.0.1","listen_port":1201}],
		  "route":{"rules":[
			{"inbound":"sub-11112222-in","action":"route","outbound":"sub-11112222-selector"},
			{"inbound":["agg-33334444-in"],"action":"route","outbound":"agg-33334444"}]}}`)

	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return dir },
		Subscriptions: func() []subscription.Subscription {
			return []subscription.Subscription{{
				Label: "Моя подписка", InboundTag: "sub-11112222-in",
				ListenPort: 1200, ProxyIndex: 3, Enabled: true,
			}}
		},
		Groups: func() []subscription.AggregateGroup {
			return []subscription.AggregateGroup{{
				Label: "Сводная", InboundTag: "agg-33334444-in",
				ListenPort: 1201, ProxyIndex: 4, Enabled: true,
			}}
		},
		DeviceProxyInstances: func() []deviceproxy.Instance {
			return []deviceproxy.Instance{{ID: "abc", Name: "Прокси гостиной"}}
		},
		NDMSProxyEnabled: func() bool { return true },
	})
	resp := callInbounds(t, h)

	if len(resp.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", resp.Warnings)
	}
	if len(resp.Inbounds) != 8 {
		t.Fatalf("expected 8 inbounds, got %d: %+v", len(resp.Inbounds), resp.Inbounds)
	}

	cases := []struct {
		tag, slot, source, owner string
	}{
		{"my-vless-in", "tunnels", "tunnel", "my-vless"},
		{"tproxy-qos-0", "qos-routes", "qos", ""},
		{"tproxy-in", "routing", "engine", ""},
		{"redirect-in", "routing", "engine", ""},
		{"tun-in", "fakeip", "engine", ""},
		{"device-proxy-abc-in", "deviceproxy", "deviceproxy", "Прокси гостиной"},
		{"sub-11112222-in", "subscriptions", "subscription", "Моя подписка"},
		{"agg-33334444-in", "subscriptions", "group", "Сводная"},
	}
	for _, c := range cases {
		e := entryByTag(t, resp, c.tag)
		if e.Slot != c.slot {
			t.Errorf("%s: slot = %q, want %q", c.tag, e.Slot, c.slot)
		}
		if e.Source != c.source {
			t.Errorf("%s: source = %q, want %q", c.tag, e.Source, c.source)
		}
		if e.OwnerLabel != c.owner {
			t.Errorf("%s: ownerLabel = %q, want %q", c.tag, e.OwnerLabel, c.owner)
		}
		if e.Idle {
			t.Errorf("%s: idle = true, want false (toggle on, entities enabled)", c.tag)
		}
	}

	// listen / listenPort нормализация.
	sub := entryByTag(t, resp, "sub-11112222-in")
	if sub.Listen != "127.0.0.1" || sub.ListenPort != 1200 || sub.Type != "mixed" {
		t.Errorf("sub entry fields = %+v", sub)
	}
	tun := entryByTag(t, resp, "tun-in")
	if tun.ListenPort != 0 || tun.Type != "tun" {
		t.Errorf("tun entry fields = %+v", tun)
	}
}

func TestSingboxInboundsHandler_IdleMatrix(t *testing.T) {
	subs := func(enabled bool, proxyIdx int) func() []subscription.Subscription {
		return func() []subscription.Subscription {
			return []subscription.Subscription{{
				Label: "S", InboundTag: "sub-aaaabbbb-in", ProxyIndex: proxyIdx, Enabled: enabled,
			}}
		}
	}
	groups := func(enabled bool, proxyIdx int) func() []subscription.AggregateGroup {
		return func() []subscription.AggregateGroup {
			return []subscription.AggregateGroup{{
				Label: "G", InboundTag: "agg-ccccdddd-in", ProxyIndex: proxyIdx, Enabled: enabled,
			}}
		}
	}

	// Idle-семантика заземлена на конфиг: наличие route-правила своего
	// слота (routed) — главный сигнал; флаги Enabled сущностей не участвуют.
	// Матрица: route-правило есть/нет × тумблер NDMS × ProxyIndex.
	cases := []struct {
		name       string
		ndmsOn     bool
		routed     bool // route-правила слота ссылаются на sub/agg/tunnel inbound'ы
		subs       func() []subscription.Subscription
		groups     func() []subscription.AggregateGroup
		tag        string
		wantIdle   bool
		wantReason string
	}{
		{"sub: всё включено", true, true, subs(true, 1), nil, "sub-aaaabbbb-in", false, ""},
		{"sub: тумблер выключен", false, true, subs(true, 1), nil, "sub-aaaabbbb-in", true, "ndms_proxy_disabled"},
		{"sub: тумблер on, ProxyN не выделен", true, true, subs(true, -1), nil, "sub-aaaabbbb-in", true, "ndms_proxy_missing"},
		{"sub: подписка отключена — правило остаётся, НЕ idle", true, true, subs(false, 1), nil, "sub-aaaabbbb-in", false, ""},
		{"sub: нет route-правила", true, true, subs(true, 1), nil, "sub-eeeeffff-in", true, "no_route_rule"},
		{"sub: нет правила и тумблер off — приоритет no_route_rule", false, true, subs(true, 1), nil, "sub-eeeeffff-in", true, "no_route_rule"},
		{"sub: nil store, тумблер on", true, true, nil, nil, "sub-aaaabbbb-in", false, ""},
		{"sub: nil store, тумблер off", false, true, nil, nil, "sub-aaaabbbb-in", true, "ndms_proxy_disabled"},
		{"sub: nil store, нет route-правила", true, false, nil, nil, "sub-aaaabbbb-in", true, "no_route_rule"},
		{"group: всё включено", true, true, nil, groups(true, 2), "agg-ccccdddd-in", false, ""},
		{"group: тумблер on, ProxyN не выделен", true, true, nil, groups(true, -1), "agg-ccccdddd-in", true, "ndms_proxy_missing"},
		{"group: включена, но без серверов — правило снято", true, false, nil, groups(true, 2), "agg-ccccdddd-in", true, "no_route_rule"},
		{"group: отключена — правило снято", true, false, nil, groups(false, 2), "agg-ccccdddd-in", true, "no_route_rule"},
		{"tunnel: тумблер on", true, true, nil, nil, "tun1-in", false, ""},
		{"tunnel: тумблер off", false, true, nil, nil, "tun1-in", true, "ndms_proxy_disabled"},
		{"tunnel: нет route-правила", true, false, nil, nil, "tun1-in", true, "no_route_rule"},
		{"engine: тумблер off — не idle", false, true, nil, nil, "tproxy-in", false, ""},
		{"deviceproxy: тумблер off — не idle", false, true, nil, nil, "device-proxy-in", false, ""},
		{"qos: тумблер off — не idle", false, true, nil, nil, "tproxy-qos-1", false, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			tunnelRules := ""
			subRules := ""
			if c.routed {
				tunnelRules = `,"route":{"rules":[{"inbound":"tun1-in","outbound":"tun1"}]}`
				// Массивная форма "inbound" у группы — обе формы валидны.
				subRules = `,"route":{"rules":[
					{"inbound":"sub-aaaabbbb-in","action":"route","outbound":"sub-aaaabbbb-selector"},
					{"inbound":["agg-ccccdddd-in"],"action":"route","outbound":"agg-ccccdddd"}]}`
			}
			writeSlot(t, dir, "10-tunnels.json",
				`{"inbounds":[{"type":"mixed","tag":"tun1-in","listen":"127.0.0.1","listen_port":1081}]`+tunnelRules+`}`)
			writeSlot(t, dir, "18-qos-routes.json",
				`{"inbounds":[{"type":"tproxy","tag":"tproxy-qos-1","listen":"127.0.0.1","listen_port":51282}]}`)
			writeSlot(t, dir, "21-routing.json",
				`{"inbounds":[{"type":"tproxy","tag":"tproxy-in","listen":"127.0.0.1","listen_port":51280}]}`)
			writeSlot(t, dir, "30-deviceproxy.json",
				`{"inbounds":[{"type":"mixed","tag":"device-proxy-in","listen":"0.0.0.0","listen_port":1099}]}`)
			// sub-eeeeffff-in — inbound без route-правила даже при routed=true
			// (случай «включённая сущность, но конфиг порт не питает»).
			writeSlot(t, dir, "40-subscriptions.json",
				`{"inbounds":[
					{"type":"mixed","tag":"sub-aaaabbbb-in","listen":"127.0.0.1","listen_port":1300},
					{"type":"mixed","tag":"agg-ccccdddd-in","listen":"127.0.0.1","listen_port":1301},
					{"type":"mixed","tag":"sub-eeeeffff-in","listen":"127.0.0.1","listen_port":1302}]`+subRules+`}`)

			h := NewSingboxInboundsHandler(SingboxInboundsDeps{
				ConfigDir:        func() string { return dir },
				Subscriptions:    c.subs,
				Groups:           c.groups,
				NDMSProxyEnabled: func() bool { return c.ndmsOn },
			})
			e := entryByTag(t, callInbounds(t, h), c.tag)
			if e.Idle != c.wantIdle || e.IdleReason != c.wantReason {
				t.Errorf("idle = (%v, %q), want (%v, %q)", e.Idle, e.IdleReason, c.wantIdle, c.wantReason)
			}
		})
	}
}

func TestSingboxInboundsHandler_UnreadableSlotWarning(t *testing.T) {
	dir := t.TempDir()
	writeSlot(t, dir, "10-tunnels.json",
		`{"inbounds":[{"type":"mixed","tag":"ok-in","listen":"127.0.0.1","listen_port":1081}]}`)
	writeSlot(t, dir, "40-subscriptions.json", `{broken json`)

	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return dir },
	})
	resp := callInbounds(t, h)

	if len(resp.Inbounds) != 1 || resp.Inbounds[0].Tag != "ok-in" {
		t.Errorf("expected only readable slot's inbound, got %+v", resp.Inbounds)
	}
	if len(resp.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", resp.Warnings)
	}
	if got := resp.Warnings[0]; !strings.Contains(got, "40-subscriptions.json") {
		t.Errorf("warning does not name the unreadable slot: %q", got)
	}
}

// TestSingboxInboundsHandler_DuplicateTagWarning: коллизия тегов между
// слотами (например, рукой отредактированный слот) — MergeDir откажется
// мержить, sing-box такой конфиг не загрузит; зеркало обязано сказать об
// этом warning'ом, а не молча показать два ряда с одним тегом.
func TestSingboxInboundsHandler_DuplicateTagWarning(t *testing.T) {
	dir := t.TempDir()
	writeSlot(t, dir, "10-tunnels.json",
		`{"inbounds":[{"type":"mixed","tag":"clash-in","listen":"127.0.0.1","listen_port":1081}]}`)
	writeSlot(t, dir, "21-routing.json",
		`{"inbounds":[{"type":"tproxy","tag":"clash-in","listen":"127.0.0.1","listen_port":51280}]}`)

	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return dir },
	})
	resp := callInbounds(t, h)

	if len(resp.Inbounds) != 2 {
		t.Fatalf("expected both colliding inbounds returned, got %+v", resp.Inbounds)
	}
	if len(resp.Warnings) != 1 {
		t.Fatalf("expected 1 duplicate-tag warning, got %v", resp.Warnings)
	}
	want := `конфликт тегов inbound: "clash-in" в слотах tunnels и routing — sing-box не загрузит такой конфиг`
	if resp.Warnings[0] != want {
		t.Errorf("warning = %q, want %q", resp.Warnings[0], want)
	}
}

// TestSingboxInboundsHandler_ActiveSlotsOnly пиннит инвариант «зеркало ==
// то, что грузит sing-box» (совпадает с configmerge.collectActiveSlots):
// припаркованные слоты в config.d/disabled/, черновики в config.d/pending/
// и не-.json файлы в config.d не дают inbound'ов и warnings.
func TestSingboxInboundsHandler_ActiveSlotsOnly(t *testing.T) {
	dir := t.TempDir()
	writeSlot(t, dir, "10-tunnels.json",
		`{"inbounds":[{"type":"mixed","tag":"active-in","listen":"127.0.0.1","listen_port":1081}]}`)
	for _, sub := range []string{"disabled", "pending"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeSlot(t, filepath.Join(dir, "disabled"), "40-subscriptions.json",
		`{"inbounds":[{"type":"mixed","tag":"parked-in","listen":"127.0.0.1","listen_port":1300}]}`)
	writeSlot(t, filepath.Join(dir, "pending"), "21-routing.json",
		`{"inbounds":[{"type":"tproxy","tag":"draft-in","listen":"127.0.0.1","listen_port":51280}]}`)
	writeSlot(t, dir, "notes.txt",
		`{"inbounds":[{"type":"mixed","tag":"not-json-in","listen":"127.0.0.1","listen_port":2000}]}`)

	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return dir },
	})
	resp := callInbounds(t, h)

	if len(resp.Inbounds) != 1 || resp.Inbounds[0].Tag != "active-in" {
		t.Errorf("expected only active slot's inbound, got %+v", resp.Inbounds)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", resp.Warnings)
	}
}

func TestSingboxInboundsHandler_EmptyConfig(t *testing.T) {
	dir := t.TempDir()
	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return dir },
	})
	resp := callInbounds(t, h)
	if len(resp.Inbounds) != 0 {
		t.Errorf("expected no inbounds, got %+v", resp.Inbounds)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", resp.Warnings)
	}
}

func TestSingboxInboundsHandler_MissingDirReturns500(t *testing.T) {
	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return filepath.Join(t.TempDir(), "nope") },
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/singbox/inbounds", nil)
	h.List(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSingboxInboundsHandler_MethodNotAllowed(t *testing.T) {
	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return t.TempDir() },
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/inbounds", nil)
	h.List(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestSingboxInboundsHandler_UnknownSlotFile проверяет fallback-атрибуцию
// для файла вне KnownSlots: имя слота = basename без числового префикса,
// mixed → other, tproxy → engine.
func TestSingboxInboundsHandler_UnknownSlotFile(t *testing.T) {
	dir := t.TempDir()
	writeSlot(t, dir, "37-custom.json",
		`{"inbounds":[
			{"type":"mixed","tag":"foreign-in","listen":"127.0.0.1","listen_port":2000},
			{"type":"tproxy","tag":"foreign-tproxy","listen":"127.0.0.1","listen_port":2001}]}`)

	h := NewSingboxInboundsHandler(SingboxInboundsDeps{
		ConfigDir: func() string { return dir },
	})
	resp := callInbounds(t, h)
	mixed := entryByTag(t, resp, "foreign-in")
	if mixed.Slot != "custom" || mixed.Source != "other" {
		t.Errorf("foreign mixed: slot/source = %q/%q, want custom/other", mixed.Slot, mixed.Source)
	}
	tproxy := entryByTag(t, resp, "foreign-tproxy")
	if tproxy.Source != "engine" {
		t.Errorf("foreign tproxy: source = %q, want engine", tproxy.Source)
	}
}
