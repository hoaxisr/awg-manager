package router

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeDNSServer(tag, typ, server, detour string) DNSServer {
	return DNSServer{Tag: tag, Type: typ, Server: server, Detour: detour}
}

// TestDNSServerLocalMarshalNoServerField проверяет, что type=local
// сериализуется без поля "server" — sing-box 1.13's `local` server не
// имеет этого поля в схеме и FATAL'ит весь конфиг с
// `unknown field "server"` на `"server": ""`. См. issue #180.
func TestDNSServerLocalMarshalNoServerField(t *testing.T) {
	srv := DNSServer{Tag: "dns-local", Type: "local"}
	raw, err := json.Marshal(srv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := got["server"]; has {
		t.Errorf("type=local marshalled with server field: %s", raw)
	}
	if got["tag"] != "dns-local" || got["type"] != "local" {
		t.Errorf("missing required fields: %s", raw)
	}
}

// TestDNSServerUDPMarshalIncludesServer проверяет, что для не-local
// типов поле "server" всё ещё сериализуется (включая edge-кейс пустого
// значения — там validator уже отверг бы конфиг до marshal'а, но
// поведение сериализатора должно быть симметричным).
func TestDNSServerUDPMarshalIncludesServer(t *testing.T) {
	srv := DNSServer{Tag: "dns-up", Type: "udp", Server: "1.1.1.1"}
	raw, err := json.Marshal(srv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["server"] != "1.1.1.1" {
		t.Errorf("expected server=1.1.1.1, got %v: %s", got["server"], raw)
	}
}

func TestDNSServerMarshalOmitsEmptyDetour(t *testing.T) {
	srv := DNSServer{Tag: "dns-up", Type: "udp", Server: "1.1.1.1", Detour: ""}
	raw, err := json.Marshal(srv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := got["detour"]; has {
		t.Errorf("empty detour must be omitted, got: %s", raw)
	}
}

func TestAddDNSServerNormalizesDirectDetour(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(makeDNSServer("bootstrap", "udp", "1.1.1.1", "direct")); err != nil {
		t.Fatal(err)
	}
	if c.DNS.Servers[0].Detour != "" {
		t.Fatalf("direct detour normalized away, got %q", c.DNS.Servers[0].Detour)
	}
}

func TestUpdateDNSServerStripsDetourOnDNSDirect(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("dns-direct", "udp", "77.88.8.8", ""))
	if err := c.UpdateDNSServer("dns-direct", makeDNSServer("dns-direct", "udp", "77.88.8.8", "wg-nl")); err != nil {
		t.Fatal(err)
	}
	if c.DNS.Servers[0].Detour != "" {
		t.Fatalf("dns-direct detour stripped, got %q", c.DNS.Servers[0].Detour)
	}
}

func TestLoadConfigPreservesLegacyDNSDirectDetour(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20-router.json")
	raw := []byte(`{
		"dns": {
			"servers": [
				{"tag":"dns-direct","type":"udp","server":"77.88.8.8","detour":"wg-nl"}
			]
		}
	}`)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNS.Servers[0].Detour != "wg-nl" {
		t.Fatalf("legacy dns-direct detour preserved in store, got %q", cfg.DNS.Servers[0].Detour)
	}
}

func TestSanitizeDNSConfigForSingboxStripsDNSDirectDetour(t *testing.T) {
	cfg := &RouterConfig{
		DNS: DNS{
			Servers: []DNSServer{
				{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8", Detour: "wg-nl"},
				{Tag: "dns-tunnel", Type: "udp", Server: "9.9.9.9", Detour: "wg-nl"},
			},
		},
	}
	SanitizeDNSConfigForSingbox(cfg)
	if cfg.DNS.Servers[0].Detour != "" {
		t.Fatalf("dns-direct detour stripped for sing-box, got %q", cfg.DNS.Servers[0].Detour)
	}
	if cfg.DNS.Servers[1].Detour != "wg-nl" {
		t.Fatalf("dns-tunnel detour kept, got %q", cfg.DNS.Servers[1].Detour)
	}
}

func TestAddDNSServerValidates(t *testing.T) {
	c := NewEmptyConfig()

	if err := c.AddDNSServer(DNSServer{Type: "udp", Server: "1.1.1.1"}); err == nil {
		t.Error("expected error for empty tag")
	}
	if err := c.AddDNSServer(DNSServer{Tag: "x", Type: "smtp", Server: "1.1.1.1"}); err == nil {
		t.Error("expected error for unknown type")
	}
	if err := c.AddDNSServer(DNSServer{Tag: "x", Type: "udp"}); err == nil {
		t.Error("expected error for empty server")
	}

	if err := c.AddDNSServer(makeDNSServer("bootstrap", "udp", "1.1.1.1", "direct")); err != nil {
		t.Fatal(err)
	}
	if err := c.AddDNSServer(makeDNSServer("bootstrap", "udp", "8.8.8.8", "direct")); !errors.Is(err, ErrDNSServerTagConflict) {
		t.Errorf("expected tag conflict, got %v", err)
	}
}

func TestAddDNSServerWithDomainResolver(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(makeDNSServer("bootstrap", "udp", "1.1.1.1", "")); err != nil {
		t.Fatal(err)
	}

	doh := DNSServer{
		Tag: "doh", Type: "https", Server: "cloudflare-dns.com",
		DomainResolver: &DomainResolver{Server: "nonexistent"},
	}
	if err := c.AddDNSServer(doh); !errors.Is(err, ErrDNSServerNotFound) {
		t.Errorf("expected not-found for unknown resolver, got %v", err)
	}
	doh.DomainResolver.Server = "bootstrap"
	if err := c.AddDNSServer(doh); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateDNSServerRenamesReferences(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("bootstrap", "udp", "1.1.1.1", ""))
	_ = c.AddDNSServer(DNSServer{
		Tag: "doh", Type: "https", Server: "cloudflare-dns.com",
		DomainResolver: &DomainResolver{Server: "bootstrap"},
	})
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}, Server: "bootstrap"})
	_ = c.SetDNSGlobals("bootstrap", "prefer_ipv4")

	if err := c.UpdateDNSServer("bootstrap", makeDNSServer("boot", "udp", "9.9.9.9", "")); err != nil {
		t.Fatal(err)
	}
	if c.DNS.Rules[0].Server != "boot" {
		t.Errorf("rule server: %q", c.DNS.Rules[0].Server)
	}
	if c.DNS.Servers[1].DomainResolver.Server != "boot" {
		t.Errorf("resolver: %q", c.DNS.Servers[1].DomainResolver.Server)
	}
	if c.DNS.Final != "boot" {
		t.Errorf("final: %q", c.DNS.Final)
	}
}

func TestDeleteDNSServerBlocksWhenReferenced(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("a", "udp", "1.1.1.1", ""))
	_ = c.AddDNSServer(makeDNSServer("b", "udp", "8.8.8.8", ""))
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}, Server: "a"})
	_ = c.SetDNSGlobals("a", "")

	if err := c.DeleteDNSServer("a", false); !errors.Is(err, ErrDNSServerReferenced) {
		t.Errorf("expected referenced error, got %v", err)
	}
	if err := c.DeleteDNSServer("a", true); err != nil {
		t.Fatal(err)
	}
	if len(c.DNS.Rules) != 0 {
		t.Errorf("rules should be cascaded on force delete: %+v", c.DNS.Rules)
	}
	if c.DNS.Final != "" {
		t.Errorf("final should be cleared: %q", c.DNS.Final)
	}
}

func TestAddDNSRuleValidates(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("s", "udp", "1.1.1.1", ""))

	if err := c.AddDNSRule(DNSRule{Server: "s"}); !errors.Is(err, ErrInvalidMatchers) {
		t.Errorf("expected invalid matchers, got %v", err)
	}
	if err := c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}}); err == nil {
		t.Error("expected error for missing server")
	}
	if err := c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}, Server: "missing"}); !errors.Is(err, ErrDNSInvalidServer) {
		t.Errorf("expected invalid server, got %v", err)
	}
	if err := c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}, Action: "reject"}); err != nil {
		t.Errorf("reject without server should be ok: %v", err)
	}
	if err := c.AddDNSRule(DNSRule{DomainSuffix: []string{".com"}, Server: "s"}); err != nil {
		t.Fatal(err)
	}
}

func TestAddDNSRuleValidatesSourceIPCIDR(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(DNSServer{Tag: "fakeip", Type: "fakeip", Inet4Range: "10.128.0.0/10"}); err != nil {
		t.Fatal(err)
	}

	if err := c.AddDNSRule(DNSRule{SourceIPCIDR: []string{"not-a-cidr"}, QueryType: []string{"A"}, Action: "route", Server: "fakeip"}); err == nil {
		t.Error("expected error for malformed source_ip_cidr")
	}
	if err := c.AddDNSRule(DNSRule{SourceIPCIDR: []string{"192.168.1.0/24"}, QueryType: []string{"A"}, Action: "route", Server: "fakeip"}); err != nil {
		t.Errorf("valid CIDR should be accepted: %v", err)
	}
	if err := c.AddDNSRule(DNSRule{SourceIPCIDR: []string{"10.0.0.5"}, QueryType: []string{"A"}, Action: "route", Server: "fakeip"}); err != nil {
		t.Errorf("bare IP should be accepted: %v", err)
	}
}

func TestAddDNSServerValidatesFakeIPRanges(t *testing.T) {
	c := NewEmptyConfig()

	if err := c.AddDNSServer(DNSServer{Tag: "f1", Type: "fakeip", Inet4Range: "garbage"}); err == nil {
		t.Error("expected error for malformed inet4_range")
	}
	if err := c.AddDNSServer(DNSServer{Tag: "f2", Type: "fakeip", Inet4Range: "3f80::/10"}); err == nil {
		t.Error("expected error for v6 prefix in inet4_range")
	}
	if err := c.AddDNSServer(DNSServer{Tag: "f3", Type: "fakeip", Inet4Range: "10.128.0.0/10", Inet6Range: "10.0.0.0/24"}); err == nil {
		t.Error("expected error for v4 prefix in inet6_range")
	}
	if err := c.AddDNSServer(DNSServer{Tag: "f4", Type: "fakeip", Inet4Range: "10.128.0.0/10", Inet6Range: "3f80::/10"}); err != nil {
		t.Errorf("valid v4+v6 ranges should be accepted: %v", err)
	}
}

func TestMoveDNSRule(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("s", "udp", "1.1.1.1", ""))
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".a"}, Server: "s"})
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".b"}, Server: "s"})
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".c"}, Server: "s"})

	if err := c.MoveDNSRule(2, 0); err != nil {
		t.Fatal(err)
	}
	if c.DNS.Rules[0].DomainSuffix[0] != ".c" {
		t.Errorf("order: %+v", c.DNS.Rules)
	}
}

func TestMoveDNSServer(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("a", "udp", "1.1.1.1", ""))
	_ = c.AddDNSServer(makeDNSServer("b", "udp", "8.8.8.8", ""))
	_ = c.AddDNSServer(makeDNSServer("c", "udp", "9.9.9.9", ""))

	if err := c.MoveDNSServer(2, 0); err != nil {
		t.Fatal(err)
	}
	if c.DNS.Servers[0].Tag != "c" {
		t.Errorf("order: %+v", c.DNS.Servers)
	}

	if err := c.MoveDNSServer(0, 5); !errors.Is(err, ErrDNSServerIndexOutOfRange) {
		t.Errorf("expected out-of-range error, got %v", err)
	}
}

func TestDNSRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "20-router.json")
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("bootstrap", "udp", "1.1.1.1", ""))
	_ = c.AddDNSServer(DNSServer{
		Tag: "vpn", Type: "https", Server: "cloudflare-dns.com",
		Detour:         "awg10",
		DomainResolver: &DomainResolver{Server: "bootstrap", Strategy: "ipv4_only"},
	})
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}, Server: "bootstrap"})
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".com"}, Server: "vpn"})
	_ = c.SetDNSGlobals("vpn", "ipv4_only")

	if err := SaveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.DNS.Servers) != 2 {
		t.Errorf("servers: %+v", loaded.DNS.Servers)
	}
	if loaded.DNS.Final != "vpn" || loaded.DNS.Strategy != "ipv4_only" {
		t.Errorf("globals: %+v", loaded.DNS)
	}
	if loaded.DNS.Servers[1].DomainResolver == nil ||
		loaded.DNS.Servers[1].DomainResolver.Server != "bootstrap" {
		t.Errorf("resolver: %+v", loaded.DNS.Servers[1])
	}
	raw, _ := json.MarshalIndent(loaded, "", "  ")
	if !json.Valid(raw) {
		t.Error("not valid JSON")
	}
}

func TestAddDNSServerLocal(t *testing.T) {
	c := NewEmptyConfig()

	// local без server/port — валиден
	if err := c.AddDNSServer(DNSServer{Tag: "sys", Type: "local"}); err != nil {
		t.Fatalf("local server should be valid: %v", err)
	}
	// udp без server — по-прежнему ошибка
	if err := c.AddDNSServer(DNSServer{Tag: "u", Type: "udp"}); err == nil {
		t.Error("udp without server must fail")
	}
	// неизвестный тип — ошибка
	if err := c.AddDNSServer(DNSServer{Tag: "x", Type: "bogus", Server: "1.1.1.1"}); err == nil {
		t.Error("unknown type must fail")
	}
}

func TestSetDNSGlobalsRejectsUnknownServer(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("s", "udp", "1.1.1.1", ""))
	if err := c.SetDNSGlobals("nope", ""); !errors.Is(err, ErrDNSServerNotFound) {
		t.Errorf("expected not found, got %v", err)
	}
	if err := c.SetDNSGlobals("s", "ipv9"); err == nil {
		t.Error("expected strategy error")
	}
	if err := c.SetDNSGlobals("", "prefer_ipv4"); err != nil {
		t.Errorf("empty final should be allowed: %v", err)
	}
}

func TestDNSRuleRegexAndBlock(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("up", "udp", "1.1.1.1", ""))

	// domain_regex как валидный матчер + route
	if err := c.AddDNSRule(DNSRule{DomainRegex: []string{`\.ru$`}, Server: "up", Action: "route"}); err != nil {
		t.Fatalf("regex route: %v", err)
	}
	// блок через predefined NXDOMAIN — server не нужен
	if err := c.AddDNSRule(DNSRule{DomainSuffix: []string{"doubleclick.net"}, Action: "predefined", Rcode: "NXDOMAIN"}); err != nil {
		t.Fatalf("predefined block: %v", err)
	}
	// reject с методом drop
	if err := c.AddDNSRule(DNSRule{DomainKeyword: []string{"ads"}, Action: "reject", RejectMethod: "drop"}); err != nil {
		t.Fatalf("reject drop: %v", err)
	}
	// predefined с неизвестным rcode — ошибка
	if err := c.AddDNSRule(DNSRule{Domain: []string{"x"}, Action: "predefined", Rcode: "BOGUS"}); err == nil {
		t.Error("bad rcode must fail")
	}
	// reject с неизвестным методом — ошибка
	if err := c.AddDNSRule(DNSRule{Domain: []string{"x"}, Action: "reject", RejectMethod: "bogus"}); err == nil {
		t.Error("bad reject method must fail")
	}
	// невалидный domain_regex — ошибка
	if err := c.AddDNSRule(DNSRule{DomainRegex: []string{"("}, Server: "up", Action: "route"}); err == nil {
		t.Error("invalid regex must fail")
	}
}

func TestAddDNSServer_FakeIP(t *testing.T) {
	c := NewEmptyConfig()
	err := c.AddDNSServer(DNSServer{Tag: "fakeip", Type: "fakeip", Inet4Range: "10.128.0.0/10", Inet6Range: "3f80::/10"})
	if err != nil {
		t.Fatalf("add fakeip: %v", err)
	}
	b, _ := json.Marshal(c.DNS.Servers[0])
	for _, want := range []string{`"type":"fakeip"`, `"inet4_range":"10.128.0.0/10"`, `"inet6_range":"3f80::/10"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s: %s", want, b)
		}
	}
}

func TestValidateDNSServer_FakeIPRequiresRange(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(DNSServer{Tag: "fakeip", Type: "fakeip"}); err == nil {
		t.Error("expected error: fakeip requires inet4_range")
	}
}

func TestAddDNSRule_SourceIPCIDRToFakeip(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(DNSServer{Tag: "fakeip", Type: "fakeip", Inet4Range: "10.128.0.0/10"})
	err := c.AddDNSRule(DNSRule{SourceIPCIDR: []string{"192.168.1.0/24"}, QueryType: []string{"A", "AAAA"}, Action: "route", Server: "fakeip"})
	if err != nil {
		t.Fatalf("add rule: %v", err)
	}
	b, _ := json.Marshal(c.DNS.Rules[0])
	if !strings.Contains(string(b), `"source_ip_cidr":["192.168.1.0/24"]`) {
		t.Errorf("missing source_ip_cidr: %s", b)
	}
}
