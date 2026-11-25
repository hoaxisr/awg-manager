package serverwizard

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tgwebproxy"
)

func planItem(items []PlanItem, target string) (PlanItem, bool) {
	for _, it := range items {
		if it.Target == target {
			return it, true
		}
	}
	return PlanItem{}, false
}

// План обещает открыть порт только с прямым адресом и называет закрытие при
// уходе с прямого подключения.
// Мутации: вынести wan_firewall из-под DirectHost; убрать пункт «disable» → красный.
func TestTelegramPlan_WANFirewallFollowsDirectHost(t *testing.T) {
	svc, _, _, tg, _ := setupTestWizard(t)
	for _, scenario := range []string{"direct_fake_tls", "dual"} {
		desired := DesiredWizardConfig{Scenario: scenario, DirectPort: 8443}
		if it, ok := planItem(svc.buildTelegramPlan(desired, "fp").Items, "wan_firewall"); ok {
			t.Fatalf("%s без адреса: план обещает порт %+v", scenario, it)
		}
		desired.DirectHost = "203.0.113.10"
		if it, ok := planItem(svc.buildTelegramPlan(desired, "fp").Items, "wan_firewall"); !ok || it.Action != "enable" || it.NewValue != "tcp/8443" {
			t.Fatalf("%s с адресом: %+v", scenario, it)
		}
	}

	cdn := DesiredWizardConfig{Scenario: "cdn_http"}
	tg.cfg = tgwebproxy.PublicConfig{Enabled: true, Scenario: "dual", DirectHost: "203.0.113.10", DirectPort: 9443}
	if it, ok := planItem(svc.buildTelegramPlan(cdn, "fp").Items, "wan_firewall"); !ok || it.Action != "disable" || it.OldValue != "tcp/9443" {
		t.Fatalf("уход на cdn_http: %+v", it)
	}
	tg.cfg.DirectHost = ""
	if it, ok := planItem(svc.buildTelegramPlan(cdn, "fp").Items, "wan_firewall"); ok {
		t.Fatalf("закрывать нечего: %+v", it)
	}
}

// Мутация: убрать проверку port_direct_wan или её ветку без адреса → красный.
func TestPreflight_ReportsWhetherWANPortOpens(t *testing.T) {
	svc, _, _, _, _ := setupTestWizard(t)
	find := func(req WizardPlanRequest) (PreflightCheck, bool) {
		for _, c := range svc.Preflight(context.Background(), req).Checks {
			if c.ID == "port_direct_wan" {
				return c, true
			}
		}
		return PreflightCheck{}, false
	}
	req := WizardPlanRequest{Kind: "tgwebproxy", Scenario: "dual", DirectPort: 9443, DirectHost: "203.0.113.10"}
	if c, ok := find(req); !ok || c.Status != CheckStatusOK || !strings.Contains(c.Message, "9443 будет открыт") {
		t.Fatalf("с адресом: %+v", c)
	}
	req.DirectPort = 443
	if c, ok := find(req); !ok || c.Status != CheckStatusWarning || !strings.Contains(c.Message, "недопустим") {
		t.Fatalf("недопустимый порт: %+v", c)
	}
	req.DirectPort, req.DirectHost = 9443, ""
	if c, ok := find(req); !ok || c.Status != CheckStatusWarning || !strings.Contains(c.Message, "прямой адрес не задан") {
		t.Fatalf("без адреса: %+v", c)
	}
	req.Scenario = "cdn_http"
	if c, ok := find(req); ok {
		t.Fatalf("cdn_http: лишняя проверка %+v", c)
	}
}
