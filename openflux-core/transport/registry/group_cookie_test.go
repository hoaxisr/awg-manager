package registry

import (
	"testing"

	"github.com/p1neappleXpress/OpenFlux/transport"
)

func TestNewMailruGroupWithCookies(t *testing.T) {
	tr, err := New(
		"mailru",
		"https://cloud.mail.ru/public/a/1,https://cloud.mail.ru/public/b/2",
		nil,
		Options{
			Base:          transport.DefaultConfig(),
			MailruCookies: "Mpop=test123; sdc=abc",
		},
	)
	if err != nil {
		t.Fatalf("registry.New(mailru group) failed: %v", err)
	}

	st, ok := tr.(*transport.StripedTransport)
	if !ok {
		t.Fatalf("expected *transport.StripedTransport, got %T", tr)
	}
	if st.LaneCount() != 2 {
		t.Fatalf("expected 2 lanes, got %d", st.LaneCount())
	}

	ce, ok := tr.(transport.CookieExchanger)
	if !ok {
		t.Fatalf("expected StripedTransport to implement CookieExchanger")
	}
	jar, err := ce.FetchCookies()
	if err != nil {
		t.Fatalf("FetchCookies failed: %v", err)
	}
	if jar["Mpop"] != "test123" || jar["sdc"] != "abc" {
		t.Fatalf("unexpected initial cookies from striped group: %+v", jar)
	}

	if err := ce.ApplyCookies(map[string]string{"Mpop": "newval", "sdc": "xyz"}); err != nil {
		t.Fatalf("ApplyCookies on striped group failed: %v", err)
	}
	jar2, err := ce.FetchCookies()
	if err != nil {
		t.Fatalf("FetchCookies after ApplyCookies failed: %v", err)
	}
	if jar2["Mpop"] != "newval" || jar2["sdc"] != "xyz" {
		t.Fatalf("unexpected updated cookies from striped group: %+v", jar2)
	}
}
