package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// Смешанный набор (домены + CIDR) в dns.rules включает в sing-box 1.15
// legacy DNS mode, а тот бьёт FATAL'ом на старте движка. Материализация
// обязана снимать это флагом rule_set_ip_cidr_match_source.
func TestMaterializeConfigSetsDNSRuleSetMatchSource(t *testing.T) {
	dir := t.TempDir()
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	cfg := &RouterConfig{}
	cfg.Route.RuleSet = []RuleSet{{Tag: "telegram", Type: "remote", Format: "binary", URL: "https://example.invalid/telegram.srs"}}
	cfg.DNS.Rules = []DNSRule{
		{RuleSet: []string{"telegram"}, Action: "route", Server: "dns-tunnel"},
		{DomainSuffix: []string{"example.com"}, Action: "route", Server: "dns-direct"},
		{RuleSet: []string{"telegram"}, MatchResponse: &DNSMatchResponse{Enabled: true}, Action: "route", Server: "dns-direct"},
	}

	out, err := m.materializeConfig(orchestrator.SlotRouter, cfg)
	if err != nil {
		t.Fatalf("materializeConfig: %v", err)
	}
	if !out.DNS.Rules[0].RuleSetIPCIDRMatchSource {
		t.Error("правило с rule_set осталось без rule_set_ip_cidr_match_source — движок упадёт на старте")
	}
	if out.DNS.Rules[1].RuleSetIPCIDRMatchSource {
		t.Error("правило без rule_set помечено флагом")
	}
	if out.DNS.Rules[2].RuleSetIPCIDRMatchSource {
		t.Error("match_response-правило помечено флагом — ip_cidr набора там матчится по ОТВЕТУ, флаг это ломает")
	}
	// Материализация отдаёт копию для sing-box; хранимая запись остаётся чистой.
	if cfg.DNS.Rules[0].RuleSetIPCIDRMatchSource {
		t.Error("флаг просочился в исходный конфиг")
	}
}

// Флаг определяется формой правила, а не тем, что прислали: снаружи он
// приезжает и через API (тело декодируется прямо в DNSRule), и из хранимого
// конфига, который restoreConfig не чистит. На match_response-правиле он ломает
// geoip по ответу, поэтому должен сниматься.
func TestMaterializeConfigOverridesIncomingMatchSource(t *testing.T) {
	m := ruleSetMaterializer{configDir: t.TempDir(), binary: "/opt/bin/sing-box"}
	cfg := &RouterConfig{}
	cfg.DNS.Rules = []DNSRule{
		{RuleSet: []string{"tg"}, MatchResponse: &DNSMatchResponse{Enabled: true}, RuleSetIPCIDRMatchSource: true},
		{DomainSuffix: []string{"example.com"}, RuleSetIPCIDRMatchSource: true},
		{RuleSet: []string{"tg"}, RuleSetIPCIDRMatchSource: true},
	}
	out, err := m.materializeConfig(orchestrator.SlotRouter, cfg)
	if err != nil {
		t.Fatalf("materializeConfig: %v", err)
	}
	if out.DNS.Rules[0].RuleSetIPCIDRMatchSource {
		t.Error("флаг на match_response-правиле не снят — geoip по ответу сломан")
	}
	if out.DNS.Rules[1].RuleSetIPCIDRMatchSource {
		t.Error("флаг на правиле без rule_set не снят")
	}
	if !out.DNS.Rules[2].RuleSetIPCIDRMatchSource {
		t.Error("флаг на обычном правиле с rule_set потерян")
	}
}

// Правка чинит только следующую запись конфига, а у пострадавшего он уже
// записан и перезаписывать его на старте некому — отсюда отдельный проход.
func TestMigrateDNSRuleSetMatchSource(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"disabled", "pending"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel string, body string) string {
		p := filepath.Join(dir, rel)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	active := write("20-router.json", `{"dns":{"rules":[
		{"rule_set":["telegram"],"action":"route","server":"dns-tunnel"},
		{"rule_set":["geoip-cn"],"match_response":{"enabled":true},"action":"route","server":"dns-direct"},
		{"domain_suffix":["example.com"],"action":"route","server":"dns-direct"},
		{"type":"logical","mode":"or","rules":[{"rule_set":["discord"]}],"action":"route","server":"dns-tunnel"}
	]}}`)
	pending := write(filepath.Join("pending", "20-router.json"), `{"dns":{"rules":[{"rule_set":["telegram"]}]}}`)
	disabled := write(filepath.Join("disabled", "40-fakeip.json"), `{"dns":{"rules":[{"rule_set":["telegram"]}]}}`)
	userSlot := write(userSlotFilename(), `{"dns":{"rules":[{"rule_set":["telegram"]}]}}`)
	untouched := write("10-tunnels.json", `{"outbounds":[{"type":"direct","tag":"direct"}]}`)

	changed, err := MigrateDNSRuleSetMatchSource(dir)
	if err != nil {
		t.Fatalf("MigrateDNSRuleSetMatchSource: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, хотя правила правились")
	}

	var cfg struct {
		DNS struct {
			Rules []map[string]any `json:"rules"`
		} `json:"dns"`
	}
	raw, _ := os.ReadFile(active)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DNS.Rules[0]["rule_set_ip_cidr_match_source"] != true {
		t.Error("правило с rule_set не помечено")
	}
	if _, has := cfg.DNS.Rules[1]["rule_set_ip_cidr_match_source"]; has {
		t.Error("match_response-правило помечено — geoip по ответу сломан")
	}
	if _, has := cfg.DNS.Rules[2]["rule_set_ip_cidr_match_source"]; has {
		t.Error("правило без rule_set помечено")
	}
	nested := cfg.DNS.Rules[3]["rules"].([]any)[0].(map[string]any)
	if nested["rule_set_ip_cidr_match_source"] != true {
		t.Error("вложенное правило logical не помечено — форк собирает признак legacy и по ним")
	}
	if cfg.DNS.Rules[3]["server"] != "dns-tunnel" {
		t.Error("миграция потеряла поля правила")
	}

	for _, p := range []string{pending, disabled} {
		if !strings.Contains(string(mustRead(t, p)), "rule_set_ip_cidr_match_source") {
			t.Errorf("%s не мигрирован", filepath.Base(p))
		}
	}
	if strings.Contains(string(mustRead(t, userSlot)), "rule_set_ip_cidr_match_source") {
		t.Error("слот ручного редактора переписан за спиной пользователя")
	}
	if got := string(mustRead(t, untouched)); !strings.Contains(got, `"outbounds"`) || strings.Contains(got, "match_source") {
		t.Error("файл без dns.rules тронут")
	}

	// Идемпотентность: второй проход не должен ничего менять.
	before := mustRead(t, active)
	changed2, err := MigrateDNSRuleSetMatchSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Error("второй проход отчитался об изменениях — миграция не идемпотентна")
	}
	if string(mustRead(t, active)) != string(before) {
		t.Error("второй проход переписал файл")
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDNSRuleSetClientMatchIssue(t *testing.T) {
	mixed := RuleSet{Tag: "lan-mix", Type: "inline", Rules: []map[string]any{
		{"domain_suffix": []any{"lan.example"}},
		{"ip_cidr": []any{"192.168.0.0/16"}},
	}}
	public := RuleSet{Tag: "tg", Type: "inline", Rules: []map[string]any{
		{"ip_cidr": []any{"91.108.4.0/22"}},
	}}

	// ip_cidr бывает трёх форм: []any после JSON-декода, []string из
	// datLinesToRuleSetRules и скаляр, который принимает сам sing-box.
	strList := RuleSet{Tag: "str-list", Type: "inline", Rules: []map[string]any{{"ip_cidr": []string{"10.0.0.0/8"}}}}
	scalar := RuleSet{Tag: "scalar", Type: "inline", Rules: []map[string]any{{"ip_cidr": "172.16.0.0/12"}}}
	// Накрывает домашние сети, хотя базовый адрес приватным не выглядит.
	wide := RuleSet{Tag: "wide", Type: "inline", Rules: []map[string]any{{"ip_cidr": []any{"0.0.0.0/0"}}}}
	cgnat := RuleSet{Tag: "cgnat", Type: "inline", Rules: []map[string]any{{"ip_cidr": []any{"100.64.0.0/10"}}}}
	ula := RuleSet{Tag: "ula", Type: "inline", Rules: []map[string]any{{"ip_cidr": []any{"fd00::/8"}}}}

	cases := []struct {
		name string
		rule DNSRule
		want bool
	}{
		{"приватный набор в DNS-правиле", DNSRule{RuleSet: []string{"lan-mix"}}, true},
		{"публичный набор", DNSRule{RuleSet: []string{"tg"}}, false},
		{"приватный набор под match_response", DNSRule{RuleSet: []string{"lan-mix"}, MatchResponse: &DNSMatchResponse{Enabled: true}}, false},
		{"ip_cidr списком строк", DNSRule{RuleSet: []string{"str-list"}}, true},
		{"ip_cidr скаляром", DNSRule{RuleSet: []string{"scalar"}}, true},
		{"набор на весь интернет", DNSRule{RuleSet: []string{"wide"}}, true},
		{"CGNAT", DNSRule{RuleSet: []string{"cgnat"}}, true},
		{"IPv6 ULA", DNSRule{RuleSet: []string{"ula"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &RouterConfig{}
			cfg.Route.RuleSet = []RuleSet{mixed, public, strList, scalar, wide, cgnat, ula}
			cfg.DNS.Rules = []DNSRule{tc.rule}
			issues := computeDNSRuleSetClientMatchIssues(cfg)
			if got := len(issues) > 0; got != tc.want {
				t.Fatalf("issues = %+v, want warning = %v", issues, tc.want)
			}
			if tc.want {
				got := issues[0]
				if got.Severity != "warning" || got.Kind != "dns-rule-set-client-match" {
					t.Errorf("severity/kind = %q/%q", got.Severity, got.Kind)
				}
				if got.RuleIndex != 0 || got.Tag != tc.rule.RuleSet[0] {
					t.Errorf("issue не указывает на правило/набор: index=%d tag=%q", got.RuleIndex, got.Tag)
				}
				if !strings.Contains(got.Message, "адресом клиента") {
					t.Errorf("невнятное сообщение: %q", got.Message)
				}
			}
		})
	}
}
