package serveringress

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/telemt"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

type resettableXray struct {
	*mockXray
	resetCalled bool
}

func (r *resettableXray) FactoryReset(ctx context.Context) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetCalled = true
	r.cfg = vlessserver.Config{
		Enabled:       false,
		ListenAddress: "127.0.0.1",
		ListenPort:    9008,
	}
	return nil
}

type resettableTg struct {
	*mockTgWebProxy
	resetCalled bool
}

func (r *resettableTg) FactoryReset(ctx context.Context) error {
	_ = ctx
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetCalled = true
	r.cfg = tgwebproxy.Config{
		Enabled:        false,
		Scenario:       tgwebproxy.ScenarioDual,
		ListenPort:     tgwebproxy.DefaultListenPort,
		AdminPort:      tgwebproxy.DefaultAdminPort,
		DirectPort:     tgwebproxy.DefaultDirectPort,
		PublicHostname: "",
	}
	return nil
}

type fakeFircResetRouting struct {
	mu           sync.Mutex
	enabled      map[string]bool
	prepareCalls []bool
	commitCount  int
	finalizeCnt  int
}

func (f *fakeFircResetRouting) IsServerRoutingEnabled(serverID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled[serverID]
}

func (f *fakeFircResetRouting) PrepareServerRouting(ctx context.Context, txID string, serverID string, enabled bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepareCalls = append(f.prepareCalls, enabled)
	f.enabled[serverID] = enabled
	return txID, nil
}

func (f *fakeFircResetRouting) CommitServerRouting(ctx context.Context, txID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commitCount++
	return nil
}

func (f *fakeFircResetRouting) RollbackServerRouting(ctx context.Context, txID string) error {
	return nil
}

func (f *fakeFircResetRouting) FinalizeServerRouting(txID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalizeCnt++
	return nil
}

func TestCoordinator_ResetVless_KeepsActiveTelegramAndDispatcherRoutes(t *testing.T) {
	dataDir := t.TempDir()
	xray := &resettableXray{
		mockXray: &mockXray{
			cfg: vlessserver.Config{
				Enabled:      true,
				PublicDomain: "vless.example.keenetic.pro,shared.example.keenetic.pro",
				OriginHost:   "origin-v.example.keenetic.pro",
				OutboundMode: "router",
			},
		},
	}
	tg := &resettableTg{
		mockTgWebProxy: &mockTgWebProxy{
			cfg: tgwebproxy.Config{
				Enabled:        true,
				Scenario:       tgwebproxy.ScenarioDual,
				ListenPort:     8085,
				PublicHostname: "tg.example.keenetic.pro",
				Endpoints: []tgwebproxy.WebProxyEndpoint{
					{ID: "ep1", Hostname: "shared.example.keenetic.pro", Enabled: true},
				},
			},
		},
	}
	disp := &mockDispatcher{
		running: true,
		cfg: cdndispatcher.Config{
			Enabled:        true,
			ListenAddr:     ":9009",
			XrayTarget:     "http://127.0.0.1:9008",
			TgTarget:       "http://127.0.0.1:8085",
			PublicHostname: "vless.example.keenetic.pro",
			XrayPublicHost: "vless.example.keenetic.pro",
			TgPublicHost:   "tg.example.keenetic.pro",
			XrayPathPrefix: "/custom-path",
			DefaultAction:  "404",
			Routes: []cdndispatcher.CDNRouteRule{
				{ID: "r1", Domain: "vless.example.keenetic.pro", RouteXray: true, RouteTelegram: false, Enabled: true},
				{ID: "r2", Domain: "shared.example.keenetic.pro", RouteXray: true, RouteTelegram: true, Enabled: true},
				{ID: "r3", Domain: "tg.example.keenetic.pro", RouteXray: false, RouteTelegram: true, Enabled: true},
			},
		},
	}
	firc := &fakeFircResetRouting{enabled: map[string]bool{"vless_xhttp_server": true}}

	coord := New(dataDir, xray, disp, tg)
	coord.SetFircRouting(firc)

	res, err := coord.ResetVless(context.Background(), ResetOptions{DeleteKeenDNS: false})
	if err != nil {
		t.Fatalf("ResetVless failed: %v", err)
	}
	if !res.Reset || res.DispatcherReset {
		t.Fatalf("expected Reset=true and DispatcherReset=false while Telegram active, got %+v", res)
	}
	if !xray.resetCalled {
		t.Fatal("expected xray FactoryReset to be called")
	}
	if tg.resetCalled || !tg.GetInternalConfig().Enabled {
		t.Fatal("expected Telegram config and runtime to remain untouched")
	}
	if firc.IsServerRoutingEnabled("vless_xhttp_server") {
		t.Fatal("expected vless_xhttp_server FIRC routing flag to be disabled")
	}
	if !disp.IsRunning() {
		t.Fatal("expected Dispatcher to stay running while Telegram is active")
	}

	gotDisp := disp.GetConfig()
	if len(gotDisp.Routes) != 2 {
		t.Fatalf("expected 2 remaining Telegram routes, got %d (%+v)", len(gotDisp.Routes), gotDisp.Routes)
	}
	for _, r := range gotDisp.Routes {
		if r.RouteXray {
			t.Errorf("expected RouteXray cleared on remaining route %+v", r)
		}
		if !r.RouteTelegram {
			t.Errorf("expected RouteTelegram kept true on remaining route %+v", r)
		}
	}
	if gotDisp.XrayPublicHost != "" || gotDisp.PublicHostname != "tg.example.keenetic.pro" {
		t.Fatalf("unexpected dispatcher hosts after VLESS reset: %+v", gotDisp)
	}
}

func TestCoordinator_ResetTelegram_KeepsActiveVlessAndPreservesTelemtBinary(t *testing.T) {
	dataDir := t.TempDir()
	telemtSvc := telemt.New(dataDir, "x86_64")
	binPath := filepath.Join(dataDir, "telemt", "telemt")
	if err := os.WriteFile(binPath, []byte("keep-telemt-bin"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "telemt", "settings.json"), []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}

	xray := &resettableXray{
		mockXray: &mockXray{
			cfg: vlessserver.Config{
				Enabled:      true,
				PublicDomain: "vless.example.keenetic.pro",
				Path:         "/cdn-bridge",
			},
		},
	}
	tg := &resettableTg{
		mockTgWebProxy: &mockTgWebProxy{
			cfg: tgwebproxy.Config{
				Enabled:        true,
				Scenario:       tgwebproxy.ScenarioDual,
				PublicHostname: "tg.example.keenetic.pro",
			},
		},
	}
	disp := &mockDispatcher{
		running: true,
		cfg: cdndispatcher.Config{
			Enabled:        true,
			ListenAddr:     ":9009",
			XrayTarget:     "http://127.0.0.1:9008",
			TgTarget:       "http://127.0.0.1:8085",
			PublicHostname: "tg.example.keenetic.pro",
			XrayPublicHost: "vless.example.keenetic.pro",
			TgPublicHost:   "tg.example.keenetic.pro",
			XrayPathPrefix: "/cdn-bridge",
			DefaultAction:  "404",
			Routes: []cdndispatcher.CDNRouteRule{
				{ID: "r1", Domain: "vless.example.keenetic.pro", RouteXray: true, RouteTelegram: true, Enabled: true},
				{ID: "r2", Domain: "tg.example.keenetic.pro", RouteXray: false, RouteTelegram: true, Enabled: true},
			},
		},
	}

	coord := New(dataDir, xray, disp, tg)
	coord.SetTelemt(telemtSvc)

	res, err := coord.ResetTelegram(context.Background(), ResetOptions{DeleteKeenDNS: false})
	if err != nil {
		t.Fatalf("ResetTelegram failed: %v", err)
	}
	if !res.Reset || res.DispatcherReset {
		t.Fatalf("expected Reset=true and DispatcherReset=false while VLESS active, got %+v", res)
	}
	if !tg.resetCalled {
		t.Fatal("expected tg FactoryReset to be called")
	}
	if xray.resetCalled || !xray.GetConfig().Enabled {
		t.Fatal("expected VLESS config and runtime to remain untouched")
	}
	if data, err := os.ReadFile(binPath); err != nil || string(data) != "keep-telemt-bin" {
		t.Fatalf("expected telemt binary preserved, got err=%v data=%q", err, string(data))
	}
	if _, err := os.Stat(filepath.Join(dataDir, "telemt", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("expected standalone telemt settings.json removed")
	}
	if !disp.IsRunning() {
		t.Fatal("expected Dispatcher to stay running while VLESS is active")
	}

	gotDisp := disp.GetConfig()
	if len(gotDisp.Routes) != 1 || gotDisp.Routes[0].Domain != "vless.example.keenetic.pro" || !gotDisp.Routes[0].RouteXray || gotDisp.Routes[0].RouteTelegram {
		t.Fatalf("expected only VLESS route with RouteTelegram cleared, got %+v", gotDisp.Routes)
	}
	if gotDisp.TgPublicHost != "" || gotDisp.PublicHostname != "vless.example.keenetic.pro" {
		t.Fatalf("unexpected dispatcher hosts after Telegram reset: %+v", gotDisp)
	}
}

func TestCoordinator_ResetSecondServer_FullyResetsDispatcher(t *testing.T) {
	dataDir := t.TempDir()
	dispDir := filepath.Join(dataDir, "dispatcher")
	txDir := filepath.Join(dispDir, "tx", "tx-1")
	if err := os.MkdirAll(txDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dispDir, "config.json"), []byte(`{"enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(txDir, "manifest.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}

	xray := &resettableXray{
		mockXray: &mockXray{
			cfg: vlessserver.Config{Enabled: true, PublicDomain: "vless.example.keenetic.pro"},
		},
	}
	tg := &resettableTg{
		mockTgWebProxy: &mockTgWebProxy{
			cfg: tgwebproxy.Config{Enabled: false, Scenario: tgwebproxy.ScenarioDual},
		},
	}
	disp := &mockDispatcher{
		running: true,
		cfg: cdndispatcher.Config{
			Enabled:        true,
			ListenAddr:     ":9009",
			PublicHostname: "vless.example.keenetic.pro",
			Routes: []cdndispatcher.CDNRouteRule{
				{ID: "r1", Domain: "vless.example.keenetic.pro", RouteXray: true, Enabled: true},
			},
		},
	}

	coord := New(dataDir, xray, disp, tg)
	res, err := coord.ResetVless(context.Background(), ResetOptions{})
	if err != nil {
		t.Fatalf("ResetVless failed: %v", err)
	}
	if !res.DispatcherReset {
		t.Fatalf("expected DispatcherReset=true when other server is disabled, got %+v", res)
	}
	if disp.IsRunning() {
		t.Fatal("expected Dispatcher stopped after full reset")
	}
	gotDisp := disp.GetConfig()
	if gotDisp.Enabled || len(gotDisp.Routes) != 0 || gotDisp.PublicHostname != "" {
		t.Fatalf("expected factory default dispatcher config, got %+v", gotDisp)
	}
	if _, err := os.Stat(filepath.Join(dispDir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("expected dispatcher/config.json deleted")
	}
	if _, err := os.Stat(filepath.Join(dispDir, "tx")); !os.IsNotExist(err) {
		t.Fatal("expected dispatcher/tx deleted")
	}
}

func TestCoordinator_Reset_KeenDNSOptionalAndSharedProtection(t *testing.T) {
	mkEntry := func(label, host, port string, auth bool) ndmsquery.HTTPProxyEntry {
		raw, _ := json.Marshal(map[string]any{"upstream": map[string]any{"proto": "http", "upstream": host, "port": port}})
		return ndmsquery.HTTPProxyEntry{
			Name:          label,
			UpstreamProto: "http",
			UpstreamHost:  host,
			UpstreamPort:  port,
			DomainNDNS:    true,
			Public:        true,
			Auth:          auth,
			PreserveHost:  true,
			RawJSON:       string(raw),
		}
	}

	t.Run("DeleteKeenDNS_false_never_deletes", func(t *testing.T) {
		dataDir := t.TempDir()
		kdState := newFakeKeenDNSState()
		kdState.entries["vless"] = mkEntry("vless", "127.0.0.1", "9009", false)
		kdOp := NewNDMSKeenDNSOperator(kdState, kdState, kdState)

		xray := &resettableXray{mockXray: &mockXray{cfg: vlessserver.Config{Enabled: true, PublicDomain: "vless.example.keenetic.pro"}}}
		disp := &mockDispatcher{running: true, cfg: cdndispatcher.Config{ListenAddr: ":9009"}}
		coord := New(dataDir, xray, disp, nil)
		coord.SetKeenDNS(kdOp)

		res, err := coord.ResetVless(context.Background(), ResetOptions{DeleteKeenDNS: false})
		if err != nil {
			t.Fatalf("ResetVless failed: %v", err)
		}
		if kdState.deleteCount != 0 || len(res.KeenDNSDeleted) != 0 {
			t.Fatalf("expected 0 KeenDNS deletions when DeleteKeenDNS=false, got count=%d res=%+v", kdState.deleteCount, res)
		}
	})

	t.Run("DeleteKeenDNS_true_deletes_exclusive_and_protects_shared_and_foreign", func(t *testing.T) {
		dataDir := t.TempDir()
		kdState := newFakeKeenDNSState()
		kdState.entries["vless"] = mkEntry("vless", "127.0.0.1", "9009", false)         // exclusive ingress -> delete
		kdState.entries["shared"] = mkEntry("shared", "127.0.0.1", "9009", false)       // shared with Telegram -> skip
		kdState.entries["foreign"] = mkEntry("foreign", "127.0.0.1", "8080", true)      // foreign auth/port -> skip
		kdState.entries["routerlan"] = mkEntry("routerlan", "192.168.1.1", "80", false) // non-127.0.0.1 -> skip
		kdOp := NewNDMSKeenDNSOperator(kdState, kdState, kdState)

		xray := &resettableXray{
			mockXray: &mockXray{
				cfg: vlessserver.Config{
					Enabled:      true,
					PublicDomain: "vless.example.keenetic.pro,shared.example.keenetic.pro,foreign.example.keenetic.pro,routerlan.example.keenetic.pro",
				},
			},
		}
		tg := &resettableTg{
			mockTgWebProxy: &mockTgWebProxy{
				cfg: tgwebproxy.Config{
					Enabled:        true,
					Scenario:       tgwebproxy.ScenarioDual,
					PublicHostname: "shared.example.keenetic.pro",
				},
			},
		}
		disp := &mockDispatcher{running: true, cfg: cdndispatcher.Config{ListenAddr: ":9009"}}
		coord := New(dataDir, xray, disp, tg)
		coord.SetKeenDNS(kdOp)

		res, err := coord.ResetVless(context.Background(), ResetOptions{DeleteKeenDNS: true})
		if err != nil {
			t.Fatalf("ResetVless failed: %v", err)
		}
		if len(res.KeenDNSDeleted) != 1 || res.KeenDNSDeleted[0] != "vless" {
			t.Fatalf("expected only 'vless' deleted, got %+v", res.KeenDNSDeleted)
		}
		if len(res.KeenDNSSkipped) < 3 {
			t.Fatalf("expected shared, foreign, routerlan skipped, got %+v", res.KeenDNSSkipped)
		}
		if _, stillThere := kdState.entries["vless"]; stillThere {
			t.Fatal("expected 'vless' entry deleted from KeenDNS state")
		}
		for _, keep := range []string{"shared", "foreign", "routerlan"} {
			if _, ok := kdState.entries[keep]; !ok {
				t.Fatalf("expected %q publication to remain untouched", keep)
			}
		}
		if kdState.saveCount != 1 {
			t.Fatalf("expected SaveConfig called once after deletion, got %d", kdState.saveCount)
		}
	})
}
