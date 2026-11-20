package tgwebproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidatePortsAndUpstreams_RejectsCollisionsAndSelfLoops(t *testing.T) {
	base := Config{
		Enabled:        true,
		Scenario:       ScenarioDual,
		ListenPort:     8085,
		DirectPort:     8443,
		AdminPort:      8086,
		PublicHostname: "tg.example.com",
		Secret:         "000102030405060708090a0b0c0d0e0f",
	}

	// Valid without CoverUpstream (uses built-in static cover fallback)
	if err := ValidatePortsAndUpstreams(base); err != nil {
		t.Fatalf("expected valid config without cover_upstream, got: %v", err)
	}

	// Valid with Xray CoverUpstream on 9008
	withXray := base
	withXray.CoverUpstream = "http://127.0.0.1:9008"
	if err := ValidatePortsAndUpstreams(withXray); err != nil {
		t.Fatalf("expected valid config with Xray cover_upstream :9008, got: %v", err)
	}

	// Self-loop: CoverUpstream points to Mux (:8085)
	loopMux := base
	loopMux.CoverUpstream = "http://127.0.0.1:8085"
	if err := ValidatePortsAndUpstreams(loopMux); err == nil {
		t.Fatalf("expected CoverUpstream referencing Mux :8085 to be rejected")
	}

	// Self-loop: CoverUpstream points to primary tproxy listen (:8084)
	loopTproxy := base
	loopTproxy.CoverUpstream = "http://127.0.0.1:8084"
	if err := ValidatePortsAndUpstreams(loopTproxy); err == nil {
		t.Fatalf("expected CoverUpstream referencing tproxy :8084 to be rejected")
	}

	// Collision: Mux port equals DirectPort
	collDirect := base
	collDirect.ListenPort = 8443
	if err := ValidatePortsAndUpstreams(collDirect); err == nil {
		t.Fatalf("expected ListenPort == DirectPort collision to be rejected")
	}

	// PrepareCandidate also fails before staging if self-loop exists
	svc, _, _ := setupTestService(t)
	if _, err := svc.PrepareCandidate("tx-loop-bad", loopMux); err == nil {
		t.Fatalf("expected PrepareCandidate to reject self-loop CoverUpstream")
	}
}

func TestTproxyBuiltinFallbackServes200WithoutXray(t *testing.T) {
	dir := t.TempDir()
	profPath := filepath.Join(dir, "profiles.json")
	cfgPath := filepath.Join(dir, "config.json")
	keyPath := filepath.Join(dir, "token.key")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0x10 + i)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}

	priListen := allocFreeLocalPort(t)
	priAdmin := allocFreeLocalPort(t)
	muxPort := allocFreeLocalPort(t)

	cfg := Config{
		Enabled:        true,
		Scenario:       ScenarioCDNHTTP,
		ListenPort:     muxPort,
		AdminPort:      priAdmin,
		PublicHostname: "tg-only.example.com",
		Secret:         "000102030405060708090a0b0c0d0e0f",
		Backend:        "127.0.0.1:2398",
		Endpoints: []WebProxyEndpoint{
			{
				ID:         "primary",
				Hostname:   "tg-only.example.com",
				ListenPort: priListen,
				AdminPort:  priAdmin,
				Preset:     PresetCDNKeenDNS,
				Enabled:    true,
			},
		},
	}

	profBytes, err := GenerateProfilesJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profPath, profBytes, 0600); err != nil {
		t.Fatal(err)
	}
	cfgBytes, err := GenerateTproxyConfigJSON(cfg, profPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(cfgBytes, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, hasUpstream := parsed["public_upstream"]; hasUpstream {
		t.Fatalf("expected Telegram-only config without CoverUpstream not to set public_upstream, got %v", parsed["public_upstream"])
	}
	if parsed["public_fallback"] != "builtin" {
		t.Fatalf("expected public_fallback=builtin, got %v", parsed["public_fallback"])
	}

	engine := NewTproxyEngine(dir)
	defer engine.Stop()
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("TproxyEngine.Apply failed: %v", err)
	}

	mux := NewMux()
	if err := mux.Start(muxPort, cfg.Endpoints, cfg.Secret); err != nil {
		t.Fatalf("mux.Start failed: %v", err)
	}
	t.Cleanup(func() { _ = mux.Stop() })

	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", muxPort), nil)
	req.Host = "tg-only.example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET / through mux -> tproxy failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 builtin cover response when Xray is absent (not 502), got %d (%q)", resp.StatusCode, string(body))
	}

	req404, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/missing-path", muxPort), nil)
	req404.Host = "tg-only.example.com"
	resp404, err := client.Do(req404)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp404.Body.Close()
	if resp404.StatusCode != http.StatusNotFound {
		t.Fatalf("expected HTTP 404 on unknown cover path, got %d", resp404.StatusCode)
	}
}

func TestRollbackRestoresExtraEndpoints(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	svc.mux = NewMux()
	t.Cleanup(func() { _ = svc.mux.Stop() })

	priListen := allocFreeLocalPort(t)
	priAdmin := allocFreeLocalPort(t)
	extraListen := allocFreeLocalPort(t)
	extraAdmin := allocFreeLocalPort(t)
	muxPort := allocFreeLocalPort(t)

	origCfg := Config{
		SchemaVersion:  CurrentSchemaVersion,
		Enabled:        true,
		Scenario:       ScenarioCDNHTTP,
		ListenPort:     muxPort,
		AdminPort:      priAdmin,
		PublicHostname: "old.example.com",
		Secret:         "00112233445566778899aabbccddeeff",
		Backend:        DefaultBackend,
		CarrierMode:    DefaultCarrierMode,
		Endpoints: []WebProxyEndpoint{
			{ID: "ep-1", Hostname: "old.example.com", ListenPort: priListen, AdminPort: priAdmin, Enabled: true},
			{ID: "ep-2", Hostname: "old-extra.example.com", ListenPort: extraListen, AdminPort: extraAdmin, Enabled: true},
		},
	}
	svc.config = origCfg
	if err := svc.saveSettingsFileLocked(origCfg); err != nil {
		t.Fatal(err)
	}
	tproxyDir := filepath.Join(tempDir, "tproxy")
	extraCfgPath := filepath.Join(tproxyDir, "config_ep-2.json")
	extraProfPath := filepath.Join(tproxyDir, "profiles_ep-2.json")
	if err := os.WriteFile(extraCfgPath, []byte(`{"sentinel":"orig-ep-2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extraProfPath, []byte(`{"sentinel":"orig-prof-2"}`), 0600); err != nil {
		t.Fatal(err)
	}

	cand := origCfg
	cand.Endpoints = []WebProxyEndpoint{
		{ID: "ep-1", Hostname: "new.example.com", ListenPort: priListen, AdminPort: priAdmin, Enabled: true},
		{ID: "ep-3", Hostname: "new-extra.example.com", ListenPort: allocFreeLocalPort(t), AdminPort: allocFreeLocalPort(t), Enabled: true},
	}

	txID, err := svc.PrepareCandidate("tx-extra-rb", cand)
	if err != nil {
		t.Fatalf("PrepareCandidate: %v", err)
	}
	if err := svc.CommitPrepared(txID); err != nil {
		t.Fatalf("CommitPrepared: %v", err)
	}
	if _, err := os.Stat(extraCfgPath); !os.IsNotExist(err) {
		t.Fatalf("expected old extra config_ep-2.json to be removed after candidate commit")
	}

	if err := svc.RollbackPrepared(txID); err != nil {
		t.Fatalf("RollbackPrepared: %v", err)
	}
	restoredBytes, err := os.ReadFile(extraCfgPath)
	if err != nil {
		t.Fatalf("expected config_ep-2.json restored after rollback, err=%v", err)
	}
	if !strings.Contains(string(restoredBytes), "orig-ep-2") {
		t.Fatalf("restored extra endpoint config mismatch: %q", string(restoredBytes))
	}
	if _, err := os.Stat(filepath.Join(tproxyDir, "config_ep-3.json")); !os.IsNotExist(err) {
		t.Fatalf("expected uncommitted config_ep-3.json to be removed on rollback")
	}
}

func TestConfigGen_ReadinessSeparateContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := NewTproxyEngine(t.TempDir())
	defer e.Stop()
	if err := e.CheckPrimaryReady(ctx); err == nil {
		t.Fatalf("expected non-running tproxy CheckPrimaryReady to fail")
	}
}
