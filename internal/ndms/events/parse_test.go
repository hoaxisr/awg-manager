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

// t= (В1) — аптайм скрипта, только для журнала. Нечисло, неположительное и
// бесконечность — 0: разбор строки поле не роняет.
func TestParseHookForm_ScriptUptime(t *testing.T) {
	cases := map[string]float64{
		"&t=122.4": 122.4, "&t=7": 7, "": 0, "&t=": 0, "&t=abc": 0,
		"&t=-5": 0, "&t=0": 0, "&t=NaN": 0, "&t=Inf": 0, "&t=1e400": 0,
	}
	for suffix, want := range cases {
		form := "type=ifcreated&id=Wireguard1" + suffix
		got, err := ParseHookForm(spoolValues(form))
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		if got.ScriptUptime != want {
			t.Errorf("%s: ScriptUptime=%v, want %v", form, got.ScriptUptime, want)
		}
	}
}
