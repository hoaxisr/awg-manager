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
