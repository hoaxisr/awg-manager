package mcp_test

import (
	"strings"
	"testing"
)

func TestTools_HydraRoute(t *testing.T) {
	s, f := newTestSession(t)

	if res, out := callTool(t, s, "get_hydraroute_status", nil); res.IsError || out["state"] != "running" || out["disabledTags"] == nil {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "control_hydraroute", map[string]any{"action": " Stop "}); res.IsError || out["running"] != false {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	res, _ := callTool(t, s, "set_hydraroute_settings", map[string]any{"ipsetMaxElem": 131072, "conntrackFlush": true})
	if res.IsError || f.Hydra.Settings.IpsetMaxElem != 131072 || !f.Hydra.Settings.ConntrackFlush || !f.Hydra.Settings.AutoStart {
		t.Fatalf("%s / %+v", toolText(res), f.Hydra.Settings)
	}
	if res, out := callTool(t, s, "list_hydraroute_geo_files", nil); res.IsError || len(out["files"].([]any)) != 2 {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	res, out := callTool(t, s, "list_hydraroute_geo_files", map[string]any{"path": "/opt/etc/awg-manager/geo/geoip.dat", "offset": 1})
	if tags, _ := out["tags"].(map[string]any); res.IsError || tags == nil || tags["total"] != float64(3) || len(tags["tags"].([]any)) != 2 {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "update_hydraroute_geo_files", nil); res.IsError || out["updated"] != float64(1) {
		t.Fatalf("external files are not updated: %s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "remove_hydraroute_disabled_tag", map[string]any{"name": "RU"}); res.IsError || out["removed"] != "RU" || len(f.Hydra.DisabledTags) != 0 {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"bad action":          {"control_hydraroute", map[string]any{"action": "reload"}},
		"no settings":         {"set_hydraroute_settings", map[string]any{}},
		"maxelem 0":           {"set_hydraroute_settings", map[string]any{"ipsetMaxElem": 0}},
		"maxelem huge":        {"set_hydraroute_settings", map[string]any{"ipsetMaxElem": 16777217}},
		"timeout 0":           {"set_hydraroute_settings", map[string]any{"ipsetTimeout": 0}},
		"timeout huge":        {"set_hydraroute_settings", map[string]any{"ipsetTimeout": 2147484}},
		"relative path":       {"list_hydraroute_geo_files", map[string]any{"path": "geoip.dat"}},
		"dotdot path":         {"list_hydraroute_geo_files", map[string]any{"path": "/opt/../etc/passwd"}},
		"negative offset":     {"list_hydraroute_geo_files", map[string]any{"path": "/opt/etc/HydraRoute/geosite.dat", "offset": -1}},
		"unknown geo file":    {"list_hydraroute_geo_files", map[string]any{"path": "/opt/x.dat"}},
		"update dotdot":       {"update_hydraroute_geo_files", map[string]any{"path": "/opt/../x"}},
		"tag with spaces":     {"remove_hydraroute_disabled_tag", map[string]any{"name": "ru ; rm"}},
		"tag not disabled":    {"remove_hydraroute_disabled_tag", map[string]any{"name": "us"}},
		"global routing kept": {"set_hydraroute_settings", map[string]any{"globalRouting": true}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError {
			t.Errorf("%s: must be refused", name)
		}
	}
	// Shape checks answer before the router is asked.
	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"dotdot path":     {"list_hydraroute_geo_files", map[string]any{"path": "/opt/etc/../etc/passwd"}},
		"tag with spaces": {"remove_hydraroute_disabled_tag", map[string]any{"name": "ru ; rm"}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError || !strings.Contains(toolText(res), "must be") {
			t.Errorf("%s: must be refused by the tool: %s", name, toolText(res))
		}
	}
}

func TestScope_ReadOnlyKeyAndHydraRoute(t *testing.T) {
	s := scopedSession(t, true)
	for name, args := range map[string]map[string]any{"get_hydraroute_status": nil, "list_hydraroute_geo_files": nil} {
		if res, _ := callTool(t, s, name, args); res.IsError {
			t.Errorf("%s must be readable: %s", name, toolText(res))
		}
	}
	for name, args := range map[string]map[string]any{
		"control_hydraroute":             {"action": "stop"},
		"set_hydraroute_settings":        {"autoStart": false},
		"update_hydraroute_geo_files":    {},
		"remove_hydraroute_disabled_tag": {"name": "ru"},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Errorf("%s: a read-only key must be refused: %q", name, toolText(res))
		}
	}
}
