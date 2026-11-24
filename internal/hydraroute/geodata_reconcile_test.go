package hydraroute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReconcile_FixesStaleExternalOnAWGMPaths(t *testing.T) {
	store := newTestGeoStore(t)
	awgmPath := filepath.Join(store.geoDir, "geoip_GA.dat")
	if err := os.WriteFile(awgmPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geoip", Path: awgmPath, External: true, URL: GroundZerroGeoIPURL},
	}
	store.mu.Unlock()

	store.mu.Lock()
	changed := store.reconcileUnlocked()
	store.mu.Unlock()
	if !changed {
		t.Fatal("expected reconcile to fix external flag")
	}
	if store.entries[0].External {
		t.Fatal("AWGM path should not be external after reconcile")
	}
}

func TestReconcile_PruneMissingFiles(t *testing.T) {
	store := newTestGeoStore(t)
	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geoip", Path: filepath.Join(store.geoDir, "missing.dat"), External: false},
	}
	changed := store.reconcileUnlocked()
	store.mu.Unlock()
	if !changed {
		t.Fatal("expected reconcile to drop missing file")
	}
	if len(store.entries) != 0 {
		t.Fatalf("entries = %+v, want empty", store.entries)
	}
}

func TestReconcile_KeepsBothAWGMAndHRWhenBothExist(t *testing.T) {
	tmp := t.TempDir()
	origHR := hrDir
	hrDir = filepath.Join(tmp, "HydraRoute")
	if err := os.MkdirAll(hrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	defer func() { hrDir = origHR }()

	store := newTestGeoStore(t)
	awgmPath := filepath.Join(store.geoDir, "geoip_GA.dat")
	hrPath := filepath.Join(hrDir, "geofile", "geoip_GA.dat")
	if err := os.MkdirAll(filepath.Dir(hrPath), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{awgmPath, hrPath} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	store.mu.Lock()
	store.entries = []GeoFileEntry{
		{Type: "geoip", Path: hrPath, External: false},
		{Type: "geoip", Path: awgmPath, External: true},
	}
	changed := store.reconcileUnlocked()
	store.mu.Unlock()
	if !changed {
		t.Fatal("expected reconcile to fix external flags")
	}
	if len(store.entries) != 2 {
		t.Fatalf("entries = %d, want 2 (AWGM + HR)", len(store.entries))
	}
	byPath := map[string]GeoFileEntry{}
	for _, e := range store.entries {
		byPath[e.Path] = e
	}
	if byPath[awgmPath].External {
		t.Fatal("AWGM entry should not be external")
	}
	if !byPath[hrPath].External {
		t.Fatal("HR entry should be external")
	}
}

func TestLoad_ReconcilesPersistedJSON(t *testing.T) {
	tmp := t.TempDir()
	geoDir := filepath.Join(tmp, "geo")
	if err := os.MkdirAll(geoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	awgmPath := filepath.Join(geoDir, "geoip_GA.dat")
	if err := os.WriteFile(awgmPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	storagePath := filepath.Join(tmp, "hydraroute-geodata.json")
	doc := `{"files":[{"type":"geoip","path":"` + awgmPath + `","external":true,"url":"https://example.com/x.dat"}]}`
	if err := os.WriteFile(storagePath, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	store := &GeoDataStore{
		storagePath: storagePath,
		geoDir:      geoDir,
		tagCache:    make(map[string][]GeoTag),
	}
	if err := store.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(store.entries) != 1 || store.entries[0].External {
		t.Fatalf("after load: %+v", store.entries)
	}
	raw, err := os.ReadFile(storagePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == doc {
		t.Fatal("expected load to persist reconciled external flag")
	}
}

func TestList_ReconcilesMissingFileOnDisk(t *testing.T) {
	store := newTestGeoStore(t)
	path := filepath.Join(store.geoDir, "vanished.dat")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	store.entries = []GeoFileEntry{{Type: "geoip", Path: path}}
	store.mu.Unlock()

	if len(store.List()) != 1 {
		t.Fatal("expected one entry before manual delete")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if entries := store.List(); len(entries) != 0 {
		t.Fatalf("List after manual delete: %+v, want empty", entries)
	}
}

func TestDedupeGeoPaths(t *testing.T) {
	in := []string{"/a/x.dat", "/b/x.dat", "/a/x.dat"}
	out := dedupeGeoPaths(in)
	if len(out) != 2 || out[0] != "/a/x.dat" || out[1] != "/b/x.dat" {
		t.Fatalf("got %v", out)
	}
}
