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
		out := ensureTProxyInbound(nil, "", false)
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
		cfg.Inbounds = ensureTProxyInbound(nil, "", false)
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

		if err := svc.healTProxyInbound(context.Background(), ""); err != nil {
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
		}, "", false)
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

// В awgm-режиме TCP-перехват уходит на tproxy-порт (терминальный -j TPROXY в
// mangle), поэтому TCP обязан приниматься тем же inbound'ом. В sing-box
// отсутствующее поле network означает «tcp+udp», так что dual-network — это
// Network: "".
func TestAwgmModeEmitsSingleDualNetworkTproxyInbound(t *testing.T) {
	out := ensureTProxyInbound(nil, "5m", true)

	var tproxy, redirect int
	for _, in := range out {
		switch in.Type {
		case "tproxy":
			tproxy++
			if in.Network != "" {
				t.Fatalf("в awgm-режиме tproxy обслуживает оба протокола: network должен быть пуст, получили %q", in.Network)
			}
			if !in.TCPFastOpen {
				t.Error("inbound принимает TCP — tcp_fast_open должен быть включён")
			}
		case "redirect":
			redirect++
		}
	}
	if tproxy != 1 {
		t.Fatalf("ожидали один tproxy-inbound, получили %d", tproxy)
	}
	if redirect != 0 {
		t.Fatalf("redirect-inbound в awgm-режиме не нужен, получили %d", redirect)
	}
}

func TestAwgmModeNormalizesLegacyConfig(t *testing.T) {
	// Конфиг, переживший legacy-эпоху: tproxy-in с network=udp и живой
	// redirect-in. В awgm-режиме первый обязан стать dual-network, второй —
	// исчезнуть. Иначе TCP-перехват уедет в несуществующий inbound.
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", Network: "udp", ListenPort: TPROXYPort},
		{Type: "redirect", Tag: "redirect-in", ListenPort: RedirectPort},
	}
	out := ensureTProxyInbound(in, "5m", true)

	seenTProxy := false
	for _, e := range out {
		if e.Tag == "redirect-in" {
			t.Fatal("redirect-in обязан быть удалён в awgm-режиме")
		}
		if e.Tag == "tproxy-in" {
			seenTProxy = true
			if e.Network != "" {
				t.Fatalf("tproxy-in обязан стать dual-network, получили %q", e.Network)
			}
			if !e.TCPFastOpen {
				t.Error("tproxy-in принимает TCP — tcp_fast_open должен быть включён")
			}
		}
	}
	if !seenTProxy {
		t.Fatal("tproxy-in пропал из конфига")
	}
}

func TestLegacyModeKeepsSplit(t *testing.T) {
	out := ensureTProxyInbound(nil, "5m", false)

	var tproxy, redirect int
	for _, in := range out {
		switch in.Type {
		case "tproxy":
			tproxy++
			if in.Network != "udp" {
				t.Fatalf("legacy-режим: tproxy только для UDP, получили %q", in.Network)
			}
			if in.TCPFastOpen {
				t.Error("legacy-режим: tcp_fast_open бессмыслен на UDP-only inbound")
			}
		case "redirect":
			redirect++
		}
	}
	if tproxy != 1 || redirect != 1 {
		t.Fatalf("legacy обязан сохранить сплит, получили tproxy=%d redirect=%d", tproxy, redirect)
	}
}

// Обратный переход: конфиг, побывавший в awgm-режиме (dual-network tproxy-in,
// redirect-in отсутствует), при возврате в legacy обязан снова разъехаться на
// пару — иначе TCP пойдёт через REDIRECT в inbound, которого нет.
func TestLegacyModeRestoresSplitFromAwgmConfig(t *testing.T) {
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", ListenPort: TPROXYPort, TCPFastOpen: true},
	}
	out := ensureTProxyInbound(in, "5m", false)

	var tproxy, redirect int
	for _, e := range out {
		switch e.Tag {
		case "tproxy-in":
			tproxy++
			if e.Network != "udp" {
				t.Errorf("tproxy-in обязан вернуться к UDP-only, получили %q", e.Network)
			}
			if e.TCPFastOpen {
				t.Error("tcp_fast_open обязан быть снят с UDP-only inbound")
			}
		case "redirect-in":
			redirect++
		}
	}
	if tproxy != 1 || redirect != 1 {
		t.Fatalf("ожидали восстановленный сплит, получили tproxy=%d redirect=%d", tproxy, redirect)
	}
}
