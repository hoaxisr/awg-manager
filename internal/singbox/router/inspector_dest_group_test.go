package router

import (
	"os"
	"path/filepath"
	"testing"
)

// Оракул issue #699. sing-box кладёт domain*/ip_cidr одного правила в общую
// группу «адрес назначения» и OR-ит их внутри группы (route/rule/
// rule_abstract.go:evaluateGroups в форке 1.14) — инспектор обязан это
// повторять, иначе он объявляет рабочее правило нерабочим.
func TestInspect_DestinationAddressGroupIsOR(t *testing.T) {
	rules := []Rule{
		{DomainSuffix: []string{"google.com"}, IPCIDR: []string{"8.8.8.0/24"}, Action: "route", Outbound: "vpn"},
	}

	byDomain := Inspect(InspectInput{Domain: "google.com"}, rules, nil, "direct", "", nil)
	if byDomain.Destination != "vpn" || byDomain.MatchedRule != 0 {
		t.Errorf("домен при непопадающем ip_cidr: dest=%q matched=%d, want vpn/0",
			byDomain.Destination, byDomain.MatchedRule)
	}

	byIP := Inspect(InspectInput{Domain: "8.8.8.8"}, rules, nil, "direct", "", nil)
	if byIP.Destination != "vpn" || byIP.MatchedRule != 0 {
		t.Errorf("IP при непопадающем domain_suffix: dest=%q matched=%d, want vpn/0",
			byIP.Destination, byIP.MatchedRule)
	}

	neither := Inspect(InspectInput{Domain: "example.org"}, rules, nil, "direct", "", nil)
	if neither.Destination != "direct" || neither.MatchedRule != -1 {
		t.Errorf("ни домен, ни IP: dest=%q matched=%d, want direct/-1",
			neither.Destination, neither.MatchedRule)
	}
}

// Матчеры вне группы адреса назначения (port/protocol) остаются AND —
// OR не должен расползтись на всё правило.
func TestInspect_NonAddressMatchersStayAND(t *testing.T) {
	rules := []Rule{
		{DomainSuffix: []string{"google.com"}, IPCIDR: []string{"8.8.8.0/24"}, Port: []int{443},
			Action: "route", Outbound: "vpn"},
	}

	noPort := Inspect(InspectInput{Domain: "google.com"}, rules, nil, "direct", "", nil)
	if noPort.MatchedRule != -1 {
		t.Errorf("порт задан в правиле, но не во вводе: matched=%d, want -1", noPort.MatchedRule)
	}

	withPort := Inspect(InspectInput{Domain: "google.com", Port: 443}, rules, nil, "direct", "", nil)
	if withPort.MatchedRule != 0 {
		t.Errorf("домен+порт совпали: matched=%d, want 0", withPort.MatchedRule)
	}

	wrongPort := Inspect(InspectInput{Domain: "google.com", Port: 80}, rules, nil, "direct", "", nil)
	if wrongPort.MatchedRule != -1 {
		t.Errorf("порт не совпал: matched=%d, want -1", wrongPort.MatchedRule)
	}
}

// Ввод «protocol» снаружи принимает только tcp/udp (validateInspectParams),
// то есть это L4 — сопоставлять его надо с rule.network. Пока этого не было,
// udp-правило отчитывалось совпадением на TCP-проверке.
func TestInspect_NetworkMatcher(t *testing.T) {
	rules := []Rule{
		{DomainSuffix: []string{"google.com"}, Network: "udp", Action: "route", Outbound: "vpn"},
	}

	if got := Inspect(InspectInput{Domain: "google.com", Protocol: "udp"}, rules, nil, "direct", "", nil); got.MatchedRule != 0 {
		t.Errorf("udp-правило на udp-проверке: matched=%d, want 0", got.MatchedRule)
	}
	if got := Inspect(InspectInput{Domain: "google.com", Protocol: "tcp"}, rules, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("udp-правило на tcp-проверке: matched=%d, want -1", got.MatchedRule)
	}
	if got := Inspect(InspectInput{Domain: "google.com"}, rules, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("udp-правило без протокола во вводе: matched=%d, want -1", got.MatchedRule)
	}
}

// protocol — прикладной протокол от сниффера, ручной проверкой он не
// задаётся. Ни сравнивать его с tcp/udp, ни считать совпавшим нельзя.
func TestInspect_SniffedProtocolIsUnverifiable(t *testing.T) {
	tls := []Rule{{DomainSuffix: []string{"google.com"}, Protocol: "tls", Action: "route", Outbound: "vpn"}}
	if got := Inspect(InspectInput{Domain: "google.com", Protocol: "tcp"}, tls, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("правило по tls: matched=%d, want -1", got.MatchedRule)
	}

	// Раньше такое правило «совпадало» при вводе tcp, хотя движок его не
	// исполняет никогда: значения tcp/udp в protocol у sing-box нет.
	bogus := []Rule{{DomainSuffix: []string{"google.com"}, Protocol: "tcp", Action: "route", Outbound: "vpn"}}
	if got := Inspect(InspectInput{Domain: "google.com", Protocol: "tcp"}, bogus, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("правило protocol=tcp: matched=%d, want -1", got.MatchedRule)
	}
}

// inbound недоступен ручной проверке — правило с ним не может отчитаться
// совпадением по остальным условиям.
func TestInspect_InboundBlocksMatch(t *testing.T) {
	rules := []Rule{
		{DomainSuffix: []string{"google.com"}, Inbound: []string{"tproxy-qos-1"}, Action: "route", Outbound: "vpn"},
	}
	if got := Inspect(InspectInput{Domain: "google.com"}, rules, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("правило с inbound: matched=%d, want -1", got.MatchedRule)
	}
}

// Системное правило UDP-таймаута (`route-options`) стоит в префиксе КАЖДОГО
// роутера и совпадает по network=udp. Оно ничего не маршрутизирует — движок
// продолжает обход, — поэтому инспектор не имеет права объявлять его
// финальным: иначе любая UDP-проверка утыкается в него и отчитывается
// «DIRECT», затеняя все пользовательские правила.
func TestInspect_RouteOptionsIsNotTerminal(t *testing.T) {
	rules := []Rule{
		{Action: "sniff"},
		{Action: "route-options", Network: "udp", UDPTimeout: "5m"},
		{DomainSuffix: []string{"google.com"}, Action: "route", Outbound: "vpn"},
	}

	got := Inspect(InspectInput{Domain: "google.com", Protocol: "udp"}, rules, nil, "direct", "", nil)
	if got.MatchedRule != 2 || got.Destination != "vpn" {
		t.Errorf("UDP-проверка: matched=%d dest=%q, want 2/vpn", got.MatchedRule, got.Destination)
	}
	if !got.Matches[1].Matched {
		t.Errorf("правило route-options обязано отчитаться совпадением по network: %+v", got.Matches[1])
	}
}

// ip_is_private движок кладёт в группу адреса назначения, значит оно
// OR-ится с ip_cidr и доменами, а не сужает правило.
func TestInspect_IPIsPrivateJoinsAddressGroup(t *testing.T) {
	yes := true
	rules := []Rule{
		{IPCIDR: []string{"8.8.8.0/24"}, IPIsPrivate: &yes, Action: "route", Outbound: "vpn"},
	}

	if got := Inspect(InspectInput{Domain: "192.168.1.5"}, rules, nil, "direct", "", nil); got.MatchedRule != 0 {
		t.Errorf("приватный адрес: matched=%d, want 0", got.MatchedRule)
	}
	if got := Inspect(InspectInput{Domain: "8.8.8.8"}, rules, nil, "direct", "", nil); got.MatchedRule != 0 {
		t.Errorf("публичный адрес из ip_cidr: matched=%d, want 0", got.MatchedRule)
	}
	if got := Inspect(InspectInput{Domain: "1.1.1.1"}, rules, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("публичный адрес вне ip_cidr: matched=%d, want -1", got.MatchedRule)
	}
}

// Ветка логического правила, состоящая только из непроверяемых условий,
// не должна обнулять всё правило: в плоской форме те же условия просто
// игнорировались.
func TestInspect_UnverifiableBranchDoesNotVeto(t *testing.T) {
	rule := []Rule{{
		Type: "logical", Mode: "and",
		Rules: []Rule{
			{SourceIPCIDR: []string{"192.168.1.0/24"}},
			{IPCIDR: []string{"66.22.192.0/18"}},
		},
		Action: "route", Outbound: "vpn",
	}}

	if got := Inspect(InspectInput{Domain: "66.22.200.1"}, rule, nil, "direct", "", nil); got.MatchedRule != 0 {
		t.Errorf("ветка source_ip_cidr не должна хоронить правило: matched=%d, want 0", got.MatchedRule)
	}
	if got := Inspect(InspectInput{Domain: "1.2.3.4"}, rule, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("адрес вне CIDR: matched=%d, want -1", got.MatchedRule)
	}

	// Правило, где проверять нечего вовсе, остаётся несовпавшим.
	onlySource := []Rule{{
		Type: "logical", Mode: "and",
		Rules:  []Rule{{SourceIPCIDR: []string{"192.168.1.0/24"}}},
		Action: "route", Outbound: "vpn",
	}}
	if got := Inspect(InspectInput{Domain: "66.22.200.1"}, onlySource, nil, "direct", "", nil); got.MatchedRule != -1 {
		t.Errorf("правило без проверяемых условий: matched=%d, want -1", got.MatchedRule)
	}
}

// Точный `domain` инспектор раньше не вычислял вовсе — правило с одним лишь
// этим матчером считалось пустым и пропускалось.
func TestInspect_ExactDomainMatcher(t *testing.T) {
	rules := []Rule{
		{Domain: []string{"google.com"}, Action: "route", Outbound: "vpn"},
	}

	exact := Inspect(InspectInput{Domain: "google.com"}, rules, nil, "direct", "", nil)
	if exact.Destination != "vpn" || exact.MatchedRule != 0 {
		t.Errorf("точный домен: dest=%q matched=%d, want vpn/0", exact.Destination, exact.MatchedRule)
	}

	sub := Inspect(InspectInput{Domain: "mail.google.com"}, rules, nil, "direct", "", nil)
	if sub.MatchedRule != -1 {
		t.Errorf("поддомен не обязан совпадать с точным domain: matched=%d, want -1", sub.MatchedRule)
	}
}

// Логические правила — основная форма после нормализации «пресет + свои
// адреса». Инспектор обязан их разбирать, иначе он видит «пустое правило».
func TestInspect_LogicalRules(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "list.srs")
	if err := os.WriteFile(tmp, []byte("x"), 0644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	origExec := ruleSetMatchExec
	ruleSetMatchExec = func(_ string, args []string) (string, string, error) {
		if len(args) > 0 && args[len(args)-1] == "google.com" {
			return "", "match rules.\n", nil
		}
		return "", "", &fakeExitErr{}
	}
	defer func() { ruleSetMatchExec = origExec }()

	ruleSets := []RuleSet{{Tag: "geosite-google", Type: "local", Path: tmp, Format: "binary"}}
	orRule := []Rule{{
		Type: "logical", Mode: "or",
		Rules: []Rule{
			{RuleSet: []string{"geosite-google"}},
			{IPCIDR: []string{"66.22.192.0/18"}},
		},
		Action: "route", Outbound: "vpn",
	}}

	bySet := Inspect(InspectInput{Domain: "google.com"}, orRule, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if bySet.Destination != "vpn" || bySet.MatchedRule != 0 {
		t.Errorf("ветка rule_set: dest=%q matched=%d, want vpn/0", bySet.Destination, bySet.MatchedRule)
	}

	byCIDR := Inspect(InspectInput{Domain: "66.22.200.1"}, orRule, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if byCIDR.Destination != "vpn" || byCIDR.MatchedRule != 0 {
		t.Errorf("ветка ip_cidr: dest=%q matched=%d, want vpn/0", byCIDR.Destination, byCIDR.MatchedRule)
	}

	none := Inspect(InspectInput{Domain: "1.2.3.4"}, orRule, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if none.MatchedRule != -1 {
		t.Errorf("ни одна ветка: matched=%d, want -1", none.MatchedRule)
	}

	andRule := []Rule{{
		Type: "logical", Mode: "and",
		Rules: []Rule{
			{DomainSuffix: []string{"google.com"}},
			{Port: []int{443}},
		},
		Action: "route", Outbound: "vpn",
	}}
	bothHit := Inspect(InspectInput{Domain: "google.com", Port: 443}, andRule, nil, "direct", "", nil)
	if bothHit.MatchedRule != 0 {
		t.Errorf("mode=and, обе ветки: matched=%d, want 0", bothHit.MatchedRule)
	}
	oneHit := Inspect(InspectInput{Domain: "google.com", Port: 80}, andRule, nil, "direct", "", nil)
	if oneHit.MatchedRule != -1 {
		t.Errorf("mode=and, одна ветка: matched=%d, want -1", oneHit.MatchedRule)
	}
}

// Не нормализованное (импортированное или досталось от старой версии) плоское
// правило «rule_set + свои адреса» инспектор показывает так же, как выглядит
// его нормализованная форма — OR внутри группы адреса назначения.
func TestInspect_FlatRuleSetWithOwnAddressesIsOR(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "list.srs")
	if err := os.WriteFile(tmp, []byte("x"), 0644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	origExec := ruleSetMatchExec
	ruleSetMatchExec = func(_ string, args []string) (string, string, error) {
		if len(args) > 0 && args[len(args)-1] == "google.com" {
			return "", "match rules.\n", nil
		}
		return "", "", &fakeExitErr{}
	}
	defer func() { ruleSetMatchExec = origExec }()

	ruleSets := []RuleSet{{Tag: "geosite-google", Type: "local", Path: tmp, Format: "binary"}}
	rules := []Rule{{
		RuleSet: []string{"geosite-google"},
		IPCIDR:  []string{"66.22.192.0/18"},
		Action:  "route", Outbound: "vpn",
	}}

	bySet := Inspect(InspectInput{Domain: "google.com"}, rules, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if bySet.MatchedRule != 0 {
		t.Errorf("набор совпал, свой CIDR нет: matched=%d, want 0", bySet.MatchedRule)
	}

	byCIDR := Inspect(InspectInput{Domain: "66.22.200.1"}, rules, ruleSets, "direct", "/usr/bin/sing-box", nil)
	if byCIDR.MatchedRule != 0 {
		t.Errorf("свой CIDR совпал, набор нет: matched=%d, want 0", byCIDR.MatchedRule)
	}
}
