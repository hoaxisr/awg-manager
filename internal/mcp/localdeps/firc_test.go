package localdeps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
)

func TestFIRCStatusSources(t *testing.T) {
	const saved = "groups: [{id: g, interface: tun0}]"
	for _, tc := range []struct {
		name, engine, response, disk string
		pingOK                       bool
		wantSource, wantRuntime      string
		wantTotal                    *int
	}{
		{"empty live inventory", "firc", `{"groups":[]}`, saved, false, "runtime", "responding", ptr(0)},
		{"missing live inventory", "firc", `{}`, saved, true, "saved", "responding", ptr(1)},
		{"unreachable", "firc", "", saved, false, "saved", "unknown", ptr(1)},
		{"other engine", "sing-box", "", saved, false, "saved", "unknown", ptr(1)},
		{"unknown selection", "", "", saved, false, "saved", "unknown", ptr(1)},
		{"unreadable inventories", "firc", "", "[", false, "unknown", "unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
			if err := os.WriteFile(op.GroupsPath(), []byte(tc.disk), 0600); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.engine != "firc" || r.Method != http.MethodGet {
					t.Errorf("unexpected %s %s", r.Method, r.URL)
				}
				switch r.URL.RequestURI() {
				case "/api/v1/groups":
					if tc.response != "" {
						fmt.Fprint(w, tc.response)
						return
					}
				case "/api/v1/system/netfilter":
					if tc.pingOK {
						fmt.Fprint(w, `{"ok":true}`)
						return
					}
				default:
					t.Errorf("unexpected endpoint %s", r.URL)
				}
				http.Error(w, "private diagnostic", 503)
			}))
			defer srv.Close()
			op.SetClient(firc.NewHTTPClient(srv.URL, srv.Client()))
			out, err := New(Config{FIRC: op, RoutingEngine: func() (string, bool) { return tc.engine, tc.engine != "" }}).FIRCStatus(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var selected *bool
			if tc.engine != "" {
				selected = ptr(tc.engine == "firc")
			}
			if !reflect.DeepEqual(out.Selected, selected) || out.Runtime != tc.wantRuntime || out.GroupsSource != tc.wantSource {
				t.Fatalf("observation %+v", out)
			}
			if !reflect.DeepEqual(out.GroupsTotal, tc.wantTotal) || out.GroupsComplete != (tc.wantTotal != nil) {
				t.Fatalf("inventory %+v", out)
			}
			if tc.wantSource == "saved" {
				if len(out.Groups) != 1 {
					t.Fatalf("saved groups %+v", out.Groups)
				}
				g := out.Groups[0]
				if g.Live != nil || g.ActiveOutbound != "" || !reflect.DeepEqual(g.ConfiguredOutbounds, []string{"tun0"}) {
					t.Fatalf("saved group %+v", g)
				}
			}
		})
	}
}

func TestFIRCStatusLivePage(t *testing.T) {
	groups := []firc.Group{
		{ID: "fallback", Enable: true, Live: true, Active: "tun1", Interface: firc.GroupInterface{"tun0", "tun1"}, Check: &firc.GroupCheck{URL: "https://secret.invalid/"}},
		{ID: "down", Enable: true, LiveReason: "not-enabled", Active: "tun0", Interface: firc.GroupInterface{"tun0"}},
		{ID: "unobserved", Enable: true, Interface: firc.GroupInterface{"tun0"}},
		{ID: "dns", Enable: true, Live: true, Active: "dns", Interface: firc.GroupInterface{"dns"}},
		{ID: "block", Enable: true, Live: true, Active: "block", Interface: firc.GroupInterface{"block"}},
		{ID: "no-active", Enable: true, Live: true, Interface: firc.GroupInterface{"tun0"}},
	}
	for len(groups) <= maxFIRCGroups {
		groups = append(groups, firc.Group{ID: fmt.Sprint(len(groups))})
	}
	op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/api/v1/groups" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": groups})
	}))
	defer srv.Close()
	op.SetClient(firc.NewHTTPClient(srv.URL, srv.Client()))
	out, err := New(Config{FIRC: op, RoutingEngine: func() (string, bool) { return "firc", true }}).FIRCStatus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if out.GroupsComplete || !reflect.DeepEqual(out.GroupsTotal, ptr(len(groups))) || len(out.Groups) != maxFIRCGroups {
		t.Fatalf("group page %+v", out)
	}
	for i, want := range []struct {
		live   *bool
		active string
	}{{ptr(true), "tun1"}, {ptr(false), ""}, {nil, ""}, {ptr(true), ""}, {ptr(true), ""}, {ptr(true), ""}} {
		got := out.Groups[i]
		if !reflect.DeepEqual(got.Live, want.live) || got.ActiveOutbound != want.active || !reflect.DeepEqual(got.ConfiguredOutbounds, []string(groups[i].Interface)) {
			t.Errorf("group %+v", got)
		}
	}
	data, err := json.Marshal(out)
	if err != nil || strings.Contains(string(data), "secret.invalid") {
		t.Fatal("unsafe status output", err)
	}
}
