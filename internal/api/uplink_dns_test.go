package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type dnsTestProviders struct{}

func (dnsTestProviders) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "ISP", Device: "wan0"}}
}
func TestUplinkDNSAPIValidationRevisionAndAutomatic(t *testing.T) {
	store, e := uplink.OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if e != nil {
		t.Fatal(e)
	}
	c := uplink.New(store, dnsTestProviders{}, nil)
	c.WANDNS, e = uplink.OpenWANDNS(filepath.Join(t.TempDir(), "dns.json"))
	if e != nil {
		t.Fatal(e)
	}
	h := &UplinkHandler{Controller: c}
	call := func(method, body string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.DNS(w, httptest.NewRequest(method, "/api/uplink/dns", strings.NewReader(body)))
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	call("GET", "", 200)
	call("PUT", `{"provider":"OTHER","revision":0,"servers":["1.1.1.1"]}`, 400)
	call("PUT", `{"provider":"ISP","revision":0,"servers":["dns.example"]}`, 400)
	call("PUT", `{"provider":"ISP","revision":0,"servers":["1.1.1.1"],"unknown":true}`, 400)
	call("PUT", `{"provider":"ISP","revision":0,"servers":["1.1.1.1"]}`, 200)
	call("PUT", `{"provider":"ISP","revision":0,"servers":[]}`, 409)
	if len(c.WANDNS.Snapshot().Overrides["ISP"]) != 1 {
		t.Fatal("stale request changed settings")
	}
	call("PUT", `{"provider":"ISP","revision":1,"servers":[]}`, 200)
	if len(c.WANDNS.Snapshot().Overrides) != 0 {
		t.Fatal("manual DNS retained")
	}
	call("DELETE", "", 405)
}
