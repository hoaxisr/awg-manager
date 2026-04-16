package hydraroute

import (
	"reflect"
	"testing"
)

func TestParseDomainConf_Basic(t *testing.T) {
	content := "## Youtube\n" +
		"youtube.com,ytimg.com/nwg0\n" +
		"## Google\n" +
		"google.com/HydraRoute\n"

	got := parseDomainConf(content)

	want := []ManagedEntry{
		{ListName: "Youtube", Domains: []string{"youtube.com", "ytimg.com"}, Iface: "nwg0"},
		{ListName: "Google", Domains: []string{"google.com"}, Iface: "HydraRoute"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseDomainConf_TolerantToBlankLines(t *testing.T) {
	content := "\n## A\n\na.com/eth0\n\n## B\nb.com/eth1\n"
	got := parseDomainConf(content)
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d: %+v", len(got), got)
	}
}

func TestParseIPList_Basic(t *testing.T) {
	content := "## Russia\n" +
		"/nwg0\n" +
		"5.8.0.0/21\n" +
		"geoip:RU\n" +
		"\n" +
		"## Telegram\n" +
		"/nwg0\n" +
		"91.108.4.0/22\n" +
		"\n"

	got, _ := parseIPList(content)

	want := []ManagedEntry{
		{ListName: "Russia", Subnets: []string{"5.8.0.0/21", "geoip:RU"}, Iface: "nwg0"},
		{ListName: "Telegram", Subnets: []string{"91.108.4.0/22"}, Iface: "nwg0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestRoundtrip_GenerateThenParse_Domains(t *testing.T) {
	original := []ManagedEntry{
		{ListName: "Youtube", Domains: []string{"youtube.com", "ytimg.com"}, Iface: "nwg0"},
		{ListName: "Google", Domains: []string{"google.com"}, Iface: "HydraRoute"},
	}
	generated := GenerateDomainConf(original)
	parsed := parseDomainConf(generated)

	if !reflect.DeepEqual(parsed, original) {
		t.Errorf("roundtrip mismatch:\noriginal: %+v\nparsed:   %+v", original, parsed)
	}
}

func TestRoundtrip_GenerateThenParse_IPList(t *testing.T) {
	original := []ManagedEntry{
		{ListName: "Russia", Subnets: []string{"5.8.0.0/21", "geoip:RU"}, Iface: "nwg0"},
	}
	generated := GenerateIPList(original)
	parsed, _ := parseIPList(generated)

	if !reflect.DeepEqual(parsed, original) {
		t.Errorf("roundtrip mismatch:\noriginal: %+v\nparsed:   %+v", original, parsed)
	}
}

func TestParseIPList_ServiceBlockReturnsOversized(t *testing.T) {
	content := "## 2ip\n" +
		"/HydraRoute\n" +
		"geoip:RU\n" +
		"\n" +
		"##impossible to use\n" +
		"#/Too-big-geoip-tag\n" +
		"geoip:ru-blocked\n" +
		"geoip:cn-heavy\n"

	entries, oversized := parseIPList(content)

	if len(entries) != 1 {
		t.Fatalf("want 1 rule, got %d: %+v", len(entries), entries)
	}
	if entries[0].ListName != "2ip" {
		t.Errorf("rule name = %q, want 2ip", entries[0].ListName)
	}

	wantOversized := []string{"geoip:ru-blocked", "geoip:cn-heavy"}
	if !reflect.DeepEqual(oversized, wantOversized) {
		t.Errorf("oversized = %v, want %v", oversized, wantOversized)
	}
}

func TestParseIPList_ServiceBlockWithoutGeoipLinesIgnored(t *testing.T) {
	content := "##impossible to use\n" +
		"#/Too-big-geoip-tag\n" +
		"not-a-geoip-tag\n"

	entries, oversized := parseIPList(content)

	if len(entries) != 0 {
		t.Errorf("expected no rule entries, got %+v", entries)
	}
	if len(oversized) != 0 {
		t.Errorf("expected no oversized entries (non-geoip line), got %v", oversized)
	}
}

func TestParseIPList_NoServiceBlock(t *testing.T) {
	content := "## OnlyRule\n/Iface\n10.0.0.0/8\n"

	entries, oversized := parseIPList(content)

	if len(entries) != 1 {
		t.Fatalf("want 1 rule, got %d", len(entries))
	}
	if len(oversized) != 0 {
		t.Errorf("expected empty oversized, got %v", oversized)
	}
}

// User's reported scenario: rules outside markers must still be visible.
// Now there are no markers — we always read every ## block.
func TestParseDomainConf_RulesAnywhereInFile(t *testing.T) {
	content := "## Outside\n" +
		"outside.com/eth0\n" +
		"## Inside\n" +
		"inside.com/eth1\n"

	got := parseDomainConf(content)

	if len(got) != 2 {
		t.Fatalf("want both rules visible, got %+v", got)
	}
}
