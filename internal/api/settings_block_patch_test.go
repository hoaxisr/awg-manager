package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// F684: блок, присланный частично, меняет только присланные поля — соседние
// сохраняются. Раньше блоки server/pingCheck/updates/dnsRoute/geoFile
// присваивались целиком: {"server":{"port":N}} обнулял interfaces (панель после
// рестарта слушала на всех интерфейсах), а {"updates":{"statsEnabled":false}}
// падал на обнулённом расписании автоустановки.
func TestUpdate_PartialBlockKeepsSiblings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		check func(t *testing.T, s *storage.Settings)
	}{
		{"server", `{"server":{"port":2223}}`, func(t *testing.T, s *storage.Settings) {
			if s.Server.Port != 2223 || !slices.Equal(s.Server.Interfaces, []string{"br0"}) || s.Server.Interface != "br0" {
				t.Errorf("server = %+v", s.Server)
			}
		}},
		{"pingCheck.defaults", `{"pingCheck":{"defaults":{"interval":90}}}`, func(t *testing.T, s *storage.Settings) {
			d := s.PingCheck.Defaults
			if !s.PingCheck.Enabled || d.Interval != 90 || d.Method != "icmp" || d.DeadInterval != 300 || d.FailThreshold != 7 {
				t.Errorf("pingCheck = %+v", s.PingCheck)
			}
		}},
		{"updates", `{"updates":{"statsEnabled":false}}`, func(t *testing.T, s *storage.Settings) {
			u := s.Updates
			if u.StatsEnabled || !u.CheckEnabled || u.Channel != "develop" || u.AutoInstallIntervalDays != 14 || u.AutoInstallTime != "04:30" {
				t.Errorf("updates = %+v", u)
			}
		}},
		{"updates.snapshotDisabled", `{"updates":{"snapshotDisabled":true}}`, func(t *testing.T, s *storage.Settings) {
			u := s.Updates
			if !u.SnapshotDisabled || !u.StatsEnabled || !u.CheckEnabled || u.Channel != "develop" || u.AutoInstallTime != "04:30" {
				t.Errorf("updates = %+v", u)
			}
		}},
		{"dnsRoute", `{"dnsRoute":{"refreshDailyTime":"02:00"}}`, func(t *testing.T, s *storage.Settings) {
			d := s.DNSRoute
			if !d.AutoRefreshEnabled || d.RefreshIntervalHours != 6 || d.RefreshMode != "daily" || d.RefreshDailyTime != "02:00" {
				t.Errorf("dnsRoute = %+v", d)
			}
		}},
		{"geoFile", `{"geoFile":{"autoRefreshEnabled":false}}`, func(t *testing.T, s *storage.Settings) {
			g := s.GeoFile
			if g.AutoRefreshEnabled || g.RefreshIntervalHours != 12 || g.RefreshMode != "interval" {
				t.Errorf("geoFile = %+v", g)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newSettingsHandlerForTest(t)
			if err := store.Update(func(cur *storage.Settings) error {
				cur.Server = storage.ServerSettings{Port: 2222, Interface: "br0", Interfaces: []string{"br0"}}
				cur.PingCheck = storage.PingCheckSettings{Enabled: true, Defaults: storage.PingCheckDefaults{
					Method: "icmp", Target: "8.8.8.8", Interval: 45, DeadInterval: 300, FailThreshold: 7}}
				cur.Updates = storage.UpdateSettings{CheckEnabled: true, Channel: "develop", StatsEnabled: true,
					AutoInstallIntervalDays: 14, AutoInstallTime: "04:30"}
				cur.DNSRoute = storage.DNSRouteSettings{AutoRefreshEnabled: true, RefreshIntervalHours: 6,
					RefreshMode: "daily", RefreshDailyTime: "03:00"}
				cur.GeoFile = storage.GeoFileSettings{AutoRefreshEnabled: true, RefreshIntervalHours: 12, RefreshMode: "interval"}
				return nil
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			rec := httptest.NewRecorder()
			h.Update(rec, httptest.NewRequest(http.MethodPost, "/settings/update", bytes.NewReader([]byte(tc.body))))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			saved, err := store.Get()
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			tc.check(t, saved)
		})
	}
}

// Проверяется только присланное: испорченное время автоустановки, уже лежащее
// в settings.json, не запирает переключение статистики, а присланное
// испорченное — отвергается.
func TestUpdate_UpdatesValidatesOnlySentFields(t *testing.T) {
	h, store := newSettingsHandlerForTest(t)
	if err := store.Update(func(cur *storage.Settings) error {
		cur.Updates = storage.UpdateSettings{StatsEnabled: true, AutoInstallIntervalDays: 7, AutoInstallTime: "5:00"}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		h.Update(rec, httptest.NewRequest(http.MethodPost, "/settings/update", bytes.NewReader([]byte(body))))
		return rec.Code
	}
	if code := post(`{"updates":{"statsEnabled":false}}`); code != http.StatusOK {
		t.Fatalf("правка соседнего поля: status %d, want 200", code)
	}
	if code := post(`{"updates":{"autoInstallTime":"25:99"}}`); code != http.StatusBadRequest {
		t.Fatalf("присланное испорченное время: status %d, want 400", code)
	}
}
