package mcp_test

import (
	"fmt"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/mcp/mcptest"
)

// bigDNSList returns a domain list with n generated domains plus the
// fields list_dns_routes never shows (excludes, subscriptions).
func bigDNSList(id string, n int) mcpsrv.DNSRouteDetail {
	domains := make([]string, 0, n)
	for i := 0; i < n; i++ {
		domains = append(domains, fmt.Sprintf("host%03d.example.com", i))
	}
	return mcpsrv.DNSRouteDetail{
		ID: id, Name: "Subscribed", Enabled: true,
		Domains: domains, ManualDomains: domains[:1],
		Excludes:      []string{"ads.example.com"},
		Subscriptions: []mcpsrv.DNSSubscription{{URL: "https://lists.example.com/a.txt", Name: "A", LastCount: n}},
		Routes:        []mcpsrv.RouteTarget{{TunnelID: "tn-1"}},
	}
}

// TestTools_GetDNSRouteReturnsWhatTheListTruncates — list_dns_routes caps
// Domains at MaxDomainsInOutput and drops excludes and subscriptions
// entirely, so an agent asked "is x.com in this list?" cannot answer from
// it. get_dns_route is the tool that can.
func TestTools_GetDNSRouteReturnsWhatTheListTruncates(t *testing.T) {
	fake := mcptest.New()
	fake.DNSRoutes = append(fake.DNSRoutes, bigDNSList("dl-big", 60))
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	// The list tool still truncates — that is the gap being filled.
	_, out := callTool(t, s, "list_dns_routes", nil)
	listed := out["routes"].([]any)[1].(map[string]any)
	if n := len(listed["domains"].([]any)); n != mcpsrv.MaxDomainsInOutput {
		t.Fatalf("list_dns_routes domains = %d, want the %d cap", n, mcpsrv.MaxDomainsInOutput)
	}

	res, out := callTool(t, s, "get_dns_route", map[string]any{"routeId": "dl-big"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	if n := len(out["domains"].([]any)); n != 60 {
		t.Fatalf("get_dns_route domains = %d, want all 60", n)
	}
	if out["domainCount"] != float64(60) {
		t.Fatalf("domainCount = %v, want 60", out["domainCount"])
	}
	if out["domainsTruncated"] != false {
		t.Fatalf("domainsTruncated = %v, want false — the whole list fits", out["domainsTruncated"])
	}
	if len(out["excludes"].([]any)) != 1 {
		t.Fatalf("excludes = %v, want the one exclude", out["excludes"])
	}
	if len(out["subscriptions"].([]any)) != 1 {
		t.Fatalf("subscriptions = %v, want the one subscription", out["subscriptions"])
	}
}

// TestTools_GetDNSRoutePagesHugeLists — a subscription list can hold tens
// of thousands of domains. The tool must page rather than flood the
// model's context, and must SAY it truncated: a silent cut would let an
// agent answer "no, that domain is not in the list" from a partial page.
func TestTools_GetDNSRoutePagesHugeLists(t *testing.T) {
	fake := mcptest.New()
	fake.DNSRoutes = append(fake.DNSRoutes, bigDNSList("dl-big", 500))
	s := connect(t, mcpsrv.NewServer(fake, "test"))

	res, out := callTool(t, s, "get_dns_route", map[string]any{"routeId": "dl-big"})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	page := out["domains"].([]any)
	if len(page) != mcpsrv.MaxDomainsInDetail {
		t.Fatalf("page = %d domains, want the %d cap", len(page), mcpsrv.MaxDomainsInDetail)
	}
	if out["domainsTruncated"] != true {
		t.Fatalf("domainsTruncated = %v, want true", out["domainsTruncated"])
	}
	if out["domainCount"] != float64(500) {
		t.Fatalf("domainCount = %v, want the full 500", out["domainCount"])
	}
	if page[0] != "host000.example.com" {
		t.Fatalf("first domain = %v", page[0])
	}

	_, out = callTool(t, s, "get_dns_route", map[string]any{"routeId": "dl-big", "domainsOffset": mcpsrv.MaxDomainsInDetail})
	page = out["domains"].([]any)
	if out["domainsOffset"] != float64(mcpsrv.MaxDomainsInDetail) {
		t.Fatalf("domainsOffset = %v, want it echoed back", out["domainsOffset"])
	}
	if page[0] != fmt.Sprintf("host%03d.example.com", mcpsrv.MaxDomainsInDetail) {
		t.Fatalf("second page starts at %v", page[0])
	}

	// An offset past the end is not an error; it is an empty page.
	_, out = callTool(t, s, "get_dns_route", map[string]any{"routeId": "dl-big", "domainsOffset": 9000})
	if n := len(out["domains"].([]any)); n != 0 {
		t.Fatalf("offset past the end returned %d domains", n)
	}
}

func TestTools_GetDNSRouteRejectsUnknownID(t *testing.T) {
	s, _ := newTestSession(t)
	if res, _ := callTool(t, s, "get_dns_route", map[string]any{"routeId": "nope"}); !res.IsError {
		t.Fatal("unknown routeId must be a tool error")
	}
	if res, _ := callTool(t, s, "get_dns_route", map[string]any{"routeId": ""}); !res.IsError {
		t.Fatal("empty routeId must be a tool error")
	}
}
