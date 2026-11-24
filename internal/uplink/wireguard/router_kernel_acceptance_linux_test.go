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

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

// Device acceptance of the common routing/controller backend with a real AWG
// driver. Mark provisioning here is test-only: this is not production lifecycle
// integration and never advertises kernel targets in the product API.
func TestRouterKernelAWGUplinkAcceptance(t *testing.T) {
	if os.Getenv("UPLINK_ROUTER_KERNEL_ACCEPTANCE") != "1" {
		t.Skip("explicit device acceptance only")
	}
	dir, ref := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_DIR"), os.Getenv("UPLINK_ROUTER_ACCEPTANCE_REF")
	if filepath.Dir(dir) != "/opt/var" || !strings.HasPrefix(filepath.Base(dir), "uplink-acceptance-") || ref != "awg10" {
		t.Fatal("unexpected acceptance scope")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	file := filepath.Join("/opt/etc/awg-manager/tunnels", ref+".json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var stored storage.AWGTunnel
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Backend != "kernel" || !stored.Enabled || stored.Locked || stored.Obfuscator != nil || !reflect.DeepEqual(stored.Peer.AllowedIPs, []string{"0.0.0.0/0"}) {
		t.Fatal("unsupported test target")
	}
	iface := "opkgtun10"
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "wg" {
			name = "/opt/sbin/awg"
		}
		return defaultRunner(ctx, name, args...)
	}
	link, err := run(ctx, "ip", "-d", "link", "show", "dev", iface)
	if err != nil || !strings.Contains(string(link), "amneziawg") {
		t.Fatal("not the AmneziaWG driver", err)
	}
	target := ManagedTarget{Ref: ref, Label: stored.Name, Interface: iface, Peer: stored.Peer.PublicKey, Endpoint: stored.Peer.Endpoint, TunnelDNS: tunnel.ParseDNSList(stored.Interface.DNS), Enabled: true}
	if err = eligible(target); err != nil {
		t.Fatal(err)
	}
	mark := uint32(0x41570001)
	markPath := filepath.Join(dir, "original-kernel-mark.json")
	cleanupOnly := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_CLEANUP") == "1"
	var original string
	if cleanupOnly {
		b, e := os.ReadFile(markPath)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, &original); e != nil {
			t.Fatal(e)
		}
	} else {
		b, e := run(ctx, "wg", "show", iface, "fwmark")
		if e != nil {
			t.Fatal(e)
		}
		original = strings.TrimSpace(string(b))
		if original != "off" {
			t.Fatal("test requires unmarked device; preserve existing reservations")
		}
		// Every fwmark selector is considered, including masked rules. Refuse
		// conflicts instead of overwriting a presumed free numeric constant.
		for _, family := range []string{"-4", "-6"} {
			b, e := run(ctx, "ip", family, "-j", "rule", "show")
			if e != nil {
				t.Fatal(e)
			}
			var rules []map[string]any
			if e = json.Unmarshal(b, &rules); e != nil {
				t.Fatal(e)
			}
			for _, r := range rules {
				if v, ok := r["fwmark"]; ok {
					m, e := strconv.ParseUint(fmt.Sprint(v), 0, 32)
					if e != nil {
						t.Fatal(e)
					}
					mask := uint64(0xffffffff)
					if v, ok = r["fwmask"]; ok {
						mask, e = strconv.ParseUint(fmt.Sprint(v), 0, 32)
						if e != nil {
							t.Fatal(e)
						}
					}
					if uint64(mark)&mask == m&mask {
						t.Fatal("reserved acceptance mark overlaps a routing rule")
					}
				}
			}
		}
		b, e = run(ctx, "wg", "show", "all", "fwmark")
		if e != nil {
			t.Fatal(e)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			p := strings.Fields(line)
			if len(p) != 2 {
				t.Fatal("invalid mark catalogue")
			}
			if p[1] != "off" {
				n, e := strconv.ParseUint(p[1], 0, 32)
				if e != nil || uint32(n) == mark {
					t.Fatal("mark already in use or unreadable")
				}
			}
		}
		b, e = json.Marshal(original)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(markPath, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	resolver := routerAcceptanceResolver{[]uplink.Provider{{ID: "GigabitEthernet1", Device: "eth3", Up: true}, {ID: "WifiMaster0/WifiStation0", Device: "apcli0", Up: true}}}
	manager, err := NewManaged(ManagedDeps{Directory: filepath.Join(dir, "managed-state"), Lookup: func(context.Context, string) (ManagedTarget, error) { return target, nil }, List: func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{target}, nil }, Providers: resolver.Providers, WithLock: func(_ context.Context, _ string, fn func() error) error { return fn() }, ApplyBinding: func(context.Context, string, string, string) error { return nil }, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	policyTarget := uplink.Target{Engine: "wireguard", Ref: ref}
	endpoint := netip.MustParseAddrPort(target.Endpoint)
	rule := []string{"OUTPUT", "-o", "eth3", "-p", "udp", "-d", endpoint.Addr().String(), "--dport", fmt.Sprint(endpoint.Port()), "-m", "mark", "--mark", fmt.Sprintf("0x%x", mark), "-m", "comment", "--comment", "awg-uplink-acceptance-" + ref, "-j", "DROP"}
	unblock := func(ctx context.Context) error {
		if _, e := run(ctx, "iptables", append([]string{"-C"}, rule...)...); e != nil {
			return nil
		}
		_, e := run(ctx, "iptables", append([]string{"-D"}, rule...)...)
		return e
	}
	cleanup := func() error {
		clean, done := context.WithTimeout(context.Background(), 35*time.Second)
		defer done()
		if e := unblock(clean); e != nil {
			return e
		}
		if e := manager.ReleaseUplink(clean, policyTarget); e != nil {
			return e
		}
		current, e := run(clean, "wg", "show", iface, "fwmark")
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(current)) != original {
			n, e := strconv.ParseUint(strings.TrimSpace(string(current)), 0, 32)
			if e != nil || uint32(n) != mark {
				return fmt.Errorf("foreign mark prevents restoration")
			}
			if _, e = run(clean, "wg", "set", iface, "fwmark", original); e != nil {
				return e
			}
		}
		current, e = run(clean, "wg", "show", iface, "fwmark")
		if e != nil || strings.TrimSpace(string(current)) != original {
			return fmt.Errorf("original mark not restored: %v", e)
		}
		b, e := os.ReadFile(file)
		if e != nil || string(b) != string(raw) {
			return fmt.Errorf("production tunnel record changed: %v", e)
		}
		for field, want := range map[string]string{"peers": target.Peer, "endpoints": target.Peer + "\t" + target.Endpoint, "allowed-ips": target.Peer + "\t0.0.0.0/0"} {
			b, e = run(clean, "wg", "show", iface, field)
			if e != nil || strings.TrimSpace(string(b)) != want {
				return fmt.Errorf("original %s not preserved: %v", field, e)
			}
		}
		t.Log("cleanup: original mark, peer, endpoint, AllowedIPs and production tunnel record restored; owned routing released")
		return nil
	}
	if cleanupOnly {
		if err = cleanup(); err != nil {
			t.Fatal(err)
		}
		return
	}
	defer func() {
		if e := cleanup(); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	if _, err = run(ctx, "wg", "set", iface, "fwmark", fmt.Sprintf("0x%x", mark)); err != nil {
		t.Fatal(err)
	}
	store, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(store, resolver, map[string]uplink.Adapter{"wireguard": manager})
	p, err := c.Put(ctx, uplink.Policy{ID: "kernel-device-acceptance", Target: policyTarget, Providers: []string{"GigabitEthernet1", "WifiMaster0/WifiStation0"}, Enabled: true, IntervalSeconds: 8, TimeoutSeconds: 8, FailureThreshold: 1, HoldDownSeconds: 10})
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
	if _, err = run(ctx, "iptables", append([]string{"-I"}, rule...)...); err != nil {
		t.Fatal(err)
	}
	b, err := run(ctx, "curl", "-4", "--interface", "eth3", "--noproxy", "*", "--connect-timeout", "4", "--max-time", "10", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "https://cp.cloudflare.com/generate_204")
	if err != nil || string(b) != "204" {
		t.Fatal("direct Ethernet witness failed", err)
	}
	time.Sleep(11 * time.Second)
	c.Tick(ctx)
	check("WifiMaster0/WifiStation0")
	b, err = run(ctx, "iptables", "-L", "OUTPUT", "-n", "-v", "-x")
	if err != nil {
		t.Fatal(err)
	}
	packets := uint64(0)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "awg-uplink-acceptance-"+ref) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				packets, _ = strconv.ParseUint(fields[0], 10, 64)
			}
			t.Log("target-only UDP drop:", line)
		}
	}
	if packets == 0 {
		t.Fatal("failure injection has no packets")
	}
	if err = unblock(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.Switch(ctx, p.ID, "GigabitEthernet1", p.Revision); err != nil {
		t.Fatal(err)
	}
	check("GigabitEthernet1")
	if err = c.Delete(context.Background(), p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	t.Log("real kernel AmneziaWG selective transport fallback and manual return passed")
}
