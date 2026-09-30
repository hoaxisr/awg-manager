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
