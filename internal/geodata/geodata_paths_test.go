package geodata

import (
	"path/filepath"
	"testing"
)

func TestIsManagedPath_AcceptsGeoAndHRDirs(t *testing.T) {
	tmp := t.TempDir()
	origHR := hrDir
	hrDir = filepath.Join(tmp, "HydraRoute")
	defer func() { hrDir = origHR }()

	store := &GeoDataStore{
		geoDir: filepath.Join(tmp, "geo"),
	}
	if !store.isManagedPath(filepath.Join(store.geoDir, "a.dat")) {
		t.Fatal("geoDir path should be managed")
	}
	if !store.isManagedPath(filepath.Join(hrDir, "b.dat")) {
		t.Fatal("hrDir path should be managed")
	}
	if store.isManagedPath(filepath.Join(tmp, "other.dat")) {
		t.Fatal("unrelated path should not be managed")
	}
}
