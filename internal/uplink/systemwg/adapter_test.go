package systemwg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestSystemLookupUsesFullPeerConfigurationAndExplicitDNS(t *testing.T) {
	for _, scenario := range []string{"valid", "excluded", "multiple", "changed-peer", "missing-dns", "host-endpoint", "wrong-id"} {
		t.Run(scenario, func(t *testing.T) {
			posts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					posts++
					t.Error("lookup mutated NDMS")
					return
				}
				if strings.Contains(r.URL.Path, "/show/rc/") {
					dns := `[{"name-server":"1.1.1.1"}]`
					if scenario == "missing-dns" {
						dns = `[]`
					}
					endpoint := "192.0.2.9:1234"
					if scenario == "host-endpoint" {
						endpoint = "vpn.example:1234"
					}
					fmt.Fprintf(w, `{"Wireguard6":{"ip":{"name-server":%s},"wireguard":{"peer":[{"key":"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=","endpoint":{"address":%q},"allow-ips":[{"address":"0.0.0.0","mask":"0.0.0.0"},{"address":"::","mask":"0"}]}]}}}`, dns, endpoint)
					return
				}
				id := "Wireguard6"
				if scenario == "wrong-id" {
					id = "Wireguard7"
				}
				key := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
				if scenario == "changed-peer" {
					key = "FOREIGN"
				}
				peer := fmt.Sprintf(`{"public-key":%q,"remote-endpoint-address":"192.0.2.9","remote-port":1234}`, key)
				if scenario == "multiple" {
					peer += "," + peer
				}
				fmt.Fprintf(w, `{"Wireguard6":{"id":%q,"type":"Wireguard","state":"up","description":"Ivan","wireguard":{"public-key":"AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=","peer":[%s]}}}`, id, peer)
			}))
			defer srv.Close()
			tr := transport.NewWithURL(srv.URL, transport.NewSemaphore(2))
			a, err := New(Deps{Directory: t.TempDir(), Transport: tr, Commands: &command.Commands{}, Providers: func() []uplink.Provider { return nil }, Excluded: func(context.Context, string) (bool, error) { return scenario == "excluded", nil }})
			if err != nil {
				t.Fatal(err)
			}
			_, target, err := a.lookup(context.Background(), "Wireguard6")
			if scenario == "valid" {
				if err != nil || len(target.AllowedIPs) != 2 || !target.Enabled || target.Interface != "nwg6" {
					t.Fatal(target, err)
				}
			} else if err == nil {
				t.Fatal("unsafe target accepted", scenario)
			}
			if posts != 0 {
				t.Fatal("mutation in discovery")
			}
			if _, err = convert(uplink.Target{Engine: "wireguard", Ref: "Wireguard6"}); !errors.Is(err, uplink.ErrUnsupported) {
				t.Fatal("wrong namespace accepted")
			}
		})
	}
}
