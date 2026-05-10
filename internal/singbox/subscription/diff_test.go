package subscription

import (
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

func mkParsed(server string, port uint16, scheme string) vlink.ParsedOutbound {
	ob := map[string]any{"type": scheme, "server": server, "server_port": port}
	if scheme == "vless" {
		ob["uuid"] = "00000000-0000-0000-0000-000000000000"
	} else if scheme == "trojan" {
		ob["password"] = "p"
	}
	raw, _ := json.Marshal(ob)
	return vlink.ParsedOutbound{
		Tag:      "tmp",
		Protocol: scheme,
		Server:   server,
		Port:     port,
		Outbound: raw,
	}
}

func TestStableTag_DeterministicAcrossInputs(t *testing.T) {
	a := StableTag("subID", mkParsed("h", 443, "vless"))
	b := StableTag("subID", mkParsed("h", 443, "vless"))
	if a != b {
		t.Errorf("stable hash should be deterministic: %s vs %s", a, b)
	}
}

func TestStableTag_DifferentServerProducesDifferentTag(t *testing.T) {
	a := StableTag("subID", mkParsed("h1", 443, "vless"))
	b := StableTag("subID", mkParsed("h2", 443, "vless"))
	if a == b {
		t.Errorf("different server should yield different tag: %s == %s", a, b)
	}
}

func TestApplyDiff_NewMembers(t *testing.T) {
	current := []string{}
	parsed := []vlink.ParsedOutbound{
		mkParsed("h1", 443, "vless"),
		mkParsed("h2", 443, "trojan"),
	}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.New) != 2 {
		t.Errorf("New=%d want 2", len(diff.New))
	}
	if len(diff.Existing) != 0 || len(diff.Orphan) != 0 {
		t.Errorf("expected only New, got Existing=%d Orphan=%d", len(diff.Existing), len(diff.Orphan))
	}
}

func TestApplyDiff_ExistingDetected(t *testing.T) {
	parsed := []vlink.ParsedOutbound{
		mkParsed("h1", 443, "vless"),
	}
	tag := StableTag("subID", parsed[0])
	current := []string{tag}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.Existing) != 1 || len(diff.New) != 0 {
		t.Errorf("expected Existing=1 New=0, got %+v", diff)
	}
}

func TestApplyDiff_OrphanDetected(t *testing.T) {
	current := []string{"sub-subID-orphanhash"}
	parsed := []vlink.ParsedOutbound{}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.Orphan) != 1 || diff.Orphan[0] != "sub-subID-orphanhash" {
		t.Errorf("expected Orphan=[sub-subID-orphanhash], got %+v", diff.Orphan)
	}
}

func TestApplyDiff_SkipsDuplicates(t *testing.T) {
	current := []string{}
	// Three identical outbounds (same protocol+server+port+credential → same StableTag)
	parsed := []vlink.ParsedOutbound{
		mkParsed("h1", 443, "vless"),
		mkParsed("h1", 443, "vless"),
		mkParsed("h1", 443, "vless"),
	}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.New) != 1 {
		t.Errorf("New=%d want 1 (dedup)", len(diff.New))
	}
	if diff.SkippedDuplicate != 2 {
		t.Errorf("SkippedDuplicate=%d want 2", diff.SkippedDuplicate)
	}
}

func TestApplyDiff_DuplicatesAcrossNewAndExisting(t *testing.T) {
	parsed1 := mkParsed("h1", 443, "vless")
	tag := StableTag("subID", parsed1)
	current := []string{tag}
	parsed := []vlink.ParsedOutbound{parsed1, parsed1}
	diff := ApplyDiff("subID", current, parsed)
	if len(diff.Existing) != 1 || len(diff.New) != 0 {
		t.Errorf("expected Existing=1 New=0, got %+v", diff)
	}
	if diff.SkippedDuplicate != 1 {
		t.Errorf("SkippedDuplicate=%d want 1", diff.SkippedDuplicate)
	}
}
