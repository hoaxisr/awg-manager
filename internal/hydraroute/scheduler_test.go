package hydraroute

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func schedFor(t *testing.T, gf storage.GeoFileSettings) *GeoRefreshScheduler {
	t.Helper()
	st := storage.NewSettingsStore(t.TempDir())
	cur, err := st.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cur.GeoFile = gf
	if err := st.Save(cur); err != nil {
		t.Fatalf("save: %v", err)
	}
	resolve := func(_ context.Context) (*http.Client, string, func(), error) {
		return &http.Client{}, "direct", func() {}, nil
	}
	// svc=nil and appLogger=nil are safe: shouldRefresh touches neither
	// (NewScopedLogger is nil-safe; the scheduler only reads settings).
	return NewGeoRefreshScheduler(nil, st, nil, resolve)
}

func TestGeoSched_DisabledDoesNotRefresh(t *testing.T) {
	s := schedFor(t, storage.GeoFileSettings{AutoRefreshEnabled: false})
	if s.shouldRefresh() {
		t.Fatal("disabled must not refresh")
	}
}

func TestGeoSched_IntervalRefreshes(t *testing.T) {
	s := schedFor(t, storage.GeoFileSettings{AutoRefreshEnabled: true, RefreshMode: "interval", RefreshIntervalHours: 6})
	if !s.shouldRefresh() {
		t.Fatal("first run (zero lastRefresh) must refresh")
	}
	s.lastRefresh = time.Now()
	if s.shouldRefresh() {
		t.Fatal("must not refresh again within interval")
	}
	s.lastRefresh = time.Now().Add(-7 * time.Hour)
	if !s.shouldRefresh() {
		t.Fatal("must refresh after interval elapsed")
	}
}

func TestGeoSched_DailyEmptyTimeGuard(t *testing.T) {
	s := schedFor(t, storage.GeoFileSettings{AutoRefreshEnabled: true, RefreshMode: "daily", RefreshDailyTime: ""})
	if s.shouldRefresh() {
		t.Fatal("empty daily time must not refresh")
	}
}
