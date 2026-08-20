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

// Боевой путь: настройки СНАЧАЛА нормализуются и только потом резолвятся
// (fakeip_config.go, service_lifecycle.go). Проверка на сырой структуре
// пропускала бы то, что нормализация штампует свой дефолт и затирает
// подстановку — именно так и случилось в первой версии этой правки.
func TestResolveFakeIPParams_RealServerFallback_AfterNormalize(t *testing.T) {
	cases := []struct {
		name       string
		realServer string
		bootstrap  string
		want       string
	}{
		{"значения не было — берём bootstrap", "", "8.8.8.8", "8.8.8.8"},
		{"значения не было и bootstrap пуст — исторический дефолт", "", "", "1.1.1.1"},
		{"значение уже есть — не трогаем", "9.9.9.9", "8.8.8.8", "9.9.9.9"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sr := storage.SingboxRouterSettings{FakeIPRealServer: c.realServer}
			if err := normalizeFakeIPSettings(&sr); err != nil {
				t.Fatalf("normalizeFakeIPSettings: %v", err)
			}
			got := resolveFakeIPParamsWith(DefaultFakeIPTunParams(), sr, c.bootstrap)
			if got.RealServer != c.want {
				t.Errorf("RealServer = %q, want %q", got.RealServer, c.want)
			}
		})
	}
}
