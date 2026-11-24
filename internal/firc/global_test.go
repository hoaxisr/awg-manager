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
	if groups[0].Check != nil {
		t.Fatalf("proxy group without check must not set one on Global, got %+v", groups[0].Check)
	}

	// Прокси-группа без check не трогает проверку Global: ни выключенную, ни свою.
	for _, own := range []*GroupCheck{nil, {Mode: "none"}, {Mode: "url", URL: "https://own.example/204", IntervalSec: 30}} {
		cur := groups[0]
		cur.Check = own
		kept, changed := syncGroupsWithExt([]Group{cur}, multiExt)
		if changed || !groupCheckEqual(kept[0].Check, own) {
			t.Fatalf("Global must keep its own check %+v: changed=%v got %+v", own, changed, kept[0].Check)
		}
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
