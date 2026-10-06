package router

import (
	"encoding/json"
	"testing"
)

func TestParseRouterConfigBytesDeduplicatesOnlyExactRules(t *testing.T) {
	direct := Rule{Domain: []string{"my.keenetic.net"}, Outbound: "direct"}
	proxy := Rule{DomainSuffix: []string{"keenetic.com"}, Outbound: "old-proxy"}
	proxyVariant := Rule{DomainSuffix: []string{"keenetic.com", "netcraze.net"}, Outbound: "old-proxy"}

	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{direct, proxy, direct, proxyVariant, proxy}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	parsed, err := parseRouterConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(parsed.Route.Rules), 3; got != want {
		t.Fatalf("len(rules) = %d, want %d: %#v", got, want, parsed.Route.Rules)
	}
	if !ruleEqualJSON(parsed.Route.Rules[0], direct) ||
		!ruleEqualJSON(parsed.Route.Rules[1], proxy) ||
		!ruleEqualJSON(parsed.Route.Rules[2], proxyVariant) {
		t.Fatalf("first occurrence order was not preserved: %#v", parsed.Route.Rules)
	}
}

func ruleEqualJSON(a, b Rule) bool {
	aRaw, _ := json.Marshal(a)
	bRaw, _ := json.Marshal(b)
	return string(aRaw) == string(bRaw)
}
