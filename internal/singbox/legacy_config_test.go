package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
