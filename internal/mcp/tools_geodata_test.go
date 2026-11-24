package mcp_test

import (
	"strings"
	"testing"
)

func TestTools_GeoData(t *testing.T) {
	s, _ := newTestSession(t)

	if res, out := callTool(t, s, "list_geo_files", nil); res.IsError || len(out["files"].([]any)) != 2 {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	res, out := callTool(t, s, "list_geo_files", map[string]any{"path": "/opt/etc/awg-manager/geo/geoip.dat", "offset": 1})
	if tags, _ := out["tags"].(map[string]any); res.IsError || tags == nil || tags["total"] != float64(3) || len(tags["tags"].([]any)) != 2 {
		t.Fatalf("%s / %v", toolText(res), out)
	}
	if res, out := callTool(t, s, "update_geo_files", nil); res.IsError || out["updated"] != float64(1) {
		t.Fatalf("external files are not updated: %s / %v", toolText(res), out)
	}
	for name, c := range map[string]struct {
		tool string
		args map[string]any
	}{
		"relative path":    {"list_geo_files", map[string]any{"path": "geoip.dat"}},
		"dotdot path":      {"list_geo_files", map[string]any{"path": "/opt/../etc/passwd"}},
		"negative offset":  {"list_geo_files", map[string]any{"path": "/opt/etc/HydraRoute/geosite.dat", "offset": -1}},
		"unknown geo file": {"list_geo_files", map[string]any{"path": "/opt/x.dat"}},
		"update dotdot":    {"update_geo_files", map[string]any{"path": "/opt/../x"}},
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
		"dotdot path": {"list_geo_files", map[string]any{"path": "/opt/etc/../etc/passwd"}},
	} {
		if res, _ := callTool(t, s, c.tool, c.args); !res.IsError || !strings.Contains(toolText(res), "must be") {
			t.Errorf("%s: must be refused by the tool: %s", name, toolText(res))
		}
	}
}

func TestScope_ReadOnlyKeyAndGeoData(t *testing.T) {
	s := scopedSession(t, true)
	for name, args := range map[string]map[string]any{"list_geo_files": nil} {
		if res, _ := callTool(t, s, name, args); res.IsError {
			t.Errorf("%s must be readable: %s", name, toolText(res))
		}
	}
	for name, args := range map[string]map[string]any{
		"update_geo_files": {},
	} {
		res, _ := callTool(t, s, name, args)
		if !res.IsError || !strings.Contains(strings.ToLower(toolText(res)), "read-only") {
			t.Errorf("%s: a read-only key must be refused: %q", name, toolText(res))
		}
	}
}
