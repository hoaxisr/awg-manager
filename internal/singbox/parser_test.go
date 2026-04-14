package singbox

import (
	"strings"
	"testing"
)

func TestParse_Dispatch(t *testing.T) {
	cases := []struct {
		link     string
		protocol string
	}{
		{"vless://uuid@h.tld:443#v", "vless"},
		{"hysteria2://pw@h.tld:443#hy", "hysteria2"},
		{"hy2://pw@h.tld:443#hy2", "hysteria2"},
		{"naive+https://u:p@h.tld:443#n", "naive"},
	}
	for _, c := range cases {
		got, err := Parse(c.link)
		if err != nil {
			t.Errorf("%s: %v", c.link, err)
			continue
		}
		if got.Protocol != c.protocol {
			t.Errorf("%s: protocol=%s want %s", c.link, got.Protocol, c.protocol)
		}
	}
}

func TestParse_Unknown(t *testing.T) {
	_, err := Parse("http://example.com/")
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected unsupported error, got %v", err)
	}
}

func TestParseBatch_MixedSuccessFailure(t *testing.T) {
	input := "vless://uuid@h.tld:443#ok\nnot-a-link\nhy2://pw@h2.tld:443#ok2"
	ok, errs := ParseBatch(input)
	if len(ok) != 2 {
		t.Errorf("ok count: %d", len(ok))
	}
	if len(errs) != 1 {
		t.Errorf("err count: %d", len(errs))
	}
}
