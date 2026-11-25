package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
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

type tgFirewallSpy struct {
	*tgwebproxy.Service
	syncs int
}

func (s *tgFirewallSpy) SyncDirectWANFirewall() error { s.syncs++; return nil }

// Загрузка приводит файрвол к конфигурации и при выключенном прокси; под
// восстановлением — не трогает.
// Мутации: убрать SyncWANIngressFirewall из startIngressServices; звать его и
// после ошибки автозапуска → красный.
func TestStartIngressServices_SyncsWANFirewall(t *testing.T) {
	dir := t.TempDir()
	tg := tgwebproxy.New(dir, nil)
	t.Cleanup(func() { _ = tg.Close() })
	spy := &tgFirewallSpy{Service: tg}
	a := &app{
		vlessServerService: vlessserver.New(dir, nil),
		tgWebProxyService:  tg,
	}
	a.ingressCoordinator = serveringress.New(dir, a.vlessServerService, nil, spy)

	a.startIngressServices(true, "проверка", false, "")
	if spy.syncs != 0 {
		t.Fatalf("под восстановлением файрвол тронут: %d", spy.syncs)
	}
	a.startIngressServices(false, "", false, "")
	if spy.syncs != 1 {
		t.Fatalf("загрузка: синхронизаций %d, ждали 1", spy.syncs)
	}
}

// Мутация: убрать вызов startIngressServices из setupServer → красный.
func TestSetupServer_StartsIngressServices(t *testing.T) {
	src, err := os.ReadFile("wiring_server.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (a *app) setupServer()")
	end := strings.Index(body[start:], "\n}\n")
	if start < 0 || end < 0 || !strings.Contains(body[start:start+end], "a.startIngressServices(") {
		t.Fatal("setupServer не поднимает серверы")
	}
}
