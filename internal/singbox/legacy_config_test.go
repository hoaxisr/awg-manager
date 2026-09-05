package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func writeLegacyConfig(t *testing.T, path string, body map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureLegacyConfigMigrated_HappyPath(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	legacy := filepath.Join(dir, "config.json")
	writeLegacyConfig(t, legacy, map[string]any{
		"log": map[string]any{"level": "info"},
		"dns": map[string]any{"servers": []any{
			map[string]any{"tag": "dns-bootstrap", "type": "udp", "server": "1.1.1.1"},
		}},
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:9099"}},
		"inbounds": []any{
			map[string]any{"tag": "vless-1-in", "type": "mixed", "listen": "127.0.0.1", "listen_port": 1080},
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "vless", "tag": "vless-1", "server": "example.com", "server_port": 443},
		},
		"route": map[string]any{
			"rules": []any{
				map[string]any{"inbound": "vless-1-in", "outbound": "vless-1"},
			},
			"final":                   "direct",
			"default_domain_resolver": "dns-bootstrap",
		},
	})

	ensureLegacyConfigMigrated(dir)

	target := filepath.Join(configDir, "10-tunnels.json")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("10-tunnels.json missing: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	// Slot-shaped: only inbounds/outbounds/route, no log/dns/experimental
	for _, k := range []string{"log", "dns", "experimental"} {
		if _, exists := got[k]; exists {
			t.Errorf("slot must not contain %q (owned by 00-base.json)", k)
		}
	}

	// Inbound preserved
	inbounds, _ := got["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Errorf("expected 1 inbound, got %d", len(inbounds))
	}

	// Outbounds: direct placeholder filtered, user vless preserved
	outbounds, _ := got["outbounds"].([]any)
	if len(outbounds) != 1 {
		t.Fatalf("expected 1 outbound (direct dropped), got %d: %v", len(outbounds), outbounds)
	}
	ob, _ := outbounds[0].(map[string]any)
	if tag, _ := ob["tag"].(string); tag != "vless-1" {
		t.Errorf("expected vless-1, got tag=%q", tag)
	}

	// Route rules preserved, route.final / default_domain_resolver dropped
	route, _ := got["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	if len(rules) != 1 {
		t.Errorf("expected 1 route rule, got %d", len(rules))
	}
	if _, has := route["final"]; has {
		t.Error("route.final must not be in slot — owned by 00-base.json")
	}
	if _, has := route["default_domain_resolver"]; has {
		t.Error("route.default_domain_resolver must not be in slot")
	}

	// Legacy config.json removed on success
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy config.json still present: %v", err)
	}
}

func TestEnsureLegacyConfigMigrated_NoLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0755); err != nil {
		t.Fatal(err)
	}
	ensureLegacyConfigMigrated(dir) // must not panic
	if _, err := os.Stat(filepath.Join(dir, "config.d", "10-tunnels.json")); !os.IsNotExist(err) {
		t.Error("no slot should be written when legacy is absent")
	}
}

func TestEnsureLegacyConfigMigrated_TargetAlreadyExists_NoOp(t *testing.T) {
	dir := t.TempDir()
	cd := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(cd, 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing slot
	preExisting := []byte(`{"inbounds":[],"outbounds":[]}`)
	if err := os.WriteFile(filepath.Join(cd, "10-tunnels.json"), preExisting, 0644); err != nil {
		t.Fatal(err)
	}
	// Legacy config.json with content
	writeLegacyConfig(t, filepath.Join(dir, "config.json"), map[string]any{
		"outbounds": []any{map[string]any{"type": "vless", "tag": "newer"}},
	})

	ensureLegacyConfigMigrated(dir)

	// Slot must remain untouched (we did not overwrite)
	got, _ := os.ReadFile(filepath.Join(cd, "10-tunnels.json"))
	if string(got) != string(preExisting) {
		t.Errorf("slot was overwritten when it should have been left alone")
	}
	// Legacy must still be present (we did not migrate)
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("legacy file removed despite no-op condition: %v", err)
	}
}

func TestEnsureLegacyConfigMigrated_CorruptJSON_NoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not json {{"), 0644); err != nil {
		t.Fatal(err)
	}

	ensureLegacyConfigMigrated(dir)

	// Slot must NOT be written from corrupt source
	if _, err := os.Stat(filepath.Join(dir, "config.d", "10-tunnels.json")); !os.IsNotExist(err) {
		t.Error("slot must not be written from corrupt legacy")
	}
	// Legacy must remain (so user / next-boot retry can act)
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Errorf("legacy file removed despite parse failure: %v", err)
	}
}

func TestEnsureLegacyConfigMigrated_StripsDeviceProxyAndPreservesDNS(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}

	legacy := filepath.Join(dir, "config.json")
	writeLegacyConfig(t, legacy, map[string]any{
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"tag": "dns-bootstrap", "type": "udp", "server": "1.1.1.1"},
				map[string]any{"tag": "dns-doh", "type": "https", "server": "cloudflare-dns.com"},
				map[string]any{"tag": "dns-custom", "type": "udp", "server": "8.8.8.8"},
			},
			"rules": []any{
				map[string]any{"domain_suffix": ".example.com", "server": "dns-custom"},
			},
		},
		"inbounds": []any{
			map[string]any{"tag": "vless-1-in", "type": "mixed", "listen_port": 1080},
			map[string]any{"tag": "device-proxy-in", "type": "mixed", "listen_port": 1099},
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "vless", "tag": "vless-1", "server": "x"},
			map[string]any{"type": "selector", "tag": "device-proxy-selector", "outbounds": []any{"vless-1"}},
		},
		"route": map[string]any{
			"rules": []any{
				map[string]any{"inbound": "vless-1-in", "outbound": "vless-1"},
				map[string]any{"inbound": "device-proxy-in", "outbound": "device-proxy-selector"},
				map[string]any{"outbound": "device-proxy-selector"},
			},
		},
	})

	ensureLegacyConfigMigrated(dir)

	target := filepath.Join(configDir, "10-tunnels.json")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("10-tunnels.json missing: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}

	// Inbounds: device-proxy-in dropped, vless-1-in kept
	inbounds, _ := got["inbounds"].([]any)
	if len(inbounds) != 1 {
		t.Errorf("inbounds = %d, want 1: %+v", len(inbounds), inbounds)
	}

	// Outbounds: direct + device-proxy-selector dropped, vless-1 kept
	outbounds, _ := got["outbounds"].([]any)
	if len(outbounds) != 1 {
		t.Errorf("outbounds = %d, want 1: %+v", len(outbounds), outbounds)
	}

	// Route rules: only the vless-1-in/vless-1 rule survives
	route, _ := got["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	if len(rules) != 1 {
		t.Errorf("route.rules = %d, want 1: %+v", len(rules), rules)
	}

	// DNS: dns-custom kept, dns-bootstrap + dns-doh stripped, rules kept
	dns, _ := got["dns"].(map[string]any)
	if dns == nil {
		t.Fatal("dns block missing")
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Errorf("dns.servers = %d, want 1: %+v", len(servers), servers)
	} else {
		s, _ := servers[0].(map[string]any)
		if tag, _ := s["tag"].(string); tag != "dns-custom" {
			t.Errorf("dns.servers[0].tag = %q, want dns-custom", tag)
		}
	}
	dnsRules, _ := dns["rules"].([]any)
	if len(dnsRules) != 1 {
		t.Errorf("dns.rules = %d, want 1", len(dnsRules))
	}
}

func TestEnsureLegacyConfigMigrated_NoCustomDNSOmitsBlock(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeLegacyConfig(t, filepath.Join(dir, "config.json"), map[string]any{
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"tag": "dns-bootstrap", "type": "udp", "server": "1.1.1.1"},
			},
		},
		"outbounds": []any{},
	})

	ensureLegacyConfigMigrated(dir)

	data, _ := os.ReadFile(filepath.Join(configDir, "10-tunnels.json"))
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if _, has := got["dns"]; has {
		t.Error("dns block must be absent when only our bootstrap remains")
	}
}

// TestEnsureLegacyConfigMigrated_DanglingDNSDohRuleValidates — пин F43.
// Легаси-конфиг несёт dns.rules со ссылкой на "dns-doh" и dns.final той же
// ссылкой. dns-doh — легаси-фантом: сервер отфильтрован (owned-set), а
// dns.final миграция НЕ копирует вовсе (dnsSlot несёт только servers+rules).
// Сирота в dns.rules[].server доезжает до merged, но наш кросс-слотовый
// валидатор его не проверяет (dnsRuleJSON собирает только rule_set), и
// sing-box резолвит dns.rules per-query, мягко деградируя на ненайденный
// транспорт — check проходит (docs/plans/2026-08-29-base-config-defects.md
// §0). Пин защищает именно это: будущая правка миграции, которая начнёт
// копировать dns.final, открыла бы жёсткую ссылку заново.
func TestEnsureLegacyConfigMigrated_DanglingDNSDohRuleValidates(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	// config.d уже существует к моменту миграции — как в реальном буте,
	// где ensureBaseConfig (шаг раньше в reconcileConfigSteps) успевает
	// создать 00-base.json первым.
	ensureBaseConfig(configDir, "info", "", 0, "")

	writeLegacyConfig(t, filepath.Join(dir, "config.json"), map[string]any{
		"dns": map[string]any{
			"final": "dns-doh",
			"servers": []any{
				map[string]any{"tag": "dns-bootstrap", "type": "udp", "server": "1.1.1.1"},
				map[string]any{"tag": "dns-doh", "type": "https", "server": "cloudflare-dns.com"},
				map[string]any{"tag": "dns-custom", "type": "udp", "server": "8.8.8.8"},
			},
			"rules": []any{
				map[string]any{"domain_suffix": ".example.com", "server": "dns-doh"},
			},
		},
		"inbounds": []any{
			map[string]any{"tag": "vless-1-in", "type": "mixed", "listen": "127.0.0.1", "listen_port": 1080},
		},
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
			map[string]any{"type": "vless", "tag": "vless-1", "server": "x"},
		},
		"route": map[string]any{
			"rules": []any{
				map[string]any{"inbound": "vless-1-in", "outbound": "vless-1"},
			},
		},
	})

	ensureLegacyConfigMigrated(dir)

	proc := NewProcess("", configDir, filepath.Join(dir, "singbox.pid"))
	orch := singboxorch.NewWithAppliedPath(configDir, proc, filepath.Join(t.TempDir(), "singbox-applied.json"))
	for _, meta := range singboxorch.KnownSlots() {
		switch meta.Slot {
		case singboxorch.SlotBase, singboxorch.SlotTunnels:
		default:
			continue
		}
		if err := orch.Register(meta); err != nil {
			t.Fatalf("register %s: %v", meta.Slot, err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	res := orch.Validate()
	if !res.Ok() {
		t.Fatalf("merged config должен быть валиден при сироте dns.rules[].server=dns-doh: %s", res.Error())
	}
}

func TestEnsureLegacyConfigMigrated_LegacyIsDirectory_NoOp(t *testing.T) {
	dir := t.TempDir()
	// `config.json` is a directory (degenerate case) — ignored
	if err := os.MkdirAll(filepath.Join(dir, "config.json"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0755); err != nil {
		t.Fatal(err)
	}

	ensureLegacyConfigMigrated(dir)

	if _, err := os.Stat(filepath.Join(dir, "config.d", "10-tunnels.json")); !os.IsNotExist(err) {
		t.Error("slot must not be written when legacy is a directory")
	}
}
