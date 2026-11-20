//go:build linux

package wireguard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type routerAcceptanceResolver struct{ providers []uplink.Provider }

func (r routerAcceptanceResolver) Providers() []uplink.Provider {
	return append([]uplink.Provider(nil), r.providers...)
}

// This is an explicit device acceptance test, never part of an ordinary test
// run. The supervisor must hold the running AWGM daemon stopped (SIGSTOP), keep
// its children alive, and arm an independent cleanup/resume watchdog first.
// It exercises production Controller/Managed/NDMS commands without installing
// another application version or overwriting the production data directory.
func TestRouterNativeUplinkAcceptance(t *testing.T) {
	ref := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_REF")
	if ref == "" {
		t.Skip("explicit device acceptance only")
	}
	dir := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_DIR")
	if filepath.Dir(dir) != "/opt/var" || !strings.HasPrefix(filepath.Base(dir), "uplink-acceptance-") || filepath.Base(ref) != ref {
		t.Fatal("invalid private acceptance directory or target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	raw, err := os.ReadFile(filepath.Join("/opt/etc/awg-manager/tunnels", ref+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored storage.AWGTunnel
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	temporaryIPv4 := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_TEMP_IPV4") == "1" && reflect.DeepEqual(stored.Peer.AllowedIPs, []string{"0.0.0.0/0", "::/0"})
	if stored.ID != ref || stored.Backend != "nativewg" || !stored.Enabled || stored.Locked || (!temporaryIPv4 && !reflect.DeepEqual(stored.Peer.AllowedIPs, []string{"0.0.0.0/0"})) {
		t.Fatal("requires enabled, unlocked, IPv4-only native test target")
	}
	tr := transport.NewWithURL("http://127.0.0.1:79/rci", transport.NewSemaphore(2))
	defer tr.Close()
	queries := query.NewQueries(query.Deps{Getter: tr, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	if err = ndmsinfo.Init(ctx, queries.SystemInfo, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Never save temporary acceptance changes to startup configuration.
	save := command.NewSaveCoordinator(tr, nil, 24*time.Hour, 24*time.Hour, 0, nil)
	commands := command.NewCommands(command.Deps{Poster: tr, Save: save, Queries: queries})
	op := nwg.NewOperator(queries, commands, tr, nil)
	if !op.SupportsNativeUplink(&stored) {
		t.Fatal("native ASC capability gate rejected target")
	}
	names := nwg.NewNWGNames(stored.NWGIndex)
	mark, err := op.NativeUplinkPeerMark(ctx, &stored)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := netip.MustParseAddrPort(stored.Peer.Endpoint)
	stateDir := filepath.Join(dir, "managed-state")
	target := ManagedTarget{Ref: ref, Label: stored.Name, Interface: names.IfaceName, Peer: stored.Peer.PublicKey, Endpoint: stored.Peer.Endpoint, TunnelDNS: tunnel.ParseDNSList(stored.Interface.DNS), Enabled: true}
	resolver := routerAcceptanceResolver{[]uplink.Provider{
		{ID: "GigabitEthernet1", Device: "eth3", Label: "Ethernet", Up: true},
		{ID: "WifiMaster0/WifiStation0", Device: "apcli0", Label: "WISP", Up: true},
	}}
	for _, provider := range resolver.Providers() {
		if _, err = uplink.InspectBinding(provider.Device); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManaged(ManagedDeps{
		Directory: stateDir,
		Lookup:    func(context.Context, string) (ManagedTarget, error) { return target, nil },
		List:      func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{target}, nil },
		Providers: resolver.Providers,
		WithLock:  func(_ context.Context, _ string, fn func() error) error { return fn() },
		ApplyBinding: func(ctx context.Context, _ string, provider, _ string) error {
			return op.ApplyNativeUplink(ctx, &stored, provider)
		},
		PeerMark: func(ctx context.Context, _ ManagedTarget) (uint32, error) {
			return op.NativeUplinkPeerMark(ctx, &stored)
		},
		PeerMarks: op.NativeUplinkPeerMarks,
	})
	if err != nil {
		t.Fatal(err)
	}
	policyStore, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(policyStore, resolver, map[string]uplink.Adapter{"wireguard": manager})
	policyTarget := uplink.Target{Engine: "wireguard", Ref: ref}
	rule := []string{"OUTPUT", "-o", "eth3", "-p", "udp", "-d", endpoint.Addr().String(), "--dport", fmt.Sprint(endpoint.Port()), "-m", "mark", "--mark", fmt.Sprintf("0x%x", mark), "-m", "comment", "--comment", "awg-uplink-acceptance-" + ref, "-j", "DROP"}
	removeBlock := func(clean context.Context) error {
		if _, e := defaultRunner(clean, "iptables", append([]string{"-C"}, rule...)...); e != nil {
			return nil
		}
		_, e := defaultRunner(clean, "iptables", append([]string{"-D"}, rule...)...)
		return e
	}
	originalRCPath := filepath.Join(dir, "original-native-rc.json")
	cleanup := func() error {
		clean, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		if e := removeBlock(clean); e != nil {
			return e
		}
		if manager.OwnsTarget(ref) {
			if e := manager.ReleaseUplink(clean, policyTarget); e != nil {
				return e
			}
		}
		// Empty ISPInterface restores native automatic connect, not an explicit
		// Ethernet override that would outlive this acceptance run.
		iface, _, ok, e := queries.Interfaces.Confirm(clean, names.NDMSName)
		if e != nil {
			return e
		}
		if !ok {
			return fmt.Errorf("%s not listed in NDMS", names.NDMSName)
		}
		if e := commands.Wireguard.SetPeerConnectVia(clean, iface, stored.Peer.PublicKey, stored.ISPInterface); e != nil {
			return e
		}
		if temporaryIPv4 {
			if e := commands.Wireguard.AddPeerAllowIP(clean, iface, stored.Peer.PublicKey, "::", "0"); e != nil {
				return e
			}
		}
		before, e := os.ReadFile(originalRCPath)
		if e != nil {
			return e
		}
		after, e := tr.GetRaw(clean, "/show/rc/interface/"+names.NDMSName)
		if e != nil {
			return e
		}
		var a, b any
		if e = json.Unmarshal(before, &a); e != nil {
			return e
		}
		if e = json.Unmarshal(after, &b); e != nil {
			return e
		}
		if !reflect.DeepEqual(a, b) {
			return fmt.Errorf("native running configuration differs after cleanup")
		}
		current, e := os.ReadFile(filepath.Join("/opt/etc/awg-manager/tunnels", ref+".json"))
		if e != nil {
			return e
		}
		if string(current) != string(raw) {
			return fmt.Errorf("production tunnel record changed during acceptance")
		}
		t.Log("cleanup: exact original NDMS configuration and tunnel record verified; owned routes/rules removed")
		return nil
	}
	if os.Getenv("UPLINK_ROUTER_ACCEPTANCE_CLEANUP") == "1" {
		if err = cleanup(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if len(policyStore.List()) != 0 || manager.OwnsTarget(ref) {
		t.Fatal("acceptance directory is not fresh; run cleanup first")
	}
	before, err := tr.GetRaw(ctx, "/show/rc/interface/"+names.NDMSName)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(originalRCPath, before, 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := cleanup(); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	if temporaryIPv4 {
		iface, _, ok, e := queries.Interfaces.Confirm(ctx, names.NDMSName)
		if e != nil {
			t.Fatal(e)
		}
		if !ok {
			t.Fatalf("%s not listed in NDMS", names.NDMSName)
		}
		if err = commands.Wireguard.RemovePeerAllowIP(ctx, iface, stored.Peer.PublicKey, "::", "0"); err != nil {
			t.Fatal(err)
		}
		stored.Peer.AllowedIPs = []string{"0.0.0.0/0"}
		mark, err = op.NativeUplinkPeerMark(ctx, &stored)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("temporary IPv4-only runtime prepared; production tunnel record untouched")
	}
	p, err := c.Put(ctx, uplink.Policy{ID: "device-acceptance", Target: policyTarget, Providers: []string{"GigabitEthernet1", "WifiMaster0/WifiStation0"}, Enabled: true, IntervalSeconds: 8, TimeoutSeconds: 8, FailureThreshold: 1, HoldDownSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	assertHealthy := func(want string) {
		t.Helper()
		entries := c.List()
		if len(entries) != 1 || entries[0].State.Phase != "healthy" || entries[0].State.Provider != want {
			t.Fatalf("expected healthy %s: %+v", want, entries)
		}
		t.Logf("HTTPS healthy through %s", want)
	}
	c.Tick(ctx)
	assertHealthy("GigabitEthernet1")
	t.Logf("initial native mark=0x%x owned table=%d", manager.profiles[ref].Mark, manager.profiles[ref].Table)
	if _, err = defaultRunner(ctx, "iptables", append([]string{"-I"}, rule...)...); err != nil {
		t.Fatal(err)
	}
	witness, err := defaultRunner(ctx, "curl", "-4", "--interface", "eth3", "--noproxy", "*", "--connect-timeout", "4", "--max-time", "10", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "https://cp.cloudflare.com/generate_204")
	if err != nil || string(witness) != "204" {
		t.Fatalf("direct Ethernet witness: %q %v", witness, err)
	}
	time.Sleep(11 * time.Second)
	c.Tick(ctx)
	// Native firmware may need a fresh handshake after moving the UDP socket.
	// Keep the selected WISP path and retry the real probe; never simulate a
	// healthy state or fall back to the system resolver.
	for attempt := 0; attempt < 3 && c.List()[0].State.Phase == "unavailable" && c.List()[0].State.Provider == "WifiMaster0/WifiStation0"; attempt++ {
		t.Logf("WISP initial probe pending (attempt %d): %s", attempt+1, c.List()[0].State.LastError)
		if output, e := defaultRunner(ctx, "wg", "show", names.IfaceName, "latest-handshakes"); e == nil {
			t.Logf("handshake: %s", output)
		}
		time.Sleep(10 * time.Second)
		probeCtx, done := context.WithTimeout(ctx, 8*time.Second)
		e := manager.Probe(probeCtx, policyTarget, "apcli0")
		done()
		if e == nil {
			if err = c.Switch(ctx, p.ID, "WifiMaster0/WifiStation0", p.Revision); err != nil {
				t.Fatal(err)
			}
			break
		}
		t.Logf("WISP probe still failed: %v", e)
	}
	assertHealthy("WifiMaster0/WifiStation0")
	t.Logf("WISP native mark=0x%x owned table=%d", manager.profiles[ref].Mark, manager.profiles[ref].Table)
	counters, err := defaultRunner(ctx, "iptables", "-L", "OUTPUT", "-n", "-v", "-x")
	if err != nil {
		t.Fatal(err)
	}
	blockedPackets := uint64(0)
	for _, line := range strings.Split(string(counters), "\n") {
		if strings.Contains(line, "awg-uplink-acceptance-"+ref) {
			t.Log("target-only UDP drop counters:", line)
			fields := strings.Fields(line)
			if len(fields) > 0 {
				blockedPackets, _ = strconv.ParseUint(fields[0], 10, 64)
			}
		}
	}
	if blockedPackets == 0 {
		t.Fatal("selective failure injection has no matching packets")
	}
	if err = removeBlock(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.Switch(ctx, p.ID, "GigabitEthernet1", p.Revision); err != nil {
		t.Fatal(err)
	}
	assertHealthy("GigabitEthernet1")
	if err = c.Delete(p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	if _, e := op.NativeUplinkPeerMark(ctx, &stored); e != nil {
		t.Fatalf("native peer runtime after release: %v", e)
	}
	binding, err := uplink.InspectBinding(names.IfaceName)
	if err != nil {
		t.Fatal(err)
	}
	binding.DNSServers = target.TunnelDNS
	if err = uplink.ProbeBinding(ctx, binding); err != nil {
		t.Fatalf("HTTPS after policy release: %v", err)
	}
	t.Log("real NDMS native ASC: selective transport failure, WISP fallback, manual Ethernet return and release passed")
}
