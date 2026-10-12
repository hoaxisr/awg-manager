package hydraroute

import (
	"context"
	"errors"
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
func setupGeoDataWithTags(t *testing.T, tagsByName map[string][]GeoTag) *GeoDataStore {
	t.Helper()
	dir := t.TempDir()

	gds := NewGeoDataStore(dir)
	path := filepath.Join(gds.geoDir, "geoip.dat")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	gds.entries = []GeoFileEntry{{Type: "geoip", Path: path}}
	all := make([]GeoTag, 0, len(tagsByName))
	for _, v := range tagsByName {
		all = append(all, v...)
	}
	gds.tagCache[path] = all
	return gds
}

func TestRemoveOversizedTag(t *testing.T) {
	const rule = "## 2ip\n/HydraRoute\ngeoip:RU\n\n"
	const seed = rule +
		"##impossible to use\n#/Too-big-geoip-tag\ngeoip:ru-blocked\ngeoip:cn\n"

	t.Run("one of several", func(t *testing.T) {
		svc, domainPath, ipPath := setupRuleFiles(t)
		if err := os.WriteFile(ipPath, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		// Регистр и префикс geoip: не важны — как HR Neo сравнивает теги.
		if err := svc.RemoveOversizedTag(" RU-Blocked "); err != nil {
			t.Fatal(err)
		}
		want := rule + "##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn\n"
		if got := readFileOrEmpty(t, ipPath); got != want {
			t.Errorf("ip.list:\n%s\nwant:\n%s", got, want)
		}
		if _, err := os.Stat(domainPath); !os.IsNotExist(err) {
			t.Errorf("domain.conf must not be written, stat err = %v", err)
		}
		svc.mu.Lock()
		restart := svc.restartTimer != nil
		svc.mu.Unlock()
		if restart {
			t.Error("HR Neo restart scheduled, want none: rules did not change")
		}
	})

	t.Run("last tag drops the section", func(t *testing.T) {
		svc, _, ipPath := setupRuleFiles(t)
		if err := os.WriteFile(ipPath, []byte(rule+"##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := svc.RemoveOversizedTag("geoip:cn"); err != nil {
			t.Fatal(err)
		}
		if got := readFileOrEmpty(t, ipPath); got != rule {
			t.Errorf("ip.list:\n%s\nwant:\n%s", got, rule)
		}
	})

	// domain.conf и ip.list расходятся (другой интерфейс, правило отключено
	// только в domain.conf): запись из RuleSet слила бы их, но удаление тега не
	// должно трогать правила — ip.list меняется только в служебном разделе.
	t.Run("rules are not merged with domain.conf", func(t *testing.T) {
		svc, domainPath, ipPath := setupRuleFiles(t)
		const domain = "## 2ip\n#2ip.ru/Wireguard0\n"
		const ip = "## 2ip\n/HydraRoute\ngeoip:RU\n\n" +
			"# свой комментарий\n" +
			"## only-ip\n#/Proxy0\n10.0.0.0/8\n\n" +
			"##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn\ngeoip:ru-blocked\n"
		if err := os.WriteFile(domainPath, []byte(domain), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ipPath, []byte(ip), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := svc.RemoveOversizedTag("geoip:cn"); err != nil {
			t.Fatal(err)
		}
		want := "## 2ip\n/HydraRoute\ngeoip:RU\n\n" +
			"# свой комментарий\n" +
			"## only-ip\n#/Proxy0\n10.0.0.0/8\n\n" +
			"##impossible to use\n#/Too-big-geoip-tag\ngeoip:ru-blocked\n"
		if got := readFileOrEmpty(t, ipPath); got != want {
			t.Errorf("ip.list:\n%s\nwant:\n%s", got, want)
		}
		if got := readFileOrEmpty(t, domainPath); got != domain {
			t.Errorf("domain.conf changed:\n%s", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		svc, _, ipPath := setupRuleFiles(t)
		if err := os.WriteFile(ipPath, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := svc.RemoveOversizedTag("geoip:ru"); !errors.Is(err, ErrOversizedTagNotFound) {
			t.Fatalf("err = %v, want ErrOversizedTagNotFound", err)
		}
		if got := readFileOrEmpty(t, ipPath); got != seed {
			t.Errorf("ip.list changed:\n%s", got)
		}
	})

	t.Run("missing ip.list", func(t *testing.T) {
		svc, _, ipPath := setupRuleFiles(t)
		if err := svc.RemoveOversizedTag("geoip:cn"); !errors.Is(err, ErrOversizedTagNotFound) {
			t.Fatalf("err = %v, want ErrOversizedTagNotFound", err)
		}
		if _, err := os.Stat(ipPath); !os.IsNotExist(err) {
			t.Errorf("ip.list must not be created, stat err = %v", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		svc, _, _ := setupRuleFiles(t)
		for _, name := range []string{"", " geoip: ", "GEOIP:"} {
			if err := svc.RemoveOversizedTag(name); !errors.Is(err, ErrInvalidOversizedTag) {
				t.Errorf("RemoveOversizedTag(%q) err = %v, want ErrInvalidOversizedTag", name, err)
			}
		}
	})

	t.Run("not installed", func(t *testing.T) {
		svc, _, ipPath := setupRuleFiles(t)
		if err := os.WriteFile(ipPath, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		svc.SetStatusForTest(false)
		if err := svc.RemoveOversizedTag("geoip:cn"); !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("err = %v, want ErrNotInstalled", err)
		}
		if got := readFileOrEmpty(t, ipPath); got != seed {
			t.Errorf("ip.list changed:\n%s", got)
		}
	})
}

func TestRemoveOversizedTagText(t *testing.T) {
	const svcHead = "##impossible to use\n#/Too-big-geoip-tag\n"
	cases := []struct {
		name    string
		in      string
		tag     string
		want    string
		removed bool
	}{
		{
			name:    "one of several",
			in:      "## A\n/Wireguard0\n1.1.1.1\n\n" + svcHead + "geoip:ru-blocked\ngeoip:cn\ngeoip:us\n",
			tag:     "geoip:cn",
			want:    "## A\n/Wireguard0\n1.1.1.1\n\n" + svcHead + "geoip:ru-blocked\ngeoip:us\n",
			removed: true,
		},
		{
			// В файле тег записан не в том регистре и с отключающей #.
			name:    "case-insensitive file line",
			in:      svcHead + "geoip:CN\n#geoip:Cn\ngeoip:us\n",
			tag:     "geoip:cn",
			want:    svcHead + "geoip:us\n",
			removed: true,
		},
		{
			name: "duplicates across two sections",
			in: "## A\n/Wireguard0\n1.1.1.1\n\n" +
				svcHead + "geoip:cn\ngeoip:us\n\n" +
				"## B\n/Proxy0\n2.2.2.2\n\n" +
				svcHead + "geoip:cn\ngeoip:cn\ngeoip:de\n",
			tag: "geoip:cn",
			want: "## A\n/Wireguard0\n1.1.1.1\n\n" +
				svcHead + "geoip:us\n\n" +
				"## B\n/Proxy0\n2.2.2.2\n\n" +
				svcHead + "geoip:de\n",
			removed: true,
		},
		{
			// Опустевший раздел посреди файла уходит вместе со своей пустой
			// строкой — двух пустых строк подряд не остаётся.
			name: "last tag of a middle section",
			in: "## A\n/Wireguard0\n1.1.1.1\n\n" +
				svcHead + "geoip:cn\n\n" +
				"## B\n/Proxy0\n2.2.2.2\n\n" +
				svcHead + "geoip:de\n",
			tag: "geoip:cn",
			want: "## A\n/Wireguard0\n1.1.1.1\n\n" +
				"## B\n/Proxy0\n2.2.2.2\n\n" +
				svcHead + "geoip:de\n",
			removed: true,
		},
		{
			name:    "last tag at the start of the file",
			in:      svcHead + "geoip:cn\n\n## A\n/Wireguard0\n1.1.1.1\n",
			tag:     "geoip:cn",
			want:    "## A\n/Wireguard0\n1.1.1.1\n",
			removed: true,
		},
		{
			name:    "last tag of the only section",
			in:      svcHead + "geoip:cn\n",
			tag:     "geoip:cn",
			want:    "",
			removed: true,
		},
		{
			// Раздел сразу за правилом, без пустой строки: правило остаётся
			// как было, пустая строка-разделитель перед ## B тоже.
			name:    "section glued to a rule",
			in:      "## A\n/Wireguard0\n1.1.1.1\n" + svcHead + "geoip:cn\n\n## B\n/Proxy0\n2.2.2.2\n",
			tag:     "geoip:cn",
			want:    "## A\n/Wireguard0\n1.1.1.1\n\n## B\n/Proxy0\n2.2.2.2\n",
			removed: true,
		},
		{
			// Правила, комментарии, нераспознанные блоки, дубли ## и
			// отключённое правило пользователя с именем «impossible to use»,
			// но другим target остаются байт в байт.
			name: "everything else stays byte-identical",
			in: "# мой комментарий\n" +
				"## A\n/Wireguard0\n1.1.1.1\n\n" +
				"## A\n/Proxy0\n3.3.3.3\n\n" +
				"##impossible to use\n#/Wireguard0\ngeoip:cn\n\n" +
				"stray line\n\n\n" +
				"## broken\n\n" +
				svcHead + "geoip:cn\ngeoip:us\n",
			tag: "geoip:cn",
			want: "# мой комментарий\n" +
				"## A\n/Wireguard0\n1.1.1.1\n\n" +
				"## A\n/Proxy0\n3.3.3.3\n\n" +
				"##impossible to use\n#/Wireguard0\ngeoip:cn\n\n" +
				"stray line\n\n\n" +
				"## broken\n\n" +
				svcHead + "geoip:us\n",
			removed: true,
		},
		{
			name: "CRLF line endings",
			in: "## A\r\n/Wireguard0\r\n1.1.1.1\r\n\r\n" +
				"##impossible to use\r\n#/Too-big-geoip-tag\r\ngeoip:cn\r\ngeoip:us\r\n\r\n" +
				"## B\r\n/Proxy0\r\n2.2.2.2\r\n",
			tag: "geoip:cn",
			want: "## A\r\n/Wireguard0\r\n1.1.1.1\r\n\r\n" +
				"##impossible to use\r\n#/Too-big-geoip-tag\r\ngeoip:us\r\n\r\n" +
				"## B\r\n/Proxy0\r\n2.2.2.2\r\n",
			removed: true,
		},
		{
			name:    "no trailing newline",
			in:      svcHead + "geoip:us\ngeoip:cn",
			tag:     "geoip:cn",
			want:    svcHead + "geoip:us\n",
			removed: true,
		},
		{
			// Тег только в правиле пользователя — не в служебном разделе.
			name:    "not found",
			in:      "## A\n/Wireguard0\ngeoip:cn\n\n" + svcHead + "geoip:us\n",
			tag:     "geoip:cn",
			want:    "## A\n/Wireguard0\ngeoip:cn\n\n" + svcHead + "geoip:us\n",
			removed: false,
		},
		{
			name:    "empty file",
			in:      "",
			tag:     "geoip:cn",
			want:    "",
			removed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, removed := removeOversizedTag(tc.in, tc.tag)
			if removed != tc.removed {
				t.Errorf("removed = %v, want %v", removed, tc.removed)
			}
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
			if !tc.removed {
				return
			}
			// Результат читается так же, как исходный файл, но без тега.
			inEntries, inTags := parseCIDRHalves(tc.in)
			gotEntries, gotTags := parseCIDRHalves(got)
			if !reflect.DeepEqual(gotEntries, inEntries) {
				t.Errorf("rules changed:\n%+v\nwant:\n%+v", gotEntries, inEntries)
			}
			for _, tag := range gotTags {
				if normalizeOversizedTag(tag) == tc.tag {
					t.Errorf("tag %q still parsed from result: %v", tc.tag, gotTags)
				}
			}
			if len(gotTags) >= len(inTags) {
				t.Errorf("oversized tags %v, want fewer than %v", gotTags, inTags)
			}
		})
	}
}
