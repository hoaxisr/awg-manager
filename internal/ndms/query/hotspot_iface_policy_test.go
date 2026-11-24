package query

import (
	"context"
	"maps"
	"testing"
)

func TestRunningConfigStore_GetInterfaceHotspotPolicy(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/running-config", []byte(`{"message":[
		"!",
		"ip hotspot",
		" policy Home Policy0",
		" policy Wireguard0 Policy1",
		"!"
	]}`))
	s := NewRunningConfigStore(fg, NopLogger())

	policy, err := s.GetInterfaceHotspotPolicy(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetInterfaceHotspotPolicy: %v", err)
	}
	if policy != "Policy1" {
		t.Fatalf("policy = %q, want Policy1", policy)
	}

	none, err := s.GetInterfaceHotspotPolicy(context.Background(), "Wireguard9")
	if err != nil {
		t.Fatalf("GetInterfaceHotspotPolicy missing: %v", err)
	}
	if none != "none" {
		t.Fatalf("missing policy = %q, want none", none)
	}
}

func TestHotspotPolicies(t *testing.T) {
	ifaces, hosts := HotspotPolicies([]string{
		"ip policy Policy9", "    description lan", "!",
		"ip hotspot",
		"    policy Home Policy0",
		"    host AA:bb:cc:dd:ee:01 permit",
		"    host AA:bb:cc:dd:ee:01 policy Policy1",
		"    host aa:bb:cc:dd:ee:02 conform",
		"    default-policy permit",
		"!",
		"    policy Guest Policy2",
	})
	if !maps.Equal(ifaces, map[string]string{"Home": "Policy0"}) {
		t.Errorf("ifaces = %v", ifaces)
	}
	if !maps.Equal(hosts, map[string]string{"aa:bb:cc:dd:ee:01": "Policy1"}) {
		t.Errorf("hosts = %v", hosts)
	}
}
