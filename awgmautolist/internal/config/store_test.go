package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaults(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	cfg := store.Config()
	if cfg.WANCheckURL != DefaultWANCheckURL || !cfg.TunnelProbe || cfg.AutoAdd ||
		cfg.WANMonitorEnabled || cfg.UDPEnabled || cfg.Verbose {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.AddsPerHour != DefaultAddsPerHour {
		t.Fatalf("addsPerHour=%d", cfg.AddsPerHour)
	}
}

func TestPatchPreservesUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStore(path)
	if _, err := store.Patch([]byte(`{"future":{"enabled":true},"autoAdd":false}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Patch([]byte(`{"verbose":true}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	future, ok := got["future"].(map[string]any)
	if !ok || future["enabled"] != true || got["verbose"] != true || got["autoAdd"] != false {
		t.Fatalf("unknown/config fields were not preserved: %s", data)
	}
}

func TestDisablingTunnelPersistsAutoAddOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStore(path)
	if _, err := store.Patch([]byte(`{"targetInterface":"nocli0","autoAdd":true}`)); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Patch([]byte(`{"tunnelProbe":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoAdd || cfg.TunnelProbe {
		t.Fatalf("invalid tunnel/auto state: %+v", cfg)
	}
	reloaded := NewStore(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if reloaded.Config().AutoAdd || reloaded.Config().TunnelProbe {
		t.Fatalf("state was not persisted: %+v", reloaded.Config())
	}
}

func TestReenablingTunnelDoesNotRestoreAutoAdd(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.Patch([]byte(`{"targetInterface":"nocli0","autoAdd":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Patch([]byte(`{"tunnelProbe":false}`)); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.Patch([]byte(`{"tunnelProbe":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoAdd {
		t.Fatal("stale raw autoAdd=true was restored")
	}
}

func TestAutoAddCannotBeEnabledWithoutTunnel(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.Patch([]byte(`{"tunnelProbe":false,"autoAdd":true}`)); err == nil {
		t.Fatal("autoAdd was enabled without tunnelProbe")
	}
	if _, err := store.Patch([]byte(`{"targetInterface":"nocli0","autoAdd":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Patch([]byte(`{"autoAdd":true,"tunnelProbe":false}`)); err == nil {
		t.Fatal("explicit invalid patch succeeded")
	}
}

func TestAddsPerHourPatchPersistsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := NewStore(path)
	if _, err := store.Patch([]byte(`{"addsPerHour":75}`)); err != nil {
		t.Fatal(err)
	}
	reloaded := NewStore(path)
	if _, err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Config().AddsPerHour; got != 75 {
		t.Fatalf("addsPerHour=%d", got)
	}
	for _, raw := range []string{`{"addsPerHour":0}`, `{"addsPerHour":501}`, `{"addsPerHour":null}`, `{"addsPerHour":"50"}`} {
		if _, err := store.Patch([]byte(raw)); err == nil {
			t.Fatalf("invalid patch accepted: %s", raw)
		}
	}
}

func TestLegacyResolverMigratesOnlyInMemoryUntilSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"resolveVia":"awgm","probeInterface":"eth4","autoAdd":false}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	report, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if store.Config().ResolveVia != "" || len(report.Warnings) != 1 {
		t.Fatalf("migration failed: cfg=%+v report=%+v", store.Config(), report)
	}
	before, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, original) {
		t.Fatal("Load rewrote flash config")
	}
	if _, err := store.Patch([]byte(`{"verbose":true}`)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var fields map[string]any
	_ = json.Unmarshal(data, &fields)
	resolveVia, present := fields["resolveVia"]
	if fields["probeInterface"] != "eth4" || (present && resolveVia != "") {
		t.Fatalf("legacy unknown field or migration lost: %s", data)
	}
}

func TestLoadRejectsInvalidKnownFields(t *testing.T) {
	for _, input := range []string{
		`null`, `[]`, `{"autoAdd":null}`, `{"autoAdd":"yes"}`,
		`{"wanCheckUrl":"file:///etc/passwd"}`, `{"excludeDomains":["bad/name"]}`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(path).Load(); err == nil {
			t.Fatalf("accepted invalid config: %s", input)
		}
	}
}

func TestConfigSnapshotOwnsSlices(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.Patch([]byte(`{"excludeDomains":["example.com"]}`)); err != nil {
		t.Fatal(err)
	}
	cfg := store.Config()
	cfg.ExcludeDomains[0] = "changed.example"
	if got := store.Config().ExcludeDomains; !reflect.DeepEqual(got, []string{"example.com"}) {
		t.Fatalf("snapshot mutated store: %v", got)
	}
}
