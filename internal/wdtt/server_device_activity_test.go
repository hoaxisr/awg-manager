package wdtt

import "testing"

func TestParseActiveDevicesFromServerLog(t *testing.T) {
	log := `[RAW] Сессия awgm-aaa зарегистрирована (ip=10.70.0.2, getConf=true)
[RAW] Relay 192.168.1.5:12345 → ip=10.70.0.3 (device=awgm-bbb)
[WG] Новое устройство fea9d79cc9370fe0 (IP: 10.66.0.3)
[RAW] Сессия awgm-aaa (ip=10.70.0.2) завершена
[RAW] Сессия awgm-ccc зарегистрирована (ip=10.70.0.4, getConf=false)
`
	active := parseActiveDevicesFromServerLog(log)
	if !active["awgm-bbb"] {
		t.Fatal("awgm-bbb should be active from relay line")
	}
	if !active["fea9d79cc9370fe0"] {
		t.Fatal("fea9d79cc9370fe0 should be active from WG line")
	}
	if !active["awgm-ccc"] {
		t.Fatal("awgm-ccc should be active")
	}
	if active["awgm-aaa"] {
		t.Fatal("awgm-aaa should be inactive after session end")
	}
}

func TestMergeDeviceActivity(t *testing.T) {
	devices := []ServerDeviceEntry{
		{DeviceID: "a"},
		{DeviceID: "b"},
	}
	out := mergeDeviceActivity(devices, map[string]bool{"a": true}, true)
	if !out[0].Active || !out[0].ActiveKnown {
		t.Fatal("device a should be active")
	}
	if out[1].Active || !out[1].ActiveKnown {
		t.Fatal("device b should be inactive but known")
	}
	if countActiveDevices(out) != 1 {
		t.Fatalf("count=%d", countActiveDevices(out))
	}
}

func TestActiveIDsFromStatsSnap(t *testing.T) {
	snap := serverStatsSnapshot{
		Active:    1,
		ActiveIDs: []string{"awgm-94f8c9011332fc74a5a84d6a"},
	}
	ids := activeIDsFromStatsSnap(snap)
	if len(ids) != 1 || !ids["awgm-94f8c9011332fc74a5a84d6a"] {
		t.Fatalf("ids=%v", ids)
	}
}

func TestFilterActiveIDsForMode(t *testing.T) {
	doc := passwordsJSON{
		Devices: map[string]any{
			"wg-only":  map[string]any{"ip": "10.66.0.2"},
			"raw-only": map[string]any{"raw_ip": "10.70.0.2"},
			"both":     map[string]any{"ip": "10.66.0.3", "raw_ip": "10.70.0.3"},
		},
	}
	in := map[string]bool{"wg-only": true, "raw-only": true, "both": true}
	wg := filterActiveIDsForMode(in, doc, ServerDeviceModeWG)
	if !wg["wg-only"] || !wg["both"] || wg["raw-only"] {
		t.Fatalf("wg filter: %v", wg)
	}
	raw := filterActiveIDsForMode(in, doc, ServerDeviceModeRaw)
	if !raw["raw-only"] || !raw["both"] || raw["wg-only"] {
		t.Fatalf("raw filter: %v", raw)
	}
}
