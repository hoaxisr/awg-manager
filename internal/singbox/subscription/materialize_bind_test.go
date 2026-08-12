package subscription

import (
	"encoding/json"
	"testing"
)

func TestMaterializeMemberOutbound(t *testing.T) {
	raw := []byte(`{"type":"vless","server":"1.2.3.4","server_port":443}`)
	got := materializeMemberOutbound(raw, "sub-abc-def", "eth3")
	var ob map[string]any
	if err := json.Unmarshal(got, &ob); err != nil {
		t.Fatal(err)
	}
	if ob["tag"] != "sub-abc-def" {
		t.Fatalf("tag = %v", ob["tag"])
	}
	if ob["bind_interface"] != "eth3" {
		t.Fatalf("bind_interface = %v", ob["bind_interface"])
	}

	cleared := materializeMemberOutbound(got, "sub-abc-def", "")
	var clearedOb map[string]any
	if err := json.Unmarshal(cleared, &clearedOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := clearedOb["bind_interface"]; ok {
		t.Fatalf("expected bind_interface removed, got %v", clearedOb["bind_interface"])
	}
}
