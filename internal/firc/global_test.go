package firc

import (
	"net/netip"
	"reflect"
	"testing"
)

func TestGlobalKeepsRulesAndPrecedence(t *testing.T) {
	ordinary := Group{ID: "12345678", Name: "ordinary", Enable: true}
	ext := ExtConfig{GlobalTarget: "pg:test", ProxyGroups: []ProxyGroup{{ID: "test", Interfaces: []string{"eth3", "tunvless1"}}}}
	groups, changed := syncGroupsWithExt([]Group{ordinary}, ext)
	if !changed || len(groups) != 2 || groups[0].ID != globalGroupID || !reflect.DeepEqual(groups[1], ordinary) {
		t.Fatalf("unexpected projection: %+v", groups)
	}
	if _, changed := syncGroupsWithExt(groups, ext); changed {
		t.Fatal("repeated projection changed groups")
	}
	for _, tc := range []struct {
		addr string
		want bool
	}{{"8.8.8.8", true}, {"192.168.90.3", false}, {"100.64.0.1", false}, {"2606:4700:4700::1111", true}, {"fe80::1", false}} {
		match := false
		for _, r := range groups[0].Rules {
			if netip.MustParsePrefix(r.Rule).Contains(netip.MustParseAddr(tc.addr)) {
				match = true
			}
		}
		if match != tc.want {
			t.Fatalf("unexpected coverage for %s", tc.addr)
		}
	}
	restored, changed := syncGroupsWithExt(groups, ExtConfig{})
	if !changed || !reflect.DeepEqual(restored, []Group{ordinary}) {
		t.Fatalf("restore: %+v", restored)
	}
}

func TestGlobalGroupCheckConfiguration(t *testing.T) {
	multiExt := ExtConfig{
		GlobalTarget: "pg:test",
		ProxyGroups:  []ProxyGroup{{ID: "test", Interfaces: []string{"qmi_br0", "opkgtun18"}}},
	}
	groups, changed := syncGroupsWithExt(nil, multiExt)
	if !changed || len(groups) != 1 {
		t.Fatalf("unexpected sync result: changed=%v groups=%+v", changed, groups)
	}
	if groups[0].Check == nil || groups[0].Check.Mode != "url" {
		t.Fatalf("multi-iface Global group must set Check.Mode=url, got %+v", groups[0].Check)
	}

	// Legacy Global groups persisted with Check=nil or Mode="none" must upgrade to Mode="url".
	legacyNil := groups[0]
	legacyNil.Check = nil
	upgraded, changed := syncGroupsWithExt([]Group{legacyNil}, multiExt)
	if !changed || upgraded[0].Check == nil || upgraded[0].Check.Mode != "url" {
		t.Fatalf("legacy nil-check Global group did not upgrade: changed=%v check=%+v", changed, upgraded[0].Check)
	}

	legacyNone := groups[0]
	legacyNone.Check = &GroupCheck{Mode: "none"}
	upgraded, changed = syncGroupsWithExt([]Group{legacyNone}, multiExt)
	if !changed || upgraded[0].Check == nil || upgraded[0].Check.Mode != "url" {
		t.Fatalf("legacy mode=none Global group did not upgrade: changed=%v check=%+v", changed, upgraded[0].Check)
	}

	// Single-interface Global group explicitly clears check with Mode="none".
	singleExt := ExtConfig{GlobalTarget: "iface:eth3"}
	singleGroups, changed := syncGroupsWithExt(upgraded, singleExt)
	if !changed || len(singleGroups) != 1 {
		t.Fatalf("single-iface sync mismatch: changed=%v groups=%+v", changed, singleGroups)
	}
	if singleGroups[0].Check == nil || singleGroups[0].Check.Mode != "none" {
		t.Fatalf("single-iface Global group must set Check.Mode=none, got %+v", singleGroups[0].Check)
	}

	// Global inherits explicit ProxyGroup.Check settings (url, keendns, none).
	customURLExt := ExtConfig{
		GlobalTarget: "pg:test",
		ProxyGroups: []ProxyGroup{{
			ID:         "test",
			Interfaces: []string{"qmi_br0", "opkgtun18"},
			Check:      &GroupCheck{Mode: "url", URL: "https://cp.cloudflare.com/generate_204"},
		}},
	}
	customGroups, changed := syncGroupsWithExt(nil, customURLExt)
	if !changed || customGroups[0].Check == nil || customGroups[0].Check.Mode != "url" || customGroups[0].Check.URL != "https://cp.cloudflare.com/generate_204" {
		t.Fatalf("Global must inherit PG url check, got %+v", customGroups[0].Check)
	}

	kdExt := ExtConfig{
		GlobalTarget: "pg:test",
		ProxyGroups: []ProxyGroup{{
			ID:         "test",
			Interfaces: []string{"qmi_br0", "opkgtun18"},
			Check:      &GroupCheck{Mode: "keendns", URL: "https://ignored.example"},
		}},
	}
	kdGroups, changed := syncGroupsWithExt(customGroups, kdExt)
	if !changed || kdGroups[0].Check == nil || kdGroups[0].Check.Mode != "keendns" || kdGroups[0].Check.TimeoutMs != 5000 || kdGroups[0].Check.URL != "" {
		t.Fatalf("Global must inherit PG keendns check with TimeoutMs=5000 and cleared URL, got %+v", kdGroups[0].Check)
	}

	noneExt := ExtConfig{
		GlobalTarget: "pg:test",
		ProxyGroups: []ProxyGroup{{
			ID:         "test",
			Interfaces: []string{"qmi_br0", "opkgtun18"},
			Check:      &GroupCheck{Mode: "none"},
		}},
	}
	noneGroups, changed := syncGroupsWithExt(kdGroups, noneExt)
	if !changed || noneGroups[0].Check == nil || noneGroups[0].Check.Mode != "none" {
		t.Fatalf("Global must inherit explicit PG Check.Mode=none, got %+v", noneGroups[0].Check)
	}
}
