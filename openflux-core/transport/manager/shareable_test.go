package manager

import "testing"

func TestShareableCookiesDropEveryAccountLogin(t *testing.T) {
	jar := map[string]string{
		"Session_id": "y", "yandex_login": "ivan", "yandexuid": "u",
		"Mpop": "m", "t": "t", "sdcs": "s", "ssdc": "d", "VID": "v", "tmr_lvid": "x",
	}
	got := shareableCookies(jar)
	for _, name := range []string{"Session_id", "yandex_login", "Mpop", "t", "sdcs", "ssdc", "VID"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s is a login cookie and must not be handed to a client", name)
		}
	}
	for _, name := range []string{"yandexuid", "tmr_lvid"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is harmless and a client may take it", name)
		}
	}
}
