package dnsroute

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F700: NDMS не принимает geo-теги в object-group fqdn ни в каком регистре
// (стенд KN-1810 5.02.B.1, 20.10: include geosite:GOOGLE / geosite:google /
// geoip:ru → status error 101582220 «invalid FQDN or IP address» и E в
// журнале ndm). NDMS-список с тегом отвергается на записи; записанные
// раньше теги снимает миграция store; строка-тег в подписке пропускается.

func TestNDMSList_GeoTagRejectedOnWrite(t *testing.T) {
	ctx := context.Background()
	text := "example.com\ngeosite:GOOGLE\n"
	cases := []struct {
		name string
		call func(svc *ServiceImpl, id string) error
	}{
		{"CreateManual", func(svc *ServiceImpl, _ string) error {
			_, err := svc.Create(ctx, DomainList{Name: "g", ManualDomains: []string{"a.example", "geosite:GOOGLE"}, Routes: routedOpkgTun0})
			return err
		}},
		{"CreateManualText", func(svc *ServiceImpl, _ string) error {
			_, err := svc.Create(ctx, DomainList{Name: "g", ManualText: &text, Routes: routedOpkgTun0})
			return err
		}},
		{"CreateExcludes", func(svc *ServiceImpl, _ string) error {
			_, err := svc.Create(ctx, DomainList{Name: "g", ManualDomains: []string{"a.example"}, Excludes: []string{"geoip:ru"}, Routes: routedOpkgTun0})
			return err
		}},
		{"CreateMixedCasePrefix", func(svc *ServiceImpl, _ string) error {
			_, err := svc.Create(ctx, DomainList{Name: "g", ManualDomains: []string{"a.example", "GeoSite:x"}, Routes: routedOpkgTun0})
			return err
		}},
		{"UpdateMixedCasePrefix", func(svc *ServiceImpl, id string) error {
			_, err := svc.Update(ctx, DomainList{ID: id, ManualDomains: []string{"seed.example", "GeoSite:x"}})
			return err
		}},
		{"UpdateManual", func(svc *ServiceImpl, id string) error {
			_, err := svc.Update(ctx, DomainList{ID: id, ManualDomains: []string{"seed.example", "geosite:google"}})
			return err
		}},
		{"UpdateExcludesText", func(svc *ServiceImpl, id string) error {
			ex := "geoip:RU\n"
			_, err := svc.Update(ctx, DomainList{ID: id, ExcludesText: &ex})
			return err
		}},
		{"CreateBatch", func(svc *ServiceImpl, _ string) error {
			_, err := svc.CreateBatch(ctx, []DomainList{
				{Name: "ok", ManualDomains: []string{"ok.example"}, Routes: routedOpkgTun0},
				{Name: "g", ManualDomains: []string{"geosite:GOOGLE"}, Routes: routedOpkgTun0},
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, poster, before := newCreateSubsService(t, &urlDownloader{})
			id := svc.store.GetCached().Lists[0].ID
			cached := snapshot(t, svc.store)
			posts := len(poster.Payloads())

			err := tc.call(svc, id)
			if err == nil || !strings.Contains(err.Error(), "не поддерживается NDMS") {
				t.Fatalf("err = %v, want отказ geo-тега", err)
			}
			now, rerr := os.ReadFile(svc.store.path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if !bytes.Equal(now, before) {
				t.Error("файл store изменён")
			}
			if snapshot(t, svc.store) != cached {
				t.Error("кэш изменён")
			}
			if got := len(poster.Payloads()); got != posts {
				t.Errorf("ушли POST: %d → %d", posts, got)
			}
		})
	}
}

// Комментарий с geo-тегом — не тег: запись проходит, строка остаётся в
// сыром тексте редактора.
func TestNDMSList_GeoTagCommentAccepted(t *testing.T) {
	svc, _, _ := newCreateSubsService(t, &urlDownloader{})
	text := "example.com\n# geosite:x\n"
	created, err := svc.Create(context.Background(), DomainList{Name: "c", ManualText: &text, Routes: routedOpkgTun0})
	if err != nil {
		t.Fatal(err)
	}
	stored := storedList(t, svc, created.ID)
	if stored.ManualText == nil || *stored.ManualText != text {
		t.Errorf("ManualText = %q, want %q", deref(stored.ManualText), text)
	}
	if strings.Join(stored.ManualDomains, ",") != "example.com" {
		t.Errorf("ManualDomains = %v, want example.com", stored.ManualDomains)
	}
}

// HydraRoute geo-теги понимает: его путь отказ не трогает.
func TestHRList_GeoTagStillAccepted(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, _ := newHRTestSvc(t, resolver)
	if _, err := svc.Create(context.Background(), DomainList{
		Name: "hr", Backend: "hydraroute", ManualDomains: []string{"geosite:GOOGLE"},
		HRRouteMode: "interface", Routes: []RouteTarget{{TunnelID: "awg10"}},
	}); err != nil {
		t.Fatalf("HR Create: %v", err)
	}
	if _, err := svc.CreateBatch(context.Background(), []DomainList{{
		Name: "hr2", Backend: "hydraroute", ManualDomains: []string{"geoip:ru"},
		HRRouteMode: "interface", Routes: []RouteTarget{{TunnelID: "awg10"}},
	}}); err != nil {
		t.Fatalf("HR CreateBatch: %v", err)
	}
}

func TestParseDomainLine_GeoTagSkipped(t *testing.T) {
	for _, in := range []string{"geosite:google", "geoip:ru", "GEOSITE:Google", " geoip:RU "} {
		if got := parseDomainLine(in); got != "" {
			t.Errorf("parseDomainLine(%q) = %q, want \"\"", in, got)
		}
	}
}

// Миграция store снимает geo-теги, записанные до F700, отовсюду, откуда они
// доехали бы до роутера или вернулись бы следующим Update через ManualText.
func TestStoreLoad_DropsGeoTags(t *testing.T) {
	dir := t.TempDir()
	manual := "# мой список\nexample.com\ngeosite:GOOGLE\n\n# geosite:comment остаётся\n10.0.0.0/8\r\n GEOIP:ru \n"
	excl := "ads.example.com\ngeosite:ads\n"
	legacy := DomainList{
		ID: "list_1", Name: "legacy", Enabled: true, GroupSlug: "legacy",
		ManualText:     &manual,
		ManualDomains:  []string{"example.com", "geosite:GOOGLE", "10.0.0.0/8", "GEOIP:ru"},
		Domains:        []string{"example.com", "geosite:google"},
		Subnets:        []string{"10.0.0.0/8", "geoip:ru"},
		ExcludesText:   &excl,
		Excludes:       []string{"ads.example.com", "geosite:ads"},
		ExcludeSubnets: []string{"geoip:cn", "1.2.3.0/24"},
	}
	raw, err := json.Marshal(StoreData{Lists: []DomainList{legacy}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "dns-routes.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	data, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	check := func(l DomainList) {
		t.Helper()
		for name, got := range map[string][]string{
			"ManualDomains":  l.ManualDomains,
			"Domains":        l.Domains,
			"Subnets":        l.Subnets,
			"Excludes":       l.Excludes,
			"ExcludeSubnets": l.ExcludeSubnets,
		} {
			for _, e := range got {
				if hasGeoPrefix(e) {
					t.Errorf("%s: geo-тег остался: %v", name, got)
				}
			}
		}
		if want := "example.com,10.0.0.0/8"; strings.Join(l.ManualDomains, ",") != want {
			t.Errorf("ManualDomains = %v, want %s", l.ManualDomains, want)
		}
		if want := "# мой список\nexample.com\n\n# geosite:comment остаётся\n10.0.0.0/8\r\n"; l.ManualText == nil || *l.ManualText != want {
			t.Errorf("ManualText = %q, want %q", deref(l.ManualText), want)
		}
		if want := "ads.example.com\n"; l.ExcludesText == nil || *l.ExcludesText != want {
			t.Errorf("ExcludesText = %q, want %q", deref(l.ExcludesText), want)
		}
		if strings.Join(l.Domains, ",") != "example.com" || strings.Join(l.Subnets, ",") != "10.0.0.0/8" ||
			strings.Join(l.Excludes, ",") != "ads.example.com" || strings.Join(l.ExcludeSubnets, ",") != "1.2.3.0/24" {
			t.Errorf("прочие строки задеты: %+v", l)
		}
	}
	check(data.Lists[0])

	// Файл переписан: перечитанный список уже чист, и повторный Load его не трогает.
	st1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewStore(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	check(again.Lists[0])
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(st1, st2) || !st1.ModTime().Equal(st2.ModTime()) {
		t.Error("повторный Load переписал файл")
	}
	if migrateNDMSGeoTags(again) {
		t.Error("повторная миграция сообщила об изменении")
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}
