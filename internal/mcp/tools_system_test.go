package mcp_test

import (
	"testing"
)

// TestTools_MonitoringMatrixLabelsSingboxRows — строка sing-box в матрице
// выглядела как туннель AWG без единой ячейки. Агент читал это как
// «туннель не отвечает», хотя матрица такие строки просто не меряет.
func TestTools_MonitoringMatrixLabelsSingboxRows(t *testing.T) {
	s, _ := newTestSession(t)

	res, out := callTool(t, s, "get_monitoring_matrix", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	rows := map[string]map[string]any{}
	for _, r := range out["tunnels"].([]any) {
		row := r.(map[string]any)
		rows[row["id"].(string)] = row
	}
	cells := map[string]bool{}
	for _, c := range out["cells"].([]any) {
		cells[c.(map[string]any)["tunnelId"].(string)] = true
	}

	awg := rows["tn-1"]
	if awg["source"] != "awg" || awg["probed"] != true || !cells["tn-1"] {
		t.Fatalf("an AWG row = %v", awg)
	}
	proxy := rows["vless-nl"]
	if proxy["source"] != "singbox" || proxy["singboxTag"] != "vless-nl" || proxy["probed"] != false {
		t.Fatalf("a sing-box proxy row = %v", proxy)
	}
	member := rows["sub-706dcf33-a1"]
	if member["source"] != "singbox" || member["subscription"] != true || member["probed"] != false {
		t.Fatalf("a subscription's active server = %v", member)
	}
	if member["urltestGroup"] != "sub-706dcf33" || member["urltestDelayMs"] != float64(48) {
		t.Fatalf("the engine's own delay must come with the group it belongs to: %v", member)
	}
	// probed=false and a cell would contradict each other.
	for id, row := range rows {
		if row["probed"] != cells[id] {
			t.Errorf("%s: probed=%v, has a cell=%v", id, row["probed"], cells[id])
		}
	}
}
