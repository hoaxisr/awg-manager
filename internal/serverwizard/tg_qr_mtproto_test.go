package serverwizard

import (
	"context"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/testutil"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

func TestBuildDesiredConfig_CdnHttpClearsDirectHost(t *testing.T) {
	desired, err := NormalizeDesiredConfig(WizardPlanRequest{
		Kind:          "tgwebproxy",
		Scenario:      "cdn_http",
		ConnectionWay: "direct",
		OriginType:    "cloud_address",
		PublicDomain:  "connect.example.keenetic.pro",
		OriginHost:    "connect.example.keenetic.pro",
		DirectHost:    "connect.example.keenetic.pro",
		TlsDomain:     "www.google.com",
	})
	if err != nil {
		t.Fatalf("NormalizeDesiredConfig cdn_http: %v", err)
	}
	if desired.DirectHost != "" {
		t.Fatalf("desired.DirectHost = %q, want empty for cdn_http", desired.DirectHost)
	}
	if desired.TlsDomain != "" {
		t.Fatalf("desired.TlsDomain = %q, want empty for cdn_http", desired.TlsDomain)
	}

	bothDesired, err := NormalizeDesiredConfig(WizardPlanRequest{
		Kind:             "both",
		Scenario:         "cdn_http",
		ConnectionWay:    "external_https",
		OriginType:       "public_address",
		PublicDomain:     "cdn.example.com",
		OriginHost:       "203.0.113.10",
		DirectHost:       "203.0.113.10",
		UpstreamDevice:   "direct",
		TgUpstreamDevice: "direct",
	})
	if err != nil {
		t.Fatalf("NormalizeDesiredConfig both+cdn_http: %v", err)
	}
	if bothDesired.DirectHost != "" {
		t.Fatalf("bothDesired.DirectHost = %q, want empty for cdn_http", bothDesired.DirectHost)
	}

	// Preflight on kind="tgwebproxy" + scenario="cdn_http" ignores stale/invalid DirectHost
	pe := NewPreflightEngine(nil, nil, nil, nil, nil)
	var preChecks []PreflightCheck
	pe.checkTelegram(context.Background(), WizardPlanRequest{
		Kind:         "tgwebproxy",
		Scenario:     "cdn_http",
		PublicDomain: "connect.example.keenetic.pro",
		DirectHost:   "not a valid host!!!",
	}, &preChecks)
	for _, ch := range preChecks {
		if ch.ID == "direct_host_format" {
			t.Fatalf("cdn_http preflight unexpectedly checked DirectHost: %+v", ch)
		}
	}
}

func TestVerifyTelegram_EmptyTlsDomainUsesDefaultYaRu(t *testing.T) {
	port := testutil.FreePort(t)

	tgSvc := tgwebproxy.New(t.TempDir(), nil)
	defer tgSvc.Close()
	if err := tgSvc.UpdateConfig(tgwebproxy.Config{
		Scenario:   tgwebproxy.ScenarioDirectFakeTLS,
		DirectHost: "203.0.113.10",
		DirectPort: port,
		TlsDomain:  "",
	}); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	wiz := &WizardService{
		tg: tgSvc,
		lookupHost: func(_ context.Context, _ string) ([]string, error) {
			return []string{"203.0.113.10"}, nil
		},
	}

	if err := tgSvc.Start(); err != nil {
		t.Fatalf("tgSvc.Start: %v", err)
	}
	defer func() { _ = tgSvc.Stop() }()

	var checks []VerificationCheck
	wiz.verifyTelegram(context.Background(), &checks)

	foundListenerOK := false
	for _, ch := range checks {
		if ch.ID == "tg_mtproto_listener" {
			if ch.Status != CheckStatusOK {
				t.Fatalf("tg_mtproto_listener status = %s (%s), want ok", ch.Status, ch.Message)
			}
			foundListenerOK = true
		}
	}
	if !foundListenerOK {
		t.Fatalf("tg_mtproto_listener check not found in %+v", checks)
	}
}

func TestCoordinator_UpdatesMaterializedUsersOnSecretRotationAndReenable(t *testing.T) {
	baseDir := t.TempDir()
	tgSvc := tgwebproxy.New(baseDir+"/tg", nil)
	defer tgSvc.Close()

	// Rename default user -> materializes Users slice on disk
	if err := tgSvc.UpdateUser("default", "Мой iPhone", nil); err != nil {
		t.Fatalf("rename default: %v", err)
	}
	origSecret := tgSvc.GetInternalConfig().Secret

	// Disable the materialized user
	off := false
	if err := tgSvc.UpdateUser("default", "", &off); err != nil {
		t.Fatalf("disable default: %v", err)
	}
	if len(tgSvc.GetInternalConfig().ActiveSecrets()) != 0 {
		t.Fatalf("expected 0 active secrets when disabled")
	}

	coord := serveringress.New(baseDir+"/coord", nil, nil, tgSvc)

	// 1. Re-enabling with preserved secret re-enables the matching materialized user and syncs multi-host endpoints
	listenPort := testutil.FreePort(t)

	if err := coord.ExecuteIngressTransaction(context.Background(), serveringress.IngressTransactionParams{
		TxID:       "tx-multi-host-sync",
		ServerKind: "tgwebproxy",
		Telegram: &serveringress.IngressTelegramCandidate{
			Enabled:        false,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     listenPort,
			PublicHostname: "tg1.example.com, tg2.example.com",
			Secret:         origSecret,
			SetUpstream:    true,
		},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction multi-host: %v", err)
	}
	syncedEps := tgSvc.GetInternalConfig().GetEndpoints()
	if len(syncedEps) != 2 || syncedEps[0].Hostname != "tg1.example.com" || syncedEps[1].Hostname != "tg2.example.com" {
		t.Fatalf("expected 2 split endpoints, got: %+v", syncedEps)
	}

	// Shifting multi-host list ("tg1, tg2" -> "tg2, tg3") keeps ep-2 for tg2 and assigns ep-3 to tg3 without ID collision
	if err := coord.ExecuteIngressTransaction(context.Background(), serveringress.IngressTransactionParams{
		TxID:       "tx-shift-multi-host",
		ServerKind: "tgwebproxy",
		Telegram: &serveringress.IngressTelegramCandidate{
			Enabled:        false,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     listenPort,
			PublicHostname: "tg2.example.com, tg3.example.com",
			Secret:         origSecret,
			SetUpstream:    true,
		},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction shift-multi-host: %v", err)
	}
	shiftedEps := tgSvc.GetInternalConfig().GetEndpoints()
	if len(shiftedEps) != 2 ||
		shiftedEps[0].Hostname != "tg2.example.com" || shiftedEps[0].ID != "ep-2" ||
		shiftedEps[1].Hostname != "tg3.example.com" || shiftedEps[1].ID != "ep-3" {
		t.Fatalf("expected shifted endpoints [tg2/ep-2, tg3/ep-3], got: %+v", shiftedEps)
	}

	// Set distinct per-endpoint presets via UpdateConfig (which mirrors only Endpoints[0].Hostname into PublicHostname),
	// then run DisableOtherService-style transaction (PublicHostname == "") -> must preserve both endpoints and presets!
	customEps := tgSvc.GetInternalConfig().GetEndpoints()
	customEps[0].Preset = tgwebproxy.PresetCDNStatic
	customEps[1].Preset = tgwebproxy.PresetKeenDNS
	if err := tgSvc.UpdateConfig(tgwebproxy.Config{Endpoints: customEps}); err != nil {
		t.Fatalf("set custom endpoint presets: %v", err)
	}
	if err := coord.ExecuteIngressTransaction(context.Background(), serveringress.IngressTransactionParams{
		TxID:       "tx-disable-preserve-endpoints",
		ServerKind: "tgwebproxy",
		Telegram: &serveringress.IngressTelegramCandidate{
			Enabled: false,
		},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction disable-preserve-endpoints: %v", err)
	}
	preservedEps := tgSvc.GetInternalConfig().GetEndpoints()
	if len(preservedEps) != 2 ||
		preservedEps[0].Hostname != "tg2.example.com" || preservedEps[0].Preset != tgwebproxy.PresetCDNStatic ||
		preservedEps[1].Hostname != "tg3.example.com" || preservedEps[1].Preset != tgwebproxy.PresetKeenDNS {
		t.Fatalf("DisableOtherService wiped secondary endpoints or custom presets: %+v", preservedEps)
	}

	// Disable ep-2 via UpdateConfig, then reduce to single host "tg2.example.com" -> must force Enabled=true (no 503 trap)
	disabledCopy := tgSvc.GetInternalConfig().GetEndpoints()
	disabledCopy[0].Enabled = false
	disabledCopy[1].Enabled = false
	if err := tgSvc.UpdateConfig(tgwebproxy.Config{Endpoints: disabledCopy}); err != nil {
		t.Fatalf("disable endpoints: %v", err)
	}

	// Reducing from 2 hosts to 1 host ("tg2.example.com") removes the stale endpoint without duplicating tg2
	if err := coord.ExecuteIngressTransaction(context.Background(), serveringress.IngressTransactionParams{
		TxID:       "tx-reduce-single-host",
		ServerKind: "tgwebproxy",
		Telegram: &serveringress.IngressTelegramCandidate{
			Enabled:        false,
			Scenario:       tgwebproxy.ScenarioCDNHTTP,
			ListenPort:     listenPort,
			PublicHostname: "tg2.example.com",
			Secret:         origSecret,
			SetUpstream:    true,
		},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction reduce-single-host: %v", err)
	}
	reducedEps := tgSvc.GetInternalConfig().GetEndpoints()
	if len(reducedEps) != 1 || reducedEps[0].Hostname != "tg2.example.com" || reducedEps[0].ListenPort != 8084 || !reducedEps[0].Enabled {
		t.Fatalf("expected 1 enabled normalized endpoint for tg2.example.com, got: %+v", reducedEps)
	}

	// Now run with Enabled: true (using non-conflicting direct_fake_tls port so no dispatcher needed)
	directPort := testutil.FreePort(t)

	if err := coord.ExecuteIngressTransaction(context.Background(), serveringress.IngressTransactionParams{
		TxID:       "tx-reenable-active",
		ServerKind: "tgwebproxy",
		Telegram: &serveringress.IngressTelegramCandidate{
			Enabled:     true,
			Scenario:    tgwebproxy.ScenarioDirectFakeTLS,
			DirectHost:  "203.0.113.10",
			DirectPort:  directPort,
			TlsDomain:   "ya.ru",
			ListenPort:  listenPort,
			Secret:      origSecret,
			SetUpstream: true,
		},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction enable: %v", err)
	}
	defer func() { _ = tgSvc.Stop() }()

	if !slices.Contains(tgSvc.GetInternalConfig().ActiveSecrets(), origSecret) {
		t.Fatalf("origSecret %q was not re-enabled in ActiveSecrets: %+v", origSecret, tgSvc.GetInternalConfig().Users)
	}

	// 2. Rotating secret via Coordinator updates materialized default user secret too
	rotatedSec := "abcdef0123456789abcdef0123456789"
	if err := coord.ExecuteIngressTransaction(context.Background(), serveringress.IngressTransactionParams{
		TxID:       "tx-rotate-secret",
		ServerKind: "tgwebproxy",
		Telegram: &serveringress.IngressTelegramCandidate{
			Enabled:     true,
			Scenario:    tgwebproxy.ScenarioDirectFakeTLS,
			DirectHost:  "203.0.113.10",
			DirectPort:  directPort,
			TlsDomain:   "ya.ru",
			ListenPort:  listenPort,
			Secret:      rotatedSec,
			SetUpstream: true,
		},
	}); err != nil {
		t.Fatalf("ExecuteIngressTransaction rotate: %v", err)
	}
	if !slices.Contains(tgSvc.GetInternalConfig().ActiveSecrets(), rotatedSec) {
		t.Fatalf("rotatedSec %q missing from ActiveSecrets: %+v", rotatedSec, tgSvc.GetInternalConfig().Users)
	}
	if tgSvc.GetInternalConfig().Users[0].Remark != "Мой iPhone" {
		t.Fatalf("expected renamed remark preserved, got %q", tgSvc.GetInternalConfig().Users[0].Remark)
	}
}
