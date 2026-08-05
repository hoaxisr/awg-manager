package router

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// TestServiceUpdateDNSServerResolverInBaseSlot воспроизводит тупик интерфейса,
// который создала миграция 5D0.
//
// Сценарий: запасной путь миграции (режим переключён, движок в нём ни разу не
// поднимался) отдаёт пользовательский DNS-сервер, заданный ИМЕНЕМ ХОСТА, с
// резолвером, перецеленным на dns-bootstrap из 00-base.json
// (healDanglingDomainResolvers → pickDomainResolverFor). Дальше любая правка
// этого сервера через API падала ErrDNSServerNotFound навсегда: валидация
// знала только серверы своего слота. Обойти было нечем — снять резолвер у
// hostname-сервера нельзя, sing-box роняет такой конфиг на `check`.
func TestServiceUpdateDNSServerResolverInBaseSlot(t *testing.T) {
	svc := newTestService(t, Deps{Singbox: newTestSingbox(t)})
	orch := attachOrch(t, svc)
	if err := orch.Register(orchestrator.SlotMeta{
		Slot: orchestrator.SlotBase, Filename: "00-base.json", AlwaysOn: true,
	}); err != nil {
		t.Fatalf("register base: %v", err)
	}
	// 00-base.json — единственный объявитель dns-bootstrap.
	if err := orch.SaveSilent(orchestrator.SlotBase, []byte(
		`{"dns":{"servers":[{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}]},`+
			`"route":{"default_domain_resolver":"dns-bootstrap"}}`)); err != nil {
		t.Fatalf("save base: %v", err)
	}
	// 21-routing.json ровно в том виде, в каком его оставляет миграция.
	if err := orch.SaveSilent(orchestrator.SlotRouting, []byte(
		`{"dns":{"servers":[{"tag":"user-doh","type":"https","server":"cloudflare-dns.com",`+
			`"domain_resolver":{"server":"dns-bootstrap"}}]},"route":{"final":"direct"}}`)); err != nil {
		t.Fatalf("save routing: %v", err)
	}

	edited := DNSServer{
		Tag: "user-doh", Type: "https", Server: "dns.google",
		DomainResolver: &DomainResolver{Server: "dns-bootstrap"},
	}
	if err := svc.UpdateDNSServer(context.Background(), "user-doh", edited); err != nil {
		t.Fatalf("правка сервера с резолвером из 00-base.json: %v", err)
	}

	cfg, err := svc.loadRouterConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.DNS.Servers) != 1 {
		t.Fatalf("серверов: %d, ожидался 1: %#v", len(cfg.DNS.Servers), cfg.DNS.Servers)
	}
	got := cfg.DNS.Servers[0]
	if got.Server != "dns.google" {
		t.Errorf("upstream не сохранён: %q", got.Server)
	}
	if got.DomainResolver == nil || got.DomainResolver.Server != "dns-bootstrap" {
		t.Errorf("резолвер потерян: %#v", got.DomainResolver)
	}

	// Контроль: чужой слот не превращает проверку в решето — тег, которого нет
	// нигде, по-прежнему отвергается.
	broken := edited
	broken.DomainResolver = &DomainResolver{Server: "dns-nowhere"}
	if err := svc.UpdateDNSServer(context.Background(), "user-doh", broken); err == nil {
		t.Fatal("резолвер, которого нет ни в одном слоте, принят")
	}
}
