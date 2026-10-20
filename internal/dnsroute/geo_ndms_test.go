package dnsroute

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// F700: NDMS не принимает geo-теги в object-group fqdn ни в каком регистре
// (стенд KN-1810 5.02.B.1, 20.10: include geosite:GOOGLE / geosite:google /
// geoip:ru → status error 101582220 «invalid FQDN or IP address» и E в
// журнале ndm). NDMS-список с тегом отвергается на записи; уже записанные
// теги не уходят на роутер; строка-тег в подписке пропускается.

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

// Уже записанный тег (список до F700) не уходит на роутер: иначе каждая
// сверка заново слала бы отвергнутый include — E в журнале ndm на прогон.
func TestReconcile_StoredGeoTagNotSent(t *testing.T) {
	ctx := context.Background()
	r := newFakeRouter()
	svc := newServiceWithRouter(t, r)
	d := svc.store.Draft()
	d.Lists = append(d.Lists, DomainList{
		ID: "list_1", Name: "legacy", Enabled: true, GroupSlug: "legacy",
		ManualDomains: []string{"geosite:google", "example.com"},
		Domains:       []string{"geosite:google", "example.com"},
		Subnets:       []string{"geoip:ru"},
		Excludes:      []string{"geosite:ads", "ads.example.com"},
		Routes:        routes("Wireguard0"),
	})
	if err := svc.store.Save(d); err != nil {
		t.Fatal(err)
	}

	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	waitQuiet(t, r)
	r.mu.Lock()
	var inc, exc []string
	for _, g := range r.groups {
		inc = append(inc, g.includes...)
		exc = append(exc, g.excludes...)
	}
	r.mu.Unlock()
	if strings.Join(inc, ",") != "example.com" || strings.Join(exc, ",") != "ads.example.com" {
		t.Errorf("на роутере include=%v exclude=%v, want только example.com / ads.example.com", inc, exc)
	}

	n := waitQuiet(t, r)
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := waitQuiet(t, r); got != n {
		t.Errorf("повторная сверка сделала %d POST, ждали 0", got-n)
	}
}

func TestParseDomainLine_GeoTagSkipped(t *testing.T) {
	for _, in := range []string{"geosite:google", "geoip:ru", "GEOSITE:Google", " geoip:RU "} {
		if got := parseDomainLine(in); got != "" {
			t.Errorf("parseDomainLine(%q) = %q, want \"\"", in, got)
		}
	}
}
