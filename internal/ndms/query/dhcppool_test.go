package query

import (
	"context"
	"testing"
)

// rcWithPools builds a running-config message body for the given lines.
func rcLines(lines ...string) []byte {
	body := `{"message":[`
	for i, l := range lines {
		if i > 0 {
			body += ","
		}
		// crude JSON string quoting (no embedded quotes in fixtures)
		body += `"` + l + `"`
	}
	body += `]}`
	return []byte(body)
}

func TestRunningConfigStore_GetPoolDNSServer(t *testing.T) {
	lines := rcLines(
		"!",
		"ip dhcp pool _WEBADMIN",
		"    range 192.168.0.50 192.168.0.149",
		"    bind Bridge0",
		"!",
		"ip dhcp pool _WEBADMIN_GUEST_AP",
		"    range 172.18.0.50 172.18.0.149",
		"    dns-server 172.18.0.2",
		"!",
		"ip dhcp pool MULTI",
		"    dns-server 10.0.0.1 10.0.0.2",
		"!",
	)

	cases := []struct {
		name string
		pool string
		want string
	}{
		{"pool with one dns-server", "_WEBADMIN_GUEST_AP", "172.18.0.2"},
		{"pool with none", "_WEBADMIN", ""},
		{"pool with two returns first", "MULTI", "10.0.0.1"},
		{"pool not present", "NOPE", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fg := newFakeGetter()
			fg.SetRaw("/show/running-config", lines)
			s := NewRunningConfigStore(fg, NopLogger())
			got, err := s.GetPoolDNSServer(context.Background(), tc.pool)
			if err != nil {
				t.Fatalf("GetPoolDNSServer: %v", err)
			}
			if got != tc.want {
				t.Fatalf("GetPoolDNSServer(%q) = %q, want %q", tc.pool, got, tc.want)
			}
		})
	}
}

// A dns-server line that belongs to a DIFFERENT pool must not leak into a
// pool that itself has no dns-server (section boundary by dedent).
func TestRunningConfigStore_GetPoolDNSServer_NoLeakAcrossPools(t *testing.T) {
	lines := rcLines(
		"ip dhcp pool A",
		"    range 192.168.1.1 192.168.1.100",
		"!",
		"ip dhcp pool B",
		"    dns-server 9.9.9.9",
		"!",
	)
	fg := newFakeGetter()
	fg.SetRaw("/show/running-config", lines)
	s := NewRunningConfigStore(fg, NopLogger())
	got, err := s.GetPoolDNSServer(context.Background(), "A")
	if err != nil {
		t.Fatalf("GetPoolDNSServer: %v", err)
	}
	if got != "" {
		t.Fatalf("pool A leaked dns-server %q from pool B", got)
	}
}

// Leading slash is REQUIRED: the Getter resolves the path against the /rci/
// base; without it the request escapes /rci/ and NDMS returns the SPA HTML stub
// (stand-found). This const must match DHCPPoolStore.List's GetRaw path exactly,
// so it doubles as a regression guard for that slash.
const dhcpPoolShowPath = "/show/ip/dhcp/pool"

func TestDHCPPoolStore_List(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(dhcpPoolShowPath, `{
		"_WEBADMIN": {
			"interface": {"binding": "fixed", "interface": "Bridge0"},
			"network": "192.168.0.1/24",
			"router": "192.168.0.1",
			"state": "running"
		},
		"_WEBADMIN_GUEST_AP": {
			"interface": {"binding": "auto"},
			"network": "172.18.0.0/24",
			"router": {"default": "172.18.0.1"},
			"state": "running"
		}
	}`)
	fg.SetRaw("/show/running-config", rcLines(
		"ip dhcp pool _WEBADMIN_GUEST_AP",
		"    dns-server 172.18.0.2",
		"!",
	))

	s := NewDHCPPoolStore(fg, NewRunningConfigStore(fg, NopLogger()), NopLogger())
	pools, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pools) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(pools), pools)
	}
	// Sorted by name: _WEBADMIN, _WEBADMIN_GUEST_AP
	if pools[0].Name != "_WEBADMIN" {
		t.Fatalf("pools[0].Name = %q", pools[0].Name)
	}
	if pools[0].Network != "192.168.0.1/24" {
		t.Fatalf("pools[0].Network = %q", pools[0].Network)
	}
	if pools[0].Interface != "Bridge0" {
		t.Fatalf("pools[0].Interface = %q", pools[0].Interface)
	}
	if pools[0].DNSServer != "" {
		t.Fatalf("pools[0].DNSServer = %q, want empty", pools[0].DNSServer)
	}
	// Guest pool: router is an OBJECT (must not break), interface absent.
	if pools[1].Name != "_WEBADMIN_GUEST_AP" {
		t.Fatalf("pools[1].Name = %q", pools[1].Name)
	}
	if pools[1].Interface != "" {
		t.Fatalf("pools[1].Interface = %q, want empty (binding auto, no iface)", pools[1].Interface)
	}
	if pools[1].DNSServer != "172.18.0.2" {
		t.Fatalf("pools[1].DNSServer = %q, want 172.18.0.2", pools[1].DNSServer)
	}
}

func TestDHCPPoolStore_List_EmptyMap(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(dhcpPoolShowPath, `[]`)
	fg.SetRaw("/show/running-config", rcLines("!"))
	s := NewDHCPPoolStore(fg, NewRunningConfigStore(fg, NopLogger()), NopLogger())
	pools, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pools) != 0 {
		t.Fatalf("len = %d, want 0", len(pools))
	}
}
