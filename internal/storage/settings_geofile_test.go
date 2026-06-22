package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeoFileSettings_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewSettingsStore(dir)
	if _, err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	cur, _ := s.Get()
	cur.GeoFile = GeoFileSettings{AutoRefreshEnabled: true, RefreshIntervalHours: 6, RefreshMode: "interval"}
	if err := s.Save(cur); err != nil {
		t.Fatalf("save: %v", err)
	}
	s2 := NewSettingsStore(dir)
	got, err := s2.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !got.GeoFile.AutoRefreshEnabled || got.GeoFile.RefreshIntervalHours != 6 {
		t.Fatalf("geoFile not persisted: %+v", got.GeoFile)
	}
}

func TestMigrateToV27_StampsVersionAndDefaultsDisabled(t *testing.T) {
	dir := t.TempDir()
	// Seed a v26 settings.json without geoFile.
	old := `{"schemaVersion":26}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSettingsStore(dir)
	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("schema not bumped: %d", got.SchemaVersion)
	}
	if got.GeoFile.AutoRefreshEnabled {
		t.Fatalf("geoFile must default to disabled")
	}
}
