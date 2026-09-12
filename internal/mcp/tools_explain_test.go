package mcp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// resolvingFake answers DNS from a table instead of the network.
type resolvingFake struct {
	*mcptest.Fake
	table map[string][]string
	err   error
}

func (f resolvingFake) ResolveDomain(_ context.Context, domain string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.table[domain], nil
}

func explainFake(t *testing.T) (resolvingFake, *mcptest.Fake) {
	t.Helper()
	fake := mcptest.New()
	return resolvingFake{Fake: fake, table: map[string][]string{
		"www.youtube.com": {"142.250.1.1"},
		"lab.corp.local":  {"10.20.5.7"},
	}}, fake
}

// TestTools_ExplainRouteFindsTheDomainList — «почему ютуб идёт не туда»
// — самый частый вопрос к маршрутизации, и до сих пор агенту пришлось бы
// вручную сверять списки, каждый из которых list_dns_routes к тому же
// обрезает.
func TestTools_ExplainRouteFindsTheDomainList(t *testing.T) {
	deps, _ := explainFake(t)
	s := connectDeps(t, deps)

	res, out := callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	matches := out["dnsMatches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("dnsMatches = %v, want the Video list", matches)
	}
	m := matches[0].(map[string]any)
	if m["routeId"] != "dl-1" || m["tunnelId"] != "tn-1" || m["matchedEntry"] != "youtube.com" {
		t.Fatalf("match = %v — a subdomain must match its parent entry", m)
	}
	if m["tunnelName"] != "Amsterdam" {
		t.Fatalf("the tunnel must be named, not just numbered: %v", m)
	}
	if out["defaultRouteTunnelId"] != "tn-1" {
		t.Fatalf("defaultRouteTunnelId = %v", out["defaultRouteTunnelId"])
	}
	if ips := out["resolvedIps"].([]any); len(ips) != 1 || ips[0] != "142.250.1.1" {
		t.Fatalf("resolvedIps = %v", ips)
	}
}

// TestTools_ExplainRouteMatchesAgainstTheWholeList — list_dns_routes
// отдаёт лишь первые 50 доменов. Сопоставление по обрезанному списку
// ответило бы «нет такого правила» на живое правило.
func TestTools_ExplainRouteMatchesAgainstTheWholeList(t *testing.T) {
	deps, fake := explainFake(t)
	big := bigDNSList("dl-big", 300)
	big.Domains[299] = "late.example.com"
	fake.DNSRoutes = append(fake.DNSRoutes, big)
	deps.table["late.example.com"] = []string{"203.0.113.9"}
	s := connectDeps(t, deps)

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "late.example.com"})
	matches := out["dnsMatches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("a domain past the list cap must still match, got %v", matches)
	}
	if matches[0].(map[string]any)["routeId"] != "dl-big" {
		t.Fatalf("match = %v", matches[0])
	}
}

// TestTools_ExplainRouteReportsWhatItCannotEvaluate — geosite:/geoip:
// раскрываются на роутере, а не здесь. Промолчать о таком списке значит
// сказать «правил нет» там, где они могут быть.
func TestTools_ExplainRouteReportsWhatItCannotEvaluate(t *testing.T) {
	deps, fake := explainFake(t)
	fake.DNSRoutes = append(fake.DNSRoutes, mcpsrv.DNSRouteDetail{
		ID: "dl-geo", Name: "Geo", Enabled: true,
		Domains: []string{"geosite:google"}, ManualDomains: []string{"geosite:google"},
		Routes: []mcpsrv.RouteTarget{{TunnelID: "tn-2"}},
	})
	s := connectDeps(t, deps)

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"})
	un := out["unevaluatedLists"].([]any)
	if len(un) != 1 {
		t.Fatalf("unevaluatedLists = %v, want the geosite list named", un)
	}
	if u := un[0].(map[string]any); u["routeId"] != "dl-geo" || u["reason"] == "" {
		t.Fatalf("entry = %v, want a routeId and a reason", u)
	}
}

// TestTools_ExplainRouteCoversSubnetsAndDevices — статический список
// сверяется по разрешённым адресам, а маршрут устройства перекрывает весь
// его трафик, независимо от адресата.
func TestTools_ExplainRouteCoversSubnetsAndDevices(t *testing.T) {
	deps, _ := explainFake(t)
	s := connectDeps(t, deps)
	if res, _ := callTool(t, s, "set_client_route", map[string]any{"clientIp": "192.168.1.20", "tunnelId": "tn-2"}); res.IsError {
		t.Fatal("setup")
	}

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "lab.corp.local", "clientIp": "192.168.1.20"})
	static := out["staticMatches"].([]any)
	if len(static) != 1 {
		t.Fatalf("staticMatches = %v, want the 10.20.0.0/16 list", static)
	}
	sm := static[0].(map[string]any)
	if sm["routeId"] != "sr-1" || sm["matchedEntry"] != "10.20.0.0/16" || sm["matchedIp"] != "10.20.5.7" {
		t.Fatalf("static match = %v", sm)
	}
	cr, _ := out["clientRoute"].(map[string]any)
	if cr == nil || cr["tunnelId"] != "tn-2" {
		t.Fatalf("clientRoute = %v", out["clientRoute"])
	}
	if out["note"] == "" {
		t.Fatal("a device route and a list both matched; the tool must say how they relate")
	}
}

// TestTools_ExplainRouteWithALiteralIP — цель может быть адресом: тогда
// резолвить нечего, а доменные записи сверять не с чем.
func TestTools_ExplainRouteWithALiteralIP(t *testing.T) {
	deps, _ := explainFake(t)
	s := connectDeps(t, deps)

	res, out := callTool(t, s, "explain_route", map[string]any{"target": "10.20.5.7"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if len(out["staticMatches"].([]any)) != 1 {
		t.Fatalf("staticMatches = %v", out["staticMatches"])
	}
	if n := len(out["dnsMatches"].([]any)); n != 0 {
		t.Fatalf("dnsMatches = %d, want none for a literal IP", n)
	}
	if err, ok := out["resolveError"]; ok {
		t.Fatalf("resolveError = %v, want none — nothing needed resolving", err)
	}
}

// TestTools_ExplainRouteSurvivesAFailedLookup — без резолва подсети
// сверить нельзя, но доменные списки — можно. Ошибка резолва не должна
// топить весь ответ.
func TestTools_ExplainRouteSurvivesAFailedLookup(t *testing.T) {
	deps, _ := explainFake(t)
	deps.err = fmt.Errorf("no such host")
	s := connectDeps(t, deps)

	res, out := callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"})
	if res.IsError {
		t.Fatalf("a failed lookup must not fail the whole call: %s", toolText(res))
	}
	if out["resolveError"] == nil || out["resolveError"] == "" {
		t.Fatal("the failure must be reported, not hidden")
	}
	if len(out["dnsMatches"].([]any)) != 1 {
		t.Fatalf("domain matching does not need DNS: %v", out["dnsMatches"])
	}
}

func TestTools_ExplainRouteRejectsBadInput(t *testing.T) {
	deps, _ := explainFake(t)
	s := connectDeps(t, deps)

	if res, _ := callTool(t, s, "explain_route", map[string]any{"target": "  "}); !res.IsError {
		t.Error("a blank target must be a tool error")
	}
	if res, _ := callTool(t, s, "explain_route", map[string]any{"target": "youtube.com", "clientIp": "999.1.1.1"}); !res.IsError {
		t.Error("an invalid clientIp must be a tool error")
	}
}

// TestTools_ExplainRouteMentionsSingboxRules — на установке, где
// маршрутизацией занимается sing-box, разбор по спискам NDMS — это
// половина ответа. Промолчать о второй половине значит уверенно назвать
// не тот туннель.
func TestTools_ExplainRouteMentionsSingboxRules(t *testing.T) {
	deps, _ := explainFake(t)
	s := connectDeps(t, deps)

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"})
	note, _ := out["note"].(string)
	if !strings.Contains(strings.ToLower(note), "sing-box") {
		t.Fatalf("note = %q, want it to point at the sing-box rules as well", note)
	}
	if !strings.Contains(note, "list_singbox_rules") {
		t.Fatalf("note = %q, want it to name the tool that shows them", note)
	}
}

// TestTools_ExplainRouteHonoursExcludes — ревью нашло: домен, явно
// вырезанный из списка через excludes, показывался как идущий через
// туннель этого списка. Роутер его так не маршрутизирует.
func TestTools_ExplainRouteHonoursExcludes(t *testing.T) {
	deps, fake := explainFake(t)
	fake.DNSRoutes = append(fake.DNSRoutes, mcpsrv.DNSRouteDetail{
		ID: "dl-google", Name: "Google", Enabled: true,
		Domains: []string{"google.com"}, ManualDomains: []string{"google.com"},
		Excludes: []string{"mail.google.com"},
		Routes:   []mcpsrv.RouteTarget{{TunnelID: "tn-1"}},
	})
	deps.table["mail.google.com"] = []string{"142.250.9.9"}
	s := connectDeps(t, deps)

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "mail.google.com"})
	for _, m := range out["dnsMatches"].([]any) {
		if m.(map[string]any)["routeId"] == "dl-google" {
			t.Fatalf("an excluded domain must not count as covered: %v", m)
		}
	}
	excluded, _ := out["excludedFrom"].([]any)
	if len(excluded) != 1 {
		t.Fatalf("excludedFrom = %v, want the exclusion reported, not silently dropped", out["excludedFrom"])
	}
	e := excluded[0].(map[string]any)
	if e["routeId"] != "dl-google" || e["excludedBy"] != "mail.google.com" {
		t.Fatalf("exclusion = %v", e)
	}

	// A sibling that is not excluded still matches.
	_, out = callTool(t, s, "explain_route", map[string]any{"target": "www.google.com"})
	if n := len(out["dnsMatches"].([]any)); n != 1 {
		t.Fatalf("dnsMatches = %v", out["dnsMatches"])
	}
}

// TestTools_ExplainRouteHonoursExcludeSubnets — ревью нашло: исключения
// сверялись только по имени. CIDR в excludes не срабатывал никогда (адреса
// в сверку не передавались), excludeSubnets не читались вовсе, а для
// запроса по литеральному адресу проверка была выключена целиком. Адрес,
// вычеркнутый из списка подсетью, показывался как маршрутизируемый.
func TestTools_ExplainRouteHonoursExcludeSubnets(t *testing.T) {
	deps, fake := explainFake(t)
	fake.DNSRoutes = []mcpsrv.DNSRouteDetail{
		{
			// A CIDR among the plain excludes: the service stores it there
			// until it splits it out, so both spellings must work.
			ID: "dl-cidr-in-excludes", Name: "Corp", Enabled: true,
			Domains: []string{"corp.local"}, ManualDomains: []string{"corp.local"},
			Excludes: []string{"10.20.0.0/16"},
			Routes:   []mcpsrv.RouteTarget{{TunnelID: "tn-1"}},
		},
		{
			ID: "dl-exclude-subnets", Name: "Lab", Enabled: true,
			Domains: []string{"corp.local"}, ManualDomains: []string{"corp.local"},
			ExcludeSubnets: []string{"10.20.5.0/24"},
			Routes:         []mcpsrv.RouteTarget{{TunnelID: "tn-2"}},
		},
		{
			// Matched by subnet, excluded by subnet: the only way a literal
			// address can be carved out, and the case the old guard skipped.
			ID: "dl-ip-only", Name: "Range", Enabled: true,
			Subnets:        []string{"10.20.0.0/16"},
			ExcludeSubnets: []string{"10.20.5.7/32"},
			Routes:         []mcpsrv.RouteTarget{{TunnelID: "tn-1"}},
		},
	}
	s := connectDeps(t, deps)

	for _, target := range []string{"lab.corp.local", "10.20.5.7"} {
		_, out := callTool(t, s, "explain_route", map[string]any{"target": target})
		if n := len(out["dnsMatches"].([]any)); n != 0 {
			t.Fatalf("%s: dnsMatches = %v, want none — every list excludes this address", target, out["dnsMatches"])
		}
		excluded, _ := out["excludedFrom"].([]any)
		byList := map[string]string{}
		for _, e := range excluded {
			m := e.(map[string]any)
			byList[m["routeId"].(string)] = m["excludedBy"].(string)
		}
		if target == "lab.corp.local" {
			if byList["dl-cidr-in-excludes"] != "10.20.0.0/16" || byList["dl-exclude-subnets"] != "10.20.5.0/24" {
				t.Fatalf("%s: excludedFrom = %v, want both domain lists carved out by their subnets", target, excluded)
			}
		} else if byList["dl-ip-only"] != "10.20.5.7/32" {
			t.Fatalf("%s: excludedFrom = %v, want the subnet list carved out by excludeSubnets", target, excluded)
		}
	}

	// A sibling address outside the excluded ranges still matches.
	_, out := callTool(t, s, "explain_route", map[string]any{"target": "10.20.9.9"})
	if n := len(out["dnsMatches"].([]any)); n != 1 {
		t.Fatalf("dnsMatches = %v, want the Range list", out["dnsMatches"])
	}
}

// TestTools_ExplainRouteFlagsGeoIPSubnets — ревью нашло: теги geoip:
// хранятся в subnets списка, а сверка подсетей молча пропускала всё, что
// не CIDR. Список из одних geoip-тегов не попадал ни в совпадения, ни в
// непроверенные, и ответ был «маршрута нет».
func TestTools_ExplainRouteFlagsGeoIPSubnets(t *testing.T) {
	deps, fake := explainFake(t)
	fake.DNSRoutes = []mcpsrv.DNSRouteDetail{{
		ID: "dl-ru", Name: "RU", Enabled: true,
		Subnets: []string{"geoip:ru"},
		Routes:  []mcpsrv.RouteTarget{{TunnelID: "tn-2"}},
	}}
	s := connectDeps(t, deps)

	_, out := callTool(t, s, "explain_route", map[string]any{"target": "10.20.5.7"})
	un, _ := out["unevaluatedLists"].([]any)
	if len(un) != 1 || un[0].(map[string]any)["routeId"] != "dl-ru" {
		t.Fatalf("a geoip-only list must be reported as unevaluated, got %v", out["unevaluatedLists"])
	}
	if note, _ := out["note"].(string); strings.Contains(note, "no routing list covers") {
		t.Fatalf("the note must not claim nothing matches: %q", note)
	}
}

// TestTools_ExplainRouteReadsListsOnce — ревью нашло: инструмент дёргал
// GetDNSRoute на каждый список, а для HydraRoute-id это перечитывание
// конфигов HR под замком на каждый вызов — O(N²) от валидного ключа.
// Полные записи читаются одним вызовом.
func TestTools_ExplainRouteReadsListsOnce(t *testing.T) {
	deps, fake := explainFake(t)
	counting := &countingDeps{resolvingFake: deps}
	fake.DNSRoutes = append(fake.DNSRoutes, bigDNSList("dl-a", 3), bigDNSList("dl-b", 3))
	s := connectDeps(t, counting)

	if res, _ := callTool(t, s, "explain_route", map[string]any{"target": "www.youtube.com"}); res.IsError {
		t.Fatal(toolText(res))
	}
	if counting.getCalls != 0 {
		t.Fatalf("GetDNSRoute was called %d times; explain_route must read all lists in one call", counting.getCalls)
	}
	if counting.listDetailCalls != 1 {
		t.Fatalf("ListDNSRouteDetails called %d times, want 1", counting.listDetailCalls)
	}
}

type countingDeps struct {
	resolvingFake
	getCalls        int
	listDetailCalls int
}

func (c *countingDeps) GetDNSRoute(ctx context.Context, id string) (mcpsrv.DNSRouteDetail, error) {
	c.getCalls++
	return c.resolvingFake.GetDNSRoute(ctx, id)
}

func (c *countingDeps) ListDNSRouteDetails(ctx context.Context) ([]mcpsrv.DNSRouteDetail, error) {
	c.listDetailCalls++
	return c.resolvingFake.ListDNSRouteDetails(ctx)
}

// TestTools_ExplainRouteCapsTheTarget — цель уходит в резолвер роутера.
// Имя длиннее допустимого для DNS нечего резолвить.
func TestTools_ExplainRouteCapsTheTarget(t *testing.T) {
	deps, _ := explainFake(t)
	s := connectDeps(t, deps)
	long := strings.Repeat("a", 254) + ".example"
	if res, _ := callTool(t, s, "explain_route", map[string]any{"target": long}); !res.IsError {
		t.Fatal("a target longer than a DNS name must be refused before resolving")
	}
}
