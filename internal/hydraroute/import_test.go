package hydraroute

import (
	"testing"
)

const managedBlock = `## --- AWG Manager START ---
## list:1:Test
t.me/Wireguard0
## --- AWG Manager END ---
`

// ---------------------------------------------------------------------------
// parseNativeDomainConf
// ---------------------------------------------------------------------------

func TestParseNativeDomainConf_Basic(t *testing.T) {
	content := `##Youtube
googlevideo.com,nhacmp3youtube.com,youtu.be/HydraRoute
##Google
android.com,google.com/HydraRoute
` + managedBlock

	rules := parseNativeDomainConf(content)
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	r0 := rules[0]
	if r0.Name != "Youtube" {
		t.Errorf("rule[0].Name: got %q, want %q", r0.Name, "Youtube")
	}
	if r0.Target != "HydraRoute" {
		t.Errorf("rule[0].Target: got %q, want %q", r0.Target, "HydraRoute")
	}
	if len(r0.Domains) != 3 {
		t.Errorf("rule[0] domains count: got %d, want 3", len(r0.Domains))
	}

	r1 := rules[1]
	if r1.Name != "Google" {
		t.Errorf("rule[1].Name: got %q, want %q", r1.Name, "Google")
	}
	if len(r1.Domains) != 2 {
		t.Errorf("rule[1] domains count: got %d, want 2", len(r1.Domains))
	}
}

func TestParseNativeDomainConf_GeoSiteTags(t *testing.T) {
	content := `##Gaming
steam.com,geosite:category-games/WG0
` + managedBlock

	rules := parseNativeDomainConf(content)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	r := rules[0]
	if r.Name != "Gaming" {
		t.Errorf("Name: got %q, want %q", r.Name, "Gaming")
	}
	if r.Target != "WG0" {
		t.Errorf("Target: got %q, want %q", r.Target, "WG0")
	}
	found := false
	for _, d := range r.Domains {
		if d == "geosite:category-games" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected geosite:category-games in domains %v", r.Domains)
	}
}

func TestParseNativeDomainConf_Empty(t *testing.T) {
	content := managedBlock
	rules := parseNativeDomainConf(content)
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
}

// ---------------------------------------------------------------------------
// parseNativeIPList
// ---------------------------------------------------------------------------

func TestParseNativeIPList_Basic(t *testing.T) {
	content := `##Youtube
/HydraRoute
208.65.152.0/22
10.0.0.0/8

##Google
/HydraRoute
8.8.8.0/24
`

	blocks := parseNativeIPList(content)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	b0 := blocks[0]
	if b0.Name != "Youtube" {
		t.Errorf("block[0].Name: got %q, want %q", b0.Name, "Youtube")
	}
	if b0.Target != "HydraRoute" {
		t.Errorf("block[0].Target: got %q, want %q", b0.Target, "HydraRoute")
	}
	if len(b0.Subnets) != 2 {
		t.Errorf("block[0] subnets count: got %d, want 2", len(b0.Subnets))
	}

	b1 := blocks[1]
	if b1.Name != "Google" {
		t.Errorf("block[1].Name: got %q, want %q", b1.Name, "Google")
	}
	if len(b1.Subnets) != 1 {
		t.Errorf("block[1] subnets count: got %d, want 1", len(b1.Subnets))
	}
}

func TestParseNativeIPList_GeoIPTags(t *testing.T) {
	content := `##Russia
/WG0
5.8.8.0/24
geoip:ru
`

	blocks := parseNativeIPList(content)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Target != "WG0" {
		t.Errorf("Target: got %q, want %q", b.Target, "WG0")
	}
	found := false
	for _, s := range b.Subnets {
		if s == "geoip:ru" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected geoip:ru in subnets %v", b.Subnets)
	}
}

func TestParseNativeIPList_SkipsManaged(t *testing.T) {
	content := `## --- AWG Manager START ---
##ManagedBlock
/Wireguard0
1.2.3.0/24

## --- AWG Manager END ---
##NativeBlock
/HydraRoute
9.9.9.0/24
`

	blocks := parseNativeIPList(content)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block (native only), got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Name != "NativeBlock" {
		t.Errorf("expected NativeBlock, got %q", blocks[0].Name)
	}
}

// ---------------------------------------------------------------------------
// removeNativeBlocks
// ---------------------------------------------------------------------------

func TestRemoveNativeBlocks(t *testing.T) {
	content := `##Youtube
googlevideo.com/HydraRoute
` + managedBlock + `##Google
android.com/HydraRoute
`

	result := removeNativeBlocks(content)
	if result != managedBlock {
		t.Errorf("removeNativeBlocks result mismatch.\ngot:\n%q\nwant:\n%q", result, managedBlock)
	}
}

func TestRemoveNativeBlocks_NoMarkers(t *testing.T) {
	content := `##Youtube
googlevideo.com/HydraRoute
`
	result := removeNativeBlocks(content)
	if result != "" {
		t.Errorf("expected empty string when no markers, got %q", result)
	}
}
