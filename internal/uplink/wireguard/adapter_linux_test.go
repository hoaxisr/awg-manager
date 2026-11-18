//go:build linux

package wireguard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func configFixture(t *testing.T) Config {
	t.Helper()
	c := Config{Ref: "wg", Interface: "lo", Peer: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), BindingFile: filepath.Join(t.TempDir(), "binding.json"), Endpoint: netip.MustParseAddrPort("203.0.113.9:51820"), Mark: 0x4151, Table: 151, Priority: 110, TunnelDNS: []string{"10.77.0.2:5300"}}
	if err := writeBinding(c.BindingFile, bindingRecord{Version: 1, Device: "wan1", Source: "10.44.1.1", Gateway: "10.44.1.2"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRejectForeignRoutingWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"valid", "terminal-missing", "terminal-selector", "foreign-route", "overlapping-mask", "numeric-mask", "inverted-mark", "selector", "foreign-peer", "changed-endpoint", "wrong-mark", "intent-mismatch", "missing-active"} {
		t.Run(scenario, func(t *testing.T) {
			c := configFixture(t)
			writes := 0
			runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "route replace") {
					writes++
					return nil, errors.New("unexpected mutation")
				}
				if name == "wg" {
					var result string
					switch args[len(args)-1] {
					case "peers":
						result = c.Peer
						if scenario == "foreign-peer" {
							result += "\nother"
						}
					case "fwmark":
						result = "0x4151"
						if scenario == "wrong-mark" {
							result = "0x4152"
						}
					case "endpoints":
						result = c.Peer + "\t" + c.Endpoint.String()
						if scenario == "changed-endpoint" {
							result = c.Peer + "\t203.0.113.8:51820"
						}
					case "allowed-ips":
						result = c.Peer + "\t0.0.0.0/0"
					}
					return []byte(result), nil
				}
				if strings.Contains(joined, "rule show") {
					rules := `{"priority":110,"src":"all","fwmark":"0x4151","table":151}`
					if scenario == "overlapping-mask" {
						rules += `,{"priority":100,"src":"all","fwmark":"0x4100","fwmask":"0xff00","table":152}`
					}
					if scenario == "numeric-mask" {
						rules += `,{"priority":100,"src":"all","fwmark":"0x4100","fwmask":65280,"table":152}`
					}
					if scenario == "inverted-mark" {
						rules += `,{"priority":100,"src":"all","fwmark":"0x4121","not":true,"table":152}`
					}
					if scenario == "selector" {
						rules = `{"priority":110,"src":"10.44.1.1/32","fwmark":"0x4151","table":151}`
					}
					return []byte("[" + rules + "]"), nil
				}
				if strings.Contains(joined, "route show") {
					terminal := `{"type":"unreachable","dst":"default","protocol":196,"metric":32767}`
					if scenario == "terminal-selector" {
						terminal = `{"type":"unreachable","dst":"default","from":"10.44.1.0/24","protocol":196,"metric":32767}`
					}
					if scenario == "terminal-missing" {
						return []byte("[]"), nil
					}
					if args[0] == "-6" {
						return []byte("[" + terminal + "]"), nil
					}
					if scenario == "missing-active" {
						return []byte("[" + terminal + "]"), nil
					}
					active := `{"dst":"default","gateway":"10.44.1.2","dev":"wan1","prefsrc":"10.44.1.1","protocol":196,"metric":10}`
					if scenario == "intent-mismatch" {
						active = strings.ReplaceAll(active, "wan1", "wan2")
					}
					if scenario == "foreign-route" {
						active = strings.ReplaceAll(active, "196", "99")
					}
					return []byte("[" + active + "," + terminal + "]"), nil
				}
				return nil, errors.New("unexpected command")
			}
			a, err := New([]Config{c}, func(context.Context, string) (Path, error) { return Path{}, uplink.ErrPathUnavailable }, runner)
			if err != nil {
				t.Fatal(err)
			}
			ob, err := a.Observe(context.Background(), uplink.Target{Engine: "wireguard", Ref: "wg"})
			valid := scenario == "valid" || scenario == "missing-active"
			if valid && (err != nil || ob.Device != "wan1") {
				t.Fatal(ob, err)
			}
			if !valid && err == nil {
				t.Fatal("foreign resource accepted")
			}
			if scenario == "missing-active" && !ob.NeedsRebind {
				t.Fatal("link-down lost durable binding")
			}
			if writes != 0 {
				t.Fatal("validation mutated routing")
			}
			if scenario == "valid" {
				target := uplink.Target{Engine: "wireguard", Ref: "wg"}
				if err := a.Activate(context.Background(), target, "wan1", "wan2"); !errors.Is(err, uplink.ErrPathUnavailable) {
					t.Fatal("preflight error classification", err)
				}
				record, err := readBinding(c.BindingFile)
				if err != nil || record.Device != "wan1" || writes != 0 {
					t.Fatal("preflight published a new binding", record, err, writes)
				}
				a.path = func(context.Context, string) (Path, error) {
					return Path{Binding: uplink.Binding{Device: "wan2", IPv4: "10.44.2.1"}, Gateway: netip.MustParseAddr("10.44.2.2")}, nil
				}
				err = a.Activate(context.Background(), target, "wan1", "wan2")
				if err == nil || errors.Is(err, uplink.ErrPathUnavailable) || writes != 1 {
					t.Fatal("apply error was mistaken for safe preparation failure", err, writes)
				}
				if _, err = a.Observe(context.Background(), target); !errors.Is(err, uplink.ErrConflict) {
					t.Fatal("interrupted apply did not preserve conflict", err)
				}
			}
		})
	}
}

func TestRejectUnsupportedAndSharedResources(t *testing.T) {
	for _, scenario := range []string{"ipv6", "duplicate-mark", "duplicate-binding", "main-table", "hostname-DNS"} {
		t.Run(scenario, func(t *testing.T) {
			c := configFixture(t)
			configs := []Config{c}
			switch scenario {
			case "ipv6":
				configs[0].Endpoint = netip.MustParseAddrPort("[2001:db8::9]:51820")
			case "main-table":
				configs[0].Table = 254
			case "hostname-DNS":
				configs[0].TunnelDNS = []string{"dns.example"}
			default:
				other := c
				other.Ref = "second"
				other.Interface = "wgsecond"
				other.Table = 152
				other.Priority = 111
				if scenario == "duplicate-mark" {
					other.BindingFile = filepath.Join(t.TempDir(), "binding.json")
				} else {
					other.Mark++
				}
				configs = append(configs, other)
			}
			if _, err := New(configs, func(context.Context, string) (Path, error) { return Path{}, nil }, nil); err == nil {
				t.Fatal("unsupported configuration accepted")
			}
		})
	}
}

func TestBindingJournalRejectsUnknownAndDoesNotExposeTemporaryFiles(t *testing.T) {
	c := configFixture(t)
	if err := os.WriteFile(c.BindingFile, []byte(`{"version":2,"device":"wan1","source":"10.44.1.1","gateway":"10.44.1.2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBinding(c.BindingFile); err == nil {
		t.Fatal("unknown version accepted")
	}
	files, err := os.ReadDir(filepath.Dir(c.BindingFile))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if (&Adapter{}).UplinkCapabilities(uplink.Target{}).AutomaticFailback {
		t.Fatal("WireGuard keys must not be duplicated for independent probes")
	}
}

func TestRouteUnmarshalHandlesMaxUint32MetricOn32BitPlatforms(t *testing.T) {
	raw := []byte(`[{"type":"unreachable","dst":"default","protocol":256,"metric":4294967295}]`)
	var routes []route
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Metric != 4294967295 {
		t.Fatalf("unexpected decoded route: %+v", routes)
	}
}
