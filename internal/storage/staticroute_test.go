package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaticRouteList_IconURL_RoundTrip(t *testing.T) {
	t.Run("iconUrl survives marshal/unmarshal", func(t *testing.T) {
		orig := StaticRouteList{
			ID:       "sr_1",
			Name:     "datacenter",
			TunnelID: "tun_de",
			IconURL:  "https://cdn.jsdelivr.net/gh/Koolson/Qure@master/IconSet/Color/Cloudflare.png",
			Subnets:  []string{"10.0.0.0/8"},
			Enabled:  true,
		}
		raw, err := json.Marshal(orig)
		if err != nil {
			t.Fatal(err)
		}
		var got StaticRouteList
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if got.IconURL != orig.IconURL {
			t.Errorf("IconURL = %q, want %q", got.IconURL, orig.IconURL)
		}
	})

	t.Run("empty iconUrl is omitted in JSON", func(t *testing.T) {
		raw, err := json.Marshal(StaticRouteList{ID: "sr_1", Name: "x"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "iconUrl") {
			t.Errorf("expected iconUrl to be omitted, got: %s", raw)
		}
	})

	t.Run("legacy JSON without iconUrl unmarshals fine", func(t *testing.T) {
		legacy := []byte(`{"id":"sr_1","name":"x","tunnelID":"t","subnets":["10.0.0.0/8"],"enabled":true,"createdAt":"","updatedAt":""}`)
		var got StaticRouteList
		if err := json.Unmarshal(legacy, &got); err != nil {
			t.Fatal(err)
		}
		if got.IconURL != "" {
			t.Errorf("IconURL = %q, want empty string", got.IconURL)
		}
	})
}

// Стор статических маршрутов — персистентный: правки обязаны доезжать до
// файла, иначе список маршрутов исчезнет с перезапуском демона. Улика —
// содержимое файла на диске и ЧУЖОЙ стор, читающий его заново: кэш `s.data`
// в памяти проходил бы тест и без записи.
func TestStaticRouteStore_PersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "static-routes.json")
	s := NewStaticRouteStore(dir)

	if err := s.AddRouteList(StaticRouteList{ID: "srl1", Name: "A", TunnelID: "awg10",
		Subnets: []string{"192.0.2.0/24"}, Enabled: true}); err != nil {
		t.Fatalf("AddRouteList: %v", err)
	}
	if !strings.Contains(readFileOrFail(t, path), `"srl1"`) {
		t.Fatalf("список не записан на диск:\n%s", readFileOrFail(t, path))
	}

	// Свежий стор ходит на диск, а не в чужую память.
	fresh, err := NewStaticRouteStore(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(fresh.RouteLists) != 1 || fresh.RouteLists[0].ID != "srl1" ||
		fresh.RouteLists[0].Name != "A" || fresh.RouteLists[0].TunnelID != "awg10" {
		t.Fatalf("перечитанные с диска данные не совпали: %+v", fresh.RouteLists)
	}

	if err := s.Save(&StaticRouteData{RouteLists: []StaticRouteList{
		{ID: "srl1", Name: "B"}, {ID: "srl2", Name: "C"},
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := NewStaticRouteStore(dir).Load()
	if err != nil {
		t.Fatalf("Load после Save: %v", err)
	}
	if len(after.RouteLists) != 2 || after.RouteLists[0].Name != "B" {
		t.Fatalf("Save не перезаписал файл: %+v", after.RouteLists)
	}

	if err := s.DeleteRouteList("srl1"); err != nil {
		t.Fatalf("DeleteRouteList: %v", err)
	}
	if strings.Contains(readFileOrFail(t, path), `"srl1"`) {
		t.Fatalf("удалённый список остался на диске:\n%s", readFileOrFail(t, path))
	}
	left, err := NewStaticRouteStore(dir).Load()
	if err != nil {
		t.Fatalf("Load после Delete: %v", err)
	}
	if len(left.RouteLists) != 1 || left.RouteLists[0].ID != "srl2" {
		t.Fatalf("после удаления на диске не тот состав: %+v", left.RouteLists)
	}
}

func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
