//go:build linux

package systemwg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type deviceResolver struct{}

func (deviceResolver) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "GigabitEthernet1", Device: "eth3", Up: true}, {ID: "WifiMaster0/WifiStation0", Device: "apcli0", Up: true}}
}

func TestRouterSystemWGUplinkAcceptance(t *testing.T) {
	if os.Getenv("UPLINK_ROUTER_SYSTEM_ACCEPTANCE") != "1" {
		t.Skip("explicit device acceptance")
	}
	dir := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_DIR")
	if filepath.Dir(dir) != "/opt/var" || !strings.HasPrefix(filepath.Base(dir), "uplink-acceptance-") {
		t.Fatal("invalid private directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 140*time.Second)
	defer cancel()
	tr := transport.NewWithURL("http://127.0.0.1:79/rci", transport.NewSemaphore(2))
	q := query.NewQueries(query.Deps{Getter: tr, Logger: query.NopLogger()})
	save := command.NewSaveCoordinator(tr, nil, 24*time.Hour, 24*time.Hour, 0, nil)
	commands := command.NewCommands(command.Deps{Poster: tr, Save: save, Queries: q})
	a, err := New(Deps{Directory: dir, Transport: tr, Queries: q, Commands: commands, Providers: (deviceResolver{}).Providers, Excluded: func(_ context.Context, id string) (bool, error) { return id != "Wireguard6", nil }})
	if err != nil {
		t.Fatal(err)
	}
	target := uplink.Target{Engine: "system-wireguard", Ref: "Wireguard6"}
	s, meta, err := a.lookup(ctx, target.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled || meta.Label != "Ivan" {
		t.Fatal("unexpected test target")
	}
	mark, err := a.op.NativeUplinkPeerMark(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := netip.MustParseAddrPort(meta.Endpoint)
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}
	rule := []string{"OUTPUT", "-o", "eth3", "-p", "udp", "-d", endpoint.Addr().String(), "--dport", fmt.Sprint(endpoint.Port()), "-m", "mark", "--mark", fmt.Sprintf("0x%x", mark), "-m", "comment", "--comment", "awg-uplink-system-acceptance", "-j", "DROP"}
	unblock := func(ctx context.Context) error {
		if _, e := run(ctx, "/opt/sbin/iptables", append([]string{"-C"}, rule...)...); e != nil {
			return nil
		}
		_, e := run(ctx, "/opt/sbin/iptables", append([]string{"-D"}, rule...)...)
		return e
	}
	path := filepath.Join(dir, "original-system-rc.json")
	cleanup := func() error {
		clean, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		if e := unblock(clean); e != nil {
			return e
		}
		if e := a.ReleaseUplink(clean, target); e != nil {
			return e
		}
		before, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		// The dedicated test target starts in automatic-via mode. Refuse any
		// future reuse of this test for a target with another original mode.
		iface, _, ok, e := q.Interfaces.Confirm(clean, target.Ref)
		if e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("%s not listed in NDMS", target.Ref)
		}
		if e := commands.Wireguard.SetPeerConnectVia(clean, iface, s.Peer.PublicKey, ""); e != nil {
			return e
		}
		after, e := tr.GetRaw(clean, "/show/rc/interface/"+target.Ref)
		if e != nil {
			return e
		}
		var b, c any
		if e = json.Unmarshal(before, &b); e != nil {
			return e
		}
		if e = json.Unmarshal(after, &c); e != nil {
			return e
		}
		if !reflect.DeepEqual(b, c) {
			return fmt.Errorf("system WG original running config differs")
		}
		t.Log("cleanup: exact original system WG config restored, including IPv6; owned routing released")
		return nil
	}
	if os.Getenv("UPLINK_ROUTER_ACCEPTANCE_CLEANUP") == "1" {
		if err = cleanup(); err != nil {
			t.Fatal(err)
		}
		return
	}
	before, err := tr.GetRaw(ctx, "/show/rc/interface/"+target.Ref)
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Wireguard struct{ Peer []map[string]json.RawMessage }
	}
	if err = json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.Wireguard.Peer) != 1 || len(original.Wireguard.Peer[0]["connect"]) != 0 {
		t.Fatal("acceptance requires original automatic connect mode")
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := cleanup(); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	store, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(store, deviceResolver{}, map[string]uplink.Adapter{"system-wireguard": a})
	p, err := c.Put(ctx, uplink.Policy{ID: "system-device-acceptance", Target: target, Providers: []string{"GigabitEthernet1", "WifiMaster0/WifiStation0"}, Enabled: true, IntervalSeconds: 8, TimeoutSeconds: 8, FailureThreshold: 1, HoldDownSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	check := func(provider string) {
		t.Helper()
		entries := c.List()
		if len(entries) != 1 || entries[0].State.Phase != "healthy" || entries[0].State.Provider != provider {
			t.Fatalf("expected healthy %s: %+v", provider, entries)
		}
		t.Log("HTTPS healthy through", provider)
	}
	c.Tick(ctx)
	check("GigabitEthernet1")
	// Reconstruct the adapter/controller from durable files before injecting
	// failure. This verifies the actual system adapter's restored profile.
	restarted, err := New(a.deps)
	if err != nil {
		t.Fatal(err)
	}
	a = restarted
	store, err = uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c = uplink.New(store, deviceResolver{}, map[string]uplink.Adapter{"system-wireguard": a})
	c.Tick(ctx)
	check("GigabitEthernet1")
	t.Log("new system adapter/controller restored durable profile and verified HTTPS")
	if _, err = run(ctx, "/opt/sbin/iptables", append([]string{"-I"}, rule...)...); err != nil {
		t.Fatal(err)
	}
	witness, err := run(ctx, "curl", "-4", "--interface", "eth3", "--noproxy", "*", "--connect-timeout", "4", "--max-time", "10", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "https://cp.cloudflare.com/generate_204")
	if err != nil || string(witness) != "204" {
		t.Fatal("Ethernet witness failed", err)
	}
	time.Sleep(11 * time.Second)
	c.Tick(ctx)
	check("WifiMaster0/WifiStation0")
	restarted, err = New(a.deps)
	if err != nil {
		t.Fatal(err)
	}
	a = restarted
	store, err = uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c = uplink.New(store, deviceResolver{}, map[string]uplink.Adapter{"system-wireguard": a})
	c.Tick(ctx)
	check("WifiMaster0/WifiStation0")
	t.Log("restart while on WISP preserved selected WAN and manual-return semantics")
	counters, err := run(ctx, "/opt/sbin/iptables", "-L", "OUTPUT", "-n", "-v", "-x")
	if err != nil {
		t.Fatal(err)
	}
	packets := uint64(0)
	for _, line := range strings.Split(string(counters), "\n") {
		if strings.Contains(line, "awg-uplink-system-acceptance") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				packets, _ = strconv.ParseUint(fields[0], 10, 64)
			}
			t.Log("target-only UDP drop:", line)
		}
	}
	if packets == 0 {
		t.Fatal("no injected transport failure")
	}
	if err = unblock(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.Switch(ctx, p.ID, "GigabitEthernet1", p.Revision); err != nil {
		t.Fatal(err)
	}
	check("GigabitEthernet1")
	if err = c.Delete(p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	t.Log("production system WG adapter: selective failure, automatic WISP fallback and manual return passed; IPv6 payload AllowedIPs preserved")
}
