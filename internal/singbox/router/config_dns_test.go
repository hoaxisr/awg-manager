package router

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func makeDNSServer(tag, typ, server, detour string) DNSServer {
	return DNSServer{Tag: tag, Type: typ, Server: server, Detour: detour}
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
