package router

import (
	"context"
	"encoding/json"
	"strings"
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

// newDNSPresetTestService — сервис на legacy-пути записи (Orch=nil) с двумя
// DNS-серверами: этого достаточно, чтобы прогнать связку
// withConfig → ensure-хук → persist.
func newDNSPresetTestService(t *testing.T) (*ServiceImpl, *storage.SettingsStore) {
	t.Helper()
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	svc := newTestService(t, Deps{Settings: store, Singbox: newTestSingbox(t)})
	for _, srv := range []DNSServer{
		{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8"},
		{Tag: "dns-tunnel", Type: "udp", Server: "9.9.9.9"},
	} {
		if err := svc.AddDNSServer(context.Background(), srv); err != nil {
			t.Fatalf("AddDNSServer %s: %v", srv.Tag, err)
		}
	}
	return svc, store
}

func TestSetDNSChainPresetService(t *testing.T) {
	ctx := context.Background()
	svc, store := newDNSPresetTestService(t)

	if err := svc.SetDNSChainPreset(ctx, storage.DNSChainPresetState{
		Mode: "resilient", DirectServer: "dns-direct", ProxyServer: "dns-tunnel",
	}); err != nil {
		t.Fatalf("SetDNSChainPreset: %v", err)
	}

	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DNSChainPreset == nil || settings.DNSChainPreset.Mode != "resilient" {
		t.Fatalf("состояние пресета должно сохраниться: %+v", settings.DNSChainPreset)
	}
	got, err := svc.GetDNSChainPreset(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "resilient" || got.DirectServer != "dns-direct" || got.ProxyServer != "dns-tunnel" {
		t.Fatalf("GetDNSChainPreset = %+v", got)
	}
	rules, err := svc.ListDNSRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("цепочка должна попасть в конфиг: %+v", rules)
	}

	// Ensure-хук: пользовательское правило, добавленное поверх, не остаётся
	// за цепочкой — оверлей переносит managed-хвост в конец.
	if err := svc.AddDNSRule(ctx, DNSRule{Domain: []string{"x.com"}, Server: "dns-tunnel"}); err != nil {
		t.Fatalf("AddDNSRule: %v", err)
	}
	rules, err = svc.ListDNSRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 5 || isManagedDNSChainRule(rules[0]) || !isManagedDNSChainRule(rules[4]) {
		t.Fatalf("ensure-хук должен держать цепочку в конце: %+v", rules)
	}

	// Выключение пресета: state снимается, managed-правила исчезают.
	if err := svc.SetDNSChainPreset(ctx, storage.DNSChainPresetState{}); err != nil {
		t.Fatalf("SetDNSChainPreset(off): %v", err)
	}
	settings, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DNSChainPreset != nil {
		t.Fatalf("состояние должно сняться: %+v", settings.DNSChainPreset)
	}
	rules, err = svc.ListDNSRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("должно остаться только пользовательское правило: %+v", rules)
	}
}

func TestSetDNSChainPresetRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	svc, store := newDNSPresetTestService(t)

	if err := svc.SetDNSChainPreset(ctx, storage.DNSChainPresetState{
		Mode: "nope", DirectServer: "dns-direct", ProxyServer: "dns-tunnel",
	}); err == nil {
		t.Fatal("неизвестный режим должен отвергаться")
	}
	if err := svc.SetDNSChainPreset(ctx, storage.DNSChainPresetState{
		Mode: "resilient", DirectServer: "missing", ProxyServer: "dns-tunnel",
	}); err == nil {
		t.Fatal("несуществующий сервер должен отвергаться")
	}
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DNSChainPreset != nil {
		t.Fatalf("невалидный пресет не должен сохраняться: %+v", settings.DNSChainPreset)
	}
}

// TestUpdateDNSServerCapturesDNSChainRename — переименование сервера, на который
// ссылается пресет, обновляет state ДО ensure (иначе ensure упал бы «сервер не
// найден» и rename стал бы невозможен).
func TestUpdateDNSServerCapturesDNSChainRename(t *testing.T) {
	ctx := context.Background()
	svc, store := newDNSPresetTestService(t)
	if err := svc.SetDNSChainPreset(ctx, storage.DNSChainPresetState{
		Mode: "resilient", DirectServer: "dns-direct", ProxyServer: "dns-tunnel",
	}); err != nil {
		t.Fatal(err)
	}

	if err := svc.UpdateDNSServer(ctx, "dns-direct", DNSServer{Tag: "dns-d2", Type: "udp", Server: "77.88.8.8"}); err != nil {
		t.Fatalf("UpdateDNSServer(rename): %v", err)
	}
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DNSChainPreset == nil || settings.DNSChainPreset.DirectServer != "dns-d2" {
		t.Fatalf("state должен подхватить новый тег: %+v", settings.DNSChainPreset)
	}
	rules, err := svc.ListDNSRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.Server == "dns-direct" {
			t.Fatalf("цепочка обязана ссылаться на новый тег: %+v", rules)
		}
	}
}

// TestForceDeleteDNSServerUsedByPreset фиксирует ОСОЗНАННОЕ поведение: force-снос
// сервера, на который ссылается активный пресет, отвергается ensure-хуком
// (ошибка ensure = ошибка мутации) — сначала выключи пресет.
func TestForceDeleteDNSServerUsedByPreset(t *testing.T) {
	ctx := context.Background()
	svc, _ := newDNSPresetTestService(t)
	if err := svc.SetDNSChainPreset(ctx, storage.DNSChainPresetState{
		Mode: "resilient", DirectServer: "dns-direct", ProxyServer: "dns-tunnel",
	}); err != nil {
		t.Fatal(err)
	}
	err := svc.DeleteDNSServer(ctx, "dns-tunnel", true)
	if err == nil {
		t.Fatal("force-снос сервера пресета должен падать на ensure")
	}
	if !strings.Contains(err.Error(), "dns-пресет") {
		t.Fatalf("ошибка должна называть пресет: %v", err)
	}
	rules, lerr := svc.ListDNSRules(ctx)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(rules) != 4 {
		t.Fatalf("конфиг не должен меняться при ошибке: %+v", rules)
	}
}
