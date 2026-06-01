package presets

import (
	"encoding/json"
	"testing"
)

func TestPresetJSONRoundTrip(t *testing.T) {
	in := Preset{
		ID: "youtube", Name: "YouTube", IconSlug: "youtube", Category: "social",
		Origin: OriginBuiltin,
		Engines: Engines{
			DNS:        &DNSEngine{Domains: []string{"youtube.com"}},
			Singbox:    &SingboxEngine{RuleSets: []RuleRef{{Tag: "geosite-youtube", URL: "https://x/youtube.srs"}}, Action: "tunnel"},
			HydraRoute: &HydraRouteEngine{GeoTags: []string{"YOUTUBE"}},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Preset
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != "youtube" || out.Engines.DNS == nil || out.Engines.Singbox.Action != "tunnel" || out.Engines.HydraRoute == nil {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}

func TestEnginesOmittedSerializeAsAbsent(t *testing.T) {
	raw, _ := json.Marshal(Preset{ID: "ads", Name: "Ads", IconSlug: "lucide-circle-slash", Engines: Engines{Singbox: &SingboxEngine{Action: "reject"}}})
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	eng := m["engines"].(map[string]any)
	if _, hasDNS := eng["dns"]; hasDNS {
		t.Fatalf("dns must be absent when nil, got %v", eng)
	}
}
