package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyConfigToConfigD(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "config.json")

	legacy := map[string]any{
		"log": map[string]any{"level": "info", "timestamp": true},
		"experimental": map[string]any{
			"clash_api": map[string]any{"external_controller": "127.0.0.1:9090"},
		},
		"inbounds": []any{
			map[string]any{"type": "mixed", "tag": "Germany-in", "listen": "127.0.0.1", "listen_port": 1080},
		},
		"outbounds": []any{
			map[string]any{"type": "vless", "tag": "Germany", "server": "de1.example.com", "server_port": 443},
		},
		"route": map[string]any{
			"rules": []any{map[string]any{"inbound": "Germany-in", "outbound": "Germany"}},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyConfigDir(dir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy config.json должен быть удалён, err=%v", err)
	}

	baseRaw, err := os.ReadFile(filepath.Join(dir, "config.d", "00-base.json"))
	if err != nil {
		t.Fatalf("read base: %v", err)
	}
	var base map[string]any
	if err := json.Unmarshal(baseRaw, &base); err != nil {
		t.Fatal(err)
	}
	if _, ok := base["log"]; !ok {
		t.Error("base должен содержать log")
	}
	if _, ok := base["experimental"]; !ok {
		t.Error("base должен содержать experimental")
	}
	if _, ok := base["inbounds"]; ok {
		t.Error("base НЕ должен содержать inbounds")
	}

	tunnelsRaw, err := os.ReadFile(filepath.Join(dir, "config.d", "10-tunnels.json"))
	if err != nil {
		t.Fatalf("read tunnels: %v", err)
	}
	var tunnels map[string]any
	if err := json.Unmarshal(tunnelsRaw, &tunnels); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"inbounds", "outbounds", "route"} {
		if _, ok := tunnels[k]; !ok {
			t.Errorf("tunnels должен содержать %q", k)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(dir, "config.d", "00-base.json")
	if err := os.WriteFile(basePath, []byte(`{"log":{"level":"info"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyConfigDir(dir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(string(raw), `"level":"info"`) {
		t.Errorf("base не должен быть перезаписан, got %q", raw)
	}
}

func TestMigrateNoLegacyCreatesConfigDir(t *testing.T) {
	dir := t.TempDir()
	if err := MigrateLegacyConfigDir(dir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.d"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("config.d должна быть директорией")
	}
}

func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
