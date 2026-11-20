//go:build linux

package wireguard

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestRouterKernelManagedUplinkAcceptance(t *testing.T) {
	if os.Getenv("UPLINK_ROUTER_KERNEL_MANAGED") != "1" {
		t.Skip("explicit kernel AWG device acceptance")
	}
	dir := os.Getenv("UPLINK_ROUTER_ACCEPTANCE_DIR")
	if filepath.Dir(dir) != "/opt/var" || !strings.HasPrefix(filepath.Base(dir), "uplink-acceptance-") {
		t.Fatal("invalid private directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	file := "/opt/etc/awg-manager/tunnels/awg10.json"
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var stored storage.AWGTunnel
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.ID != "awg10" || stored.Backend != "kernel" || !stored.Enabled || stored.Locked || stored.Obfuscator != nil {
		t.Fatal("unexpected dedicated kernel target")
	}
	run := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "wg" {
			name = "/opt/sbin/awg"
		}
		return defaultRunner(ctx, name, args...)
	}
	pub, err := run(ctx, "wg", "show", "opkgtun10", "public-key")
	if err != nil {
		t.Fatal(err)
	}
	private, err := base64.StdEncoding.DecodeString(stored.Interface.PrivateKey)
	if err != nil {
		t.Fatal("invalid stored client identity")
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		t.Fatal("invalid stored client identity")
	}
	if base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()) != strings.TrimSpace(string(pub)) {
		t.Fatal("stored and live client identities differ")
	}
	target := ManagedTarget{Ref: stored.ID, Label: stored.Name, Interface: "opkgtun10", Peer: stored.Peer.PublicKey, Endpoint: stored.Peer.Endpoint, TunnelDNS: tunnel.ParseDNSList(stored.Interface.DNS), AllowedIPs: stored.Peer.AllowedIPs, LocalPublicKey: strings.TrimSpace(string(pub)), Enabled: true}
	tr := transport.NewWithURL("http://127.0.0.1:79/rci", transport.NewSemaphore(2))
	op := nwg.NewOperator(nil, nil, tr, nil)
	deps := KernelDeps{ManagedDeps: ManagedDeps{Directory: dir, Run: run, Lookup: func(context.Context, string) (ManagedTarget, error) { return target, nil }, List: func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{target}, nil }, Providers: (routerAcceptanceResolver{[]uplink.Provider{{ID: "GigabitEthernet1", Device: "eth3", Up: true}, {ID: "WifiMaster0/WifiStation0", Device: "apcli0", Up: true}}}).Providers, WithLock: func(_ context.Context, _ string, fn func() error) error { return fn() }, ApplyBinding: func(context.Context, string, string, string) error { return nil }}, NativeMarks: op.NativeUplinkPeerMarks, StartProviders: func(string) []string { return []string{"GigabitEthernet1", "WifiMaster0/WifiStation0"} }, RestoreRoute: func(ctx context.Context, _ string) error {
		// This acceptance process never changed NDMS's baseline route. Prove
		// it still selects Ethernet before allowing the owned mark to go off.
		out, e := run(ctx, "ip", "-4", "-j", "route", "get", netip.MustParseAddrPort(target.Endpoint).Addr().String(), "mark", "0x0")
		if e != nil {
			return e
		}
		var rows []route
		if e = json.Unmarshal(out, &rows); e != nil {
			return e
		}
		if len(rows) != 1 || rows[0].Dev != "eth3" {
			return fmt.Errorf("original baseline route changed")
		}
		return nil
	}}
	k, err := NewKernel(deps)
	if err != nil {
		t.Fatal(err)
	}
	policyTarget := uplink.Target{Engine: "kernel-amneziawg", Ref: target.Ref}
	endpoint := netip.MustParseAddrPort(target.Endpoint)
	mark := uint32(0)
	if c, ok := k.m.profiles[target.Ref]; ok {
		mark = c.Mark
	}
	rule := func() []string {
		return []string{"OUTPUT", "-o", "eth3", "-p", "udp", "-d", endpoint.Addr().String(), "--dport", fmt.Sprint(endpoint.Port()), "-m", "mark", "--mark", fmt.Sprintf("0x%x", mark), "-m", "comment", "--comment", "awg-uplink-kernel-managed", "-j", "DROP"}
	}
	unblock := func(ctx context.Context) error {
		if _, e := run(ctx, "iptables", append([]string{"-C"}, rule()...)...); e != nil {
			return nil
		}
		_, e := run(ctx, "iptables", append([]string{"-D"}, rule()...)...)
		return e
	}
	cleanup := func() error {
		clean, done := context.WithTimeout(context.Background(), 40*time.Second)
		defer done()
		if e := unblock(clean); e != nil {
			return e
		}
		if e := k.ReleaseUplink(clean, policyTarget); e != nil {
			return e
		}
		if _, e := run(clean, "ip", "link", "set", "up", "dev", target.Interface); e != nil {
			return e
		}
		b, e := run(clean, "wg", "show", target.Interface, "fwmark")
		if e != nil || strings.TrimSpace(string(b)) != "off" {
			return fmt.Errorf("original mark not restored: %v", e)
		}
		b, e = os.ReadFile(file)
		if e != nil || string(b) != string(raw) {
			return fmt.Errorf("production tunnel record changed: %v", e)
		}
		if e := k.identity(clean, target); e != nil {
			return e
		}
		t.Log("cleanup: original off mark, exact keys/peer/endpoint/prefix identity and original tunnel record preserved; interface up")
		return nil
	}
	if os.Getenv("UPLINK_ROUTER_ACCEPTANCE_CLEANUP") == "1" {
		if err = cleanup(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if k.OwnsTarget(target.Ref) {
		t.Fatal("use a fresh acceptance directory")
	}
	if err = deps.RestoreRoute(ctx, target.Ref); err != nil {
		t.Fatal("test requires unchanged Ethernet baseline before mutations", err)
	}
	defer func() {
		if e := cleanup(); e != nil {
			t.Errorf("cleanup: %v", e)
		}
	}()
	if err = k.Validate(ctx, policyTarget); err != nil {
		t.Fatal(err)
	}
	unchanged, err := run(ctx, "wg", "show", target.Interface, "fwmark")
	if err != nil || strings.TrimSpace(string(unchanged)) != "off" {
		t.Fatal("validation changed mark")
	}
	store, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(store, routerAcceptanceResolver{deps.Providers()}, map[string]uplink.Adapter{"kernel-amneziawg": k})
	p, err := c.Put(ctx, uplink.Policy{ID: "managed-kernel", Target: policyTarget, Providers: []string{"GigabitEthernet1", "WifiMaster0/WifiStation0"}, Enabled: true, IntervalSeconds: 8, TimeoutSeconds: 8, FailureThreshold: 1, HoldDownSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	mark = k.m.profiles[target.Ref].Mark
	t.Logf("production kernel adapter allocated mark=0x%x table=%d", mark, k.m.profiles[target.Ref].Table)
	check := func(want string) {
		t.Helper()
		entries := c.List()
		if len(entries) != 1 || entries[0].State.Phase != "healthy" || entries[0].State.Provider != want {
			t.Fatalf("expected healthy %s: %+v", want, entries)
		}
		t.Log("HTTPS healthy through", want)
	}
	c.Tick(ctx)
	check("GigabitEthernet1")
	if _, err = run(ctx, "iptables", append([]string{"-I"}, rule()...)...); err != nil {
		t.Fatal(err)
	}
	witness, err := run(ctx, "curl", "-4", "--interface", "eth3", "--noproxy", "*", "--connect-timeout", "4", "--max-time", "10", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "https://cp.cloudflare.com/generate_204")
	if err != nil || string(witness) != "204" {
		t.Fatal("direct Ethernet witness failed", err)
	}
	time.Sleep(11 * time.Second)
	c.Tick(ctx)
	check("WifiMaster0/WifiStation0")
	k, err = NewKernel(deps)
	if err != nil {
		t.Fatal(err)
	}
	store, err = uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c = uplink.New(store, routerAcceptanceResolver{deps.Providers()}, map[string]uplink.Adapter{"kernel-amneziawg": k})
	c.Tick(ctx)
	check("WifiMaster0/WifiStation0")
	// Simulate the critical setconf/recreated-device boundary while DOWN.
	// Full NDMS interface recreation is a separate lifecycle acceptance gate.
	if _, err = run(ctx, "ip", "link", "set", "down", "dev", target.Interface); err != nil {
		t.Fatal(err)
	}
	if _, err = run(ctx, "wg", "set", target.Interface, "fwmark", "off"); err != nil {
		t.Fatal(err)
	}
	profile := k.m.profiles[target.Ref]
	for _, family := range []string{"-4", "-6"} {
		if _, err = run(ctx, "ip", family, "rule", "del", "pref", strconv.Itoa(profile.Priority), "fwmark", fmt.Sprintf("0x%x", profile.Mark), "lookup", strconv.Itoa(profile.Table)); err != nil {
			t.Fatal(err)
		}
	}
	if err = k.PrepareForStart(ctx, target.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err = run(ctx, "ip", "link", "set", "up", "dev", target.Interface); err != nil {
		t.Fatal(err)
	}
	if err = k.Probe(ctx, policyTarget, "apcli0"); err != nil {
		t.Fatal(err)
	}
	t.Log("new adapter/controller retained WISP; pre-up restored a cleared device mark and missing owned rules before HTTPS")
	counters, err := run(ctx, "iptables", "-L", "OUTPUT", "-n", "-v", "-x")
	if err != nil {
		t.Fatal(err)
	}
	packets := uint64(0)
	for _, line := range strings.Split(string(counters), "\n") {
		if strings.Contains(line, "awg-uplink-kernel-managed") {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				packets, _ = strconv.ParseUint(fields[0], 10, 64)
			}
			t.Log("target-only drop:", line)
		}
	}
	if packets == 0 {
		t.Fatal("no failure injection packets")
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
	t.Log("production kernel AWG manager: allocate, selective WAN fallback, restart, pre-up recovery, manual return and release passed")
}
