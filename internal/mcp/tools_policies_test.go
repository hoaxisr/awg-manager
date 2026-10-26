package mcp_test

import (
	"strings"
	"testing"
)

func TestTools_AccessPolicyLifecycle(t *testing.T) {
	s, f := newTestSession(t)

	res, out := callTool(t, s, "create_access_policy", map[string]any{"description": " Kids "})
	if res.IsError {
		t.Fatal(toolText(res))
	}
	name := out["name"].(string)
	if res, out := callTool(t, s, "set_access_policy_interface", map[string]any{"policy": name, "interface": "Wireguard0", "permit": true}); res.IsError || len(out["interfaces"].([]any)) != 1 {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "update_access_policy", map[string]any{"policy": name, "standalone": true}); res.IsError || out["standalone"] != true {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	mac := f.Devices[0].MAC
	if res, out := callTool(t, s, "assign_device_policy", map[string]any{"mac": strings.ToUpper(mac), "policy": name}); res.IsError || out["policy"] != name {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "delete_access_policy", map[string]any{"policy": name}); res.IsError || out["deleted"].(map[string]any)["name"] != name {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"empty description": {"create_access_policy", map[string]any{"description": " "}},
		"bad policy name":   {"delete_access_policy", map[string]any{"policy": "../x"}},
		"nothing to change": {"update_access_policy", map[string]any{"policy": "Policy0"}},
		"unknown interface": {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "Ghost0", "permit": true}},
		"bad interface":     {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "a b", "permit": true}},
		"negative order":    {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "ISP", "permit": true, "order": -1}},
		"stale order":       {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "ISP", "permit": true, "order": 0, "expectOrder": []any{"Ghost0"}}},
		"order, no expect":  {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "ISP", "permit": true, "order": 0}},
		"order on removal":  {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "Wireguard0", "permit": false, "order": 0, "expectOrder": []any{"Wireguard0"}}},
		"remove absent":     {"set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "Ghost0", "permit": false}},
		"spaced name":       {"create_access_policy", map[string]any{"description": "Kids room"}},
		"bad mac":           {"assign_device_policy", map[string]any{"mac": "not-a-mac", "policy": "Policy0"}},
		"unknown device":    {"assign_device_policy", map[string]any{"mac": "11:22:33:44:55:66", "policy": "Policy0"}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// TestTools_AccessPolicyInterfaceGoesLastByDefault — без order интерфейс
// встаёт последним, а с order — первым только по явной просьбе с
// expectOrder.
func TestTools_AccessPolicyInterfaceGoesLastByDefault(t *testing.T) {
	s, _ := newTestSession(t)
	res, out := callTool(t, s, "set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "ISP", "permit": true})
	if res.IsError || strings.Join(toStrings(out["interfaces"]), ",") != "Wireguard0,ISP" {
		t.Fatalf("%s / %v", toolText(res), out["interfaces"])
	}
	res, out = callTool(t, s, "set_access_policy_interface", map[string]any{"policy": "Policy0", "interface": "ISP", "permit": true, "order": 0, "expectOrder": []any{"Wireguard0", "ISP"}})
	if res.IsError || strings.Join(toStrings(out["interfaces"]), ",") != "ISP,Wireguard0" {
		t.Fatalf("%s / %v", toolText(res), out["interfaces"])
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}
