package awgm

import (
	"context"
	"testing"
)

type fakeDNSCatalog struct {
	globals DNSGlobals
	servers []DNSServer
}

func (f fakeDNSCatalog) DNSGlobals(context.Context) (DNSGlobals, error)  { return f.globals, nil }
func (f fakeDNSCatalog) DNSServers(context.Context) ([]DNSServer, error) { return f.servers, nil }

func TestNewDNSResolverUsesFinal(t *testing.T) {
	catalog := fakeDNSCatalog{
		globals: DNSGlobals{Final: "dns-direct", Timeout: "2s"},
		servers: []DNSServer{{Tag: "dns-direct", Type: "udp", Server: "77.88.8.8"}},
	}
	resolver, err := NewDNSResolver(context.Background(), catalog, nil, "")
	if err != nil || resolver == nil {
		t.Fatalf("resolver=%v err=%v", resolver, err)
	}
}

func TestNewDNSResolverRejectsUnsupported(t *testing.T) {
	for name, server := range map[string]DNSServer{
		"hostname":    {Tag: "dns", Type: "https", Server: "dns.example.com"},
		"system-like": {Tag: "dns", Type: "local"},
		"bootstrap":   {Tag: "dns", Type: "udp", Server: "9.9.9.9", DomainResolver: map[string]any{"server": "other"}},
		"source-bind": {Tag: "dns", Type: "udp", Server: "9.9.9.9", Inet4BindAddress: "192.168.1.1"},
	} {
		t.Run(name, func(t *testing.T) {
			catalog := fakeDNSCatalog{globals: DNSGlobals{Final: "dns"}, servers: []DNSServer{server}}
			if _, err := NewDNSResolver(context.Background(), catalog, nil, ""); err == nil {
				t.Fatal("unsupported DNS server was accepted")
			}
		})
	}
}

func TestNewDNSResolverRequiresExplicitChoiceWithoutFinal(t *testing.T) {
	catalog := fakeDNSCatalog{servers: []DNSServer{{Tag: "dns", Type: "udp", Server: "9.9.9.9"}}}
	if _, err := NewDNSResolver(context.Background(), catalog, nil, ""); err == nil {
		t.Fatal("missing dns.final was accepted")
	}
	if _, err := NewDNSResolver(context.Background(), catalog, nil, "sb:dns"); err != nil {
		t.Fatal(err)
	}
}
