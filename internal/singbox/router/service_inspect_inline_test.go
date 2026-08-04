package router

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// #506: инспектор должен находить материализованные inline rule-set'ы.
// Тест идёт через ПОЛНЫЙ сервисный путь: persistSlotDirect материализует
// inline-набор в managed local "X-srs" и переписывает ссылки правил,
// InspectDNS/Inspect восстанавливают правила (теги без -srs) и обязаны
// резолвить их через алиасы — откат wiring'а на сырой cfg.Route.RuleSet
// снова даст «не определён в rule_set[]».
func TestInspect_MaterializedInlineRuleSetResolves(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	svc.deps.Singbox.(*fakeSingbox).binary = "/opt/bin/sing-box"

	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	origExec := ruleSetMatchExec
	ruleSetMatchExec = func(binary string, args []string) (string, string, error) {
		if len(args) > 0 && args[len(args)-1] == "t.me" {
			return "", "match rules.\n", nil
		}
		return "", "", &fakeExitErr{}
	}
	defer func() { ruleSetMatchExec = origExec }()

	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:   "geo-telegram",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"t.me"}}},
	}}
	cfg.Route.Rules = []Rule{{Action: "route", RuleSet: []string{"geo-telegram"}, Outbound: "vpn"}}
	cfg.DNS.Servers = []DNSServer{
		{Tag: "fakeip", Type: "fakeip", Inet4Range: "198.18.0.0/15"},
		{Tag: "real", Type: "udp", Server: "1.1.1.1"},
	}
	cfg.DNS.Rules = []DNSRule{{Action: "route", RuleSet: []string{"geo-telegram"}, Server: "fakeip"}}
	cfg.DNS.Final = "real"
	// Правила и наборы — в общем слоте, DNS-механизм — в режимном; инспектор
	// обязан собрать merged-вид, иначе ссылка на набор не резолвится.
	shared := NewEmptyConfig()
	shared.Route.RuleSet = cfg.Route.RuleSet
	shared.Route.Rules = cfg.Route.Rules
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouting, true); err != nil {
		t.Fatalf("enable SlotRouting: %v", err)
	}
	if err := svc.persistSlotDirect(orchestrator.SlotRouting, shared, false); err != nil {
		t.Fatalf("persist routing slot: %v", err)
	}
	mode := NewEmptyConfig()
	mode.Route.Final = ""
	mode.DNS = cfg.DNS
	if err := svc.persistSlotDirect(orchestrator.SlotFakeIP, mode, true); err != nil {
		t.Fatalf("persist fakeip slot: %v", err)
	}

	ctx := context.Background()

	dnsRes, err := svc.InspectDNS(ctx, InspectDNSInput{Domain: "t.me"})
	if err != nil {
		t.Fatalf("InspectDNS: %v", err)
	}
	if strings.Contains(dnsRes.Note, "не определён") {
		t.Fatalf("DNS inspector must resolve the materialized inline set, Note = %q", dnsRes.Note)
	}
	if dnsRes.MatchedRule != 0 || dnsRes.Server != "fakeip" {
		t.Fatalf("MatchedRule=%d Server=%q, want 0/fakeip; matches: %+v", dnsRes.MatchedRule, dnsRes.Server, dnsRes.Matches)
	}
	if len(dnsRes.Matches) == 0 || !containsCondition(dnsRes.Matches[0].Conditions, `rule_set "geo-telegram" → совпало`) {
		t.Fatalf("conditions must show the inline tag matched, got %+v", dnsRes.Matches)
	}

	routeRes, err := svc.Inspect(ctx, InspectInput{Domain: "t.me"})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if routeRes.Destination != "vpn" {
		t.Fatalf("route Destination = %q, want vpn", routeRes.Destination)
	}
	if strings.Contains(routeRes.Note, "не определён") {
		t.Fatalf("route inspector must resolve the materialized inline set, Note = %q", routeRes.Note)
	}
	// Теги в условиях — как в UI (без -srs).
	if len(routeRes.Matches) == 0 || !containsCondition(routeRes.Matches[0].Conditions, `rule_set "geo-telegram" → совпало`) {
		t.Fatalf("route conditions must show the inline tag, got %+v", routeRes.Matches)
	}
}

func containsCondition(conditions []string, want string) bool {
	for _, c := range conditions {
		if strings.Contains(c, want) {
			return true
		}
	}
	return false
}
