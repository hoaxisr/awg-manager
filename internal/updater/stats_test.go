package updater

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func newStatsTestService(t *testing.T, enabled bool) (*Service, *[]downloader.Request) {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	if err := store.Update(func(cur *storage.Settings) error { cur.Updates.StatsEnabled = enabled; return nil }); err != nil {
		t.Fatal(err)
	}
	s := New("2.0.0", store, &recordingAppLogger{}, dir, nil)
	var seen []downloader.Request
	s.SetDownloader(&fakeDownloader{
		readAllFn: func(_ context.Context, req downloader.Request) ([]byte, downloader.ResponseMeta, error) {
			seen = append(seen, req)
			return gzipBytes(t, "Package: awg-manager\nVersion: 1.0.0\nFilename: x.ipk\n"), downloader.ResponseMeta{StatusCode: http.StatusOK}, nil
		},
	})
	s.SetFeatures(func() Features {
		return Features{SingboxRouter: true, SingboxMode: "policy-tun", DNSRoute: true, DeviceProxy: true}
	})
	return s, &seen
}

// Запрос Packages.gz несёт ID и флаги; ID один и тот же между проверками.
func TestStats_HeadersOnPackagesCheck(t *testing.T) {
	s, seen := newStatsTestService(t, true)
	s.CheckNow(context.Background())
	s.CheckNow(context.Background())
	if len(*seen) != 2 {
		t.Fatalf("запросов %d, want 2", len(*seen))
	}
	first, second := (*seen)[0].Headers, (*seen)[1].Headers
	id := first.Get(headerInstance)
	if len(id) != 32 {
		t.Fatalf("ID = %q", id)
	}
	if second.Get(headerInstance) != id {
		t.Fatalf("ID сменился: %q → %q", id, second.Get(headerInstance))
	}
	if got := first.Get(headerFeatures); got != "sb-policy-tun,dnsroute,deviceproxy" {
		t.Fatalf("features = %q", got)
	}
}

func TestStats_DisabledSendsNothing(t *testing.T) {
	s, seen := newStatsTestService(t, false)
	s.CheckNow(context.Background())
	if len(*seen) != 1 {
		t.Fatalf("запросов %d, want 1", len(*seen))
	}
	if h := (*seen)[0].Headers; h != nil {
		t.Fatalf("при выключенной статистике ушли заголовки: %v", h)
	}
}

func TestFeatures_Tokens(t *testing.T) {
	cases := []struct {
		f    Features
		want string
	}{
		{Features{}, "sb-off"},
		{Features{SingboxRouter: true}, "sb-tproxy"},
		{Features{SingboxRouter: true, SingboxMode: "fakeip-tun", ClientRoute: true}, "sb-fakeip-tun,clientroute"},
		{Features{SingboxRouter: true, SingboxMode: "1.2.3.4 secret"}, "sb-other"},
		{Features{SingboxMode: "policy-tun"}, "sb-off"},
	}
	for _, c := range cases {
		if got := strings.Join(c.f.tokens(), ","); got != c.want {
			t.Errorf("%+v → %q, want %q", c.f, got, c.want)
		}
	}
}
