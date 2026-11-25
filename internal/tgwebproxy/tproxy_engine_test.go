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

	"github.com/hoaxisr/awg-manager/internal/testutil"
)

func TestValidateTproxyFiles_ValidAndInvalid(t *testing.T) {
	dir := t.TempDir()
	profPath := filepath.Join(dir, "profiles.json")
	cfgPath := filepath.Join(dir, "config.json")
	keyPath := filepath.Join(dir, "token.key")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Enabled:        true,
		Scenario:       ScenarioDual,
		NetworkPreset:  PresetCDNKeenDNS,
		ListenPort:     8085,
		AdminPort:      18086,
		PublicHostname: "connect.example.com",
		Secret:         "000102030405060708090a0b0c0d0e0f",
		Backend:        "127.0.0.1:2398",
	}

	profBytes, err := GenerateProfilesJSON(cfg)
	if err != nil {
		t.Fatalf("GenerateProfilesJSON: %v", err)
	}
	if err := os.WriteFile(profPath, profBytes, 0600); err != nil {
		t.Fatal(err)
	}

	ep0 := cfg.GetEndpoints()[0]
	ep0.ListenPort = testutil.FreePort(t)
	ep0.AdminPort = testutil.FreePort(t)
	cfgBytes, err := GenerateEndpointTproxyConfigJSON(ep0, profPath, keyPath)
	if err != nil {
		t.Fatalf("GenerateEndpointTproxyConfigJSON: %v", err)
	}
	if err := os.WriteFile(cfgPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}

	if err := validateTproxyFiles(cfgPath, profPath); err != nil {
		t.Fatalf("expected valid tproxy config, got: %v", err)
	}

	// Verify staging candidate filenames work even when config.json.candidate points profiles_file elsewhere
	stagingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stagingDir, "profiles.json.candidate"), profBytes, 0600); err != nil {
		t.Fatal(err)
	}
	cfgCandBytes, err := GenerateEndpointTproxyConfigJSON(ep0, "/opt/nonexistent/profiles.json", keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "config.json.candidate"), cfgCandBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := defaultTproxyValidator(stagingDir); err != nil {
		t.Fatalf("expected defaultTproxyValidator to validate staging candidate files, got: %v", err)
	}

	// Corrupt config should fail validation
	if err := os.WriteFile(filepath.Join(stagingDir, "config.json.candidate"), []byte("{bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := defaultTproxyValidator(stagingDir); err == nil {
		t.Fatalf("expected defaultTproxyValidator to reject invalid JSON")
	}
}

func TestTproxyEngine_ApplyProfilesUpdateAndStop(t *testing.T) {
	dir := t.TempDir()
	profPath := filepath.Join(dir, "profiles.json")
	cfgPath := filepath.Join(dir, "config.json")
	keyPath := filepath.Join(dir, "token.key")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0xa0 + i)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}

	priListen := testutil.FreePort(t)
	priAdmin := testutil.FreePort(t)
	extraListen := testutil.FreePort(t)
	extraAdmin := testutil.FreePort(t)

	cfg := Config{
		Enabled:       true,
		Scenario:      ScenarioDual,
		NetworkPreset: PresetCDNKeenDNS,
		ListenPort:    8085,
		AdminPort:     priAdmin,
		Secret:        "000102030405060708090a0b0c0d0e0f",
		Backend:       "127.0.0.1:2398",
		Endpoints: []WebProxyEndpoint{
			{
				ID:         "ep-primary",
				Hostname:   "Proxy.Example.COM",
				ListenPort: priListen,
				AdminPort:  priAdmin,
				Preset:     PresetCDNKeenDNS,
				Enabled:    true,
			},
			{
				ID:         "ep-extra",
				Hostname:   "extra.example.com",
				ListenPort: extraListen,
				AdminPort:  extraAdmin,
				Preset:     PresetStaticIP,
				Enabled:    true,
			},
		},
	}

	writeCfg := func(c Config) {
		pBytes, err := GenerateProfilesJSON(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(profPath, pBytes, 0600); err != nil {
			t.Fatal(err)
		}
		cBytes, err := GenerateTproxyConfigJSON(c, profPath, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, cBytes, 0600); err != nil {
			t.Fatal(err)
		}
		ep1 := c.Endpoints[1]
		epProf := filepath.Join(dir, "profiles_ep-extra.json")
		epCfg := filepath.Join(dir, "config_ep-extra.json")
		epProfBytes, err := GenerateEndpointProfilesJSON(ep1, c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(epProf, epProfBytes, 0600); err != nil {
			t.Fatal(err)
		}
		epCfgBytes, err := GenerateEndpointTproxyConfigJSON(ep1, epProf, keyPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(epCfg, epCfgBytes, 0600); err != nil {
			t.Fatal(err)
		}
	}

	writeCfg(cfg)
	engine := NewTproxyEngine(dir)
	defer engine.Stop()

	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("engine.Apply failed: %v", err)
	}
	if !engine.IsPrimaryRunning() {
		t.Fatal("expected primary instance to be running")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := engine.CheckPrimaryReady(ctx); err != nil {
		t.Fatalf("CheckPrimaryReady failed: %v", err)
	}

	// Verify extra endpoint admin metrics & bridge endpoint work
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", extraAdmin))
	if err != nil {
		t.Fatalf("extra endpoint metrics failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "tproxy_") {
		t.Fatalf("unexpected extra metrics response: %d %q", resp.StatusCode, string(body))
	}

	// Verify config_gen lowercased hostname in config.json
	var parsed map[string]any
	rawCfg, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(rawCfg, &parsed)
	if parsed["public_hostname"] != "proxy.example.com" {
		t.Fatalf("expected lowercased public_hostname, got %v", parsed["public_hostname"])
	}

	// Update secret only -> should hot-update profiles in-place without dropping listener
	cfg.Secret = "11112222333344445555666677778888"
	writeCfg(cfg)
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("hot profile update Apply failed: %v", err)
	}
	if !engine.IsPrimaryRunning() {
		t.Fatal("expected primary to remain running after hot profile update")
	}

	// Restart should cleanly rebind
	if err := engine.Restart(); err != nil {
		t.Fatalf("engine.Restart failed: %v", err)
	}
	if err := engine.CheckPrimaryReady(ctx); err != nil {
		t.Fatalf("CheckPrimaryReady after restart failed: %v", err)
	}

	// Disable -> should stop everything
	cfg.Enabled = false
	if err := engine.Apply(cfg); err != nil {
		t.Fatalf("disable Apply failed: %v", err)
	}
	if engine.IsPrimaryRunning() {
		t.Fatal("expected primary to be stopped when disabled")
	}
}
