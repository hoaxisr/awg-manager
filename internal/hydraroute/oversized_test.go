package hydraroute

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOversizedTags_EnrichesWithCounts(t *testing.T) {
	svc, _, ipPath := setupRuleFiles(t)
	svc.geodata = setupGeoDataWithTags(t, map[string][]GeoTag{
		"ru-blocked": {{Name: "ru-blocked", Count: 82411}},
		"cn-heavy":   {{Name: "cn-heavy", Count: 128953}},
	})

	if err := os.WriteFile(ipPath, []byte(
		"##impossible to use\n"+
			"#/Too-big-geoip-tag\n"+
			"geoip:ru-blocked\n"+
			"geoip:cn-heavy\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := svc.OversizedTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := []OversizedTag{
		{Name: "geoip:ru-blocked", Count: 82411, File: svc.geodata.entries[0].Path},
		{Name: "geoip:cn-heavy", Count: 128953, File: svc.geodata.entries[0].Path},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestOversizedTags_UnknownTagHasNegativeCount(t *testing.T) {
	svc, _, ipPath := setupRuleFiles(t)
	svc.geodata = setupGeoDataWithTags(t, map[string][]GeoTag{
		"ru-blocked": {{Name: "ru-blocked", Count: 82411}},
	})

	if err := os.WriteFile(ipPath, []byte(
		"##impossible to use\n"+
			"#/Too-big-geoip-tag\n"+
			"geoip:gone-from-dat\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := svc.OversizedTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "geoip:gone-from-dat" || got[0].Count != -1 {
		t.Errorf("want one entry with count=-1, got %+v", got)
	}
}

// setupGeoDataWithTags stages an in-memory GeoDataStore with a single
// geoip file whose tag cache is pre-populated from the provided map.
// The temp directory is installed as hrDir so GeoDataStore.GetTags
// accepts paths rooted under it.
func setupGeoDataWithTags(t *testing.T, tagsByName map[string][]GeoTag) *GeoDataStore {
	t.Helper()
	dir := t.TempDir()
	origHRDir := hrDir
	hrDir = dir
	t.Cleanup(func() { hrDir = origHRDir })

	path := filepath.Join(dir, "geoip.dat")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	gds := NewGeoDataStore(dir)
	gds.entries = []GeoFileEntry{{Type: "geoip", Path: path}}
	all := make([]GeoTag, 0, len(tagsByName))
	for _, v := range tagsByName {
		all = append(all, v...)
	}
	gds.tagCache[path] = all
	return gds
}
