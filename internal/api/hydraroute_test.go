package api

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestWrapGeoDownloadError_SingboxEOF(t *testing.T) {
	msg, code := wrapGeoDownloadError(
		downloader.RouteInfo{Tag: "RelayCH", Kind: "singbox"},
		errors.New(`Get "https://example.com": EOF`),
		"GEO_DOWNLOAD_ERROR",
	)
	if code != string(downloader.ErrCodeSingboxEgressFailed) {
		t.Fatalf("code = %q, want %q", code, downloader.ErrCodeSingboxEgressFailed)
	}
	if msg == "" {
		t.Fatal("expected non-empty message")
	}
}

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
	if err := store.Save(st); err != nil {
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
	if err := store.Save(st); err != nil {
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
