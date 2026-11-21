package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

func TestWiringServer_ShutdownHookPreservesEnabledState(t *testing.T) {
	tmpDir := t.TempDir()
	xrayDir := filepath.Join(tmpDir, "xray")
	tgDir := filepath.Join(tmpDir, "tproxy")
	_ = os.MkdirAll(xrayDir, 0755)
	_ = os.MkdirAll(tgDir, 0755)

	// Write VLESS server config with Enabled: true
	vlessCfg := vlessserver.Config{
		Enabled:    true,
		ListenPort: 9008,
	}
	xrayData, _ := json.MarshalIndent(vlessCfg, "", "  ")
	if err := os.WriteFile(filepath.Join(xrayDir, "xray-server-settings.json"), xrayData, 0600); err != nil {
		t.Fatalf("write vless settings: %v", err)
	}

	// Write TG config with Enabled: true
	tgCfg := tgwebproxy.Config{
		Enabled:    true,
		ListenPort: 8085,
	}
	tgData, _ := json.MarshalIndent(tgCfg, "", "  ")
	if err := os.WriteFile(filepath.Join(tgDir, "tgwebproxy-settings.json"), tgData, 0600); err != nil {
		t.Fatalf("write tg settings: %v", err)
	}

	vlessSvc := vlessserver.New(tmpDir, nil)
	tgSvc := tgwebproxy.New(tmpDir, nil)

	if !vlessSvc.GetConfig().Enabled {
		t.Fatalf("initial vless config must be enabled")
	}
	if !tgSvc.GetConfig().Enabled {
		t.Fatalf("initial tgwebproxy config must be enabled")
	}

	a := &app{
		vlessServerService: vlessSvc,
		tgWebProxyService:  tgSvc,
	}

	// Call production shutdown helper exactly as wired in wiring_server.go
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdownIngressRuntime(ctx, a.vlessServerService, a.tgWebProxyService, a.cdnDispatcher); err != nil {
		t.Fatalf("shutdownIngressRuntime failed: %v", err)
	}

	// 1. In-memory config must remain enabled (NOT set to false like Stop() would)
	if !vlessSvc.GetConfig().Enabled {
		t.Errorf("expected in-memory vless Enabled to remain true after shutdown hook")
	}
	if !tgSvc.GetConfig().Enabled {
		t.Errorf("expected in-memory tgwebproxy Enabled to remain true after shutdown hook")
	}

	// 2. Persistent disk config must remain enabled when reloaded from scratch
	reloadedVless := vlessserver.New(tmpDir, nil)
	if !reloadedVless.GetConfig().Enabled {
		t.Errorf("expected persistent on-disk vless Enabled to remain true across shutdown/reload")
	}

	reloadedTG := tgwebproxy.New(tmpDir, nil)
	defer reloadedTG.Close()
	if !reloadedTG.GetConfig().Enabled {
		t.Errorf("expected persistent on-disk tgwebproxy Enabled to remain true across shutdown/reload")
	}
}

func TestWiring_AssertProductionWiring_RequiresFircEngineCoordinator(t *testing.T) {
	aNil := &app{}
	if err := assertProductionWiring(aNil); err != nil {
		t.Fatalf("expected nil error for nil fircOp, got: %v", err)
	}

	// Firc Operator without production engine-switch hooks must fail
	fop := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
	aFircUnwired := &app{
		fircOp:      fop,
		fircHandler: api.NewFircHandler(fop),
	}
	if err := assertProductionWiring(aFircUnwired); err == nil {
		t.Fatal("expected error when fircOp has no production engine-switch hooks")
	}

	// Wire production engine coordinator -> assertProductionWiring must pass
	aFircUnwired.settingsStore = storage.NewSettingsStore(t.TempDir())
	aFircUnwired.wireFircEngineCoordinator()
	if err := assertProductionWiring(aFircUnwired); err != nil {
		t.Fatalf("expected nil error after wireFircEngineCoordinator, got: %v", err)
	}
	if !aFircUnwired.fircHandler.HasInstaller() || aFircUnwired.fircInstaller == nil {
		t.Fatal("expected fircHandler to have wired OnDemandInstaller after wireFircEngineCoordinator")
	}

	// Unset installer explicitly -> assertProductionWiring must fail
	aFircUnwired.fircHandler.SetInstaller(nil)
	if err := assertProductionWiring(aFircUnwired); err == nil {
		t.Fatal("expected error when fircHandler has nil installer")
	}
}

func TestWiring_ExtractOpenfluxConflictIface(t *testing.T) {
	tests := []struct {
		errStr   string
		expected string
	}{
		{
			errStr:   `router reported error: "OpkgTun14": network 10.10.10.0/24 conflicts with interface "OpkgTun11"`,
			expected: "OpkgTun11",
		},
		{
			errStr:   `interface up OpkgTun12: router reported error: "OpkgTun12": network 10.10.10.0/24 conflicts with interface 'opkgtun15'`,
			expected: "OpkgTun15",
		},
		{
			errStr:   `conflicts with interface OpkgTun7`,
			expected: "OpkgTun7",
		},
		{
			errStr:   `router reported error: cannot allocate IP`,
			expected: "",
		},
		{
			errStr:   "",
			expected: "",
		},
	}

	for _, tt := range tests {
		got := extractOpenfluxConflictIface(tt.errStr)
		if got != tt.expected {
			t.Errorf("extractOpenfluxConflictIface(%q) = %q, want %q", tt.errStr, got, tt.expected)
		}
	}
}
