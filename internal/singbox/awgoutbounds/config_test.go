// internal/singbox/awgoutbounds/config_test.go
package awgoutbounds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveFile_AtomicAndContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "15-awg.json")

	entries := []AWGEntry{
		{Tag: "awg-tunnel-a", Label: "A", Kind: "managed", Iface: "t2s0"},
		{Tag: "awg-sys-Wireguard0", Label: "W0", Kind: "system", Iface: "nwg0"},
	}
	if err := saveFile(path, entries); err != nil {
		t.Fatalf("saveFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 2 {
		t.Fatalf("want 2 outbounds, got %d", len(got.Outbounds))
	}
	first := got.Outbounds[0]
	if first["type"] != "direct" || first["tag"] != "awg-tunnel-a" || first["bind_interface"] != "t2s0" {
		t.Errorf("first outbound shape wrong: %+v", first)
	}
}

func TestSaveFile_EmptyEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "15-awg.json")
	if err := saveFile(path, nil); err != nil {
		t.Fatalf("saveFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) == "" {
		t.Fatalf("expected non-empty file")
	}
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 0 {
		t.Errorf("want 0 outbounds, got %d", len(got.Outbounds))
	}
}

func TestSaveFile_OverwriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "15-awg.json")
	if err := os.WriteFile(path, []byte(`{"old":"junk"}`), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := saveFile(path, []AWGEntry{{Tag: "awg-x", Iface: "t2s0"}}); err != nil {
		t.Fatalf("saveFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var got struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 1 {
		t.Errorf("expected file to be replaced, got %d outbounds", len(got.Outbounds))
	}
}
