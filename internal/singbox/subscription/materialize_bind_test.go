package subscription

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeMemberOutbound(t *testing.T) {
	tempNet := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tempNet, "eth3"), 0755)
	oldRoot := sysClassNet
	sysClassNet = tempNet
	t.Cleanup(func() { sysClassNet = oldRoot })

	raw := []byte(`{"type":"vless","server":"1.2.3.4","server_port":443}`)
	got := materializeMemberOutbound(context.Background(), nil, raw, "sub-abc-def", "eth3")
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

	cleared := materializeMemberOutbound(context.Background(), nil, got, "sub-abc-def", "")
	var clearedOb map[string]any
	if err := json.Unmarshal(cleared, &clearedOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := clearedOb["bind_interface"]; ok {
		t.Fatalf("expected bind_interface removed, got %v", clearedOb["bind_interface"])
	}

	// Non-existent interface is stripped to prevent sing-box FATAL
	missing := materializeMemberOutbound(context.Background(), nil, raw, "sub-abc-def", "eth-missing")
	var missingOb map[string]any
	if err := json.Unmarshal(missing, &missingOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := missingOb["bind_interface"]; ok {
		t.Fatalf("expected missing bind_interface stripped, got %v", missingOb["bind_interface"])
	}
}
