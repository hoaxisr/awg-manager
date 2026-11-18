//go:build linux

package firc

import (
	"context"
	"crypto/tls"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/testsupport"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

type nativeWANResolver struct{}

func (nativeWANResolver) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "isp1", Device: "wan1", Up: true}, {ID: "isp2", Device: "wan2", Up: true}}
}

func nativeTLSFixture(t *testing.T) tls.Certificate       { return testsupport.TLSFixture(t) }
func nativeFixtureProcess(t *testing.T, n int) func() int { return testsupport.StartVLESS(t, n) }
func TestNativeUplinkVLESSFixture(t *testing.T)           { testsupport.ServeVLESS(t) }

// Opt-in only: run via the private mount/network namespace fixture. No mock
// adapter, transport, daemon API, DNS resolver or SOCKS server is used here.
func TestNativeUplinkFallback(t *testing.T) {
	if os.Getenv("FIRC_UPLINK_NATIVE") != "1" {
		t.Skip("requires isolated native runtime fixture")
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	host, _ := os.Readlink("/proc/1/ns/net")
	if self == "" || self == host {
		t.Fatal("native smoke requires a private network namespace")
	}
	nativeTLSFixture(t)
	first := nativeFixtureProcess(t, 1)
	second := nativeFixtureProcess(t, 2)
	log, err := os.Create("/opt/fircd-smoke.log")
	if err != nil {
		t.Fatal(err)
	}
	daemon := exec.Command("/opt/bin/fircd")
	daemon.Stdout = log
	daemon.Stderr = log
	if err = daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- daemon.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			daemon.Process.Kill()
			<-done
		}
		log.Close()
		if t.Failed() {
			b, _ := os.ReadFile("/opt/fircd-smoke.log")
			t.Log(string(b))
		}
	})
	client := NewClient("/tmp/firc/firc.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err = client.Ping(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon API unavailable", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	operator := NewOperator(Options{ConfigDir: "/opt/etc/firc", SocketPath: "/tmp/firc/firc.sock"})
	operator.SetClient(client)
	adapter := &UplinkAdapter{Operator: operator, Active: func() bool { return true }, DNSForDevice: func(context.Context, string) ([]string, error) { return []string{"9.9.9.9:5300"}, nil }}
	store, err := uplink.OpenStore("/opt/etc/firc/uplink.json")
	if err != nil {
		t.Fatal(err)
	}
	controller := uplink.New(store, nativeWANResolver{}, map[string]uplink.Adapter{"firc": adapter})
	operator.SetManagedUplinkConfig(func(ctx context.Context, tunnels []Tunnel) ([]Tunnel, error) {
		return MaterializeOwnedUplinks(ctx, tunnels, store.MaterializationPolicies(), controller.Providers(), adapter.DNSForDevice, client)
	})
	policy, err := controller.Put(ctx, uplink.Policy{ID: "native", Target: uplink.Target{Engine: "firc", Ref: "managed"}, Providers: []string{"isp1", "isp2"}, Enabled: true, IntervalSeconds: 5, TimeoutSeconds: 5, FailureThreshold: 1, HoldDownSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	controller.Tick(ctx)
	state := controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp1" || first() == 0 {
		t.Fatalf("initial state=%+v requests=%d", state, first())
	}
	t.Logf("WAN1 healthy through native SOCKS/TLS: %+v", state)
	unblock := testsupport.BlockVLESS(t, "wan1")
	testsupport.AssertDirectWAN(t, "wan1", 1)
	baselineBlocked := first()
	time.Sleep(6 * time.Second)
	controller.Tick(ctx)
	state = controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp2" || second() == 0 || first() != baselineBlocked {
		t.Fatalf("selective VLESS drop: %+v requests=%d/%d", state, first(), second())
	}
	testsupport.AssertDirectWAN(t, "wan1", 1)
	unblock()
	if err := controller.Switch(ctx, policy.ID, "isp1", policy.Revision); err != nil {
		t.Fatal(err)
	}
	t.Log("kernel selective VLESS drop triggered fallback while direct WAN1 HTTP remained reachable")
	if out, err := exec.Command("ip", "link", "set", "wan1", "down").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// Keep catalogue Up=true: tunnel reachability, rather than the catalogue,
	// must detect the failed path and trigger fallback.
	// Wait out the preceding manual switch's ten-second hold-down as well.
	time.Sleep(11 * time.Second)
	before := first()
	controller.Tick(ctx)
	state = controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp2" || second() == 0 {
		t.Fatalf("fallback state=%+v WAN1=%d WAN2=%d", state, first(), second())
	}
	if first() != before {
		t.Fatal("failed WAN1 was used during fallback")
	}
	tunnels, err := client.GetTunnels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 || tunnels[0].Uplink.Ref != "wan2" || len(tunnels[0].EndpointDNS) != 1 {
		t.Fatal(tunnels)
	}
	t.Logf("WAN2 healthy after real WAN1 link failure: %+v, TLS requests=%d", state, second())
	baseline1, baseline2 := first(), second()
	if out, err := exec.Command("ip", "link", "set", "wan2", "down").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	time.Sleep(6 * time.Second)
	controller.Tick(ctx)
	state = controller.List()[0].State
	if state.Phase != "unavailable" || first() != baseline1 || second() != baseline2 {
		t.Fatalf("both WANs down: state=%+v requests=%d/%d", state, first(), second())
	}
	t.Logf("Both WANs down: %+v; no new TLS requests", state)
	if out, err := exec.Command("ip", "link", "set", "wan2", "up").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// Emulate DHCP restoring the provider gateway after link-down purged it.
	if out, err := exec.Command("ip", "route", "replace", "default", "via", "10.44.2.2", "dev", "wan2", "table", "202").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("ip", "-6", "addr", "replace", "fd44:2::1/64", "dev", "wan2", "nodad").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if out, err := exec.Command("ip", "-6", "route", "replace", "default", "via", "fd44:2::2", "dev", "wan2", "table", "202").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if err := controller.Switch(ctx, policy.ID, "isp2", policy.Revision); err != nil {
		t.Logf("recovery state: %+v", controller.List()[0].State)
		routes, _ := exec.Command("ip", "route", "show", "table", "all").CombinedOutput()
		t.Logf("routes: %s", routes)
		t.Fatal(err)
	}
	state = controller.List()[0].State
	if state.Phase != "healthy" || state.Provider != "isp2" || second() <= baseline2 {
		t.Fatalf("WAN2 recovery: %+v requests=%d", state, second())
	}
	t.Logf("WAN2 recovered through native SOCKS/TLS: %+v", state)
	server6 := testsupport.StartVLESSAddress(t, 2, "2001:db8:113::9")
	tunnels, err = client.GetTunnels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tunnels[0].Sources[0].Link = "vless://8f7d3b1a-2c4e-4f60-9a81-b5d7e6c30124@[2001:db8:113::9]:8443?security=none&type=tcp"
	if _, err := operator.ApplyTunnels(ctx, tunnels, false); err != nil {
		t.Fatal(err)
	}
	probe6, cancel6 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel6()
	if err := adapter.Probe(probe6, uplink.Target{Engine: "firc", Ref: "managed"}, "wan2"); err != nil {
		t.Fatal(err)
	}
	if server6() == 0 {
		t.Fatal("IPv6 outer transport was not used")
	}
	t.Log("native FIRC IPv6 outer transport plus SOCKS/TLS passed")
}
