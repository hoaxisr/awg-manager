package wdtt

import (
	"testing"
)

func TestListServerDevicesRawAndWG(t *testing.T) {
	doc := passwordsJSON{
		MainPassword: "main",
		Passwords: map[string]passwordsJSONUser{
			"client1": {Comment: "Дом", DeviceID: "awgm-home"},
		},
		Devices: map[string]any{
			"awgm-home": map[string]any{"ip": "10.66.0.2", "raw_ip": "10.70.66.2", "comment": "роутер"},
			"awgm-vps":  map[string]any{"raw_ip": "10.70.66.3"},
			"wg-only":   map[string]any{"ip": "10.66.0.5"},
			gatewayReservedDeviceID: map[string]any{"ip": DefaultWdttServerGatewayAddr},
		},
	}

	wg := listServerDevices(doc, ServerDeviceModeWG)
	if len(wg) != 2 {
		t.Fatalf("wg devices = %d, want 2: %#v", len(wg), wg)
	}

	raw := listServerDevices(doc, ServerDeviceModeRaw)
	if len(raw) != 2 {
		t.Fatalf("raw devices = %d, want 2: %#v", len(raw), raw)
	}
	if raw[0].PasswordComment != "Дом" {
		t.Fatalf("password comment = %q", raw[0].PasswordComment)
	}
}

func TestValidateRawAndWGClientIP(t *testing.T) {
	devices := map[string]any{
		"a": map[string]any{"ip": "10.66.0.2", "raw_ip": "10.70.66.2"},
	}
	if err := validateWGClientIP("10.66.0.1", "", devices); err == nil {
		t.Fatal("gateway IP should be rejected")
	}
	if err := validateWGClientIP("10.66.0.3", "", devices); err != nil {
		t.Fatal(err)
	}
	if err := validateRawClientIP("10.70.0.2", "", devices); err != nil {
		t.Fatal(err)
	}
	if err := validateRawClientIP("10.70.66.2", "", devices); err == nil {
		t.Fatal("duplicate raw IP should be rejected")
	}
}

func TestRemoveAndUnbindPasswordsDevice(t *testing.T) {
	doc := passwordsJSON{
		Passwords: map[string]passwordsJSONUser{
			"p1": {DeviceID: "dev1", DeviceIDs: []string{"dev1", "dev2"}},
		},
		Devices: map[string]any{
			"dev1": map[string]any{"raw_ip": "10.70.66.2"},
			"dev2": map[string]any{"raw_ip": "10.70.66.3"},
		},
	}
	unbindPasswordsDevice(&doc, "dev1")
	if doc.Passwords["p1"].DeviceID != "" {
		t.Fatalf("device_id not cleared: %#v", doc.Passwords["p1"])
	}
	if len(doc.Passwords["p1"].DeviceIDs) != 1 || doc.Passwords["p1"].DeviceIDs[0] != "dev2" {
		t.Fatalf("device_ids = %#v", doc.Passwords["p1"].DeviceIDs)
	}
	removePasswordsDevice(&doc, "dev2")
	if _, ok := doc.Devices["dev2"]; ok {
		t.Fatal("device row not removed")
	}
}

func TestServiceListServerDevices(t *testing.T) {
	dir := t.TempDir()
	s := NewService(dir, dir, "/bin/sh", "/bin/sh")
	cfg := DefaultServerConfig()
	cfg.Password = "mainpass0000000000000000"
	if _, err := s.UpdateServerInstance(DefaultInstanceID, cfg); err != nil {
		t.Fatal(err)
	}
	cfgDir, err := s.serverConfigDir(DefaultInstanceID, cfg)
	if err != nil {
		t.Fatal(err)
	}
	doc := passwordsJSON{
		MainPassword: "main",
		Devices: map[string]any{
			"awgm-test": map[string]any{"raw_ip": "10.70.66.10"},
		},
	}
	if err := savePasswordsJSONDoc(cfgDir, doc); err != nil {
		t.Fatal(err)
	}
	st, err := s.ListServerDevices(DefaultInstanceID, ServerDeviceModeRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Devices) != 1 || st.Devices[0].RawIP != "10.70.66.10" {
		t.Fatalf("devices = %#v", st.Devices)
	}
}
