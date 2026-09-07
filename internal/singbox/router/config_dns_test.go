package router

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
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

func TestDNSServerTLSMarshalAndValidation(t *testing.T) {
	srv := DNSServer{
		Tag: "dot", Type: "tls", Server: "dns.example",
		TLS: &DNSClientTLSOptions{ServerName: "dns.example", Insecure: true, ALPN: []string{"dot"}, MinVersion: "1.2", MaxVersion: "1.3", CertificatePublicKeySHA256: []string{"pin"}},
	}
	raw, err := json.Marshal(srv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"tls":{"server_name":"dns.example","insecure":true,"alpn":["dot"],"min_version":"1.2","max_version":"1.3","certificate_public_key_sha256":["pin"]}`) {
		t.Fatalf("tls was not retained: %s", raw)
	}
	if err := validateDNSServer(srv); err != nil {
		t.Fatalf("valid tls rejected: %v", err)
	}
	srv.TLS.MinVersion, srv.TLS.MaxVersion = "1.3", "1.2"
	if err := validateDNSServer(srv); err == nil {
		t.Fatal("invalid tls version range accepted")
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

func TestAddDNSServerWithDetourClearsDomainResolver(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(DNSServer{
		Tag: "dot", Type: "tls", Server: "dns.example", Detour: "tunnel",
		DomainResolver: &DomainResolver{Server: "bootstrap"},
	}); err != nil {
		t.Fatal(err)
	}
	if c.DNS.Servers[0].DomainResolver != nil {
		t.Fatalf("domain_resolver retained with detour: %#v", c.DNS.Servers[0])
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

// TestUpdateDNSServerFakeIPWithEvaluate: инвариант «evaluate не может
// использовать fakeip-сервер» обходился сменой ТИПА живого сервера — правила не
// трогали, а сервер становился fakeip.
func TestUpdateDNSServerFakeIPWithEvaluate(t *testing.T) {
	c := NewEmptyConfig()
	if err := c.AddDNSServer(makeDNSServer("resolver", "udp", "1.1.1.1", "")); err != nil {
		t.Fatal(err)
	}
	if err := c.AddDNSServer(makeDNSServer("plain", "udp", "8.8.8.8", "")); err != nil {
		t.Fatal(err)
	}
	if err := c.AddDNSRule(DNSRule{Domain: []string{"x.com"}, Action: "evaluate", Server: "resolver"}); err != nil {
		t.Fatal(err)
	}

	err := c.UpdateDNSServer("resolver", DNSServer{Tag: "resolver", Type: "fakeip", Inet4Range: "198.18.0.0/15"})
	if err == nil {
		t.Fatal("ожидалась ошибка: на сервер ссылается evaluate-правило")
	}
	if c.DNS.Servers[0].Type != "udp" {
		t.Fatalf("сервер изменён при ошибке: %#v", c.DNS.Servers[0])
	}

	// Переименование в том же вызове: правила ещё ссылаются на старый tag.
	if err := c.UpdateDNSServer("resolver", DNSServer{Tag: "fake", Type: "fakeip", Inet4Range: "198.18.0.0/15"}); err == nil {
		t.Fatal("ожидалась ошибка при смене типа+тега под evaluate")
	}

	// Контроль: сервер без evaluate-ссылок меняет тип свободно.
	if err := c.UpdateDNSServer("plain", DNSServer{Tag: "plain", Type: "fakeip", Inet4Range: "198.18.0.0/15"}); err != nil {
		t.Fatalf("смена типа без evaluate-правил: %v", err)
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
	_ = c.SetDNSGlobals("bootstrap", "prefer_ipv4", "")

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
	_ = c.SetDNSGlobals("a", "", "")

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

func TestDeleteDNSServerForceCascadesChain(t *testing.T) {
	mk := func(rules ...DNSRule) *RouterConfig {
		c := NewEmptyConfig()
		_ = c.AddDNSServer(makeDNSServer("x", "udp", "1.1.1.1", ""))
		_ = c.AddDNSServer(makeDNSServer("y", "udp", "8.8.8.8", ""))
		c.DNS.Rules = rules
		return c
	}
	cases := []struct {
		name  string
		rules []DNSRule
		want  int
	}{
		{
			"тегированный evaluate уносит своего match_response",
			[]DNSRule{
				{Action: "evaluate", Server: "x", Tag: "rd"},
				{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "rd"}, ResponseRcode: "NOERROR", Action: "respond"},
			},
			0,
		},
		{
			"анонимный evaluate уносит bare respond",
			[]DNSRule{
				{Action: "evaluate", Server: "x"},
				{Domain: []string{"a.com"}, Action: "respond"},
			},
			0,
		},
		{
			"каскад транзитивен",
			[]DNSRule{
				{Action: "evaluate", Server: "x", Tag: "a"},
				{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "a"}, Action: "evaluate", Server: "y", Tag: "b"},
				{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "b"}, ResponseRcode: "NOERROR", Action: "respond"},
			},
			0,
		},
		{
			"независимые правила выживают",
			[]DNSRule{
				{DomainSuffix: []string{".ru"}, Server: "y"},
				{Action: "evaluate", Server: "x", Tag: "rd"},
				{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "rd"}, ResponseRcode: "NOERROR", Action: "respond"},
			},
			1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mk(tc.rules...)
			if err := c.DeleteDNSServer("x", true); err != nil {
				t.Fatalf("force delete: %v", err)
			}
			if len(c.DNS.Rules) != tc.want {
				t.Fatalf("rules=%d, want %d: %+v", len(c.DNS.Rules), tc.want, c.DNS.Rules)
			}
			if err := validateDNSChain(c.DNS.Rules); err != nil {
				t.Fatalf("цепочка после force-delete должна быть валидной: %v", err)
			}
		})
	}
}

func TestAddDNSRuleValidates(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("s", "udp", "1.1.1.1", ""))

	// Matcher-less + server is a valid catch-all (#445 phase 3): accepted.
	if err := c.AddDNSRule(DNSRule{Server: "s"}); err != nil {
		t.Errorf("matcher-less catch-all with server should be accepted, got %v", err)
	}
	// Bare rule: no matcher, no server, no action → still invalid.
	if err := c.AddDNSRule(DNSRule{}); !errors.Is(err, ErrInvalidMatchers) {
		t.Errorf("bare rule with no matcher/server/action should be rejected, got %v", err)
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

func TestDNSCatchAllRule(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("s", "udp", "1.1.1.1", ""))

	// Matcher-less rule referencing an unknown server is still rejected.
	if err := c.AddDNSRule(DNSRule{Server: "missing"}); !errors.Is(err, ErrDNSInvalidServer) {
		t.Errorf("catch-all with unknown server should be rejected, got %v", err)
	}
	// Matcher-less catch-all with a valid server is accepted.
	if err := c.AddDNSRule(DNSRule{Server: "s"}); err != nil {
		t.Fatalf("catch-all with valid server should be accepted, got %v", err)
	}
	// Matcher-less action-only rule (reject) is accepted.
	if err := c.AddDNSRule(DNSRule{Action: "reject"}); err != nil {
		t.Errorf("matcher-less reject catch-all should be accepted, got %v", err)
	}
	// UpdateDNSRule honors the same relaxed contract.
	if err := c.UpdateDNSRule(0, DNSRule{Server: "s"}); err != nil {
		t.Errorf("UpdateDNSRule to matcher-less catch-all with server should be accepted, got %v", err)
	}
	if err := c.UpdateDNSRule(0, DNSRule{}); !errors.Is(err, ErrInvalidMatchers) {
		t.Errorf("UpdateDNSRule to bare rule should be rejected, got %v", err)
	}
}

func TestDNSRulesShadowedByCatchAll(t *testing.T) {
	c := NewEmptyConfig()
	_ = c.AddDNSServer(makeDNSServer("s", "udp", "1.1.1.1", ""))

	// No catch-all → nothing shadowed.
	_ = c.AddDNSRule(DNSRule{DomainSuffix: []string{".ru"}, Server: "s"})
	if got := c.DNSRulesShadowedByCatchAll(); got != nil {
		t.Errorf("no catch-all should shadow nothing, got %v", got)
	}
	// Add a catch-all, then two rules after it → both shadowed.
	_ = c.AddDNSRule(DNSRule{Server: "s"})                        // index 1: catch-all
	_ = c.AddDNSRule(DNSRule{Domain: []string{"a"}, Server: "s"}) // index 2: shadowed
	_ = c.AddDNSRule(DNSRule{Domain: []string{"b"}, Server: "s"}) // index 3: shadowed
	got := c.DNSRulesShadowedByCatchAll()
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("want [2 3] shadowed, got %v", got)
	}
}

func TestDNSRulesShadowedByCatchAll_EvaluateDoesNotShadow(t *testing.T) {
	cfg := &RouterConfig{}
	cfg.DNS.Servers = []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "1.1.1.1"}}
	cfg.DNS.Rules = []DNSRule{
		{Action: "evaluate", Server: "dns-direct", Tag: "rd"}, // catch-all evaluate
		{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "rd"}, ResponseRcode: "NOERROR", Action: "respond"},
	}
	if got := cfg.DNSRulesShadowedByCatchAll(); got != nil {
		t.Fatalf("evaluate не должен затенять: got %v", got)
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
	_ = c.SetDNSGlobals("vpn", "ipv4_only", "")

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
	if loaded.DNS.Servers[1].DomainResolver != nil {
		t.Errorf("domain_resolver must be cleared when detour is set: %+v", loaded.DNS.Servers[1])
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
	if err := c.SetDNSGlobals("nope", "", ""); !errors.Is(err, ErrDNSServerNotFound) {
		t.Errorf("expected not found, got %v", err)
	}
	if err := c.SetDNSGlobals("s", "ipv9", ""); err == nil {
		t.Error("expected strategy error")
	}
	if err := c.SetDNSGlobals("", "prefer_ipv4", ""); err != nil {
		t.Errorf("empty final should be allowed: %v", err)
	}
}

// dns.timeout (sing-box 1.14): таймаут запроса, пусто = 10s движка.
func TestSetDNSGlobals_Timeout(t *testing.T) {
	c := &RouterConfig{}
	if err := c.SetDNSGlobals("", "prefer_ipv4", "5s"); err != nil || c.DNS.Timeout != "5s" {
		t.Fatalf("timeout not stored: %v %+v", err, c.DNS)
	}
	if err := c.SetDNSGlobals("", "prefer_ipv4", ""); err != nil || c.DNS.Timeout != "" {
		t.Fatalf("empty must clear: %v %+v", err, c.DNS)
	}
	if err := c.SetDNSGlobals("", "prefer_ipv4", "fast"); err == nil {
		t.Error("garbage duration must be rejected")
	}
	raw, _ := json.Marshal(RouterConfig{DNS: DNS{Timeout: "5s"}})
	if !strings.Contains(string(raw), `"timeout":"5s"`) {
		t.Errorf("json: %s", raw)
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

func TestDNSMatchResponseJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string // JSON правила
		want DNSMatchResponse
		out  string // ожидаемая сериализация match_response
	}{
		{"bool true", `{"match_response":true,"server":"dns-direct"}`, DNSMatchResponse{Enabled: true}, `true`},
		{"tag", `{"match_response":"rd","server":"dns-direct"}`, DNSMatchResponse{Enabled: true, Tag: "rd"}, `"rd"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r DNSRule
			if err := json.Unmarshal([]byte(tc.in), &r); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if r.MatchResponse == nil || *r.MatchResponse != tc.want {
				t.Fatalf("got %+v, want %+v", r.MatchResponse, tc.want)
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(b), `"match_response":`+tc.out) {
				t.Fatalf("marshal = %s, want match_response %s", b, tc.out)
			}
		})
	}
}

func TestDNSMatchResponseJSONErrors(t *testing.T) {
	var r DNSRule
	if err := json.Unmarshal([]byte(`{"match_response":""}`), &r); err == nil {
		t.Fatal("пустой тег match_response должен быть ошибкой")
	}
	if err := json.Unmarshal([]byte(`{"match_response":42}`), &r); err == nil {
		t.Fatal("число в match_response должно быть ошибкой")
	}
}

func TestDNSRuleNewFieldsRoundTrip(t *testing.T) {
	in := `{"action":"evaluate","server":"dns-direct","tag":"rd","speculative":true,` +
		`"race":false,"ip_cidr":["10.0.0.0/8"],"response_rcode":"NOERROR",` +
		`"response_answer":["a"],"response_ns":["b"],"response_extra":["c"]}`
	var r DNSRule
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.Tag != "rd" || !r.Speculative || len(r.IPCIDR) != 1 || r.ResponseRcode != "NOERROR" ||
		len(r.ResponseAnswer) != 1 || len(r.ResponseNS) != 1 || len(r.ResponseExtra) != 1 {
		t.Fatalf("поля потеряны: %+v", r)
	}
	b, _ := json.Marshal(r)
	var back DNSRule
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.Tag != r.Tag || back.Speculative != r.Speculative {
		t.Fatalf("round-trip разошёлся: %+v vs %+v", back, r)
	}
}

func TestValidateDNSRuleBeta1(t *testing.T) {
	servers := map[string]string{"dns-direct": "udp", "dns-proxy": "udp", "fake": "fakeip"}
	mrTrue := &DNSMatchResponse{Enabled: true}
	mrRD := &DNSMatchResponse{Enabled: true, Tag: "rd"}
	cases := []struct {
		name    string
		r       DNSRule
		wantErr bool
	}{
		{"evaluate ok", DNSRule{Domain: []string{"x.com"}, Action: "evaluate", Server: "dns-direct", Tag: "rd", Speculative: true}, false},
		{"evaluate без server", DNSRule{Domain: []string{"x.com"}, Action: "evaluate"}, true},
		{"evaluate несуществующий server", DNSRule{Domain: []string{"x.com"}, Action: "evaluate", Server: "nope"}, true},
		{"evaluate на fakeip-сервер", DNSRule{Domain: []string{"x.com"}, Action: "evaluate", Server: "fake"}, true},
		{"respond ok", DNSRule{MatchResponse: mrRD, ResponseRcode: "NOERROR", Action: "respond"}, false},
		{"respond с server", DNSRule{MatchResponse: mrRD, Action: "respond", Server: "dns-direct"}, true},
		{"respond с rcode", DNSRule{MatchResponse: mrRD, Action: "respond", Rcode: "NOERROR"}, true},
		{"respond с method", DNSRule{MatchResponse: mrRD, Action: "respond", RejectMethod: "default"}, true},
		{"respond с tag", DNSRule{MatchResponse: mrRD, Action: "respond", Tag: "x"}, true},
		{"respond со speculative", DNSRule{MatchResponse: mrRD, Action: "respond", Speculative: true}, true},
		{"tag не на evaluate", DNSRule{Domain: []string{"x.com"}, Action: "route", Server: "dns-direct", Tag: "rd"}, true},
		{"speculative на route ВАЛИДЕН", DNSRule{Domain: []string{"x.com"}, Action: "route", Server: "dns-direct", Speculative: true}, false},
		{"speculative на reject", DNSRule{Domain: []string{"x.com"}, Action: "reject", Speculative: true}, true},
		{"race ok", DNSRule{MatchResponse: mrRD, Race: true, Action: "respond"}, false},
		{"race без match_response", DNSRule{Domain: []string{"x.com"}, Race: true, Action: "route", Server: "dns-direct"}, true},
		{"race на evaluate", DNSRule{MatchResponse: mrTrue, Race: true, Action: "evaluate", Server: "dns-direct"}, true},
		{"race+speculative", DNSRule{Domain: []string{"x.com"}, MatchResponse: mrTrue, Race: true, Speculative: true, Action: "route", Server: "dns-direct"}, true},
		{"response_* без match_response", DNSRule{Domain: []string{"x.com"}, ResponseRcode: "NOERROR", Action: "route", Server: "dns-direct"}, true},
		{"ip_cidr без match_response", DNSRule{IPCIDR: []string{"10.0.0.0/8"}, Action: "route", Server: "dns-direct"}, true},
		{"ip_cidr с match_response ok", DNSRule{MatchResponse: mrRD, IPCIDR: []string{"10.0.0.0/8"}, Action: "route", Server: "dns-direct"}, false},
		{"расширенный rcode NOTAUTH ok", DNSRule{MatchResponse: mrRD, ResponseRcode: "NOTAUTH", Action: "respond"}, false},
		{"плохой response_rcode", DNSRule{MatchResponse: mrRD, ResponseRcode: "WAT", Action: "respond"}, true},
		{"пустая строка в response_answer", DNSRule{MatchResponse: mrRD, ResponseAnswer: []string{" "}, Action: "respond"}, true},
		{"плохой ip_cidr формат", DNSRule{MatchResponse: mrRD, IPCIDR: []string{"нет"}, Action: "route", Server: "dns-direct"}, true},
		{"match_response считается матчером", DNSRule{MatchResponse: mrTrue, Action: "route", Server: "dns-direct"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDNSRule(tc.r, servers)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDNSChain(t *testing.T) {
	evalRD := DNSRule{Action: "evaluate", Server: "dns-direct", Tag: "rd"}
	evalAnon := DNSRule{Action: "evaluate", Server: "dns-direct"}
	mrRD := &DNSMatchResponse{Enabled: true, Tag: "rd"}
	respondRD := DNSRule{MatchResponse: mrRD, ResponseRcode: "NOERROR", Action: "respond"}
	cases := []struct {
		name    string
		rules   []DNSRule
		wantErr bool
	}{
		{"здоровая цепочка", []DNSRule{evalRD, respondRD}, false},
		{"match_response по тегу без evaluate выше", []DNSRule{respondRD}, true},
		{"evaluate НИЖЕ match_response не считается", []DNSRule{respondRD, evalRD}, true},
		{"анонимный match_response без evaluate выше", []DNSRule{{MatchResponse: &DNSMatchResponse{Enabled: true}, ResponseRcode: "NOERROR", Action: "respond"}}, true},
		{"анонимный match_response после анонимного evaluate", []DNSRule{evalAnon, {MatchResponse: &DNSMatchResponse{Enabled: true}, ResponseRcode: "NOERROR", Action: "respond"}}, false},
		{"анонимный match_response после ТЕГИРОВАННОГО evaluate", []DNSRule{evalRD, {MatchResponse: &DNSMatchResponse{Enabled: true}, ResponseRcode: "NOERROR", Action: "respond"}}, true},
		{"дубль тега evaluate", []DNSRule{evalRD, evalRD, respondRD}, true},
		{"дубль анонимных evaluate — не ошибка", []DNSRule{evalAnon, evalAnon, {MatchResponse: &DNSMatchResponse{Enabled: true}, ResponseRcode: "NOERROR", Action: "respond"}}, false},
		{"respond без match_response без анонимного evaluate", []DNSRule{evalRD, {Action: "respond"}}, true},
		{"respond без match_response после анонимного evaluate", []DNSRule{evalAnon, {Action: "respond"}}, false},
		{"legacy-конфиг без нового механизма — ок", []DNSRule{{Domain: []string{"x.com"}, Server: "dns-direct"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDNSChain(tc.rules)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestDNSRuleMutationsValidateChain(t *testing.T) {
	mk := func() *RouterConfig {
		cfg := &RouterConfig{}
		cfg.DNS.Servers = []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "1.1.1.1"}}
		if err := cfg.AddDNSRule(DNSRule{Action: "evaluate", Server: "dns-direct", Tag: "rd"}); err != nil {
			t.Fatalf("seed evaluate: %v", err)
		}
		if err := cfg.AddDNSRule(DNSRule{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "rd"}, ResponseRcode: "NOERROR", Action: "respond"}); err != nil {
			t.Fatalf("seed respond: %v", err)
		}
		return cfg
	}
	t.Run("Delete evaluate с потребителем — ошибка", func(t *testing.T) {
		cfg := mk()
		if err := cfg.DeleteDNSRule(0); err == nil {
			t.Fatal("удаление evaluate с match_response ниже должно падать")
		}
		if len(cfg.DNS.Rules) != 2 {
			t.Fatalf("состояние не должно меняться: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("Move respond выше evaluate — ошибка", func(t *testing.T) {
		cfg := mk()
		if err := cfg.MoveDNSRule(1, 0); err == nil {
			t.Fatal("перенос match_response выше evaluate должен падать")
		}
		if cfg.DNS.Rules[0].Action != "evaluate" {
			t.Fatalf("состояние не должно меняться: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("Add match_response без evaluate — ошибка", func(t *testing.T) {
		cfg := &RouterConfig{}
		cfg.DNS.Servers = []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "1.1.1.1"}}
		if err := cfg.AddDNSRule(DNSRule{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "rd"}, ResponseRcode: "NOERROR", Action: "respond"}); err == nil {
			t.Fatal("match_response без evaluate должен падать")
		}
	})
	t.Run("Update evaluate в route ломает потребителя — ошибка", func(t *testing.T) {
		cfg := mk()
		if err := cfg.UpdateDNSRule(0, DNSRule{Domain: []string{"x.com"}, Server: "dns-direct"}); err == nil {
			t.Fatal("замена evaluate на обычное правило должна падать")
		}
		if cfg.DNS.Rules[0].Action != "evaluate" {
			t.Fatalf("состояние не должно меняться: %+v", cfg.DNS.Rules)
		}
	})
}

func TestDNSChainTagPrefixReservedOnMutations(t *testing.T) {
	cfg := &RouterConfig{}
	cfg.DNS.Servers = []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "1.1.1.1"}}
	// evaluate с зарезервированным тегом — Add отвергает
	if err := cfg.AddDNSRule(DNSRule{Action: "evaluate", Server: "dns-direct", Tag: "awgm-dns-x"}); err == nil {
		t.Fatal("Add: префикс awgm-dns- зарезервирован (tag)")
	}
	// match_response-ссылка на зарезервированный тег — тоже
	if err := cfg.AddDNSRule(DNSRule{MatchResponse: &DNSMatchResponse{Enabled: true, Tag: "awgm-dns-rd"}, ResponseRcode: "NOERROR", Action: "respond"}); err == nil {
		t.Fatal("Add: префикс awgm-dns- зарезервирован (match_response)")
	}
	// validateDNSRule сам по себе managed-правило ПРИНИМАЕТ (нужно оверлею)
	if err := validateDNSRule(DNSRule{Action: "evaluate", Server: "dns-direct", Tag: "awgm-dns-x"}, cfg.dnsServerTypes()); err != nil {
		t.Fatalf("validateDNSRule не должен знать о резерве: %v", err)
	}
}

// TestDNSChainManagedRuleGuards — managed-правила пресета неприкосновенны для
// пользовательских CRUD: Update/Delete отвергаются, Move отвергается только по
// from (перенос ПОЛЬЗОВАТЕЛЬСКОГО правила через хвост цепочки легален —
// ensure-хук всё равно нормализует порядок).
func TestDNSChainManagedRuleGuards(t *testing.T) {
	// [0] = пользовательское правило, [1..4] = цепочка resilient.
	base := func(t *testing.T) *RouterConfig {
		t.Helper()
		cfg := &RouterConfig{}
		cfg.DNS.Servers = []DNSServer{
			{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8"},
			{Tag: "dns-tunnel", Type: "udp", Server: "9.9.9.9"},
		}
		cfg.DNS.Rules = []DNSRule{{Domain: []string{"x.com"}, Server: "dns-tunnel"}}
		st := &storage.DNSChainPresetState{Mode: "resilient", DirectServer: "dns-direct", ProxyServer: "dns-tunnel"}
		if err := ensureDNSChainOverlay(cfg, st); err != nil {
			t.Fatalf("ensureDNSChainOverlay: %v", err)
		}
		return cfg
	}

	t.Run("Update managed-правила отвергается", func(t *testing.T) {
		cfg := base(t)
		err := cfg.UpdateDNSRule(1, DNSRule{Domain: []string{"y.com"}, Server: "dns-direct"})
		if !errors.Is(err, ErrDNSRuleManaged) {
			t.Fatalf("UpdateDNSRule(managed) = %v, want ErrDNSRuleManaged", err)
		}
		if len(cfg.DNS.Rules) != 5 || !isManagedDNSChainRule(cfg.DNS.Rules[1]) {
			t.Fatalf("состояние не должно меняться: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("Delete managed-правила отвергается", func(t *testing.T) {
		cfg := base(t)
		if err := cfg.DeleteDNSRule(4); !errors.Is(err, ErrDNSRuleManaged) {
			t.Fatalf("DeleteDNSRule(managed) = %v, want ErrDNSRuleManaged", err)
		}
		if len(cfg.DNS.Rules) != 5 {
			t.Fatalf("состояние не должно меняться: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("Move managed-правила отвергается", func(t *testing.T) {
		cfg := base(t)
		if err := cfg.MoveDNSRule(1, 0); !errors.Is(err, ErrDNSRuleManaged) {
			t.Fatalf("MoveDNSRule(from=managed) = %v, want ErrDNSRuleManaged", err)
		}
		if isManagedDNSChainRule(cfg.DNS.Rules[0]) {
			t.Fatalf("состояние не должно меняться: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("Move пользовательского правила через цепочку легален", func(t *testing.T) {
		cfg := base(t)
		if err := cfg.MoveDNSRule(0, 4); err != nil {
			t.Fatalf("MoveDNSRule(user) = %v, want nil", err)
		}
		if cfg.DNS.Rules[4].Domain == nil {
			t.Fatalf("пользовательское правило должно оказаться последним: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("пользовательское правило редактируется и удаляется", func(t *testing.T) {
		cfg := base(t)
		if err := cfg.UpdateDNSRule(0, DNSRule{Domain: []string{"y.com"}, Server: "dns-direct"}); err != nil {
			t.Fatalf("UpdateDNSRule(user): %v", err)
		}
		if err := cfg.DeleteDNSRule(0); err != nil {
			t.Fatalf("DeleteDNSRule(user): %v", err)
		}
		if len(cfg.DNS.Rules) != 4 {
			t.Fatalf("ожидалась только цепочка: %+v", cfg.DNS.Rules)
		}
	})
	t.Run("force-снос сервера пресета каскадом не блокируется guard'ами", func(t *testing.T) {
		cfg := base(t)
		if err := cfg.DeleteDNSServer("dns-tunnel", true); err != nil {
			t.Fatalf("DeleteDNSServer(force): %v", err)
		}
		if err := validateDNSChain(cfg.DNS.Rules); err != nil {
			t.Fatalf("каскад обязан оставить валидную цепочку: %v", err)
		}
	})
}
