// internal/singbox/deviceproxy_migrate_test.go
package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateDeviceProxyOutOfTunnelsHappyPath(t *testing.T) {
	dir := t.TempDir()
	// Build a tunnels file containing device-proxy inbound/outbound/rule
	// plus a regular tunnel outbound, mimicking the legacy single-file
	// layout where device-proxy injected itself into 10-tunnels.json.
	cfg := NewConfig()
	cfg.upsertOutbound("vpn-tunnel-1", map[string]any{
		"tag":  "vpn-tunnel-1",
		"type": "wireguard",
	})
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "192.168.1.1",
		Port:        1080,
		SelectedTag: "vpn-tunnel-1",
		SBTags:      []string{"vpn-tunnel-1"},
	}
	if err := cfg.EnsureDeviceProxy(spec); err != nil {
		t.Fatalf("ensure device proxy: %v", err)
	}
	legacyData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	legacyPath := filepath.Join(dir, "10-tunnels.json")
	if err := os.WriteFile(legacyPath, legacyData, 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateDeviceProxyOutOfTunnels(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// F34: признак «файлы переписаны» — по нему демон решает про reload.
	if !migrated {
		t.Error("migrated = false, want true: файлы переписаны")
	}

	// 30-deviceproxy.json must exist with the device-proxy artefacts.
	dpData, err := os.ReadFile(filepath.Join(dir, "30-deviceproxy.json"))
	if err != nil {
		t.Fatalf("read deviceproxy: %v", err)
	}
	var dpRaw map[string]any
	if err := json.Unmarshal(dpData, &dpRaw); err != nil {
		t.Fatal(err)
	}
	dpInbounds, _ := dpRaw["inbounds"].([]any)
	if len(dpInbounds) == 0 {
		t.Errorf("deviceproxy file missing inbounds: %s", dpData)
	}
	dpOutbounds, _ := dpRaw["outbounds"].([]any)
	var foundSelector bool
	for _, v := range dpOutbounds {
		ob, _ := v.(map[string]any)
		if tag, _ := ob["tag"].(string); tag == deviceProxySelectorTag {
			foundSelector = true
		}
		// Regular tunnel outbound must NOT have leaked into the
		// deviceproxy slot.
		if tag, _ := ob["tag"].(string); tag == "vpn-tunnel-1" {
			t.Errorf("vpn-tunnel-1 leaked into deviceproxy slot: %s", dpData)
		}
	}
	if !foundSelector {
		t.Errorf("deviceproxy file missing selector outbound: %s", dpData)
	}

	// 10-tunnels.json must NO LONGER contain device-proxy artefacts.
	tunnelsData, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read tunnels: %v", err)
	}
	var tcfg Config
	if err := json.Unmarshal(tunnelsData, &tcfg); err != nil {
		t.Fatal(err)
	}
	if tcfg.HasDeviceProxy() {
		t.Errorf("tunnels file still contains device-proxy: %s", tunnelsData)
	}
	// Regular tunnel outbound must remain in the tunnels slot.
	var foundTunnel bool
	for _, v := range tcfg.outbounds() {
		ob, _ := v.(map[string]any)
		if tag, _ := ob["tag"].(string); tag == "vpn-tunnel-1" {
			foundTunnel = true
		}
	}
	if !foundTunnel {
		t.Errorf("vpn-tunnel-1 missing from stripped tunnels: %s", tunnelsData)
	}
}

// Частичный провал миграции: 30-deviceproxy.json уже записан, а перезапись
// 10-tunnels.json упала — device-proxy остался в ОБОИХ слотах (дубль
// device-proxy-in валит preflight холодного старта). Следующий вызов обязан
// долечить: гард смотрит на СОДЕРЖИМОЕ 10-tunnels, а не на существование
// 30-файла (F73).
func TestMigrateHealsPartialSplit(t *testing.T) {
	dir := t.TempDir()

	// 30-файл-маркер: его владелец с момента раскола — Reconcile сервиса,
	// добор миграции обязан оставить его нетронутым.
	marker := []byte(`{"inbounds":[{"tag":"device-proxy-in","type":"mixed"}]}`)
	if err := os.WriteFile(filepath.Join(dir, "30-deviceproxy.json"), marker, 0644); err != nil {
		t.Fatal(err)
	}

	cfg := NewConfig()
	cfg.upsertOutbound("vpn-tunnel-1", map[string]any{
		"tag":  "vpn-tunnel-1",
		"type": "wireguard",
	})
	spec := DeviceProxySpec{
		Enabled:     true,
		ListenAddr:  "192.168.1.1",
		Port:        1080,
		SelectedTag: "vpn-tunnel-1",
		SBTags:      []string{"vpn-tunnel-1"},
	}
	if err := cfg.EnsureDeviceProxy(spec); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	legacy, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "10-tunnels.json"), legacy, 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateDeviceProxyOutOfTunnels(dir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !migrated {
		t.Error("migrated = false, want true: 10-tunnels ещё нёс device-proxy")
	}

	tunnelsData, err := os.ReadFile(filepath.Join(dir, "10-tunnels.json"))
	if err != nil {
		t.Fatal(err)
	}
	stripped := NewConfig()
	if err := json.Unmarshal(tunnelsData, stripped); err != nil {
		t.Fatal(err)
	}
	if stripped.HasDeviceProxy() {
		t.Errorf("device-proxy остался в 10-tunnels: %s", tunnelsData)
	}
	if !strings.Contains(string(tunnelsData), "vpn-tunnel-1") {
		t.Errorf("пользовательский туннель потерян: %s", tunnelsData)
	}

	// 30-файл не перезаписан: им владеет Reconcile, наша копия протухла бы.
	dpData, err := os.ReadFile(filepath.Join(dir, "30-deviceproxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(dpData) != string(marker) {
		t.Errorf("30-deviceproxy.json перезаписан: %s", dpData)
	}
}

// Штатный no-op: раскол уже завершён — 30-файл есть, 10-tunnels чист.
//
// Прежняя редакция этого теста («30 есть + 10-tunnels ЕЩЁ несёт device-proxy
// → no-op») закрепляла дефект F73 и переписана.
func TestMigrateNoOpWhenAlreadySplitAndTunnelsClean(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "30-deviceproxy.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := NewConfig()
	cfg.upsertOutbound("vpn", map[string]any{
		"tag":  "vpn",
		"type": "wireguard",
	})
	clean, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "10-tunnels.json"), clean, 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateDeviceProxyOutOfTunnels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("migrated = true, want false: раскол уже чист")
	}
	dpData, err := os.ReadFile(filepath.Join(dir, "30-deviceproxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(dpData) != `{}` {
		t.Errorf("30-deviceproxy.json тронут: %s", dpData)
	}
}

func TestMigrateNoOpWhenNoTunnelsFile(t *testing.T) {
	dir := t.TempDir()
	migrated, err := MigrateDeviceProxyOutOfTunnels(dir)
	if err != nil {
		t.Errorf("expected no error on missing tunnels file, got: %v", err)
	}
	if migrated {
		t.Error("migrated = true, want false: мигрировать было нечего")
	}
	if _, err := os.Stat(filepath.Join(dir, "30-deviceproxy.json")); !os.IsNotExist(err) {
		t.Errorf("deviceproxy file should not exist")
	}
}

func TestMigrateNoOpWhenTunnelsHasNoDeviceProxy(t *testing.T) {
	dir := t.TempDir()
	cfg := NewConfig()
	cfg.upsertOutbound("vpn-only", map[string]any{"tag": "vpn-only", "type": "wireguard"})
	legacy, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "10-tunnels.json"), legacy, 0644); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateDeviceProxyOutOfTunnels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("migrated = true, want false: device-proxy в источнике не было")
	}
	if _, err := os.Stat(filepath.Join(dir, "30-deviceproxy.json")); !os.IsNotExist(err) {
		t.Errorf("deviceproxy file should not be created when source had none")
	}
}
