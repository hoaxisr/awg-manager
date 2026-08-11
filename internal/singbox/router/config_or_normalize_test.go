package router

import (
	"reflect"
	"testing"
)

func TestNormalizeAddressOrRule(t *testing.T) {
	cases := []struct {
		name string
		in   Rule
		want Rule
	}{
		{
			name: "набор без своих адресов не трогаем",
			in:   Rule{RuleSet: []string{"geosite-discord"}, Action: "route", Outbound: "vpn"},
			want: Rule{RuleSet: []string{"geosite-discord"}, Action: "route", Outbound: "vpn"},
		},
		{
			name: "свои адреса без набора не трогаем",
			in:   Rule{DomainSuffix: []string{"a.com"}, IPCIDR: []string{"1.2.3.0/24"}, Action: "route", Outbound: "vpn"},
			want: Rule{DomainSuffix: []string{"a.com"}, IPCIDR: []string{"1.2.3.0/24"}, Action: "route", Outbound: "vpn"},
		},
		{
			name: "уже логическое не трогаем",
			in: Rule{Type: "logical", Mode: "or", Rules: []Rule{
				{RuleSet: []string{"geosite-discord"}}, {IPCIDR: []string{"1.2.3.0/24"}},
			}, Action: "route", Outbound: "vpn"},
			want: Rule{Type: "logical", Mode: "or", Rules: []Rule{
				{RuleSet: []string{"geosite-discord"}}, {IPCIDR: []string{"1.2.3.0/24"}},
			}, Action: "route", Outbound: "vpn"},
		},
		{
			name: "набор + свой CIDR → logical or",
			in: Rule{RuleSet: []string{"geosite-discord"}, IPCIDR: []string{"66.22.192.0/18"},
				Action: "route", Outbound: "vpn"},
			want: Rule{Type: "logical", Mode: "or", Rules: []Rule{
				{RuleSet: []string{"geosite-discord"}},
				{IPCIDR: []string{"66.22.192.0/18"}},
			}, Action: "route", Outbound: "vpn"},
		},
		{
			name: "набор + свои домены и CIDR → адреса в одной ветке",
			in: Rule{RuleSet: []string{"geosite-discord"}, Domain: []string{"a.com"},
				DomainSuffix: []string{"b.com"}, IPCIDR: []string{"1.2.3.0/24"},
				Action: "reject"},
			want: Rule{Type: "logical", Mode: "or", Rules: []Rule{
				{RuleSet: []string{"geosite-discord"}},
				{Domain: []string{"a.com"}, DomainSuffix: []string{"b.com"}, IPCIDR: []string{"1.2.3.0/24"}},
			}, Action: "reject"},
		},
		{
			// ip_is_private в движке живёт в группе адреса назначения
			// (destinationIPCIDRItems) и OR-ится с ip_cidr/domain — вынести
			// его в сужающую ветку значило бы сузить правило почти до нуля.
			name: "ip_is_private идёт в адресную ветку, а не в сужения",
			in: Rule{RuleSet: []string{"geosite-discord"}, IPCIDR: []string{"1.2.3.0/24"},
				IPIsPrivate: boolPtr(true), Action: "route", Outbound: "vpn"},
			want: Rule{Type: "logical", Mode: "or", Rules: []Rule{
				{RuleSet: []string{"geosite-discord"}},
				{IPCIDR: []string{"1.2.3.0/24"}, IPIsPrivate: boolPtr(true)},
			}, Action: "route", Outbound: "vpn"},
		},
		{
			name: "набор + один лишь ip_is_private тоже нормализуется",
			in: Rule{RuleSet: []string{"geosite-discord"}, IPIsPrivate: boolPtr(true),
				Action: "route", Outbound: "vpn"},
			want: Rule{Type: "logical", Mode: "or", Rules: []Rule{
				{RuleSet: []string{"geosite-discord"}},
				{IPIsPrivate: boolPtr(true)},
			}, Action: "route", Outbound: "vpn"},
		},
		{
			name: "сужающие матчеры остаются внешним AND",
			in: Rule{RuleSet: []string{"geosite-discord"}, IPCIDR: []string{"1.2.3.0/24"},
				Port: []int{443}, Network: "udp", Action: "route", Outbound: "vpn"},
			want: Rule{Type: "logical", Mode: "and", Rules: []Rule{
				{Port: []int{443}, Network: "udp"},
				{Type: "logical", Mode: "or", Rules: []Rule{
					{RuleSet: []string{"geosite-discord"}},
					{IPCIDR: []string{"1.2.3.0/24"}},
				}},
			}, Action: "route", Outbound: "vpn"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeAddressOrRule(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("normalizeAddressOrRule()\n got = %+v\nwant = %+v", got, c.want)
			}
			// Идемпотентность: повторный прогон ничего не меняет.
			if again := normalizeAddressOrRule(got); !reflect.DeepEqual(again, got) {
				t.Errorf("не идемпотентно:\n first = %+v\nsecond = %+v", got, again)
			}
		})
	}
}

func TestAddAndUpdateRuleNormalize(t *testing.T) {
	c := &RouterConfig{}
	flat := Rule{RuleSet: []string{"geosite-discord"}, IPCIDR: []string{"66.22.192.0/18"},
		Action: "route", Outbound: "vpn"}
	if err := c.AddRule(flat); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if got := c.Route.Rules[0]; got.Type != "logical" || got.Mode != "or" || len(got.Rules) != 2 {
		t.Fatalf("AddRule не нормализовал: %+v", got)
	}
	if got := c.Route.Rules[0]; got.Outbound != "vpn" || got.Action != "route" {
		t.Errorf("действие потерялось при нормализации: %+v", got)
	}

	if err := c.UpdateRule(0, flat); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if got := c.Route.Rules[0]; got.Type != "logical" || len(got.RuleSet) != 0 || len(got.IPCIDR) != 0 {
		t.Fatalf("UpdateRule не нормализовал: %+v", got)
	}
}

// force-удаление набора вычищает тег из ветки — опустевшая ветка обязана
// схлопнуться. Пустое sub-rule sing-box считает невалидным (IsValid) и
// отвергает ВЕСЬ конфиг, то есть слот перестал бы применяться целиком.
func TestDeleteRuleSetForce_CollapsesEmptyBranch(t *testing.T) {
	c := &RouterConfig{}
	if err := c.AddRuleSet(RuleSet{Tag: "geosite-x", Type: "remote", URL: "https://x/y.srs", Format: "binary"}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if err := c.AddRule(Rule{RuleSet: []string{"geosite-x"}, IPCIDR: []string{"1.2.3.0/24"},
		Port: []int{443}, Action: "route", Outbound: "vpn"}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := c.DeleteRuleSet("geosite-x", true); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}

	got := c.Route.Rules[0]
	if hasEmptyNestedRule(got) {
		t.Fatalf("осталась пустая ветка: %+v", got)
	}
	// Сужение по порту обязано пережить схлопывание.
	flat := got
	for flat.Type == "logical" && len(flat.Rules) == 1 {
		flat = flat.Rules[0]
	}
	if len(got.Rules) == 0 && len(got.IPCIDR) == 0 {
		t.Errorf("правило потеряло адреса: %+v", got)
	}
	if !ruleMentionsPort(got, 443) {
		t.Errorf("правило потеряло порт: %+v", got)
	}
	if got.Outbound != "vpn" {
		t.Errorf("правило потеряло outbound: %+v", got)
	}
}

func hasEmptyNestedRule(r Rule) bool {
	for _, nested := range r.Rules {
		if !nested.hasAnyMatcher() {
			return true
		}
		if hasEmptyNestedRule(nested) {
			return true
		}
	}
	return false
}

func ruleMentionsPort(r Rule, port int) bool {
	for _, p := range r.Port {
		if p == port {
			return true
		}
	}
	for _, nested := range r.Rules {
		if ruleMentionsPort(nested, port) {
			return true
		}
	}
	return false
}

// Ссылки на наборы внутри веток обязаны находиться — иначе удаление набора
// снесёт его из-под живого правила, а GC вычистит артефакты.
func TestRuleSetReferencesSeeNestedBranches(t *testing.T) {
	c := &RouterConfig{}
	if err := c.AddRuleSet(RuleSet{Tag: "geosite-discord", Type: "remote", URL: "https://x/y.srs", Format: "binary"}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	if err := c.AddRule(Rule{RuleSet: []string{"geosite-discord"}, IPCIDR: []string{"66.22.192.0/18"},
		Action: "route", Outbound: "vpn"}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := c.DeleteRuleSet("geosite-discord", false); err == nil {
		t.Fatal("удаление набора, на который ссылается ветка правила, должно быть отклонено")
	}
}
