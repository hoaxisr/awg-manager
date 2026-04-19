package hydraroute

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdoptExternalFiles_AddsUnknownFiles(t *testing.T) {
	tmp := t.TempDir()
	origHrDir := hrDir
	hrDir = tmp
	defer func() { hrDir = origHrDir }()

	store := &GeoDataStore{
		storagePath: filepath.Join(tmp, "hydraroute-geodata.json"),
		tagCache:    make(map[string][]GeoTag),
	}

	geositePath := filepath.Join(tmp, "geosite.dat")
	geoipPath := filepath.Join(tmp, "geoip.dat")
	if err := os.WriteFile(geositePath, []byte("fake-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(geoipPath, []byte("fake-content"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		GeoSiteFiles: []string{geositePath},
		GeoIPFiles:   []string{geoipPath},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 2 {
		t.Fatalf("adopted count = %d, want 2", n)
	}

	entries := store.List()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if !e.External {
			t.Errorf("entry %q: External=false, want true", e.Path)
		}
		if e.URL != "" {
			t.Errorf("entry %q: URL=%q, want empty", e.Path, e.URL)
		}
	}
}

func TestAdoptExternalFiles_SkipsAlreadyTracked(t *testing.T) {
	tmp := t.TempDir()
	origHrDir := hrDir
	hrDir = tmp
	defer func() { hrDir = origHrDir }()
	existingPath := filepath.Join(tmp, "existing.dat")
	if err := os.WriteFile(existingPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &GeoDataStore{
		storagePath: filepath.Join(tmp, "hydraroute-geodata.json"),
		tagCache:    make(map[string][]GeoTag),
		entries: []GeoFileEntry{
			{Type: "geosite", Path: existingPath, URL: "https://example.com/f.dat"},
		},
	}

	cfg := &Config{
		GeoSiteFiles: []string{existingPath},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 0 {
		t.Fatalf("adopted = %d, want 0 (path already tracked)", n)
	}
	if len(store.entries) != 1 {
		t.Fatalf("entries = %d, want 1 (no duplicate)", len(store.entries))
	}
	if store.entries[0].External {
		t.Error("pre-existing tracked entry should not be marked External")
	}
}

func TestAdoptExternalFiles_SkipsMissingFiles(t *testing.T) {
	tmp := t.TempDir()
	origHrDir := hrDir
	hrDir = tmp
	defer func() { hrDir = origHrDir }()
	store := &GeoDataStore{
		storagePath: filepath.Join(tmp, "hydraroute-geodata.json"),
		tagCache:    make(map[string][]GeoTag),
	}
	cfg := &Config{
		GeoSiteFiles: []string{filepath.Join(tmp, "does-not-exist.dat")},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 0 {
		t.Fatalf("adopted = %d, want 0 (file missing)", n)
	}
	if len(store.entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(store.entries))
	}
}

func TestAdoptExternalFiles_NilConfig(t *testing.T) {
	store := &GeoDataStore{
		storagePath: filepath.Join(t.TempDir(), "hydraroute-geodata.json"),
		tagCache:    make(map[string][]GeoTag),
	}
	n, err := store.AdoptExternalFiles(nil)
	if err != nil {
		t.Fatalf("AdoptExternalFiles(nil): %v", err)
	}
	if n != 0 {
		t.Fatalf("adopted = %d, want 0", n)
	}
}

func TestAdoptExternalFiles_SkipsOutsideHrDir(t *testing.T) {
	tmp := t.TempDir()
	origHrDir := hrDir
	hrDir = filepath.Join(tmp, "hr")
	if err := os.Mkdir(hrDir, 0755); err != nil {
		t.Fatal(err)
	}
	defer func() { hrDir = origHrDir }()

	// A file outside hrDir — reachable on disk but should not be adopted.
	outsidePath := filepath.Join(tmp, "outside.dat")
	if err := os.WriteFile(outsidePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// A file inside hrDir — should be adopted.
	insidePath := filepath.Join(hrDir, "inside.dat")
	if err := os.WriteFile(insidePath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &GeoDataStore{
		storagePath: filepath.Join(tmp, "hydraroute-geodata.json"),
		tagCache:    make(map[string][]GeoTag),
	}
	cfg := &Config{
		GeoSiteFiles: []string{outsidePath, insidePath},
	}

	n, err := store.AdoptExternalFiles(cfg)
	if err != nil {
		t.Fatalf("AdoptExternalFiles: %v", err)
	}
	if n != 1 {
		t.Fatalf("adopted = %d, want 1 (only path under hrDir)", n)
	}
	if len(store.entries) != 1 || store.entries[0].Path != insidePath {
		t.Fatalf("entries = %+v, want only %q", store.entries, insidePath)
	}
}

func TestUpdate_RejectsExternalEntryWithoutURL(t *testing.T) {
	tmp := t.TempDir()
	origHrDir := hrDir
	hrDir = tmp
	defer func() { hrDir = origHrDir }()

	path := filepath.Join(tmp, "adopted.dat")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &GeoDataStore{
		storagePath: filepath.Join(tmp, "hydraroute-geodata.json"),
		tagCache:    make(map[string][]GeoTag),
		entries: []GeoFileEntry{
			{Type: "geosite", Path: path, URL: "", External: true},
		},
	}

	_, err := store.Update(path)
	if err == nil {
		t.Fatal("Update returned nil, expected error for external entry")
	}
	want := "cannot update external file: no source URL on record"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err, want)
	}
}
