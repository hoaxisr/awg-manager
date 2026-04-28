package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20-router.json")

	cfg := &RouterConfig{
		Inbounds: []Inbound{{
			Type: "tproxy", Tag: "tproxy-in", Listen: "127.0.0.1",
			ListenPort: 51271, Network: "tcp,udp", UDPTimeout: "5m", RoutingMark: 1,
		}},
		Outbounds: []Outbound{
			{Type: "direct", Tag: "awg10", BindInterface: "opkgtun10"},
		},
		Route: Route{
			RuleSet: []RuleSet{
				{Tag: "geosite-youtube", Type: "remote", Format: "binary",
					URL: "https://example.com/geosite-youtube.srs", UpdateInterval: "24h"},
			},
			Rules: []Rule{
				{Action: "sniff"},
				{RuleSet: []string{"geosite-youtube"}, Action: "route", Outbound: "awg10"},
			},
			Final: "direct",
		},
	}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Inbounds) != 1 || loaded.Inbounds[0].Tag != "tproxy-in" {
		t.Errorf("inbounds: %+v", loaded.Inbounds)
	}
	if len(loaded.Outbounds) != 1 || loaded.Outbounds[0].BindInterface != "opkgtun10" {
		t.Errorf("outbounds: %+v", loaded.Outbounds)
	}
	if loaded.Route.Final != "direct" {
		t.Errorf("final: %q", loaded.Route.Final)
	}
	if len(loaded.Route.Rules) != 2 || len(loaded.Route.RuleSet) != 1 {
		t.Errorf("route: %+v", loaded.Route)
	}
}

func TestLoadConfigMissingReturnsEmpty(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Inbounds) != 0 || len(cfg.Outbounds) != 0 {
		t.Error("expected empty config")
	}
}

func TestSaveProducesValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "20-router.json")
	if err := SaveConfig(path, NewEmptyConfig()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	for _, k := range []string{"inbounds", "outbounds", "route"} {
		if _, ok := parsed[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

func TestStripLegacyAWGDirect(t *testing.T) {
	in := []Outbound{
		{Type: "direct", Tag: "legacy-a", BindInterface: "t2s0"},
		{Type: "direct", Tag: "direct"}, // no bind_interface — keep
		{Type: "selector", Tag: "comp", Outbounds: []string{"awg-x"}},
		{Type: "direct", Tag: "legacy-b", BindInterface: "nwg0"},
	}
	got := stripLegacyAWGDirect(in)
	if len(got) != 2 {
		t.Fatalf("want 2 outbounds (direct + selector), got %d (%+v)", len(got), got)
	}
	if got[0].Tag != "direct" || got[1].Tag != "comp" {
		t.Errorf("unexpected outbounds: %+v", got)
	}
}
