package main

import (
	"strconv"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/presets"
	ip "github.com/hoaxisr/awg-manager/internal/singbox/router/internalpresets"
)

func fakeDecompiler(big map[string]bool) decompiler {
	return func(url string) ([]string, []string, error) {
		if big[url] {
			d := make([]string, 600)
			for i := range d {
				d[i] = "x" + strconv.Itoa(i) + ".com" // unique → survives dedup, exceeds cap
			}
			return d, nil, nil
		}
		return []string{"a.com", "b.com"}, []string{"1.2.3.0/24"}, nil
	}
}

func TestBuildMergesRouterAndDNS(t *testing.T) {
	router := []ip.Preset{{
		ID: "youtube", Name: "YouTube", Category: "social", IconSlug: "youtube",
		RuleSets: []ip.RuleRef{{Tag: "geosite-youtube", URL: "u/youtube.srs"}},
		Rules:    []ip.RuleLink{{RuleSetRef: "geosite-youtube", ActionTarget: "tunnel"}},
	}}
	svc := []servicePreset{{ID: "russian-services", Name: "RU", Domains: []string{"yandex.ru"}, SubscriptionURL: "sub"}}
	out := build(router, svc, nil, fakeDecompiler(nil))
	byID := indexByID(out)

	yt := byID["youtube"]
	if yt.Engines.Singbox == nil || yt.Engines.Singbox.Action != "tunnel" {
		t.Fatalf("youtube singbox missing/wrong: %+v", yt.Engines.Singbox)
	}
	if yt.Engines.DNS == nil || len(yt.Engines.DNS.Domains) != 2 {
		t.Fatalf("youtube dns from decompile expected: %+v", yt.Engines.DNS)
	}
	ru := byID["russian-services"]
	if ru.Engines.Singbox != nil || ru.Engines.DNS == nil || ru.Engines.DNS.SubscriptionURL != "sub" {
		t.Fatalf("russian-services must be dns-only with subscription: %+v", ru.Engines)
	}
}

func TestBuildSizeCapDropsDNS(t *testing.T) {
	router := []ip.Preset{{
		ID: "ads", Name: "Ads", Category: "block", IconSlug: "lucide-circle-slash",
		RuleSets: []ip.RuleRef{{Tag: "geosite-ads", URL: "u/ads.srs"}},
		Rules:    []ip.RuleLink{{RuleSetRef: "geosite-ads", ActionTarget: "reject"}},
	}}
	out := build(router, nil, nil, fakeDecompiler(map[string]bool{"u/ads.srs": true}))
	ads := indexByID(out)["ads"]
	if ads.Engines.Singbox == nil || ads.Engines.Singbox.Action != "reject" {
		t.Fatalf("ads singbox missing")
	}
	if ads.Engines.DNS != nil {
		t.Fatalf("ads over 500-cap must stay singbox-only, got dns=%+v", ads.Engines.DNS)
	}
}

func TestBuildAddsNewPresets(t *testing.T) {
	out := build(nil, nil, []addition{{"meta", "Meta", "meta", "social", "u/meta.srs", "tunnel"}}, fakeDecompiler(nil))
	meta := indexByID(out)["meta"]
	if meta.IconSlug != "meta" || meta.Engines.Singbox == nil || meta.Engines.DNS == nil {
		t.Fatalf("meta addition wrong: %+v", meta)
	}
}

func TestBuildDeterministicOrderByCategoryThenID(t *testing.T) {
	router := []ip.Preset{
		{ID: "spotify", Category: "media", Name: "S", IconSlug: "spotify"},
		{ID: "discord", Category: "social", Name: "D", IconSlug: "discord"},
		{ID: "netflix", Category: "media", Name: "N", IconSlug: "netflix"},
	}
	out := build(router, nil, nil, fakeDecompiler(nil))
	got := []string{out[0].ID, out[1].ID, out[2].ID}
	want := []string{"netflix", "spotify", "discord"} // (media,netflix)<(media,spotify)<(social,discord)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want %v", got, want)
		}
	}
}

func indexByID(ps []presets.Preset) map[string]presets.Preset {
	m := map[string]presets.Preset{}
	for _, p := range ps {
		m[p.ID] = p
	}
	return m
}

func TestBuildPropagatesFeatured(t *testing.T) {
	router := []ip.Preset{{ID: "x", Name: "X", Category: "social", IconSlug: "x", Featured: true}}
	out := build(router, nil, nil, fakeDecompiler(nil))
	if !indexByID(out)["x"].Featured {
		t.Fatalf("Featured must propagate from router preset")
	}
}

func TestBuildDedupsDNSAcrossRuleSets(t *testing.T) {
	router := []ip.Preset{{
		ID: "multi", Name: "Multi", Category: "media", IconSlug: "lucide-film",
		RuleSets: []ip.RuleRef{
			{Tag: "geosite-a", URL: "u/a.srs"},
			{Tag: "geosite-b", URL: "u/b.srs"},
		},
		Rules: []ip.RuleLink{{RuleSetRef: "geosite-a", ActionTarget: "tunnel"}},
	}}
	dc := func(url string) ([]string, []string, error) {
		switch url {
		case "u/a.srs":
			return []string{"shared.com", "a.com"}, nil, nil
		case "u/b.srs":
			return []string{"shared.com", "b.com"}, nil, nil
		}
		return nil, nil, nil
	}
	dns := indexByID(build(router, nil, nil, dc))["multi"].Engines.DNS
	if dns == nil || len(dns.Domains) != 3 {
		t.Fatalf("cross-ruleset dedup expected 3 unique domains, got %+v", dns)
	}
}
