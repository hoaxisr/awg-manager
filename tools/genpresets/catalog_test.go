package main

import "testing"

func TestCanonicalID(t *testing.T) {
	cases := map[string]string{
		"twitter": "x", "chatgpt": "openai", "cloudflare-ips": "cloudflare",
		"youtube": "youtube", "russian-services": "russian-services",
	}
	for in, want := range cases {
		if got := canonicalID(in); got != want {
			t.Errorf("canonicalID(%q)=%q want %q", in, got, want)
		}
	}
}

func TestServicePresetSkipsSocialBundle(t *testing.T) {
	if !isDroppedSourceID("social") {
		t.Error("social bundle must be dropped (members exist as separate presets)")
	}
	if isDroppedSourceID("youtube") {
		t.Error("youtube must not be dropped")
	}
}

func TestAdditionsCoverMetaAndOculus(t *testing.T) {
	byID := map[string]addition{}
	for _, a := range additions {
		byID[a.id] = a
	}
	for _, id := range []string{"meta", "oculus", "soundcloud", "slack", "blizzard", "threads"} {
		a, ok := byID[id]
		if !ok {
			t.Fatalf("addition %q missing", id)
		}
		if a.name == "" || a.iconSlug == "" || a.category == "" || a.srsURL == "" || a.action == "" {
			t.Errorf("addition %q incomplete: %+v", id, a)
		}
	}
}

func TestDNSOnlyMetaAssignsIconAndCategory(t *testing.T) {
	for _, id := range []string{"atlassian", "russian-services", "all-blocked", "torrents"} {
		m, ok := dnsOnlyMeta[id]
		if !ok {
			t.Fatalf("dnsOnlyMeta[%q] missing", id)
		}
		if m.iconSlug == "" || m.category == "" || m.name == "" {
			t.Errorf("dnsOnlyMeta[%q] incomplete: %+v", id, m)
		}
	}
}
