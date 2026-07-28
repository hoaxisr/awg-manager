package router

import (
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestEnsureDNSChainOverlay(t *testing.T) {
	base := func() *RouterConfig {
		cfg := &RouterConfig{}
		cfg.DNS.Servers = []DNSServer{
			{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8"},
			{Tag: "dns-tunnel", Type: "udp", Server: "9.9.9.9", Detour: "tun0"},
		}
		cfg.DNS.Rules = []DNSRule{{Domain: []string{"x.com"}, Server: "dns-tunnel"}}
		return cfg
	}
	resilient := &storage.DNSChainPresetState{Mode: "resilient", DirectServer: "dns-direct", ProxyServer: "dns-tunnel"}

	t.Run("resilient добавляет 4 правила в конец, всё валидно", func(t *testing.T) {
		cfg := base()
		if err := ensureDNSChainOverlay(cfg, resilient); err != nil {
			t.Fatal(err)
		}
		if len(cfg.DNS.Rules) != 5 || cfg.DNS.Rules[0].Domain == nil {
			t.Fatalf("пользовательское правило должно остаться первым: %+v", cfg.DNS.Rules)
		}
		if err := validateDNSChain(cfg.DNS.Rules); err != nil {
			t.Fatalf("цепочка оверлея обязана проходить validateDNSChain: %v", err)
		}
		for _, r := range cfg.DNS.Rules[1:] {
			if err := validateDNSRule(r, cfg.dnsServerTypes()); err != nil {
				t.Fatalf("каждое managed-правило валидно per-rule: %v (%+v)", err, r)
			}
		}
	})
	t.Run("идемпотентность", func(t *testing.T) {
		cfg := base()
		_ = ensureDNSChainOverlay(cfg, resilient)
		snap, _ := json.Marshal(cfg.DNS.Rules)
		_ = ensureDNSChainOverlay(cfg, resilient)
		snap2, _ := json.Marshal(cfg.DNS.Rules)
		if string(snap) != string(snap2) {
			t.Fatal("повторный ensure не должен менять конфиг")
		}
	})
	t.Run("правило, оказавшееся ПОСЛЕ цепочки, ensure переносит цепочку в конец", func(t *testing.T) {
		cfg := base()
		_ = ensureDNSChainOverlay(cfg, resilient)
		cfg.DNS.Rules = append(cfg.DNS.Rules, DNSRule{Domain: []string{"y.com"}, Server: "dns-direct"}) // симуляция AddDNSRule при активном пресете
		if err := ensureDNSChainOverlay(cfg, resilient); err != nil {
			t.Fatal(err)
		}
		last := cfg.DNS.Rules[len(cfg.DNS.Rules)-1]
		if !isManagedDNSChainRule(last) {
			t.Fatalf("цепочка должна вернуться в конец, last=%+v", last)
		}
		if isManagedDNSChainRule(cfg.DNS.Rules[1]) {
			t.Fatalf("y.com должен стоять до цепочки: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("переключение resilient→antipoison заменяет цепочку", func(t *testing.T) {
		cfg := base()
		_ = ensureDNSChainOverlay(cfg, resilient)
		ap := &storage.DNSChainPresetState{Mode: "antipoison", DirectServer: "dns-direct", ProxyServer: "dns-tunnel"}
		if err := ensureDNSChainOverlay(cfg, ap); err != nil {
			t.Fatal(err)
		}
		if len(cfg.DNS.Rules) != 4 {
			t.Fatalf("ожидалось 4 правила (1 user + 3 antipoison), got %d", len(cfg.DNS.Rules))
		}
		if err := validateDNSChain(cfg.DNS.Rules); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Mode пустой / nil state удаляет цепочку", func(t *testing.T) {
		for _, st := range []*storage.DNSChainPresetState{{Mode: ""}, nil} {
			cfg := base()
			_ = ensureDNSChainOverlay(cfg, resilient)
			if err := ensureDNSChainOverlay(cfg, st); err != nil {
				t.Fatal(err)
			}
			if len(cfg.DNS.Rules) != 1 {
				t.Fatalf("managed-правила должны исчезнуть: %+v", cfg.DNS.Rules)
			}
		}
	})
	t.Run("несуществующий сервер — ошибка", func(t *testing.T) {
		cfg := base()
		if err := ensureDNSChainOverlay(cfg, &storage.DNSChainPresetState{Mode: "resilient", DirectServer: "nope", ProxyServer: "dns-tunnel"}); err == nil {
			t.Fatal("ожидалась ошибка")
		}
	})
	t.Run("antipoison: пустой PoisonCIDRs получает дефолтный сид", func(t *testing.T) {
		cfg := base()
		_ = ensureDNSChainOverlay(cfg, &storage.DNSChainPresetState{Mode: "antipoison", DirectServer: "dns-direct", ProxyServer: "dns-tunnel"})
		found := false
		for _, r := range cfg.DNS.Rules {
			if len(r.IPCIDR) > 0 {
				found = true
			}
		}
		if !found {
			t.Fatal("poison-правило с ip_cidr не найдено")
		}
	})
}
