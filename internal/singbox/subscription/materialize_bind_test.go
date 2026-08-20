package subscription

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeMemberOutbound(t *testing.T) {
	tempNet := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tempNet, "eth3"), 0755)
	oldRoot := sysClassNet
	sysClassNet = tempNet
	t.Cleanup(func() { sysClassNet = oldRoot })

	raw := []byte(`{"type":"vless","server":"1.2.3.4","server_port":443}`)
	got := materializeMemberOutbound(raw, "sub-abc-def", "eth3", nil)
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

	cleared := materializeMemberOutbound(got, "sub-abc-def", "", nil)
	var clearedOb map[string]any
	if err := json.Unmarshal(cleared, &clearedOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := clearedOb["bind_interface"]; ok {
		t.Fatalf("expected bind_interface removed, got %v", clearedOb["bind_interface"])
	}

	// Non-existent interface is stripped to prevent sing-box FATAL, and logs warning
	var loggedAction, loggedTarget, loggedMsg string
	logFn := func(action, target, msg string) {
		loggedAction = action
		loggedTarget = target
		loggedMsg = msg
	}
	missing := materializeMemberOutbound(raw, "sub-abc-def", "eth-missing", logFn)
	var missingOb map[string]any
	if err := json.Unmarshal(missing, &missingOb); err != nil {
		t.Fatal(err)
	}
	if _, ok := missingOb["bind_interface"]; ok {
		t.Fatalf("expected missing bind_interface stripped, got %v", missingOb["bind_interface"])
	}
	if loggedAction != "subscription-bind" || loggedTarget != "sub-abc-def" || !strings.Contains(loggedMsg, "eth-missing") {
		t.Fatalf("expected warning logged, got action=%q target=%q msg=%q", loggedAction, loggedTarget, loggedMsg)
	}
}
