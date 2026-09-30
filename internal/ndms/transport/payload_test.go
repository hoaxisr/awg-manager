package transport

import (
	"encoding/json"
	"testing"
)

// marshal is a test helper — payload builders return `any` because the
// production caller (Client.Post) marshals via encoding/json, so the
// builder type is opaque. To assert shape we round-trip through JSON.
func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestShowInterface_BareName(t *testing.T) {
	got := marshal(t, ShowInterface("GigabitEthernet0/Vlan2", nil))
	want := `{"show":{"interface":{"name":"GigabitEthernet0/Vlan2"}}}`
	if got != want {
		t.Errorf("\n  got  %s\n  want %s", got, want)
	}
}

func TestShowInterface_WithDetails(t *testing.T) {
	got := marshal(t, ShowInterface("Wireguard0", map[string]any{"details": "yes"}))
	// Map iteration order is randomised, so don't assert string-equal;
	// re-decode and compare shape.
	var decoded map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	iface := decoded["show"]["interface"]
	if iface["name"] != "Wireguard0" || iface["details"] != "yes" {
		t.Errorf("unexpected interface payload: %v", iface)
	}
	if len(iface) != 2 {
		t.Errorf("expected exactly 2 keys (name, details), got %d: %v", len(iface), iface)
	}
}

func TestShowInterface_NilExtraStillEmitsName(t *testing.T) {
	got := marshal(t, ShowInterface("Wireguard0", nil))
	want := `{"show":{"interface":{"name":"Wireguard0"}}}`
	if got != want {
		t.Errorf("\n  got  %s\n  want %s", got, want)
	}
}

func TestShowQuery_EmptyArgsListingForm(t *testing.T) {
	got := marshal(t, ShowQuery([]string{"interface"}, nil))
	want := `{"show":{"interface":{}}}`
	if got != want {
		t.Errorf("\n  got  %s\n  want %s", got, want)
	}
}

func TestShowQuery_NestedPath(t *testing.T) {
	got := marshal(t, ShowQuery([]string{"ip", "route"}, map[string]any{"prefix": "0.0.0.0/0"}))
	want := `{"show":{"ip":{"route":{"prefix":"0.0.0.0/0"}}}}`
	if got != want {
		t.Errorf("\n  got  %s\n  want %s", got, want)
	}
}

func TestShowQuery_ArgsAreCopied(t *testing.T) {
	// Caller's map must not leak into the produced payload — otherwise
	// later mutations would corrupt an in-flight request.
	src := map[string]any{"name": "X"}
	out := ShowQuery([]string{"interface"}, src)
	src["name"] = "MUTATED"
	got := marshal(t, out)
	want := `{"show":{"interface":{"name":"X"}}}`
	if got != want {
		t.Errorf("payload reflected caller mutation\n  got  %s\n  want %s", got, want)
	}
}
