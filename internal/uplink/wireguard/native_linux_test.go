//go:build linux

package wireguard

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/testsupport"
)

func TestWireguardHTTPSFixture(t *testing.T) {
	if os.Getenv("UPLINK_WG_HTTPS_FIXTURE") != "1" {
		t.Skip("private fixture only")
	}
	cert, err := tls.LoadX509KeyPair("/opt/etc/firc/test-ca.pem", "/opt/etc/firc/test-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	l, err := tls.Listen("tcp", "203.0.113.9:443", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/generate_204" || (r.Host != "cp.cloudflare.com" && r.Host != "www.gstatic.com") {
			http.NotFound(w, r)
			return
		}
		f, e := os.OpenFile("/opt/wg-https.requests", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			http.Error(w, "fixture", 500)
			return
		}
		fmt.Fprintln(f, "TLS probe")
		f.Close()
		w.WriteHeader(204)
	})}
	t.Cleanup(func() { server.Close() })
	if err := os.WriteFile("/opt/wg-https.ready", []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(l); err != nil {
		t.Fatal(err)
	}
}

type resolver struct{}

func TestNativeUplinkVLESSFixture(t *testing.T) { testsupport.ServeVLESS(t) }

func (resolver) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "isp1", Device: "wan1", Up: true}, {ID: "isp2", Device: "wan2", Up: true}}
}

func TestNativeWireguardFallback(t *testing.T) {
	if os.Getenv("UPLINK_WG_NATIVE") != "1" {
		t.Skip("private native fixture only")
	}
	self, _ := os.Readlink("/proc/self/ns/net")
	host, _ := os.Readlink("/proc/1/ns/net")
	if self == "" || self == host {
		t.Fatal("private namespace required")
	}
	testsupport.TLSFixture(t)
	testsupport.StartVLESS(t, 1) // independent direct HTTP witness on WAN1
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	run := defaultRunner
	command := func(name string, args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
		return out
	}
	// One remote WireGuard interface is reachable through both ISP namespaces;
	// its peer must roam to the second source, retaining the same key/session.
	command("ip", "netns", "add", "wgserver")
	command("ip", "netns", "exec", "wgserver", "ip", "link", "set", "lo", "up")
	command("ip", "netns", "exec", "wgserver", "ip", "addr", "add", "203.0.113.9/32", "dev", "lo")
	for n := 1; n <= 2; n++ {
		isp := fmt.Sprintf("isp%d", n)
		clientDev := fmt.Sprintf("server%d", n)
		remoteDev := fmt.Sprintf("uplink%d", n)
		command("ip", "netns", "exec", isp, "ip", "link", "add", clientDev, "type", "veth", "peer", "name", remoteDev)
		command("ip", "netns", "exec", isp, "ip", "link", "set", remoteDev, "netns", "wgserver")
		command("ip", "netns", "exec", isp, "ip", "addr", "add", fmt.Sprintf("10.55.%d.1/24", n), "dev", clientDev)
		command("ip", "netns", "exec", isp, "ip", "link", "set", clientDev, "up")
		command("ip", "netns", "exec", "wgserver", "ip", "addr", "add", fmt.Sprintf("10.55.%d.2/24", n), "dev", remoteDev)
		command("ip", "netns", "exec", "wgserver", "ip", "link", "set", remoteDev, "up")
		command("ip", "netns", "exec", isp, "ip", "route", "add", "203.0.113.9/32", "via", fmt.Sprintf("10.55.%d.2", n))
		// Keep server-initiated handshake/rekey packets on its advertised
		// endpoint. Without prefsrc, an asynchronous packet can originate at a
		// veth address, legitimately roam the client and invalidate the fixture.
		command("ip", "netns", "exec", "wgserver", "ip", "route", "add", fmt.Sprintf("10.44.%d.0/24", n), "via", fmt.Sprintf("10.55.%d.1", n), "src", "203.0.113.9")
		command("ip", "netns", "exec", isp, "sysctl", "-w", "net.ipv4.ip_forward=1")
	}
	command("ip", "netns", "exec", "wgserver", "ip", "addr", "add", "203.0.113.1/32", "dev", "lo")
	clientPrivate := strings.TrimSpace(string(command("wg", "genkey")))
	serverPrivate := strings.TrimSpace(string(command("wg", "genkey")))
	public := func(private string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "wg", "pubkey")
		cmd.Stdin = strings.NewReader(private)
		b, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(b))
	}
	clientPublic, serverPublic := public(clientPrivate), public(serverPrivate)
	for name, key := range map[string]string{"client": clientPrivate, "server": serverPrivate} {
		if err := os.WriteFile("/opt/wg-"+name+".key", []byte(key), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command("ip", "link", "add", "wguplink", "type", "wireguard")
	command("ip", "addr", "add", "10.77.0.1/24", "dev", "wguplink")
	command("wg", "set", "wguplink", "private-key", "/opt/wg-client.key", "fwmark", "0x4151", "peer", serverPublic, "allowed-ips", "0.0.0.0/0", "endpoint", "203.0.113.9:51820")
	command("ip", "link", "set", "wguplink", "up")
	// Provision the payload route as a real tunnel lifecycle would. A bound
	// socket alone does not supply a reverse route for rp_filter after the
	// primary main-table default disappears. Outer marked UDP bypasses this
	// route through the separate native/managed transport tables.
	command("ip", "route", "add", "203.0.113.9/32", "dev", "wguplink")
	if os.Getenv("UPLINK_WG_STRICT_RPF") == "1" {
		command("sysctl", "-w", "net.ipv4.conf.wguplink.rp_filter=1")
	}
	t.Log("fixture reverse-path filter:", string(command("sysctl", "net.ipv4.conf.wguplink.rp_filter")))
	command("ip", "netns", "exec", "wgserver", "ip", "link", "add", "wgremote", "type", "wireguard")
	command("ip", "netns", "exec", "wgserver", "ip", "addr", "add", "10.77.0.2/24", "dev", "wgremote")
	command("ip", "netns", "exec", "wgserver", "wg", "set", "wgremote", "private-key", "/opt/wg-server.key", "listen-port", "51820", "peer", clientPublic, "allowed-ips", "10.77.0.1/32")
	command("ip", "netns", "exec", "wgserver", "ip", "link", "set", "wgremote", "up")
	for _, family := range []string{"-4", "-6"} {
		command("ip", family, "route", "add", "unreachable", "default", "table", "151", "proto", "196", "metric", "32767")
		command("ip", family, "rule", "add", "pref", "110", "fwmark", "0x4151", "lookup", "151")
	}
	command("ip", "route", "add", "default", "via", "10.44.1.2", "dev", "wan1", "src", "10.44.1.1", "table", "151", "proto", "196", "metric", "10")
	exe, _ := os.Executable()
	https := exec.Command("ip", "netns", "exec", "wgserver", exe, "-test.run", "^TestWireguardHTTPSFixture$", "-test.timeout", "110s")
	https.Env = append(os.Environ(), "UPLINK_WG_HTTPS_FIXTURE=1")
	https.Stdout = os.Stdout
	https.Stderr = os.Stderr
	if err := https.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { https.Process.Kill(); https.Wait() })
	dns := exec.Command("ip", "netns", "exec", "wgserver", os.Getenv("FIRC_UPLINK_WORKSPACE_ROOT")+"/tmp/bounddnsmatch", "--serve", "10.77.0.2", "5300", "9", "0")
	dns.Stdout = os.Stdout
	dns.Stderr = os.Stderr
	if err := dns.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dns.Process.Kill(); dns.Wait() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat("/opt/wg-https.ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTPS fixture not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	command("ip", "route", "add", "blackhole", "203.0.113.250/32", "table", "100", "proto", "42")
	foreignBefore := string(command("ip", "-j", "route", "show", "table", "100"))
	dataDir := t.TempDir()
	target := ManagedTarget{Ref: "kernel-wg", Label: "native fixture", Interface: "wguplink", Peer: serverPublic, Endpoint: "203.0.113.9:51820", TunnelDNS: []string{"10.77.0.2:5300"}, Enabled: true}
	var tunnelMu sync.Mutex
	lastProvider := "isp1"
	deps := ManagedDeps{Directory: dataDir, Lookup: func(context.Context, string) (ManagedTarget, error) { return target, nil }, List: func(context.Context) ([]ManagedTarget, error) { return []ManagedTarget{target}, nil }, Providers: (resolver{}).Providers, Run: run,
		PeerMark: func(context.Context, ManagedTarget) (uint32, error) { return 0x4151, nil },
		PeerMarks: func(context.Context) (map[string][]uint32, error) {
			return map[string][]uint32{"wguplink": {0x4151}}, nil
		},
		WithLock: func(ctx context.Context, ref string, work func() error) error {
			tunnelMu.Lock()
			defer tunnelMu.Unlock()
			return work()
		},
		ApplyBinding: func(ctx context.Context, ref, provider, device string) error {
			lastProvider = provider
			n := "1"
			if device == "wan2" {
				n = "2"
			}
			// Stand in for NDMS connect-via, preserving its own mark/table.
			binding, err := uplink.InspectBinding(device)
			if err != nil {
				return err
			}
			_, err = run(ctx, "ip", "route", "replace", "default", "via", "10.44."+n+".2", "dev", device, "src", binding.IPv4, "table", "151", "proto", "196", "metric", "10")
			return err
		},
	}
	adapter, err := NewManaged(deps)
	if err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(dataDir, "policies.json")
	store, err := uplink.OpenStore(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(store, resolver{}, map[string]uplink.Adapter{"wireguard": adapter})
	p, err := c.Put(ctx, uplink.Policy{ID: "wg", Target: uplink.Target{Engine: "wireguard", Ref: "kernel-wg"}, Providers: []string{"isp1", "isp2"}, Enabled: true, IntervalSeconds: 5, TimeoutSeconds: 5, FailureThreshold: 1, HoldDownSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	c.Tick(ctx)
	if s := c.List()[0].State; s.Phase != "healthy" || s.Provider != "isp1" {
		t.Fatal("initial", s)
	}
	unblock := testsupport.BlockTransport(t, "wan1", "udp", "51820")
	testsupport.AssertDirectWAN(t, "wan1", 1)
	time.Sleep(6 * time.Second)
	c.Tick(ctx)
	if s := c.List()[0].State; s.Phase != "healthy" || s.Provider != "isp2" {
		t.Fatal("selective UDP drop", s)
	}
	testsupport.AssertDirectWAN(t, "wan1", 1)
	unblock()
	if err := c.Switch(ctx, p.ID, "isp1", p.Revision); err != nil {
		t.Fatal(err)
	}
	t.Log("kernel selective WireGuard UDP drop triggered fallback while direct WAN1 HTTP remained reachable")
	command("ip", "link", "set", "wan1", "down")
	time.Sleep(11 * time.Second) // manual-switch hold-down
	c.Tick(ctx)
	if s := c.List()[0].State; s.Phase != "healthy" || s.Provider != "isp2" {
		t.Fatal("fallback", s)
	}
	remote := string(command("ip", "netns", "exec", "wgserver", "wg", "show", "wgremote", "endpoints"))
	if !strings.Contains(remote, "10.44.2.1:") {
		t.Fatal("peer did not roam to ISP2", remote)
	}
	b, _ := os.ReadFile("/opt/wg-https.requests")
	before := strings.Count(string(b), "TLS probe")
	if before < 2 {
		t.Fatal("handshake without HTTPS payload")
	}
	command("ip", "link", "set", "wan2", "down")
	time.Sleep(6 * time.Second)
	c.Tick(ctx)
	if s := c.List()[0].State; s.Phase != "unavailable" {
		t.Fatal("both paths down", s)
	}
	b, _ = os.ReadFile("/opt/wg-https.requests")
	if strings.Count(string(b), "TLS probe") != before {
		t.Fatal("payload escaped failed WANs")
	}
	command("ip", "link", "set", "wan2", "up")
	command("ip", "route", "replace", "default", "via", "10.44.2.2", "dev", "wan2", "table", "202") // DHCP restores the provider route
	if err := c.Switch(ctx, p.ID, "isp2", p.Revision); err != nil {
		t.Fatal(err)
	}
	if s := c.List()[0].State; s.Phase != "healthy" || s.Provider != "isp2" {
		t.Fatal("recovery", s)
	}
	command("ip", "addr", "del", "10.44.2.1/24", "dev", "wan2")
	command("ip", "addr", "add", "10.44.2.3/24", "dev", "wan2")
	command("ip", "route", "replace", "default", "via", "10.44.2.2", "dev", "wan2", "table", "202")
	command("ip", "rule", "del", "pref", "150", "from", "10.44.2.1/32", "lookup", "202")
	command("ip", "rule", "add", "pref", "150", "from", "10.44.2.3/32", "lookup", "202")
	time.Sleep(6 * time.Second)
	c.Tick(ctx)
	if s := c.List()[0].State; s.Phase != "healthy" || s.Provider != "isp2" {
		t.Fatal("WAN source address refresh", s)
	}
	if remote := string(command("ip", "netns", "exec", "wgserver", "wg", "show", "wgremote", "endpoints")); !strings.Contains(remote, "10.44.2.3:") {
		t.Fatal("source address stayed stale", remote)
	}
	t.Log("native manager refreshed changed WAN IPv4 source and confirmed HTTPS")
	reopened, err := uplink.OpenStore(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	restartedAdapter, err := NewManaged(deps)
	if err != nil {
		t.Fatal(err)
	}
	restarted := uplink.New(reopened, resolver{}, map[string]uplink.Adapter{"wireguard": restartedAdapter})
	restarted.Tick(ctx)
	if s := restarted.List()[0].State; s.Phase != "healthy" || s.Provider != "isp2" {
		t.Fatal("daemon restart lost durable WAN binding", s)
	}
	t.Log("kernel WireGuard daemon restart reconciled durable WAN binding and verified HTTPS")
	profile := restartedAdapter.profiles["kernel-wg"]
	if profile.Table == 100 || profile.Table == 151 || profile.Mark != 0x4151 {
		t.Fatal("allocator took foreign resources", profile)
	}
	for _, family := range []string{"-4", "-6"} {
		command("ip", family, "rule", "del", "pref", fmt.Sprint(profile.Priority), "fwmark", "0x4151", "lookup", fmt.Sprint(profile.Table))
		command("ip", family, "route", "del", "unreachable", "default", "table", fmt.Sprint(profile.Table), "proto", "196", "metric", "32767")
		if family == "-4" {
			command("ip", family, "route", "del", "default", "table", fmt.Sprint(profile.Table), "proto", "196", "metric", "10")
		}
	}
	if err := deps.WithLock(ctx, target.Ref, func() error { return restartedAdapter.PrepareForStart(ctx, target.Ref) }); err != nil {
		t.Fatal("before-start rehydration", err)
	}
	if err := restartedAdapter.Probe(ctx, p.Target, "wan2"); err != nil {
		t.Fatal("rehydrated path", err)
	}
	if err := restarted.Delete(context.Background(), p.ID, p.Revision); err != nil {
		t.Fatal("release policy", err)
	}
	if restartedAdapter.OwnsTarget(target.Ref) || lastProvider != "isp2" {
		t.Fatal("cleanup lost last explicit binding")
	}
	if foreignAfter := string(command("ip", "-j", "route", "show", "table", "100")); foreignAfter != foreignBefore {
		t.Fatal("cleanup changed foreign table")
	}
	if got := strings.TrimSpace(string(command("wg", "show", "wguplink", "fwmark"))); got != "0x4151" {
		t.Fatal("kernel mark changed", got)
	}
	remaining, err := uplink.InspectBinding("wguplink")
	if err != nil {
		t.Fatal(err)
	}
	remaining.DNSServers = target.TunnelDNS
	if err := uplink.ProbeBinding(ctx, remaining); err != nil {
		t.Fatal("cleanup lost the last explicit path", err)
	}
	t.Log("native manager allocated, rehydrated and released exclusive routes; native mark and foreign table preserved")
	t.Log("real kernel WireGuard: HTTPS, WAN fallback, peer roaming, both-down fail-closed, manual recovery passed")
}
