package dnsroute

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_DropsLegacyHRRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dns-routes.json")
	raw := `{"lists":[
		{"id":"list_1","name":"ndms-rule","backend":"ndms","manualDomains":["a.com"],"domains":["a.com"]},
		{"id":"list_2","name":"hr-rule","backend":"hydraroute","manualDomains":["2ip.ru"],"domains":["2ip.ru"]}
	]}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(dir)
	data, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}

	// HR-строка убрана из кэша, NDMS осталась.
	if len(data.Lists) != 1 || data.Lists[0].Name != "ndms-rule" {
		t.Fatalf("expected only ndms-rule kept, got %+v", data.Lists)
	}
	for _, l := range data.Lists {
		if isHydraRoute(l.Backend) {
			t.Fatalf("hydraroute row not dropped: %+v", l)
		}
	}

	// Файл на диске перезаписан без HR-строки.
	reread, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reread), "hydraroute") {
		t.Fatalf("disk file still contains hydraroute row: %s", reread)
	}
}
