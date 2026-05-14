package singbox

import (
	"encoding/json"
	"testing"
)

func TestAllocUniqueTunnelTag(t *testing.T) {
	used := map[string]bool{"a": true, "a-2": true}
	if got := allocUniqueTunnelTag(used, "a"); got != "a-3" {
		t.Fatalf("got %q want a-3", got)
	}
	used["b"] = true
	if got := allocUniqueTunnelTag(used, "b"); got != "b-2" {
		t.Fatalf("got %q want b-2", got)
	}
	if got := allocUniqueTunnelTag(used, "fresh"); got != "fresh" {
		t.Fatalf("got %q want fresh", got)
	}
	if got := allocUniqueTunnelTag(map[string]bool{}, ""); got != "tunnel" {
		t.Fatalf("empty base: got %q", got)
	}
}

func TestOutboundJSONWithTag(t *testing.T) {
	raw := json.RawMessage(`{"type":"vless","tag":"old","server":"x"}`)
	out, err := outboundJSONWithTag(raw, "new")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["tag"] != "new" {
		t.Fatalf("tag=%v", m["tag"])
	}
	if m["type"] != "vless" {
		t.Fatalf("type=%v", m["type"])
	}
}
