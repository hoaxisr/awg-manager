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

// Страховка от тихого регресса: normalizeAddressOrRule — ЗАКРЫТОЕ
// перечисление полей Rule. Матчер, забытый при добавлении нового поля,
// останется на внешнем logical-правиле, а sing-box отвергает такое правило
// вместе со ВСЕМ слотом («unknown field»): движок не знает матчеров на
// логическом правиле. Тест перебирает поля Rule рефлексией и требует, чтобы
// после нормализации на внешнем правиле не осталось ничего, кроме
// служебных полей.
func TestNormalizeAddressOrRule_AllMatchersRouted(t *testing.T) {
	// Поля, которым МЕСТО на внешнем правиле логической формы.
	outerOnly := map[string]bool{
		"Type": true, "Mode": true, "Rules": true,
		"Action": true, "Outbound": true, "UDPTimeout": true, "AwgmManaged": true,
	}

	base := Rule{RuleSet: []string{"geosite-x"}, IPCIDR: []string{"1.2.3.0/24"},
		Action: "route", Outbound: "vpn"}
	rt := reflect.TypeOf(base)

	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if outerOnly[field.Name] {
			continue
		}
		in := base
		setNonZero(t, &in, field.Name)

		got := normalizeAddressOrRule(in)
		if got.Type != "logical" {
			t.Errorf("%s: правило перестало нормализоваться", field.Name)
			continue
		}
		outer := got
		outer.Type, outer.Mode, outer.Rules = "", "", nil
		outer.Action, outer.Outbound, outer.UDPTimeout, outer.AwgmManaged = "", "", "", ""
		if outer.hasAnyMatcher() {
			t.Errorf("%s: матчер остался на внешнем logical-правиле (%+v) — sing-box отвергнет весь слот; "+
				"добавьте поле в адресную или сужающую ветку normalizeAddressOrRule", field.Name, outer)
		}
	}
}

// setNonZero проставляет полю правила ненулевое значение подходящего типа.
func setNonZero(t *testing.T, r *Rule, name string) {
	t.Helper()
	v := reflect.ValueOf(r).Elem().FieldByName(name)
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Slice:
		switch v.Type().Elem().Kind() {
		case reflect.String:
			v.Set(reflect.ValueOf([]string{"x"}))
		case reflect.Int:
			v.Set(reflect.ValueOf([]int{443}))
		default:
			t.Fatalf("%s: неизвестный тип элемента среза %s — допишите setNonZero", name, v.Type())
		}
	case reflect.Ptr:
		if v.Type().Elem().Kind() != reflect.Bool {
			t.Fatalf("%s: неизвестный тип указателя %s — допишите setNonZero", name, v.Type())
		}
		v.Set(reflect.ValueOf(boolPtr(true)))
	default:
		t.Fatalf("%s: неизвестный вид поля %s — допишите setNonZero", name, v.Kind())
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

// Правило, державшееся на одном наборе, после force-удаления обязано
// исчезнуть: правило без матчеров матчит ВЕСЬ трафик, то есть «пресет →
// VPN» увёл бы в туннель всё подряд.
func TestDeleteRuleSetForce_DropsRuleLeftWithoutMatchers(t *testing.T) {
	c := &RouterConfig{}
	if err := c.AddRuleSet(RuleSet{Tag: "geosite-x", Type: "remote", URL: "https://x/y.srs", Format: "binary"}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	c.EnsureSystemRules(true)
	systemCount := len(c.Route.Rules)
	if err := c.AddRule(Rule{RuleSet: []string{"geosite-x"}, Action: "route", Outbound: "vpn"}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := c.DeleteRuleSet("geosite-x", true); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	if len(c.Route.Rules) != systemCount {
		t.Fatalf("правило без матчеров осталось: %+v", c.Route.Rules)
	}
	for _, r := range c.Route.Rules {
		if !r.hasAnyMatcher() && !isSystemRule(r) {
			t.Errorf("правило без матчеров матчит весь трафик: %+v", r)
		}
	}
}

// `"ip_is_private": false` в sing-box означает «условия нет». Нормализовать
// по нему нельзя: ветка вышла бы пустой, а пустая ветка заставляет sing-box
// отвергнуть ВЕСЬ слот.
func TestNormalizeAddressOrRule_IgnoresFalseIPIsPrivate(t *testing.T) {
	no := false
	in := Rule{RuleSet: []string{"geosite-x"}, IPIsPrivate: &no, Action: "route", Outbound: "vpn"}
	if got := normalizeAddressOrRule(in); got.Type != "" {
		t.Errorf("правило нормализовано по пустому условию: %+v", got)
	}

	// Вместе с настоящим адресом — нормализуем, но false в ветку не тащим.
	withCIDR := Rule{RuleSet: []string{"geosite-x"}, IPCIDR: []string{"1.2.3.0/24"},
		IPIsPrivate: &no, Action: "route", Outbound: "vpn"}
	got := normalizeAddressOrRule(withCIDR)
	if got.Type != "logical" || len(got.Rules) != 2 {
		t.Fatalf("не нормализовано: %+v", got)
	}
	if got.Rules[1].IPIsPrivate != nil {
		t.Errorf("false-условие уехало в ветку: %+v", got.Rules[1])
	}
}

// force-удаление трогает только то, что осиротило само: чужое правило без
// матчеров — не наше решение (в DNS matcher-less catch-all вообще норма).
func TestDeleteRuleSetForce_KeepsUnrelatedMatcherlessRules(t *testing.T) {
	c := &RouterConfig{}
	if err := c.AddRuleSet(RuleSet{Tag: "geosite-x", Type: "remote", URL: "https://x/y.srs", Format: "binary"}); err != nil {
		t.Fatalf("AddRuleSet: %v", err)
	}
	c.Route.Rules = []Rule{
		{RuleSet: []string{"geosite-x"}, Action: "route", Outbound: "vpn"},
		{Action: "route", Outbound: "direct"}, // чужой catch-all, набора не касался
	}
	c.DNS.Rules = []DNSRule{
		{RuleSet: []string{"geosite-x"}, Server: "remote"},
		{Server: "local"}, // DNS catch-all
	}

	if err := c.DeleteRuleSet("geosite-x", true); err != nil {
		t.Fatalf("DeleteRuleSet: %v", err)
	}
	if len(c.Route.Rules) != 1 || c.Route.Rules[0].Outbound != "direct" {
		t.Errorf("route-правила: %+v", c.Route.Rules)
	}
	if len(c.DNS.Rules) != 1 || c.DNS.Rules[0].Server != "local" {
		t.Errorf("DNS-правила: %+v", c.DNS.Rules)
	}
}

// Повторное применение пресета к правилу, которое пользователь дополнил
// своими адресами, не должно плодить дубликат: набор и outbound те же.
func TestApplyPresetToConfig_NoDuplicateOnCustomizedRule(t *testing.T) {
	c := &RouterConfig{}
	preset := Preset{
		ID:       "discord",
		RuleSets: []RuleRef{{Tag: "geosite-discord", URL: "https://x/discord.srs"}},
		Rules:    []RuleLink{{RuleSetRef: "geosite-discord", ActionTarget: "tunnel"}},
	}
	if err := ApplyPresetToConfig(c, preset, "vpn"); err != nil {
		t.Fatalf("ApplyPresetToConfig: %v", err)
	}
	if len(c.Route.Rules) != 1 {
		t.Fatalf("после первого применения: %+v", c.Route.Rules)
	}

	// Пользователь дописал свои подсети — правило стало логическим.
	if err := c.UpdateRule(0, Rule{RuleSet: []string{"geosite-discord"},
		IPCIDR: []string{"66.22.192.0/18"}, Action: "route", Outbound: "vpn"}); err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}

	if err := ApplyPresetToConfig(c, preset, "vpn"); err != nil {
		t.Fatalf("повторное применение: %v", err)
	}
	if len(c.Route.Rules) != 1 {
		t.Errorf("пресет продублировал кастомизированное правило: %+v", c.Route.Rules)
	}

	// А правило, суженное портом, покрывает меньше пресета — оно не дубликат.
	c2 := &RouterConfig{}
	if err := c2.AddRule(Rule{RuleSet: []string{"geosite-discord"}, IPCIDR: []string{"66.22.192.0/18"},
		Port: []int{443}, Action: "route", Outbound: "vpn"}); err != nil {
		t.Fatalf("AddRule: %v", err)
	}
	if err := ApplyPresetToConfig(c2, preset, "vpn"); err != nil {
		t.Fatalf("ApplyPresetToConfig: %v", err)
	}
	if len(c2.Route.Rules) != 2 {
		t.Errorf("суженное правило приняли за дубликат пресета: %+v", c2.Route.Rules)
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

func TestRule_SourceMACAddress(t *testing.T) {
	r := Rule{SourceMACAddress: []string{"aa:bb:cc:dd:ee:ff"}, Action: "route", Outbound: "vpn"}
	if !r.hasAnyMatcher() {
		t.Fatal("MAC must count as a matcher")
	}
	if err := validateRule(r); err != nil {
		t.Fatalf("valid MAC rejected: %v", err)
	}
	if err := validateRule(Rule{SourceMACAddress: []string{"not-a-mac"}}); err == nil {
		t.Error("garbage MAC accepted")
	}
	// С rule_set + свои адреса MAC уходит в сужающую ветку logical(and).
	mixed := Rule{RuleSet: []string{"geosite-x"}, IPCIDR: []string{"1.2.3.0/24"},
		SourceMACAddress: r.SourceMACAddress, Action: "route", Outbound: "vpn"}
	got := normalizeAddressOrRule(mixed)
	if got.Mode != "and" || len(got.Rules) != 2 || len(got.Rules[0].SourceMACAddress) != 1 {
		t.Errorf("narrowing branch lost MAC: %+v", got)
	}
}
