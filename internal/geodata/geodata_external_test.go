package geodata

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newExternalGeoStore — каталог прежней версии: файл лежит в каталоге другой
// программы и записан в hydraroute-geodata.json.
func newExternalGeoStore(t *testing.T, flagged bool) (*GeoDataStore, string) {
	t.Helper()
	root := t.TempDir()
	orig := hrDir
	hrDir = filepath.Join(root, "HydraRoute")
	t.Cleanup(func() { hrDir = orig })
	dataDir := filepath.Join(root, "awg-manager")
	for _, d := range []string{hrDir, dataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(hrDir, "geoip_GA.dat")
	if err := os.WriteFile(path, buildGeoDAT([][]byte{buildGeoEntry(1, "RU", 2, 3)}), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(geoDataJSON{Files: []GeoFileEntry{{Type: "geoip", Path: path, URL: GroundZerroGeoIPURL, External: flagged}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, geoDataFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return NewGeoDataStore(dataDir), path
}

// Чужой файл не удаляется ни с диска, ни из каталога: от него зависят готовые
// правила. Признак — путь, а не флаг: в старом JSON флага могло не быть.
func TestDelete_ExternalRefused(t *testing.T) {
	for _, flagged := range []bool{true, false} {
		s, path := newExternalGeoStore(t, flagged)
		if err := s.Delete(path); err == nil {
			t.Fatalf("flagged=%v: Delete чужого файла прошёл", flagged)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("flagged=%v: чужой файл удалён: %v", flagged, err)
		}
		if geoIP, _ := s.GeoFilePaths(); len(geoIP) != 1 {
			t.Fatalf("flagged=%v: правила потеряли файл: %v", flagged, geoIP)
		}
	}
}

// Чужие записи места в лимите не занимают.
func TestDownload_ExternalNotCounted(t *testing.T) {
	s, _ := newExternalGeoStore(t, true)
	ext := s.entries[0]
	for range maxGeoFiles {
		s.entries = append(s.entries, ext)
	}
	_, err := s.Download("geoip", "http://host.invalid/geoip.dat")
	if err == nil || strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("err = %v, want ошибка загрузки, не лимита", err)
	}
}

// Наш файл удаляется и с диска.
func TestDelete_OwnRemovesFile(t *testing.T) {
	s := newTestGeoStore(t)
	path := filepath.Join(s.geoDir, "geoip.dat")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.entries = []GeoFileEntry{{Type: "geoip", Path: path}}
	if err := s.Delete(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл остался: %v", err)
	}
}

// Правила sing-box продолжают читать файл, подключённый до обновления, но в
// списке панели его нет.
func TestExternal_ReadableAfterUpgrade(t *testing.T) {
	s, path := newExternalGeoStore(t, false)
	if files := s.List(); len(files) != 0 {
		t.Fatalf("List = %+v, want пусто", files)
	}
	tags, err := s.GetTags(path)
	if err != nil || len(tags) != 1 || tags[0].Name != "RU" {
		t.Fatalf("GetTags = %+v, %v", tags, err)
	}
	if c := s.GeoIPTagCounts(); c["ru"] != 3 {
		t.Fatalf("GeoIPTagCounts = %v, want ru=3", c)
	}
	if _, err := s.Update(path); err == nil {
		t.Fatal("чужой файл не обновляется")
	}
}
