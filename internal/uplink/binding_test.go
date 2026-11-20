package uplink

import "testing"

func TestProviderDNSNeverSubstitutesGlobalOrOtherProvider(t *testing.T) {
	raw := []byte(`[{"address":"1.1.1.1","interface":"ISP1"},{"address":"9.9.9.9"},{"address":"10.0.0.1","interface":"ISP2","domain":"corp.example"},{"address":"198.51.100.53:5353","interface":"ISP2"},{"address":"127.0.0.1","interface":"ISP2"},{"address":"dns.example.com","interface":"ISP2"}]`)
	servers, err := ProviderDNS(raw, "ISP2")
	if err != nil || len(servers) != 1 || servers[0] != "198.51.100.53:5353" {
		t.Fatal(servers, err)
	}
	if _, err = ProviderDNS(raw, "ISP3"); err == nil {
		t.Fatal("missing provider used a global resolver")
	}
}

func TestProviderRuntimeDNSAcceptsProviderDHCPAndRejectsGlobal(t *testing.T) {
	raw := []byte(`{"server":[{"address":"192.168.90.1","domain":"","service":"Dhcp::Client-GigabitEthernet1","interface":"GigabitEthernet1"},{"address":"8.8.8.8","domain":""},{"address":"10.1.0.53","domain":"corp.local","interface":"GigabitEthernet1"}]}`)
	servers, err := ProviderRuntimeDNS(raw, "GigabitEthernet1")
	if err != nil || len(servers) != 1 || servers[0] != "192.168.90.1:53" {
		t.Fatalf("runtime DHCP DNS mismatch: servers=%v err=%v", servers, err)
	}
	if _, err = ProviderRuntimeDNS(raw, "WifiMaster0/WifiStation0"); err == nil {
		t.Fatal("missing runtime provider accepted global server")
	}
}

func TestBindingRejectsImplicitDNSAndBadSources(t *testing.T) {
	for _, b := range []Binding{
		{Device: "wan0", IPv4: "192.0.2.2", DNSServers: []string{"dns.example.com"}},
		{Device: "wan0", IPv4: "::1"},
		{Device: "wan0"},
		{Device: "wan 0", IPv4: "192.0.2.2"},
	} {
		if err := b.Validate(); err == nil {
			t.Fatal("invalid binding accepted", b)
		}
	}
}
