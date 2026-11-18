//go:build linux

package wireguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestManagedReleaseAuditsOwnedResourcesWithoutOldKernelMark(t *testing.T) {
	c := configFixture(t)
	binds, writes := 0, 0
	deps := ManagedDeps{Directory: filepath.Dir(c.BindingFile), Providers: func() []uplink.Provider { return []uplink.Provider{{ID: "ISP1", Device: "wan1"}} }, WithLock: func(ctx context.Context, id string, work func() error) error { return work() }, ApplyBinding: func(context.Context, string, string, string) error { binds++; return nil },
		Lookup: func(context.Context, string) (ManagedTarget, error) {
			return ManagedTarget{Ref: c.Ref, Interface: c.Interface, Peer: c.Peer, Endpoint: c.Endpoint.String(), TunnelDNS: c.TunnelDNS, Enabled: true}, nil
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "wg" {
				t.Fatal("release depended on a kernel mark that may change on reboot")
			}
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "rule show") {
				return []byte(fmt.Sprintf(`[{"priority":%d,"src":"all","fwmark":"0x%x","table":%d}]`, c.Priority, c.Mark, c.Table)), nil
			}
			if strings.Contains(joined, "route show") {
				terminal := `{"table":151,"type":"unreachable","dst":"default","protocol":196,"metric":32767}`
				if args[0] == "-6" {
					return []byte("[" + terminal + "]"), nil
				}
				return []byte(`[{"table":151,"dst":"default","gateway":"10.44.1.2","dev":"wan1","prefsrc":"10.44.1.1","protocol":196,"metric":10},` + terminal + "]"), nil
			}
			if strings.Contains(joined, " del ") {
				writes++
				return nil, nil
			}
			t.Fatal("unexpected command", args)
			return nil, nil
		},
	}
	m := &Managed{deps: deps, profiles: map[string]Config{c.Ref: c}, registry: filepath.Join(deps.Directory, "uplink-wireguard.json")}
	m.publishProtection()
	if err := m.ReleaseUplink(context.Background(), uplink.Target{Engine: "wireguard", Ref: c.Ref}); err != nil {
		t.Fatal(err)
	}
	if m.OwnsTarget(c.Ref) || binds != 1 || writes != 5 {
		t.Fatal(m.OwnsTarget(c.Ref), binds, writes)
	}
	if _, err := os.Stat(c.BindingFile); !os.IsNotExist(err) {
		t.Fatal("binding survived cleanup", err)
	}
}

func TestManagedNativePeerMarkDoesNotRequireDeviceMark(t *testing.T) {
	c := configFixture(t)
	shared := false
	target := ManagedTarget{Ref: c.Ref, Interface: c.Interface, Peer: c.Peer, Endpoint: c.Endpoint.String(), TunnelDNS: c.TunnelDNS}
	m := &Managed{profiles: map[string]Config{c.Ref: c}, deps: ManagedDeps{
		PeerMark: func(context.Context, ManagedTarget) (uint32, error) { return c.Mark, nil },
		PeerMarks: func(context.Context) (map[string][]uint32, error) {
			v := map[string][]uint32{c.Interface: {c.Mark}}
			if shared {
				v["other"] = []uint32{c.Mark}
			}
			return v, nil
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch args[len(args)-1] {
			case "peers":
				return []byte(c.Peer), nil
			case "endpoints":
				return []byte(c.Peer + "\t" + c.Endpoint.String()), nil
			case "allowed-ips":
				return []byte(c.Peer + "\t0.0.0.0/0"), nil
			default:
				t.Fatal("queried generic device mark instead of native peer mark", args)
				return nil, nil
			}
		},
	}}
	if mark, err := m.mark(context.Background(), target); err != nil || mark != c.Mark {
		t.Fatal(mark, err)
	}
	shared = true
	if _, err := m.mark(context.Background(), target); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("shared native peer mark accepted", err)
	}
}

func TestManagedPointToPointBindingRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ppp.json")
	r := bindingRecord{Version: 1, Device: "ppp0", Source: "10.42.0.1", Direct: true}
	if err := writeBinding(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := readBinding(path)
	if err != nil || got != r {
		t.Fatal(got, err)
	}
	r.Direct = false
	if err := writeBinding(path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := readBinding(path); err == nil {
		t.Fatal("missing gateway accepted without point-to-point contract")
	}
}
