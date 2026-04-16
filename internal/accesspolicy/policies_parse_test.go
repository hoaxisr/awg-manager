package accesspolicy

import (
	"reflect"
	"testing"
)

func TestParsePoliciesRC_FullPolicy(t *testing.T) {
	raw := []byte(`{
		"Policy0": {
			"description": "Mallware",
			"permit": [
				{"enabled": true, "interface": "PPPoE0"},
				{"enabled": true, "interface": "Wireguard0"}
			]
		}
	}`)

	got, err := parsePoliciesRC(raw)
	if err != nil {
		t.Fatal(err)
	}

	p, ok := got["Policy0"]
	if !ok {
		t.Fatal("Policy0 missing")
	}
	if p.description != "Mallware" {
		t.Errorf("description = %q, want Mallware", p.description)
	}
	if p.standalone {
		t.Error("standalone should be false when key absent")
	}
	want := []PermittedIface{
		{Name: "PPPoE0", Order: 0},
		{Name: "Wireguard0", Order: 1},
	}
	if !reflect.DeepEqual(p.interfaces, want) {
		t.Errorf("interfaces = %+v, want %+v", p.interfaces, want)
	}
}

// Regression: HydraRoute-style custom policy has no description field but
// still lists permit[]. Previous text-running-config parser returned empty
// interfaces here because it expected a different block layout.
func TestParsePoliciesRC_CustomPolicyNoDescription(t *testing.T) {
	raw := []byte(`{
		"HydraRoute": {
			"permit": [
				{"enabled": true, "interface": "PPPoE0"}
			]
		}
	}`)

	got, err := parsePoliciesRC(raw)
	if err != nil {
		t.Fatal(err)
	}

	p, ok := got["HydraRoute"]
	if !ok {
		t.Fatal("HydraRoute missing")
	}
	if p.description != "" {
		t.Errorf("description = %q, want empty", p.description)
	}
	if len(p.interfaces) != 1 || p.interfaces[0].Name != "PPPoE0" {
		t.Errorf("interfaces = %+v, want [PPPoE0]", p.interfaces)
	}
}

func TestParsePoliciesRC_DisabledInterfaceMarkedDenied(t *testing.T) {
	raw := []byte(`{
		"Policy1": {
			"description": "Kids",
			"permit": [
				{"enabled": true,  "interface": "Wireguard0"},
				{"enabled": false, "interface": "PPPoE0"}
			]
		}
	}`)

	got, _ := parsePoliciesRC(raw)
	p := got["Policy1"]

	if len(p.interfaces) != 2 {
		t.Fatalf("want 2 interfaces, got %+v", p.interfaces)
	}
	if p.interfaces[0].Denied {
		t.Error("Wireguard0 must not be denied")
	}
	if !p.interfaces[1].Denied {
		t.Error("PPPoE0 must be denied (enabled=false)")
	}
}

func TestParsePoliciesRC_StandaloneFlag(t *testing.T) {
	raw := []byte(`{
		"Policy2": {
			"description": "Guests",
			"standalone": {},
			"permit": []
		}
	}`)

	got, _ := parsePoliciesRC(raw)
	p := got["Policy2"]

	if !p.standalone {
		t.Error("standalone should be true when key present")
	}
	if len(p.interfaces) != 0 {
		t.Errorf("interfaces = %+v, want empty", p.interfaces)
	}
}

func TestParsePoliciesRC_EmptyMap(t *testing.T) {
	got, err := parsePoliciesRC([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want empty map, got %+v", got)
	}
}
