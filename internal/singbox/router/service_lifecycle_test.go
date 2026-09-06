package router

import (
	"context"
	"path/filepath"
	"testing"
)

// Issue #689: tproxy-in на 0.0.0.0 ловит любой UDP на TPROXYPort (включая
// пакеты на WAN-IP роутера) и релеит их сам себе — самоподдерживающаяся
// петля флоу. TPROXY-правила задают --on-ip 127.0.0.1, листенер обязан быть
// там же. redirect-in остаётся на 0.0.0.0 — REDIRECT переписывает dst на
// primary IP интерфейса (96a61c77).
func TestEnsureTProxyInbound_ListenSplit(t *testing.T) {
	t.Run("creates canonical listens", func(t *testing.T) {
		out := ensureTProxyInbound(nil, "", 0)
		for _, in := range out {
			switch in.Tag {
			case "tproxy-in":
				if in.Listen != "127.0.0.1" {
					t.Errorf("tproxy-in listen = %q, want 127.0.0.1", in.Listen)
				}
			case "redirect-in":
				if in.Listen != "0.0.0.0" {
					t.Errorf("redirect-in listen = %q, want 0.0.0.0", in.Listen)
				}
			}
		}
	})

	// Upgrade path (issue #689): рестарт демона после обновления НЕ трогает
	// sing-box и не переустанавливает iptables → Reconcile идёт через
	// healTProxyInbound, а не через Enable. Его steady-state guard проверял
	// только UDP-timeout'ы — конфиг с верным таймаутом, но listen 0.0.0.0
	// считался здоровым и дрейф не лечился до ручного передёргивания движка.
	t.Run("heal fixes listen drift with healthy timeouts", func(t *testing.T) {
		svc, dir := newOrchedTestService(t)

		cfg := NewEmptyConfig()
		cfg.Inbounds = ensureTProxyInbound(nil, "", 0)
		for i := range cfg.Inbounds {
			if cfg.Inbounds[i].Tag == "tproxy-in" {
				cfg.Inbounds[i].Listen = "0.0.0.0" // как писали версии до фикса
			}
		}
		cfg.EnsureUDPTimeoutRule(DefaultUDPTimeout) // ruleOK в guard'е — true
		if err := SaveConfig(filepath.Join(dir, "20-router.json"), cfg); err != nil {
			t.Fatalf("seed active: %v", err)
		}
		if err := svc.deps.Orch.Bootstrap(); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}

		if err := svc.healTProxyInbound(context.Background(), "", 0); err != nil {
			t.Fatalf("healTProxyInbound: %v", err)
		}

		healed, err := svc.loadAppliedRouterConfig()
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		for _, in := range healed.Inbounds {
			if in.Tag == "tproxy-in" && in.Listen != "127.0.0.1" {
				t.Errorf("listen drift not healed: %q", in.Listen)
			}
		}
	})

	t.Run("heals drifted listens", func(t *testing.T) {
		out := ensureTProxyInbound([]Inbound{
			{Type: "tproxy", Tag: "tproxy-in", Listen: "0.0.0.0", ListenPort: TPROXYPort, Network: "udp"},
			{Type: "redirect", Tag: "redirect-in", Listen: "127.0.0.1", ListenPort: RedirectPort},
		}, "", 0)
		for _, in := range out {
			switch in.Tag {
			case "tproxy-in":
				if in.Listen != "127.0.0.1" {
					t.Errorf("tproxy-in not healed: listen = %q", in.Listen)
				}
			case "redirect-in":
				if in.Listen != "0.0.0.0" {
					t.Errorf("redirect-in not healed: listen = %q", in.Listen)
				}
			}
		}
	})
}
