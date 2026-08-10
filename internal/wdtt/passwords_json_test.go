package wdtt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizePasswordsDevices_RemovesGatewayIP(t *testing.T) {
	devices := map[string]any{
		"good": map[string]any{"ip": "10.66.0.2"},
		"bad":  map[string]any{"ip": DefaultWdttServerGatewayAddr},
	}
	out, changed := sanitizePasswordsDevices(devices)
	if !changed {
		t.Fatal("expected changed=true")
	}
	if len(out) != 1 {
		t.Fatalf("devices = %#v", out)
	}
	if _, ok := out["good"]; !ok {
		t.Fatalf("good device removed: %#v", out)
	}
}

func TestPreparePasswordsJSONForServer_PreservesDevices(t *testing.T) {
	dir := t.TempDir()
	existing := passwordsJSON{
		MainPassword: "main",
		Devices: map[string]any{
			"dev1": map[string]any{"ip": "10.66.0.3", "pub_key": "abc"},
		},
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwordsJSONPath(dir), data, 0600); err != nil {
		t.Fatal(err)
	}
	doc, sanitized, err := preparePasswordsJSONForServer(dir, "main", "", "", []ServerClient{
		{Password: "client1", Comment: "Иван"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sanitized {
		t.Fatal("sanitized should be false for valid device IP")
	}
	if len(doc.Devices) != 2 {
		t.Fatalf("devices not preserved/reserved: %#v", doc.Devices)
	}
	if _, ok := doc.Devices["dev1"]; !ok {
		t.Fatalf("dev1 missing: %#v", doc.Devices)
	}
	if _, ok := doc.Passwords["client1"]; !ok {
		t.Fatalf("client password missing: %#v", doc.Passwords)
	}
}

func TestReserveGatewayIPInDevices(t *testing.T) {
	out := reserveGatewayIPInDevices(map[string]any{})
	if len(out) != 1 {
		t.Fatalf("reservation missing: %#v", out)
	}
	for _, entry := range out {
		if deviceIPFromPasswordsEntry(entry) != DefaultWdttServerGatewayAddr {
			t.Fatalf("unexpected ip: %#v", entry)
		}
	}
	if len(reserveGatewayIPInDevices(out)) != 1 {
		t.Fatal("duplicate reservation")
	}
}

func TestSyncPasswordsJSON_DropsGatewayDevice(t *testing.T) {
	dir := t.TempDir()
	existing := passwordsJSON{
		MainPassword: "main",
		Devices: map[string]any{
			"bad": map[string]any{"ip": DefaultWdttServerGatewayAddr},
		},
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "passwords.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := syncPasswordsJSON(dir, "main", "", "", nil); err != nil {
		t.Fatal(err)
	}
	out, err := loadPasswordsJSON(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Devices) != 1 {
		t.Fatalf("expected gateway reservation only: %#v", out.Devices)
	}
	if deviceIPFromPasswordsEntry(out.Devices["__awgm_gateway_reserved__"]) != DefaultWdttServerGatewayAddr {
		t.Fatalf("reservation missing: %#v", out.Devices)
	}
}
