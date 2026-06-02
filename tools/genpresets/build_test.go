package main

import (
	"strconv"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

func fakeDecompiler(big map[string]bool) decompiler {
	return func(url string) ([]string, []string, error) {
		if big[url] {
			d := make([]string, 600)
			for i := range d {
				d[i] = "x" + strconv.Itoa(i) + ".com"
			}
			return d, nil, nil
		}
		return []string{"a.com", "b.com"}, []string{"1.2.3.0/24"}, nil
	}
}

func indexByID(ps []presets.Preset) map[string]presets.Preset {
	m := map[string]presets.Preset{}
	for _, p := range ps {
		m[p.ID] = p
	}
	return m
}

func TestBuildRefreshesDNSForSingboxPresets(t *testing.T) {
	base := []presets.Preset{{
		ID: "youtube", Name: "YouTube", Category: "social", IconSlug: "youtube",
		Engines: presets.Engines{Singbox: &presets.SingboxEngine{
			RuleSets: []presets.RuleRef{{Tag: "geosite-youtube", URL: "u/youtube.srs"}}, Action: "tunnel"}},
	}}
	out := indexByID(build(base, nil, fakeDecompiler(nil)))
	yt := out["youtube"]
	if yt.Engines.DNS == nil || len(yt.Engines.DNS.Domains) != 2 {
		t.Fatalf("DNS refresh expected: %+v", yt.Engines.DNS)
	}
	if yt.Name != "YouTube" || yt.IconSlug != "youtube" {
		t.Fatalf("non-DNS fields must be preserved: %+v", yt)
	}
}

func TestBuildPreservesDNSOnlyBase(t *testing.T) {
	base := []presets.Preset{{
		ID: "russian-services", Name: "RU", Category: "block", IconSlug: "rkn",
		Engines: presets.Engines{DNS: &presets.DNSEngine{Domains: []string{"yandex.ru"}, SubscriptionURL: "sub"}},
	}}
	ru := indexByID(build(base, nil, fakeDecompiler(nil)))["russian-services"]
	if ru.Engines.Singbox != nil || ru.Engines.DNS == nil || ru.Engines.DNS.SubscriptionURL != "sub" {
		t.Fatalf("dns-only base must pass through untouched: %+v", ru.Engines)
	}
}

func TestBuildSizeCapClearsDNS(t *testing.T) {
	base := []presets.Preset{{
		ID: "ads", Name: "Ads", Category: "block", IconSlug: "lucide-circle-slash",
		Engines: presets.Engines{Singbox: &presets.SingboxEngine{
			RuleSets: []presets.RuleRef{{Tag: "geosite-ads", URL: "u/ads.srs"}}, Action: "reject"}},
	}}
	ads := indexByID(build(base, nil, fakeDecompiler(map[string]bool{"u/ads.srs": true})))["ads"]
	if ads.Engines.DNS != nil {
		t.Fatalf("over-cap must clear DNS: %+v", ads.Engines.DNS)
	}
}

func TestBuildAppendsNewAdditions(t *testing.T) {
	out := indexByID(build(nil, []addition{{"meta", "Meta", "meta", "social", "u/meta.srs", "tunnel"}}, fakeDecompiler(nil)))
	meta := out["meta"]
	if meta.IconSlug != "meta" || meta.Engines.Singbox == nil || meta.Engines.DNS == nil {
		t.Fatalf("addition wrong: %+v", meta)
	}
}

func TestBuildSkipsExistingAdditions(t *testing.T) {
	base := []presets.Preset{{ID: "meta", Name: "Meta orig", Category: "social", IconSlug: "meta",
		Engines: presets.Engines{Singbox: &presets.SingboxEngine{RuleSets: []presets.RuleRef{{Tag: "geosite-meta", URL: "u/meta.srs"}}, Action: "tunnel"}}}}
	out := indexByID(build(base, []addition{{"meta", "Meta dup", "meta", "social", "u/meta.srs", "tunnel"}}, fakeDecompiler(nil)))
	if out["meta"].Name != "Meta orig" {
		t.Fatalf("existing base preset must win over addition: %+v", out["meta"])
	}
}
