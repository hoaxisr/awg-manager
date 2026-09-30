package events

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseHookForm_FourTypes(t *testing.T) {
	cases := []struct {
		form string
		want Event
	}{
		{"type=iflayerchanged&id=Wireguard0&system_name=nwg0&layer=conf&level=running",
			Event{Type: EventIfLayerChanged, ID: "Wireguard0", SystemName: "nwg0", Layer: "conf", Level: "running"}},
		{"type=ifcreated&id=Wireguard1&system_name=nwg1",
			Event{Type: EventIfCreated, ID: "Wireguard1", SystemName: "nwg1"}},
		{"type=ifdestroyed&id=OpkgTun10&system_name=opkgtun10",
			Event{Type: EventIfDestroyed, ID: "OpkgTun10", SystemName: "opkgtun10"}},
		{"type=ifipchanged&id=PPPoE0&system_name=ppp0&address=203.0.113.9",
			Event{Type: EventIfIPChanged, ID: "PPPoE0", SystemName: "ppp0", Address: "203.0.113.9"}},
	}
	for _, c := range cases {
		v, err := url.ParseQuery(c.form)
		if err != nil {
			t.Fatalf("%s: %v", c.form, err)
		}
		got, err := ParseHookForm(v)
		if err != nil {
			t.Fatalf("%s: %v", c.form, err)
		}
		if got != c.want {
			t.Errorf("%s:\n got %#v\nwant %#v", c.form, got, c.want)
		}
	}
}

func TestParseHookForm_UnknownType(t *testing.T) {
	for _, typ := range []string{"bogus", ""} {
		v := url.Values{"type": {typ}, "id": {"x"}}
		_, err := ParseHookForm(v)
		if err == nil {
			t.Fatalf("type %q: ошибки нет", typ)
		}
		if typ != "" && !strings.Contains(err.Error(), typ) {
			t.Errorf("type %q: в ошибке нет типа: %v", typ, err)
		}
	}
}

// up/connected форвардер присылал всегда; им не доверяем (линк — из
// iflayerchanged), поэтому они не должны ни ломать разбор, ни попадать в Event.
func TestParseHookForm_IgnoresUpConnected(t *testing.T) {
	v, _ := url.ParseQuery("type=ifipchanged&id=PPPoE0&address=203.0.113.9&up=1&connected=yes")
	got, err := ParseHookForm(v)
	if err != nil {
		t.Fatal(err)
	}
	want := Event{Type: EventIfIPChanged, ID: "PPPoE0", Address: "203.0.113.9"}
	if got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}
