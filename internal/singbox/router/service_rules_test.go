package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Индексы правил в API — это индексы СПИСКА, который отдал ListRules, а он
// прячет авто-управляемые selective-ip правила. Мутации же индексировали сырой
// конфиг: на конфигурации с такими правилами (остались с версий, писавших их в
// маршрутный слот) правка «правила №1» молча уезжала в другое правило.
//
// Проверяются все четыре мутирующих пути разом: у них общий перевод индекса.
func TestRuleMutationsUseVisibleIndexes(t *testing.T) {
	// Сырой конфиг: авто-правило стоит МЕЖДУ пользовательскими, поэтому
	// нумерация списка и конфига расходятся начиная со второго правила.
	seed := func(t *testing.T) (*ServiceImpl, func() []Rule) {
		t.Helper()
		svc, _ := newOrchedTestService(t)
		cfg := NewEmptyConfig()
		cfg.Route.Rules = []Rule{
			{DomainSuffix: []string{"first.example"}, Action: "route", Outbound: "direct"},
			{IPCIDR: []string{"10.9.9.9/32"}, Action: "route", Outbound: "direct", AwgmManaged: selectiveIPRuleManaged},
			{DomainSuffix: []string{"second.example"}, Action: "route", Outbound: "direct"},
		}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotRouting, true); err != nil {
			t.Fatal(err)
		}
		if err := svc.deps.Orch.SaveSilent(orchestrator.SlotRouting, data); err != nil {
			t.Fatal(err)
		}
		raw := func() []Rule {
			t.Helper()
			c, err := svc.loadRouterConfig()
			if err != nil {
				t.Fatal(err)
			}
			return c.Route.Rules
		}
		visible, err := svc.ListRules(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(visible) != 2 {
			t.Fatalf("предпосылка теста: ListRules обязан прятать авто-правило, отдал %d", len(visible))
		}
		return svc, raw
	}
	ctx := context.Background()

	// Правка: видимое №1 — это "second.example", а НЕ авто-правило.
	svc, raw := seed(t)
	if err := svc.UpdateRule(ctx, 1, Rule{DomainSuffix: []string{"edited.example"}, Action: "route", Outbound: "direct"}); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	rules := raw()
	if len(rules) != 3 || rules[1].AwgmManaged != selectiveIPRuleManaged {
		t.Errorf("правка попала в авто-правило: %+v", rules)
	} else if len(rules[2].DomainSuffix) == 0 || rules[2].DomainSuffix[0] != "edited.example" {
		t.Errorf("правка не дошла до нужного правила: %+v", rules)
	}

	// Удаление.
	svc, raw = seed(t)
	if err := svc.DeleteRule(ctx, 1); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	rules = raw()
	if len(rules) != 2 || rules[1].AwgmManaged != selectiveIPRuleManaged {
		t.Errorf("удалено не то правило: %+v", rules)
	}

	// Перестановка: видимые правила меняются местами.
	svc, _ = seed(t)
	if err := svc.MoveRule(ctx, 1, 0); err != nil {
		t.Fatalf("MoveRule: %v", err)
	}
	visible, err := svc.ListRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || visible[0].DomainSuffix[0] != "second.example" || visible[1].DomainSuffix[0] != "first.example" {
		t.Errorf("перестановка видимых правил неверна: %+v", visible)
	}

	// Групповая смена выхода.
	svc, raw = seed(t)
	if err := svc.BulkSetRuleOutbound(ctx, []int{1}, "block"); err != nil {
		t.Fatalf("BulkSetRuleOutbound: %v", err)
	}
	rules = raw()
	if len(rules) != 3 {
		t.Fatalf("правила потеряны: %+v", rules)
	}
	if rules[1].Outbound != "direct" {
		t.Errorf("групповая правка задела авто-правило: %+v", rules[1])
	}
	if rules[2].Outbound != "block" {
		t.Errorf("групповая правка не дошла до нужного правила: %+v", rules[2])
	}

	// Индекс за границей ВИДИМОГО списка отвергается, а не проваливается в
	// авто-правило: правил в конфиге три, видимых — два.
	svc, _ = seed(t)
	if err := svc.DeleteRule(ctx, 2); err == nil {
		t.Error("индекс за границей видимого списка обязан быть отвергнут")
	}
}
