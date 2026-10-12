package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestToDownloaderRoute(t *testing.T) {
	if got := toDownloaderRoute(nil); got != nil {
		t.Fatalf("nil route: got %+v", got)
	}
	got := toDownloaderRoute(&DownloadRouteDTO{Tag: "awg-a", Kind: "awg"})
	if got == nil {
		t.Fatal("expected non-nil route")
	}
	if got.Tag != "awg-a" || got.Kind != "awg" {
		t.Fatalf("unexpected route: %+v", got)
	}
}

func TestDownloadDeviceProxyAdapter(t *testing.T) {
	adapter := downloader.NewDeviceProxyOutboundsProvider(nil)
	if adapter != nil {
		t.Fatalf("nil service should produce nil provider")
	}
	dl := downloader.NewService(downloader.Deps{})
	list := dl.ListOutbounds(context.Background())
	if len(list) == 0 {
		t.Fatal("expected at least direct outbound")
	}
	if list[0].Tag != "direct" {
		t.Fatalf("first outbound tag: got %q want direct", list[0].Tag)
	}
}

func TestDownloadTransportAdaptersNilSafe(t *testing.T) {
	if got := downloader.NewSingboxTunnelPortAdapter(nil); got != nil {
		t.Fatalf("nil singbox op should produce nil tunnel port adapter")
	}
	if got := downloader.NewSubscriptionPortAdapter(nil); got != nil {
		t.Fatalf("nil subscription svc should produce nil port adapter")
	}
	if got := downloader.NewSingboxRuntimeAdapter(nil); got != nil {
		t.Fatalf("nil singbox op should produce nil runtime adapter")
	}
}

func TestDownloadSettingsRouteProvider_DefaultDirect(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}
	p := downloader.NewSettingsRouteProvider(store)
	route, err := p.GetDownloadRoute(context.Background())
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if route == nil || route.Tag != "direct" {
		t.Fatalf("route = %+v, want direct", route)
	}
}

func TestDownloadSettingsRouteProvider_UsesStoredTag(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	st, err := store.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	st.Download.RouteTag = "awg-test"
	if err := store.Update(func(cur *storage.Settings) error { *cur = *st; return nil }); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	p := downloader.NewSettingsRouteProvider(store)
	route, err := p.GetDownloadRoute(context.Background())
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if route == nil || route.Tag != "awg-test" {
		t.Fatalf("route = %+v, want awg-test", route)
	}

	// Ensure empty value is normalized to direct.
	st.Download.RouteTag = ""
	if err := store.Update(func(cur *storage.Settings) error { *cur = *st; return nil }); err != nil {
		t.Fatalf("save empty routeTag: %v", err)
	}
	route, err = p.GetDownloadRoute(context.Background())
	if err != nil {
		t.Fatalf("get route after empty: %v", err)
	}
	if route == nil || route.Tag != "direct" {
		t.Fatalf("route after empty = %+v, want direct", route)
	}

}

func TestDeleteOversizedTag(t *testing.T) {
	const seed = "## A\n/Wireguard0\n1.1.1.1\n\n" +
		"##impossible to use\n#/Too-big-geoip-tag\ngeoip:ru-blocked\ngeoip:cn\n"

	setup := func(t *testing.T, installed bool) (*HydraRouteHandler, string) {
		t.Helper()
		dir := t.TempDir()
		ipPath := filepath.Join(dir, "ip.list")
		if err := os.WriteFile(ipPath, []byte(seed), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(hydraroute.SetPaths(filepath.Join(dir, "domain.conf"), ipPath))
		svc := &hydraroute.Service{}
		svc.SetStatusForTest(installed)
		return NewHydraRouteHandler(svc, nil), ipPath
	}

	cases := []struct {
		name      string
		method    string
		query     string
		installed bool
		status    int
		ipList    string
	}{
		{"wrong method", http.MethodPost, "?name=geoip:cn", true, http.StatusMethodNotAllowed, seed},
		{"no name", http.MethodDelete, "", true, http.StatusBadRequest, seed},
		{"blank name", http.MethodDelete, "?name=%20", true, http.StatusBadRequest, seed},
		{"prefix only", http.MethodDelete, "?name=geoip:", true, http.StatusBadRequest, seed},
		{"not installed", http.MethodDelete, "?name=geoip:cn", false, http.StatusBadRequest, seed},
		{"missing tag", http.MethodDelete, "?name=geoip:us", true, http.StatusNotFound, seed},
		{"removed", http.MethodDelete, "?name=GEOIP:CN", true, http.StatusOK,
			"## A\n/Wireguard0\n1.1.1.1\n\n##impossible to use\n#/Too-big-geoip-tag\ngeoip:ru-blocked\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ipPath := setup(t, tc.installed)
			req := httptest.NewRequest(tc.method, "/api/hydraroute/oversized-tags/delete"+tc.query, nil)
			rec := httptest.NewRecorder()
			h.DeleteOversizedTag(rec, req)
			if rec.Code != tc.status {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			got, err := os.ReadFile(ipPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.ipList {
				t.Errorf("ip.list:\n%s\nwant:\n%s", got, tc.ipList)
			}
		})
	}
}

func newHydraRouteTestHandler(t *testing.T) (*HydraRouteHandler, *hydraroute.Service) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(hydraroute.SetPaths(filepath.Join(dir, "domain.conf"), filepath.Join(dir, "ip.list")))
	t.Cleanup(hydraroute.SetConfPathForTest(filepath.Join(dir, "hrneo.conf")))
	svc := &hydraroute.Service{}
	svc.SetStatusForTest(true)
	h := NewHydraRouteHandler(svc, nil)
	h.SetEventBus(events.NewBus())
	return h, svc
}

func TestDeleteTarget_StaleRevision409(t *testing.T) {
	h, _ := newHydraRouteTestHandler(t)
	body := strings.NewReader(`{"target":"P","revision":"000000000000"}`)
	rec := httptest.NewRecorder()
	h.DeleteTarget(rec, httptest.NewRequest(http.MethodPost, "/api/hydraroute/targets/delete", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteTarget_EmptyRevision400(t *testing.T) {
	h, _ := newHydraRouteTestHandler(t)
	body := strings.NewReader(`{"target":"P","revision":""}`)
	rec := httptest.NewRecorder()
	h.DeleteTarget(rec, httptest.NewRequest(http.MethodPost, "/api/hydraroute/targets/delete", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetConfig_HasRevision(t *testing.T) {
	h, svc := newHydraRouteTestHandler(t)
	rec := httptest.NewRecorder()
	h.GetConfig(rec, httptest.NewRequest(http.MethodGet, "/api/hydraroute/config", nil))
	rev, _ := svc.RulesRevision()
	if !strings.Contains(rec.Body.String(), `"hrRevision":"`+rev+`"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
