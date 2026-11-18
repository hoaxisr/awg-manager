package serveringress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

func TestConfigWatcher_HotReloadOnDiskChange(t *testing.T) {
	tempDir := t.TempDir()

	xrayDir := filepath.Join(tempDir, "xray")
	tproxyDir := filepath.Join(tempDir, "tproxy")
	dispDir := filepath.Join(tempDir, "dispatcher")

	_ = os.MkdirAll(xrayDir, 0700)
	_ = os.MkdirAll(tproxyDir, 0700)
	_ = os.MkdirAll(dispDir, 0700)

	xrayFile := filepath.Join(xrayDir, "xray-server-settings.json")
	tgFile := filepath.Join(tproxyDir, "tgwebproxy-settings.json")
	dispFile := filepath.Join(dispDir, "config.json")

	initialXray := vlessserver.Config{
		Enabled:        true,
		Path:           "/cdn-bridge/",
		DomainStrategy: "ipv4_only",
	}
	initialTg := tgwebproxy.Config{
		Enabled:        false,
		PublicHostname: "host1.example.com",
	}
	initialDisp := cdndispatcher.Config{
		Enabled:    true,
		ListenAddr: ":9009",
	}

	xrayBytes, _ := json.Marshal(initialXray)
	tgBytes, _ := json.Marshal(initialTg)
	dispBytes, _ := json.Marshal(initialDisp)

	_ = os.WriteFile(xrayFile, xrayBytes, 0600)
	_ = os.WriteFile(tgFile, tgBytes, 0600)
	_ = os.WriteFile(dispFile, dispBytes, 0600)

	xrayComp := vlessserver.New(tempDir, nil)
	xrayComp.SetTestRuntimeHook(func(cfg xhttpserver.Config) error { return nil })
	dispComp := cdndispatcher.NewWithDataDir(tempDir, initialDisp)
	tgComp := tgwebproxy.New(tempDir, nil)

	coord := New(tempDir, xrayComp, dispComp, tgComp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := coord.StartConfigWatcher(ctx)
	defer watcher.Stop()

	// 1. Modify xray config on disk
	time.Sleep(100 * time.Millisecond)
	updatedXray := initialXray
	updatedXray.DomainStrategy = "prefer_ipv4"
	updatedXrayBytes, _ := json.Marshal(updatedXray)
	_ = os.WriteFile(xrayFile, updatedXrayBytes, 0600)

	// Wait for watcher to pick it up
	var sawStrategy bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cfg := xrayComp.GetConfig()
		if cfg.DomainStrategy == "prefer_ipv4" {
			sawStrategy = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !sawStrategy {
		t.Fatalf("expected watcher to reload DomainStrategy to prefer_ipv4")
	}

	// 2. Test ReloadFromDisk
	updatedXray.DomainStrategy = "ipv4_only"
	updatedXrayBytes2, _ := json.Marshal(updatedXray)
	_ = os.WriteFile(xrayFile, updatedXrayBytes2, 0600)

	if err := coord.ReloadFromDisk(context.Background()); err != nil {
		t.Fatalf("ReloadFromDisk failed: %v", err)
	}

	if xrayComp.GetConfig().DomainStrategy != "ipv4_only" {
		t.Fatalf("expected ReloadFromDisk to update DomainStrategy to ipv4_only")
	}
}

func TestConfigWatcher_SkipsInFlightTransactionAndRollback(t *testing.T) {
	tempDir := t.TempDir()

	xrayDir := filepath.Join(tempDir, "xray")
	tproxyDir := filepath.Join(tempDir, "tproxy")
	dispDir := filepath.Join(tempDir, "dispatcher")
	_ = os.MkdirAll(xrayDir, 0700)
	_ = os.MkdirAll(tproxyDir, 0700)
	_ = os.MkdirAll(dispDir, 0700)

	initialTg := tgwebproxy.Config{
		Enabled:    false,
		Scenario:   tgwebproxy.ScenarioDual,
		ListenPort: 8085,
		Secret:     "00112233445566778899aabbccddeeff",
	}
	tgBytes, _ := json.Marshal(initialTg)
	_ = os.WriteFile(filepath.Join(tproxyDir, "tgwebproxy-settings.json"), tgBytes, 0600)

	xrayComp := vlessserver.New(tempDir, nil)
	dispComp := cdndispatcher.NewWithDataDir(tempDir, cdndispatcher.Config{ListenAddr: ":9009"})
	tgComp := tgwebproxy.New(tempDir, nil)
	t.Cleanup(func() { _ = dispComp.Stop(); tgComp.Close() })

	coord := New(tempDir, xrayComp, dispComp, tgComp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher := coord.NewConfigWatcher()
	watcher.pollInterval = 20 * time.Millisecond
	watcher.Start(ctx)
	defer watcher.Stop()

	// Run a transaction whose readiness probe stalls long enough for multiple watcher ticks, then fails & rolls back
	err := coord.ExecuteIngressTransaction(ctx, IngressTransactionParams{
		TxID:       "tx-watcher-race-1",
		ServerKind: "tgwebproxy",
		Telegram: &IngressTelegramCandidate{
			Enabled:        true,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     0,
			PublicHostname: "race.example.test",
			Secret:         "abcdef0123456789abcdef0123456789",
		},
		ReadinessProbe: func(probeCtx context.Context, topo IngressTopology) error {
			time.Sleep(250 * time.Millisecond)
			return os.ErrDeadlineExceeded
		},
	})
	if err == nil {
		t.Fatalf("expected ExecuteIngressTransaction to fail on probe error")
	}

	// Let watcher poll again after rollback
	time.Sleep(300 * time.Millisecond)
	if tgComp.GetConfig().Enabled {
		t.Fatalf("ConfigWatcher replayed rolled-back candidate config: Enabled=true, want false")
	}
}
