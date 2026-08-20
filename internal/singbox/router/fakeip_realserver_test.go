package router

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Адрес «настоящего» резолвера fakeip: явная настройка FakeIP важнее, но при
// её отсутствии берётся общий bootstrap-адрес sing-box (issue #770) — иначе в
// режиме fakeip-tun настройка bootstrap ни на что не влияет, потому что
// резолвером доменных адресов владеет слот fakeip.
func TestResolveFakeIPParams_RealServerFallback(t *testing.T) {
	cases := []struct {
		name       string
		realServer string
		bootstrap  string
		want       string
	}{
		{"обе пусты — исторический дефолт", "", "", "1.1.1.1"},
		{"только bootstrap", "", "8.8.8.8", "8.8.8.8"},
		{"явная настройка fakeip важнее", "9.9.9.9", "8.8.8.8", "9.9.9.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sr := storage.SingboxRouterSettings{FakeIPRealServer: c.realServer}
			got := resolveFakeIPParamsWith(DefaultFakeIPTunParams(), sr, c.bootstrap)
			if got.RealServer != c.want {
				t.Errorf("RealServer = %q, want %q", got.RealServer, c.want)
			}
		})
	}
}
