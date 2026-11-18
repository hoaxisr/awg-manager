//go:build linux

package firc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const (
	netnsEnvWorker        = "FIRC_NETNS_WORKER"
	cutoverNetnsEnvWorker = "FIRC_CUTOVER_NETNS_WORKER"
	netnsCloudIP1         = "198.51.100.10"
	netnsCloudIP2         = "198.51.100.20"
	netnsStaticCIDRIP     = "185.162.93.10"
	netnsStaticCIDR       = "185.162.93.0/24"
	netnsControlWANIP     = "203.0.113.10"
	netnsPort             = 19001
	netnsRedirPort        = 19443
	netnsForeignMark      = 0x00001234
	netnsTunBypassMark    = 0x00020000
	cutoverNetnsPort      = 19080
	cutoverFakeIP         = "198.18.0.10"
)

func TestKeenDNSOutputRoutingNetns(t *testing.T) {
	if os.Getenv(netnsEnvWorker) != "" || os.Getenv(cutoverNetnsEnvWorker) != "" {
		return
	}
	if os.Geteuid() != 0 {
		t.Skipf("requires root for Linux netns/iptables/ipset test (euid=%d)", os.Geteuid())
	}
	for _, bin := range []string{"ip", "iptables", "ipset"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary unavailable: %v", bin, err)
		}
	}

	suffix := fmt.Sprintf("%d", os.Getpid())
	nsRouter := "fext-rtr-" + suffix
	nsWAN := "fext-wan-" + suffix
	nsTun := "fext-tun-" + suffix

	mustRun := func(name string, args ...string) {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(out))
		}
	}
	nsRun := func(ns string, args ...string) {
		t.Helper()
		full := append([]string{"netns", "exec", ns}, args...)
		mustRun("ip", full...)
	}

	_ = exec.Command("ip", "netns", "del", nsRouter).Run()
	_ = exec.Command("ip", "netns", "del", nsWAN).Run()
	_ = exec.Command("ip", "netns", "del", nsTun).Run()

	mustRun("ip", "netns", "add", nsRouter)
	mustRun("ip", "netns", "add", nsWAN)
	mustRun("ip", "netns", "add", nsTun)
	t.Cleanup(func() {
		_ = exec.Command("ip", "netns", "del", nsRouter).Run()
		_ = exec.Command("ip", "netns", "del", nsWAN).Run()
		_ = exec.Command("ip", "netns", "del", nsTun).Run()
	})

	// Create router <-> WAN and router <-> Tun veth pairs.
	mustRun("ip", "link", "add", "vwan_r", "netns", nsRouter, "type", "veth", "peer", "name", "vwan_p", "netns", nsWAN)
	mustRun("ip", "link", "add", "vtun_r", "netns", nsRouter, "type", "veth", "peer", "name", "vtun_p", "netns", nsTun)

	// Configure router namespace.
	nsRun(nsRouter, "ip", "link", "set", "lo", "up")
	nsRun(nsRouter, "ip", "addr", "add", "10.201.0.1/24", "dev", "vwan_r")
	nsRun(nsRouter, "ip", "link", "set", "vwan_r", "up")
	nsRun(nsRouter, "ip", "addr", "add", "10.202.0.1/24", "dev", "vtun_r")
	nsRun(nsRouter, "ip", "link", "set", "vtun_r", "up")
	nsRun(nsRouter, "ip", "route", "replace", "default", "via", "10.201.0.2", "dev", "vwan_r")
	nsRun(nsRouter, "ip", "route", "replace", "default", "via", "10.202.0.2", "dev", "vtun_r", "table", "201")
	nsRun(nsRouter, "ip", "rule", "add", "pref", "100", "fwmark", "0x40010000/0x40ff0000", "lookup", "201")
	_ = exec.Command("ip", "netns", "exec", nsRouter, "sysctl", "-w",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.vwan_r.rp_filter=0",
		"net.ipv4.conf.vtun_r.rp_filter=0",
		"net.ipv4.conf.all.route_localnet=1",
	).Run()

	// Configure WAN remote namespace.
	nsRun(nsWAN, "ip", "link", "set", "lo", "up")
	nsRun(nsWAN, "ip", "addr", "add", "10.201.0.2/24", "dev", "vwan_p")
	nsRun(nsWAN, "ip", "link", "set", "vwan_p", "up")
	for _, ip := range []string{netnsCloudIP1, netnsCloudIP2, netnsStaticCIDRIP, netnsControlWANIP} {
		nsRun(nsWAN, "ip", "addr", "add", ip+"/32", "dev", "lo")
	}
	nsRun(nsWAN, "ip", "route", "replace", "10.201.0.0/24", "dev", "vwan_p")
	nsRun(nsWAN, "ip", "route", "replace", "default", "via", "10.201.0.1", "dev", "vwan_p")
	_ = exec.Command("ip", "netns", "exec", nsWAN, "sysctl", "-w",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.vwan_p.rp_filter=0",
	).Run()

	// Configure TUN remote namespace. Deliberately only routes 10.202.0.0/24 (not 10.201.0.0/24)
	// and drops non-redirected TCP to cloud IPs, so misclassified WAN connections break immediately.
	nsRun(nsTun, "ip", "link", "set", "lo", "up")
	nsRun(nsTun, "ip", "addr", "add", "10.202.0.2/24", "dev", "vtun_p")
	nsRun(nsTun, "ip", "link", "set", "vtun_p", "up")
	for _, ip := range []string{netnsCloudIP1, netnsCloudIP2, netnsStaticCIDRIP} {
		nsRun(nsTun, "ip", "addr", "add", ip+"/32", "dev", "lo")
	}
	nsRun(nsTun, "ip", "route", "replace", "10.202.0.0/24", "dev", "vtun_p")
	_ = exec.Command("ip", "netns", "exec", nsTun, "sysctl", "-w",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.vtun_p.rp_filter=0",
	).Run()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	startPeer := func(ns, role string) {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", ns, exe, "-test.run=^TestKeenDNSOutputRoutingNetnsWorker$", "-test.v")
		cmd.Env = append(os.Environ(), netnsEnvWorker+"="+role)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("StdoutPipe(%s): %v", role, err)
		}
		var stderrBuf strings.Builder
		cmd.Stderr = &stderrBuf
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s worker: %v", role, err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})

		ready := make(chan error, 1)
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				if strings.TrimSpace(sc.Text()) == "READY "+role {
					ready <- nil
					// Drain remaining output.
					_, _ = io.Copy(io.Discard, stdout)
					return
				}
			}
			ready <- fmt.Errorf("%s worker exited before READY; stderr=%s", role, stderrBuf.String())
		}()
		select {
		case err := <-ready:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s worker READY; stderr=%s", role, stderrBuf.String())
		}
	}

	startPeer(nsWAN, "wan")
	startPeer(nsTun, "tun")

	routerCmd := exec.Command("ip", "netns", "exec", nsRouter, exe, "-test.run=^TestKeenDNSOutputRoutingNetnsWorker$", "-test.v")
	routerCmd.Env = append(os.Environ(), netnsEnvWorker+"=router")
	out, err := routerCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("netns router worker failed: %v\n%s", err, string(out))
	}
	t.Logf("netns router worker output:\n%s", string(out))
}

func TestKeenDNSOutputRoutingNetnsWorker(t *testing.T) {
	role := os.Getenv(netnsEnvWorker)
	if role == "" {
		t.Skip("worker-only helper invoked by TestKeenDNSOutputRoutingNetns")
	}

	switch role {
	case "wan":
		runNetnsRemoteWorker(t, "wan", "WAN", true, []string{netnsCloudIP1, netnsCloudIP2, netnsStaticCIDRIP, netnsControlWANIP})
	case "tun":
		runNetnsRemoteWorker(t, "tun", "TUN", false, []string{netnsCloudIP1, netnsCloudIP2, netnsStaticCIDRIP})
	case "router":
		runNetnsRouterWorker(t)
	default:
		t.Fatalf("unknown %s=%q", netnsEnvWorker, role)
	}
}

func runNetnsRemoteWorker(t *testing.T, role, tag string, acceptTCP bool, udpIPs []string) {
	t.Helper()
	var listeners []io.Closer
	defer func() {
		for _, c := range listeners {
			_ = c.Close()
		}
	}()

	if acceptTCP {
		ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", netnsPort))
		if err != nil {
			t.Fatalf("%s listen tcp: %v", role, err)
		}
		listeners = append(listeners, ln)
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				go serveTaggedEchoTCP(conn, tag)
			}
		}()
	}

	// Bind per-IP UDP listeners so replies originate from the exact queried destination IP
	// instead of falling back to the primary veth address (10.201.0.2 / 10.202.0.2).
	for _, ip := range udpIPs {
		udpConn, err := net.ListenPacket("udp4", fmt.Sprintf("%s:%d", ip, netnsPort))
		if err != nil {
			t.Fatalf("%s listen udp %s: %v", role, ip, err)
		}
		listeners = append(listeners, udpConn)
		go func(c net.PacketConn) {
			buf := make([]byte, 2048)
			for {
				n, addr, err := c.ReadFrom(buf)
				if err != nil {
					return
				}
				_, _ = c.WriteTo([]byte(tag+":"+string(buf[:n])), addr)
			}
		}(udpConn)
	}

	fmt.Printf("READY %s\n", role)
	// Wait until killed by parent Cleanup.
	select {}
}

func serveTaggedEchoTCP(conn net.Conn, tag string) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		_, err := fmt.Fprintf(conn, "%s:%s\n", tag, sc.Text())
		if err != nil {
			return
		}
	}
}

func runNetnsRouterWorker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start local FIRC TCP redirect listener (target of FIRC_REDIR).
	redirLn, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", netnsRedirPort))
	if err != nil {
		t.Fatalf("listen redir proxy: %v", err)
	}
	defer redirLn.Close()
	go func() {
		for {
			c, err := redirLn.Accept()
			if err != nil {
				return
			}
			go serveTaggedEchoTCP(c, "PROXY")
		}
	}()

	mustIpt := func(args ...string) {
		t.Helper()
		out, err := exec.Command("iptables", append([]string{"-w"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("iptables %s: %v\n%s", strings.Join(args, " "), err, string(out))
		}
	}

	// Simulate simulated upstream fircd chains + foreign service chains & ipsets.
	mustIpt("-t", "mangle", "-N", "FOREIGN_OUT")
	mustIpt("-t", "mangle", "-A", "FOREIGN_OUT", "-j", "RETURN")
	mustIpt("-t", "mangle", "-A", "OUTPUT", "-j", "FOREIGN_OUT")
	mustIpt("-t", "mangle", "-N", "FOREIGN_POST")
	// Match specific 0x40011234 before masked 0x40010000/0x40ff0000 so ACCEPT on the first does not shadow the second.
	mustIpt("-t", "mangle", "-A", "FOREIGN_POST", "-o", "vtun_r", "-m", "mark", "--mark", "0x40011234/0xffffffff", "-j", "ACCEPT")
	mustIpt("-t", "mangle", "-A", "FOREIGN_POST", "-o", "vtun_r", "-m", "mark", "--mark", "0x40010000/0x40ff0000", "-j", "ACCEPT")
	mustIpt("-t", "mangle", "-A", "FOREIGN_POST", "-o", "vwan_r", "-m", "mark", "--mark", "0x00001234/0xffffffff", "-j", "ACCEPT")
	mustIpt("-t", "mangle", "-A", "POSTROUTING", "-j", "FOREIGN_POST")
	// Real tunnel interfaces SNAT locally-originated traffic after OUTPUT mark rerouting;
	// otherwise connected sockets bound to vwan_r (10.201.0.1) reach nsTun with an unroutable source IP.
	mustIpt("-t", "nat", "-A", "POSTROUTING", "-o", "vtun_r", "-j", "SNAT", "--to-source", "10.202.0.1")
	mustIpt("-t", "filter", "-N", "FOREIGN_FLT")
	mustIpt("-t", "filter", "-A", "FOREIGN_FLT", "-j", "RETURN")
	mustIpt("-t", "filter", "-A", "OUTPUT", "-j", "FOREIGN_FLT")
	if out, err := exec.Command("ipset", "create", "FOREIGN_SET", "hash:net", "family", "inet", "-exist").CombinedOutput(); err != nil {
		t.Fatalf("ipset FOREIGN_SET: %v\n%s", err, string(out))
	}
	if out, err := exec.Command("ipset", "add", "FOREIGN_SET", "198.51.100.99", "-exist").CombinedOutput(); err != nil {
		t.Fatalf("ipset add FOREIGN_SET: %v\n%s", err, string(out))
	}

	// Build real fircd source chains in mangle and nat tables.
	if out, err := exec.Command("ipset", "create", "firc_c100d001", "hash:net", "family", "inet", "-exist").CombinedOutput(); err != nil {
		t.Fatalf("ipset create firc_c100d001: %v\n%s", err, string(out))
	}
	mustIpt("-t", "mangle", "-N", "FIRC_c100d001")
	mustIpt("-t", "mangle", "-N", "FIRC_c100d001D")
	mustIpt("-t", "mangle", "-A", "FIRC_c100d001D", "-j", "MARK", "--set-xmark", "0x40010000/0x40ff0000")
	mustIpt("-t", "mangle", "-A", "FIRC_c100d001", "-m", "set", "--match-set", "firc_c100d001", "dst", "-j", "FIRC_c100d001D")
	mustIpt("-t", "mangle", "-A", "FIRC_c100d001", "-d", netnsStaticCIDR, "-j", "FIRC_c100d001D")

	mustIpt("-t", "nat", "-N", "FIRC_REDIR")
	mustIpt("-t", "nat", "-A", "FIRC_REDIR", "-p", "tcp", "-j", "REDIRECT", "--to-ports", strconv.Itoa(netnsRedirPort))

	foreignBeforeMangleOut, _ := exec.Command("iptables", "-w", "-t", "mangle", "-S", "FOREIGN_OUT").Output()
	foreignBeforeFilterFlt, _ := exec.Command("iptables", "-w", "-t", "filter", "-S", "FOREIGN_FLT").Output()
	foreignBeforeIpset, _ := exec.Command("ipset", "list", "FOREIGN_SET").Output()

	// Use REAL iptables / ipset / DNS / KeenCloud runners inside this namespace!
	// Pass the default runners explicitly so hasCustomExt* returns true even when
	// ConfigDir is t.TempDir() (not /opt/etc/firc).
	SetExtIptablesRunner(defaultExtIptablesRunner)
	SetExtIpsetRunner(defaultExtIpsetRunner)
	InvalidateOutputRoutingCache()
	t.Cleanup(func() {
		SetExtIptablesRunner(nil)
		SetExtIpsetRunner(nil)
		InvalidateOutputRoutingCache()
		SetExtDNSLookupIPv4(nil)
	})

	var cloudIPMu sync.Mutex
	activeCloudIP := netnsCloudIP1
	SetExtDNSLookupIPv4(func(_ context.Context, _ string) ([]netip.Addr, error) {
		cloudIPMu.Lock()
		ip := activeCloudIP
		cloudIPMu.Unlock()
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	op.SetKeenCloudProvider(func(_ context.Context) (KeenCloudRelaySnapshot, error) {
		cloudIPMu.Lock()
		ip := activeCloudIP
		cloudIPMu.Unlock()
		return KeenCloudRelaySnapshot{Enabled: true, RelayIPs: []string{ip}}, nil
	})

	var groupsMu sync.Mutex
	groups := []Group{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/system" || r.URL.Path == "/api/v1/system/netfilter"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"dirty":false}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/reload":
			_, _ = w.Write([]byte(`{"dirty":false}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/groups"):
			groupsMu.Lock()
			snap := cloneGroups(groups)
			groupsMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": snap})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/groups":
			var req struct {
				Groups []Group `json:"groups"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			groupsMu.Lock()
			groups = cloneGroups(req.Groups)
			groupsMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": req.Groups})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))

	dialTCPWithMark := func(ip string, soMark int) (net.Conn, error) {
		d := net.Dialer{
			Timeout: 3 * time.Second,
			Control: func(_, _ string, c syscall.RawConn) error {
				if soMark == 0 {
					return nil
				}
				var sockErr error
				if err := c.Control(func(fd uintptr) {
					sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, soMark)
				}); err != nil {
					return err
				}
				return sockErr
			},
		}
		return d.DialContext(ctx, "tcp4", fmt.Sprintf("%s:%d", ip, netnsPort))
	}

	roundTripTCP := func(c net.Conn, msg string) string {
		t.Helper()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := fmt.Fprintf(c, "%s\n", msg); err != nil {
			t.Fatalf("TCP write %q: %v", msg, err)
		}
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			t.Fatalf("TCP read for %q: %v", msg, err)
		}
		return strings.TrimSpace(line)
	}

	dialUDPWithMark := func(ip string, soMark int) *net.UDPConn {
		t.Helper()
		d := net.Dialer{
			Timeout: 3 * time.Second,
			Control: func(_, _ string, c syscall.RawConn) error {
				if soMark == 0 {
					return nil
				}
				var sockErr error
				if err := c.Control(func(fd uintptr) {
					sockErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, soMark)
				}); err != nil {
					return err
				}
				return sockErr
			},
		}
		conn, err := d.DialContext(ctx, "udp4", fmt.Sprintf("%s:%d", ip, netnsPort))
		if err != nil {
			t.Fatalf("dial UDP %s (mark=0x%x): %v", ip, soMark, err)
		}
		return conn.(*net.UDPConn)
	}

	roundTripUDP := func(c *net.UDPConn, msg string) string {
		t.Helper()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write([]byte(msg)); err != nil {
			t.Fatalf("UDP write %q: %v", msg, err)
		}
		buf := make([]byte, 512)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("UDP read for %q: %v", msg, err)
		}
		return string(buf[:n])
	}

	// 1. Open long-lived non-FIRC TCP connections BEFORE enabling KeenDNS:
	//    - dynamic ipset target (netnsCloudIP1) with plain mark=0 and foreign SO_MARK=0x1234
	//    - static cloud CIDR target (netnsStaticCIDRIP)
	//    - unrelated WAN control target
	preIpsetConn, err := dialTCPWithMark(netnsCloudIP1, 0)
	if err != nil {
		t.Fatalf("pre-enable dial cloudIP1: %v", err)
	}
	defer preIpsetConn.Close()
	if got := roundTripTCP(preIpsetConn, "before_ipset"); got != "WAN:before_ipset" {
		t.Fatalf("preIpsetConn before enable got %q, want WAN:before_ipset", got)
	}

	preForeignConn, err := dialTCPWithMark(netnsCloudIP1, netnsForeignMark)
	if err != nil {
		t.Fatalf("pre-enable dial foreign mark: %v", err)
	}
	defer preForeignConn.Close()
	if got := roundTripTCP(preForeignConn, "before_foreign"); got != "WAN:before_foreign" {
		t.Fatalf("preForeignConn before enable got %q, want WAN:before_foreign", got)
	}

	preStaticConn, err := dialTCPWithMark(netnsStaticCIDRIP, 0)
	if err != nil {
		t.Fatalf("pre-enable dial static CIDR: %v", err)
	}
	defer preStaticConn.Close()
	if got := roundTripTCP(preStaticConn, "before_static"); got != "WAN:before_static" {
		t.Fatalf("preStaticConn before enable got %q, want WAN:before_static", got)
	}

	preControlConn, err := dialTCPWithMark(netnsControlWANIP, 0)
	if err != nil {
		t.Fatalf("pre-enable dial control WAN: %v", err)
	}
	defer preControlConn.Close()
	if got := roundTripTCP(preControlConn, "before_ctrl"); got != "WAN:before_ctrl" {
		t.Fatalf("preControlConn before enable got %q, want WAN:before_ctrl", got)
	}

	// 2. Enable KeenDNS Cloud with XHTTP DISABLED (needXHTTP == false).
	cfgKDOnXhttpOff := ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"tunvless0"}},
	}
	if _, err := op.ApplyExtConfig(ctx, cfgKDOnXhttpOff); err != nil {
		t.Fatalf("ApplyExtConfig(KD=on,XHTTP=off): %v", err)
	}

	// Inspect real iptables -S canonicalization inside netns to guarantee reconcile idempotency.
	if err := op.ReconcileExtNetfilterStrict(ctx, cfgKDOnXhttpOff, []Group{{ID: KeenDNSGroupID, Name: "KeenDNS", Interface: []string{"tunvless0"}, Enable: true}}); err != nil {
		t.Fatalf("idempotent ReconcileExtNetfilterStrict failed (possible iptables -S canonical mismatch): %v", err)
	}

	// Pre-existing ESTABLISHED connections MUST stay on direct WAN!
	for _, tc := range []struct {
		name string
		c    net.Conn
		msg  string
		want string
	}{
		{"pre-ipset-TCP", preIpsetConn, "after_ipset", "WAN:after_ipset"},
		{"pre-foreign-mark-TCP", preForeignConn, "after_foreign", "WAN:after_foreign"},
		{"pre-static-CIDR-TCP", preStaticConn, "after_static", "WAN:after_static"},
		{"pre-control-WAN-TCP", preControlConn, "after_ctrl", "WAN:after_ctrl"},
	} {
		if got := roundTripTCP(tc.c, tc.msg); got != tc.want {
			t.Fatalf("%s disrupted after enabling KeenDNS: got %q want %q", tc.name, got, tc.want)
		}
	}

	// New TCP connections to dynamic FEXT_CLOUD IP and static cloud CIDR MUST enter proxy and stay proxied on subsequent packets.
	newIpsetTCP, err := dialTCPWithMark(netnsCloudIP1, 0)
	if err != nil {
		t.Fatalf("new ipset TCP dial failed: %v", err)
	}
	defer newIpsetTCP.Close()
	for _, msg := range []string{"pkt1", "pkt2", "pkt3"} {
		if got := roundTripTCP(newIpsetTCP, msg); got != "PROXY:"+msg {
			t.Fatalf("new ipset TCP (%s): got %q want PROXY:%s", msg, got, msg)
		}
	}

	newStaticTCP, err := dialTCPWithMark(netnsStaticCIDRIP, 0)
	if err != nil {
		t.Fatalf("new static CIDR TCP dial failed: %v", err)
	}
	defer newStaticTCP.Close()
	for _, msg := range []string{"spkt1", "spkt2"} {
		if got := roundTripTCP(newStaticTCP, msg); got != "PROXY:"+msg {
			t.Fatalf("new static CIDR TCP (%s): got %q want PROXY:%s", msg, got, msg)
		}
	}

	// New UDP flows with XHTTP OFF MUST restore 0x40010000 on packet 2+ so every packet goes via vtun_r ("TUN:...").
	// Test both dynamic ipset and static CIDR, AND foreign SO_MARK=0x1234 preservation.
	newIpsetUDP := dialUDPWithMark(netnsCloudIP1, 0)
	defer newIpsetUDP.Close()
	for _, msg := range []string{"u1", "u2", "u3"} {
		if got := roundTripUDP(newIpsetUDP, msg); got != "TUN:"+msg {
			t.Fatalf("new ipset UDP packet %q with XHTTP=off got %q, want TUN:%s", msg, got, msg)
		}
	}

	newStaticForeignUDP := dialUDPWithMark(netnsStaticCIDRIP, netnsForeignMark)
	defer newStaticForeignUDP.Close()
	for _, msg := range []string{"sfu1", "sfu2"} {
		if got := roundTripUDP(newStaticForeignUDP, msg); got != "TUN:"+msg {
			t.Fatalf("new static CIDR UDP with foreign SO_MARK=0x%x packet %q got %q, want TUN:%s",
				netnsForeignMark, msg, got, msg)
		}
	}

	// Verify kernel counters proved exact mark preservation:
	// - vtun_r saw packets with 0x40011234/0xffffffff (foreign 0x1234 preserved + FIRC 0x40010000 restored)
	// - vwan_r saw preForeignConn packets with 0x00001234/0xffffffff (FIRC bits remained 0)
	postCounters, _ := exec.Command("iptables", "-w", "-t", "mangle", "-L", "FOREIGN_POST", "-v", "-n", "-x").CombinedOutput()
	postText := string(postCounters)
	for _, wantMark := range []string{"0x40011234", "0x1234", "0x40010000"} {
		if !strings.Contains(postText, wantMark) {
			t.Fatalf("FOREIGN_POST missing expected mark line %s:\n%s", wantMark, postText)
		}
	}
	if countLine := extractMarkPacketCount(postText, "0x40011234"); countLine < 2 {
		t.Fatalf("expected >=2 packets on vtun_r with exact preserved+restored mark 0x40011234, got %d\n%s", countLine, postText)
	}
	if countLine := extractMarkPacketCount(postText, "0x1234"); countLine < 1 {
		t.Fatalf("expected >=1 packets on vwan_r with untouched foreign mark 0x1234, got %d\n%s", countLine, postText)
	}

	// Verify tunnel uplink bypass mark (0x00020000, inside 0x00ff0000) is NOT hijacked into FEXT_c100d001 / REDIR.
	bypassTCP, err := dialTCPWithMark(netnsCloudIP1, netnsTunBypassMark)
	if err != nil {
		t.Fatalf("tunnel bypass dial failed: %v", err)
	}
	defer bypassTCP.Close()
	if got := roundTripTCP(bypassTCP, "bypass"); got != "WAN:bypass" {
		t.Fatalf("tunnel uplink bypass mark 0x%x was hijacked: got %q want WAN:bypass", netnsTunBypassMark, got)
	}

	// 3. Test dynamic FEXT_CLOUD swap:
	//    Open a direct connection to netnsCloudIP2 while it is NOT in FEXT_CLOUD yet.
	preSwapConn2, err := dialTCPWithMark(netnsCloudIP2, 0)
	if err != nil {
		t.Fatalf("pre-swap dial cloudIP2: %v", err)
	}
	defer preSwapConn2.Close()
	if got := roundTripTCP(preSwapConn2, "preswap2"); got != "WAN:preswap2" {
		t.Fatalf("preSwapConn2 before swap got %q, want WAN:preswap2", got)
	}

	cloudIPMu.Lock()
	activeCloudIP = netnsCloudIP2
	cloudIPMu.Unlock()
	if err := op.syncKeenDNSCloudWithGen(ctx, true, 0, true); err != nil {
		t.Fatalf("syncKeenDNSCloudWithGen swap to cloudIP2 failed: %v", err)
	}
	// Existing ESTABLISHED connection to cloudIP2 MUST survive swap on WAN; already-active FIRC UDP to cloudIP1 MUST stay on TUN via connmark!
	if got := roundTripTCP(preSwapConn2, "afterswap2"); got != "WAN:afterswap2" {
		t.Fatalf("preSwapConn2 broke after FEXT_CLOUD swap: got %q want WAN:afterswap2", got)
	}
	if got := roundTripUDP(newIpsetUDP, "afterswap_existing_firc"); got != "TUN:afterswap_existing_firc" {
		t.Fatalf("already-classified FIRC flow to cloudIP1 lost route after swap: got %q want TUN:...", got)
	}
	newIP2TCP, err := dialTCPWithMark(netnsCloudIP2, 0)
	if err != nil {
		t.Fatalf("new cloudIP2 TCP dial after swap: %v", err)
	}
	defer newIP2TCP.Close()
	if got := roundTripTCP(newIP2TCP, "ip2_proxy"); got != "PROXY:ip2_proxy" {
		t.Fatalf("new connection to swapped cloudIP2 got %q, want PROXY:ip2_proxy", got)
	}

	// 4. Disable KeenDNS -> cleanup, existing WAN connections survive, new cloud connections return to WAN, foreign rules untouched.
	cfgKDOff := ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: false, Interfaces: []string{"tunvless0"}},
	}
	if _, err := op.ApplyExtConfig(ctx, cfgKDOff); err != nil {
		t.Fatalf("ApplyExtConfig(KD=off) failed: %v", err)
	}
	if got := roundTripTCP(preIpsetConn, "during_disable"); got != "WAN:during_disable" {
		t.Fatalf("preIpsetConn failed during disable: %q", got)
	}
	duringDisableNew, err := dialTCPWithMark(netnsCloudIP2, 0)
	if err != nil {
		t.Fatalf("dial cloudIP2 while disabled failed: %v", err)
	}
	defer duringDisableNew.Close()
	if got := roundTripTCP(duringDisableNew, "wan_disabled"); got != "WAN:wan_disabled" {
		t.Fatalf("expected direct WAN while KeenDNS disabled, got %q", got)
	}

	// 5. Re-enable KeenDNS while duringDisableNew stays ESTABLISHED!
	if _, err := op.ApplyExtConfig(ctx, cfgKDOnXhttpOff); err != nil {
		t.Fatalf("ApplyExtConfig re-enable failed: %v", err)
	}
	if got := roundTripTCP(duringDisableNew, "survived_reenable"); got != "WAN:survived_reenable" {
		t.Fatalf("pre-existing connection opened during disable broke on re-enable: got %q", got)
	}
	reenabledNew, err := dialTCPWithMark(netnsCloudIP2, 0)
	if err != nil {
		t.Fatalf("new dial after re-enable failed: %v", err)
	}
	defer reenabledNew.Close()
	if got := roundTripTCP(reenabledNew, "reenabled_proxy"); got != "PROXY:reenabled_proxy" {
		t.Fatalf("new connection after re-enable got %q, want PROXY:reenabled_proxy", got)
	}

	// 6. Disable again and assert foreign iptables chains & foreign ipsets had zero unwanted changes.
	if _, err := op.ApplyExtConfig(ctx, cfgKDOff); err != nil {
		t.Fatalf("final disable failed: %v", err)
	}
	foreignAfterMangleOut, _ := exec.Command("iptables", "-w", "-t", "mangle", "-S", "FOREIGN_OUT").Output()
	foreignAfterFilterFlt, _ := exec.Command("iptables", "-w", "-t", "filter", "-S", "FOREIGN_FLT").Output()
	foreignAfterIpset, _ := exec.Command("ipset", "list", "FOREIGN_SET").Output()
	if string(foreignBeforeMangleOut) != string(foreignAfterMangleOut) {
		t.Fatalf("foreign mangle chain altered:\nbefore:\n%s\nafter:\n%s", foreignBeforeMangleOut, foreignAfterMangleOut)
	}
	if string(foreignBeforeFilterFlt) != string(foreignAfterFilterFlt) {
		t.Fatalf("foreign filter chain altered:\nbefore:\n%s\nafter:\n%s", foreignBeforeFilterFlt, foreignAfterFilterFlt)
	}
	if string(foreignBeforeIpset) != string(foreignAfterIpset) {
		t.Fatalf("foreign ipset altered:\nbefore:\n%s\nafter:\n%s", foreignBeforeIpset, foreignAfterIpset)
	}
}

func extractMarkPacketCount(iptablesLOutput, markHex string) uint64 {
	for _, line := range strings.Split(iptablesLOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		matchedMark := false
		for _, f := range fields {
			if f == markHex || f == markHex+"/0xffffffff" {
				matchedMark = true
				break
			}
		}
		if matchedMark {
			if n, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}

type cutoverProbeReport struct {
	Total100    int    `json:"total100"`
	Tun100      int    `json:"tun100"`
	Wan100      int    `json:"wan100"`
	Refused100  int    `json:"refused100"`
	TotalFake   int    `json:"totalFake"`
	TunFake     int    `json:"tunFake"`
	WanFake     int    `json:"wanFake"`
	RefusedFake int    `json:"refusedFake"`
	FirstLeak   string `json:"firstLeak,omitempty"`
}

func TestEngineSwitchCutoverZeroWANLeakNetns(t *testing.T) {
	if os.Getenv(netnsEnvWorker) != "" || os.Getenv(cutoverNetnsEnvWorker) != "" {
		return
	}
	if os.Geteuid() != 0 {
		t.Skipf("requires root for Linux netns/iptables test (euid=%d)", os.Geteuid())
	}
	for _, bin := range []string{"ip", "iptables"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary unavailable: %v", bin, err)
		}
	}

	suffix := fmt.Sprintf("%d", os.Getpid())
	nsClient := "co-cli-" + suffix
	nsRouter := "co-rtr-" + suffix
	nsWAN := "co-wan-" + suffix
	nsTun := "co-tun-" + suffix

	mustRun := func(name string, args ...string) {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, string(out))
		}
	}
	nsRun := func(ns string, args ...string) {
		t.Helper()
		mustRun("ip", append([]string{"netns", "exec", ns}, args...)...)
	}

	for _, ns := range []string{nsClient, nsRouter, nsWAN, nsTun} {
		_ = exec.Command("ip", "netns", "del", ns).Run()
		mustRun("ip", "netns", "add", ns)
	}
	t.Cleanup(func() {
		for _, ns := range []string{nsClient, nsRouter, nsWAN, nsTun} {
			_ = exec.Command("ip", "netns", "del", ns).Run()
		}
	})

	mustRun("ip", "link", "add", "br0", "netns", nsRouter, "type", "veth", "peer", "name", "vcli_p", "netns", nsClient)
	mustRun("ip", "link", "add", "vwan_r", "netns", nsRouter, "type", "veth", "peer", "name", "vwan_p", "netns", nsWAN)
	mustRun("ip", "link", "add", "tunvless0", "netns", nsRouter, "type", "veth", "peer", "name", "vtunf_p", "netns", nsTun)
	mustRun("ip", "link", "add", "opkgtun0", "netns", nsRouter, "type", "veth", "peer", "name", "vtuns_p", "netns", nsTun)

	// Configure Client namespace.
	nsRun(nsClient, "ip", "link", "set", "lo", "up")
	nsRun(nsClient, "ip", "addr", "add", "10.210.0.2/24", "dev", "vcli_p")
	nsRun(nsClient, "ip", "link", "set", "vcli_p", "up")
	nsRun(nsClient, "ip", "route", "replace", "default", "via", "10.210.0.1", "dev", "vcli_p")
	_ = exec.Command("ip", "netns", "exec", nsClient, "sysctl", "-w",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.vcli_p.rp_filter=0",
	).Run()

	// Configure Router namespace (acts as LAN/WAN/tunnel forwarder).
	nsRun(nsRouter, "ip", "link", "set", "lo", "up")
	for _, item := range [][2]string{
		{"br0", "10.210.0.1/24"},
		{"vwan_r", "10.211.0.1/24"},
		{"tunvless0", "10.212.0.1/24"},
		{"opkgtun0", "10.213.0.1/24"},
	} {
		nsRun(nsRouter, "ip", "addr", "add", item[1], "dev", item[0])
		nsRun(nsRouter, "ip", "link", "set", item[0], "up")
	}
	nsRun(nsRouter, "ip", "route", "replace", "default", "via", "10.211.0.2", "dev", "vwan_r")
	nsRun(nsRouter, "ip", "route", "replace", "198.18.0.0/15", "via", "10.211.0.2", "dev", "vwan_r")
	nsRun(nsRouter, "ip", "route", "replace", "default", "via", "10.212.0.2", "dev", "tunvless0", "table", "201")
	nsRun(nsRouter, "ip", "route", "replace", "default", "via", "10.213.0.2", "dev", "opkgtun0", "table", "101")
	nsRun(nsRouter, "ip", "rule", "add", "pref", "101", "fwmark", "0x10000/0xff0000", "lookup", "101")
	nsRun(nsRouter, "ip", "rule", "add", "pref", "201", "fwmark", "0x40010000/0x40ff0000", "lookup", "201")
	_ = exec.Command("ip", "netns", "exec", nsRouter, "sysctl", "-w",
		"net.ipv4.ip_forward=1",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.br0.rp_filter=0",
		"net.ipv4.conf.vwan_r.rp_filter=0",
		"net.ipv4.conf.tunvless0.rp_filter=0",
		"net.ipv4.conf.opkgtun0.rp_filter=0",
	).Run()
	nsRun(nsRouter, "iptables", "-w", "-P", "FORWARD", "ACCEPT")
	for _, outIface := range []string{"vwan_r", "tunvless0", "opkgtun0"} {
		nsRun(nsRouter, "iptables", "-w", "-t", "nat", "-A", "POSTROUTING", "-o", outIface, "-j", "MASQUERADE")
	}

	// Configure WAN namespace with kernel packet counters for both destinations.
	nsRun(nsWAN, "ip", "link", "set", "lo", "up")
	nsRun(nsWAN, "ip", "addr", "add", "10.211.0.2/24", "dev", "vwan_p")
	nsRun(nsWAN, "ip", "link", "set", "vwan_p", "up")
	nsRun(nsWAN, "ip", "addr", "add", netnsCloudIP1+"/32", "dev", "lo")
	nsRun(nsWAN, "ip", "addr", "add", cutoverFakeIP+"/32", "dev", "lo")
	nsRun(nsWAN, "ip", "route", "replace", "default", "via", "10.211.0.1", "dev", "vwan_p")
	_ = exec.Command("ip", "netns", "exec", nsWAN, "sysctl", "-w",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.vwan_p.rp_filter=0",
	).Run()
	nsRun(nsWAN, "iptables", "-w", "-t", "mangle", "-A", "PREROUTING", "-d", netnsCloudIP1, "-j", "ACCEPT")
	nsRun(nsWAN, "iptables", "-w", "-t", "mangle", "-A", "PREROUTING", "-d", cutoverFakeIP, "-j", "ACCEPT")

	// Configure TUN namespace (shared sink reachable via both tunvless0 and opkgtun0).
	nsRun(nsTun, "ip", "link", "set", "lo", "up")
	nsRun(nsTun, "ip", "addr", "add", "10.212.0.2/24", "dev", "vtunf_p")
	nsRun(nsTun, "ip", "link", "set", "vtunf_p", "up")
	nsRun(nsTun, "ip", "addr", "add", "10.213.0.2/24", "dev", "vtuns_p")
	nsRun(nsTun, "ip", "link", "set", "vtuns_p", "up")
	nsRun(nsTun, "ip", "addr", "add", netnsCloudIP1+"/32", "dev", "lo")
	nsRun(nsTun, "ip", "addr", "add", cutoverFakeIP+"/32", "dev", "lo")
	_ = exec.Command("ip", "netns", "exec", nsTun, "sysctl", "-w",
		"net.ipv4.conf.all.rp_filter=0",
		"net.ipv4.conf.default.rp_filter=0",
		"net.ipv4.conf.vtunf_p.rp_filter=0",
		"net.ipv4.conf.vtuns_p.rp_filter=0",
	).Run()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	ctrlDir := t.TempDir()
	startFile := filepath.Join(ctrlDir, "start.probe")
	stopFile := filepath.Join(ctrlDir, "stop.probe")
	reportFile := filepath.Join(ctrlDir, "report.json")

	startWorker := func(ns, role string, extraEnv ...string) *exec.Cmd {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", ns, exe, "-test.run=^TestEngineSwitchCutoverZeroWANLeakNetnsWorker$", "-test.v")
		cmd.Env = append(os.Environ(), cutoverNetnsEnvWorker+"="+role)
		cmd.Env = append(cmd.Env, extraEnv...)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("StdoutPipe(%s): %v", role, err)
		}
		var stderrBuf strings.Builder
		cmd.Stderr = &stderrBuf
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %s worker: %v", role, err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})

		ready := make(chan error, 1)
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				if strings.TrimSpace(sc.Text()) == "READY "+role {
					ready <- nil
					_, _ = io.Copy(io.Discard, stdout)
					return
				}
			}
			ready <- fmt.Errorf("%s worker exited before READY; stderr=%s", role, stderrBuf.String())
		}()
		select {
		case err := <-ready:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s worker READY; stderr=%s", role, stderrBuf.String())
		}
		return cmd
	}

	startWorker(nsWAN, "wan-sink", "FIRC_CUTOVER_STOP_FILE="+stopFile)
	startWorker(nsTun, "tun-sink", "FIRC_CUTOVER_STOP_FILE="+stopFile)
	proberCmd := startWorker(
		nsClient,
		"prober",
		"FIRC_CUTOVER_START_FILE="+startFile,
		"FIRC_CUTOVER_STOP_FILE="+stopFile,
		"FIRC_CUTOVER_REPORT_FILE="+reportFile,
	)

	routerCmd := exec.Command("ip", "netns", "exec", nsRouter, exe, "-test.run=^TestEngineSwitchCutoverZeroWANLeakNetnsWorker$", "-test.v")
	routerCmd.Env = append(
		os.Environ(),
		cutoverNetnsEnvWorker+"=router",
		"FIRC_CUTOVER_CLIENT_NS="+nsClient,
		"FIRC_CUTOVER_START_FILE="+startFile,
		"FIRC_CUTOVER_EXE="+exe,
	)
	routerOut, routerErr := routerCmd.CombinedOutput()
	_ = os.WriteFile(stopFile, []byte("stop\n"), 0644)
	_ = proberCmd.Wait()
	if routerErr != nil {
		t.Fatalf("cutover router worker failed: %v\n%s", routerErr, string(routerOut))
	}
	t.Logf("cutover router worker output:\n%s", string(routerOut))

	repBytes, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatalf("read prober report %s: %v", reportFile, err)
	}
	var rep cutoverProbeReport
	if err := json.Unmarshal(repBytes, &rep); err != nil {
		t.Fatalf("decode prober report: %v\n%s", err, string(repBytes))
	}
	t.Logf("continuous 10ms probe summary: %+v", rep)
	if rep.Wan100 > 0 || rep.WanFake > 0 || rep.FirstLeak != "" {
		t.Fatalf("WAN leak detected by continuous prober: %+v", rep)
	}
	if rep.Total100 < 10 || rep.TotalFake < 10 || rep.Tun100 == 0 || rep.TunFake == 0 {
		t.Fatalf("insufficient tunnel probe coverage: %+v", rep)
	}

	wanMangleOut, err := exec.Command("ip", "netns", "exec", nsWAN, "iptables", "-w", "-t", "mangle", "-L", "PREROUTING", "-v", "-n", "-x").CombinedOutput()
	if err != nil {
		t.Fatalf("read WAN iptables packet counters: %v\n%s", err, string(wanMangleOut))
	}
	pkts100 := extractDstPacketCount(string(wanMangleOut), netnsCloudIP1)
	pktsFake := extractDstPacketCount(string(wanMangleOut), cutoverFakeIP)
	if pkts100 != 0 || pktsFake != 0 {
		t.Fatalf("WAN mangle PREROUTING observed leaked packets (198.51.100.10=%d, 198.18.0.10=%d):\n%s", pkts100, pktsFake, string(wanMangleOut))
	}
}

func TestEngineSwitchCutoverZeroWANLeakNetnsWorker(t *testing.T) {
	role := os.Getenv(cutoverNetnsEnvWorker)
	if role == "" {
		t.Skip("worker-only helper invoked by TestEngineSwitchCutoverZeroWANLeakNetns")
	}

	switch role {
	case "wan-sink":
		runCutoverTaggedTCPSink(t, role, "WAN", os.Getenv("FIRC_CUTOVER_STOP_FILE"))
	case "tun-sink":
		runCutoverTaggedTCPSink(t, role, "TUN", os.Getenv("FIRC_CUTOVER_STOP_FILE"))
	case "prober":
		runCutoverContinuousProber(t, os.Getenv("FIRC_CUTOVER_START_FILE"), os.Getenv("FIRC_CUTOVER_STOP_FILE"), os.Getenv("FIRC_CUTOVER_REPORT_FILE"))
	case "probe-once":
		runCutoverProbeOnce(t, os.Getenv("FIRC_CUTOVER_PROBE_ADDR"))
	case "router":
		runCutoverRouterWorker(t)
	default:
		t.Fatalf("unknown %s=%q", cutoverNetnsEnvWorker, role)
	}
}

func runCutoverTaggedTCPSink(t *testing.T, role, tag, stopFile string) {
	t.Helper()
	ln, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", cutoverNetnsPort))
	if err != nil {
		t.Fatalf("%s listen tcp: %v", role, err)
	}
	defer ln.Close()

	go func() {
		for {
			if _, err := os.Stat(stopFile); err == nil {
				_ = ln.Close()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	fmt.Printf("READY %s\n", role)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = fmt.Fprintf(c, "%s\n", tag)
		}(conn)
	}
}

func probeCutoverAddr(addr string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("tcp4", addr, timeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func runCutoverProbeOnce(t *testing.T, addr string) {
	t.Helper()
	tag, err := probeCutoverAddr(addr, 1500*time.Millisecond)
	if err != nil {
		fmt.Printf("ERR:%v\n", err)
		t.Fatalf("probe-once %s failed: %v", addr, err)
	}
	fmt.Printf("TAG:%s\n", tag)
}

func runCutoverContinuousProber(t *testing.T, startFile, stopFile, reportFile string) {
	t.Helper()
	fmt.Println("READY prober")
	for {
		if _, err := os.Stat(startFile); err == nil {
			break
		}
		if _, err := os.Stat(stopFile); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	var mu sync.Mutex
	var rep cutoverProbeReport
	var wg sync.WaitGroup

	probeLoop := func(addr string, isFake bool) {
		defer wg.Done()
		for {
			if _, err := os.Stat(stopFile); err == nil {
				return
			}
			tag, err := probeCutoverAddr(addr, 60*time.Millisecond)
			mu.Lock()
			if isFake {
				rep.TotalFake++
			} else {
				rep.Total100++
			}
			if err == nil {
				if tag == "TUN" {
					if isFake {
						rep.TunFake++
					} else {
						rep.Tun100++
					}
				} else if strings.HasPrefix(tag, "WAN") {
					if isFake {
						rep.WanFake++
					} else {
						rep.Wan100++
					}
					if rep.FirstLeak == "" {
						rep.FirstLeak = fmt.Sprintf("addr=%s got=%q", addr, tag)
					}
				}
			} else if strings.Contains(strings.ToLower(err.Error()), "connection refused") {
				if isFake {
					rep.RefusedFake++
				} else {
					rep.Refused100++
				}
			}
			mu.Unlock()
			time.Sleep(10 * time.Millisecond)
		}
	}

	wg.Add(2)
	go probeLoop(fmt.Sprintf("%s:%d", netnsCloudIP1, cutoverNetnsPort), false)
	go probeLoop(fmt.Sprintf("%s:%d", cutoverFakeIP, cutoverNetnsPort), true)
	wg.Wait()

	data, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(reportFile, data, 0644); err != nil {
		t.Fatalf("write prober report: %v", err)
	}
}

func runCutoverRouterWorker(t *testing.T) {
	t.Helper()
	nsClient := os.Getenv("FIRC_CUTOVER_CLIENT_NS")
	startFile := os.Getenv("FIRC_CUTOVER_START_FILE")
	exe := os.Getenv("FIRC_CUTOVER_EXE")

	SetExtIptablesRunner(defaultExtIptablesRunner)
	defer SetExtIptablesRunner(nil)
	InvalidateOutputRoutingCache()

	runIpt := func(args ...string) error {
		out, err := exec.Command("iptables", append([]string{"-w"}, args...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("iptables %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	mustIpt := func(args ...string) {
		t.Helper()
		if err := runIpt(args...); err != nil {
			t.Fatal(err)
		}
	}

	enableSingboxMangle := func() {
		_ = runIpt("-t", "mangle", "-N", "SINGBOX_TEST")
		_ = runIpt("-t", "mangle", "-F", "SINGBOX_TEST")
		mustIpt("-t", "mangle", "-A", "SINGBOX_TEST", "-i", "br0", "-d", netnsCloudIP1, "-j", "MARK", "--set-xmark", "0x10000/0xff0000")
		mustIpt("-t", "mangle", "-A", "SINGBOX_TEST", "-i", "br0", "-d", "198.18.0.0/15", "-j", "MARK", "--set-xmark", "0x10000/0xff0000")
		_ = runIpt("-t", "mangle", "-D", "PREROUTING", "-j", "SINGBOX_TEST")
		mustIpt("-t", "mangle", "-I", "PREROUTING", "1", "-j", "SINGBOX_TEST")
	}
	disableSingboxMangle := func() {
		for runIpt("-t", "mangle", "-D", "PREROUTING", "-j", "SINGBOX_TEST") == nil {
		}
		_ = runIpt("-t", "mangle", "-F", "SINGBOX_TEST")
		_ = runIpt("-t", "mangle", "-X", "SINGBOX_TEST")
	}
	enableFircMangle := func() {
		_ = runIpt("-t", "mangle", "-N", "FIRC_TEST")
		_ = runIpt("-t", "mangle", "-F", "FIRC_TEST")
		mustIpt("-t", "mangle", "-A", "FIRC_TEST", "-i", "br0", "-d", netnsCloudIP1, "-j", "MARK", "--set-xmark", "0x40010000/0x40ff0000")
		_ = runIpt("-t", "mangle", "-D", "PREROUTING", "-j", "FIRC_TEST")
		mustIpt("-t", "mangle", "-I", "PREROUTING", "1", "-j", "FIRC_TEST")
	}
	disableFircMangle := func() {
		for runIpt("-t", "mangle", "-D", "PREROUTING", "-j", "FIRC_TEST") == nil {
		}
		_ = runIpt("-t", "mangle", "-F", "FIRC_TEST")
		_ = runIpt("-t", "mangle", "-X", "FIRC_TEST")
	}

	probeOne := func(addr, wantTag string) {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", nsClient, exe, "-test.run=^TestEngineSwitchCutoverZeroWANLeakNetnsWorker$")
		cmd.Env = append(os.Environ(), cutoverNetnsEnvWorker+"=probe-once", "FIRC_CUTOVER_PROBE_ADDR="+addr)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probeOne(%s) failed: %v\n%s", addr, err, string(out))
		}
		if !strings.Contains(string(out), "TAG:"+wantTag) {
			t.Fatalf("probeOne(%s) expected TAG:%s, got:\n%s", addr, wantTag, string(out))
		}
	}
	probeExpectRefused := func(addr string) {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", nsClient, exe, "-test.run=^TestEngineSwitchCutoverZeroWANLeakNetnsWorker$")
		cmd.Env = append(os.Environ(), cutoverNetnsEnvWorker+"=probe-once", "FIRC_CUTOVER_PROBE_ADDR="+addr)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(strings.ToLower(string(out)), "connection refused") {
			t.Fatalf("probeExpectRefused(%s) expected connection refused from FIRC_POOLREJECT, got err=%v\n%s", addr, err, string(out))
		}
	}

	var fircRunning atomic.Bool
	var failFircStart atomic.Bool
	var failActivate atomic.Bool

	mockMux := http.NewServeMux()
	mockMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, _ *http.Request) {
		if !fircRunning.Load() {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(NetfilterHealth{Ok: true})
	})
	mockMux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, _ *http.Request) {
		if !fircRunning.Load() {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(PutTunnelsResponse{})
	})
	mockMux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		if !fircRunning.Load() {
			http.Error(w, "stopped", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"groups": []Group{}})
	})
	mockSrv := httptest.NewServer(mockMux)
	defer mockSrv.Close()

	op := NewOperator(Options{ConfigDir: t.TempDir()})
	op.SetForceKernelNetfilter(true)
	op.SetClient(NewHTTPClient(mockSrv.URL, mockSrv.Client()))

	op.SetLifecycleHooks(
		func(_ context.Context) error {
			if failFircStart.Load() {
				return errors.New("injected firc start failure")
			}
			fircRunning.Store(true)
			_ = runIpt("-t", "filter", "-N", "FIRC_POOLREJECT")
			if err := runIpt("-t", "filter", "-F", "FIRC_POOLREJECT"); err != nil {
				return err
			}
			if err := runIpt("-t", "filter", "-A", "FIRC_POOLREJECT", "-d", "198.18.0.0/15", "-p", "tcp", "-j", "REJECT", "--reject-with", "tcp-reset"); err != nil {
				return err
			}
			if err := runIpt("-t", "filter", "-A", "FIRC_POOLREJECT", "-d", "198.18.0.0/15", "-j", "REJECT", "--reject-with", "icmp-host-unreachable"); err != nil {
				return err
			}
			for runIpt("-t", "filter", "-D", "FORWARD", "-j", "FIRC_POOLREJECT") == nil {
			}
			if err := runIpt("-t", "filter", "-I", "FORWARD", "1", "-j", "FIRC_POOLREJECT"); err != nil {
				return err
			}
			return nil
		},
		func(_ context.Context) error {
			disableFircMangle()
			fircRunning.Store(false)
			// Inject 80ms unrouted gap after clearing FIRC routes while FIRC_POOLREJECT still remains until CleanupPoolRejectBarrier.
			time.Sleep(80 * time.Millisecond)
			return nil
		},
	)

	currentEngine := "sing-box"
	hooks := EngineSwitchHooks{
		GetCurrentEngine: func(_ context.Context) (string, error) {
			return currentEngine, nil
		},
		StartTarget: func(_ context.Context, target string) error {
			// Intentionally leave target PREROUTING parked until ActivateTarget (matching sing-box Start + ResumeFromExternalEngine).
			_ = target
			return nil
		},
		StopEngine: func(_ context.Context, eng string) error {
			if eng == "sing-box" {
				disableSingboxMangle()
				time.Sleep(80 * time.Millisecond)
			}
			return nil
		},
		ParkEngineForStandby: func(_ context.Context, eng string) error {
			if eng == "sing-box" {
				disableSingboxMangle()
				time.Sleep(80 * time.Millisecond)
			}
			return nil
		},
		ActivateTarget: func(_ context.Context, target string) error {
			time.Sleep(80 * time.Millisecond)
			if failActivate.CompareAndSwap(true, false) {
				return errors.New("injected activate failure")
			}
			switch target {
			case "firc":
				enableFircMangle()
			case "sing-box":
				enableSingboxMangle()
			}
			return nil
		},
		CommitSettings: func(_ context.Context, target string) error {
			currentEngine = target
			return nil
		},
	}

	// Start with sing-box active, then start continuous client probing.
	enableSingboxMangle()
	if err := os.WriteFile(startFile, []byte("go\n"), 0644); err != nil {
		t.Fatalf("write start probe signal: %v", err)
	}
	probeOne(fmt.Sprintf("%s:%d", netnsCloudIP1, cutoverNetnsPort), "TUN")
	probeOne(fmt.Sprintf("%s:%d", cutoverFakeIP, cutoverNetnsPort), "TUN")

	ctx := context.Background()

	// 1. sing-box -> firc (with 80ms gaps around park & activate).
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{SingboxStandby: true}, hooks); err != nil {
		t.Fatalf("SwitchEngineWithOptions(sing-box->firc) failed: %v", err)
	}
	probeOne(fmt.Sprintf("%s:%d", netnsCloudIP1, cutoverNetnsPort), "TUN")
	probeExpectRefused(fmt.Sprintf("%s:%d", cutoverFakeIP, cutoverNetnsPort))

	// 2. firc -> sing-box (with 80ms gap after FIRC_POOLREJECT removal & before SINGBOX_TEST activation).
	if err := op.SwitchEngineWithOptions(ctx, "sing-box", EngineSwitchOptions{}, hooks); err != nil {
		t.Fatalf("SwitchEngineWithOptions(firc->sing-box) failed: %v", err)
	}
	probeOne(fmt.Sprintf("%s:%d", netnsCloudIP1, cutoverNetnsPort), "TUN")
	probeOne(fmt.Sprintf("%s:%d", cutoverFakeIP, cutoverNetnsPort), "TUN")

	// 3. Failed sing-box -> firc rollback restores sing-box cleanly with 0 WAN leak.
	failActivate.Store(true)
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{}, hooks); err == nil {
		t.Fatal("expected SwitchEngineWithOptions(sing-box->firc) failure at ActivateTarget")
	}
	if op.IsRecoveryRequired() {
		t.Fatal("expected clean rollback without RecoveryRequired")
	}
	probeOne(fmt.Sprintf("%s:%d", netnsCloudIP1, cutoverNetnsPort), "TUN")
	probeOne(fmt.Sprintf("%s:%d", cutoverFakeIP, cutoverNetnsPort), "TUN")

	// 4. Double-fault firc -> sing-box -> 120ms probing during RecoveryRequired -> ReconcileRecovery("sing-box").
	failActivate.Store(false)
	if err := op.SwitchEngineWithOptions(ctx, "firc", EngineSwitchOptions{}, hooks); err != nil {
		t.Fatalf("setup switch to firc failed: %v", err)
	}
	failActivate.Store(true)
	failFircStart.Store(true)
	if err := op.SwitchEngineWithOptions(ctx, "sing-box", EngineSwitchOptions{}, hooks); err == nil || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("expected ErrRecoveryRequired on double fault, got: %v", err)
	}
	fwdOut, _ := exec.Command("iptables", "-w", "-t", "mangle", "-S", "FORWARD").CombinedOutput()
	if !strings.Contains(string(fwdOut), "-j "+cutoverGuardChain) {
		t.Fatalf("expected %s retained during RecoveryRequired, FORWARD:\n%s", cutoverGuardChain, string(fwdOut))
	}
	filterFwdOut, _ := exec.Command("iptables", "-w", "-t", "filter", "-S", "FORWARD").CombinedOutput()
	if !strings.Contains(string(filterFwdOut), "-j "+cutoverGuardFwdChain) {
		t.Fatalf("expected %s retained during RecoveryRequired, filter FORWARD:\n%s", cutoverGuardFwdChain, string(filterFwdOut))
	}
	preOut, _ := exec.Command("iptables", "-w", "-t", "mangle", "-S", "PREROUTING").CombinedOutput()
	if !strings.Contains(string(preOut), "-j "+cutoverGuardPreChain) {
		t.Fatalf("expected %s retained during RecoveryRequired, mangle PREROUTING:\n%s", cutoverGuardPreChain, string(preOut))
	}
	time.Sleep(120 * time.Millisecond)

	failActivate.Store(false)
	failFircStart.Store(false)
	if _, err := op.ReconcileRecovery(ctx, "sing-box", hooks); err != nil {
		t.Fatalf("ReconcileRecovery(sing-box) failed: %v", err)
	}
	fwdAfterOut, _ := exec.Command("iptables", "-w", "-t", "mangle", "-S", "FORWARD").CombinedOutput()
	if strings.Contains(string(fwdAfterOut), "-j "+cutoverGuardChain) {
		t.Fatalf("expected %s released after ReconcileRecovery, FORWARD:\n%s", cutoverGuardChain, string(fwdAfterOut))
	}
	filterFwdAfterOut, _ := exec.Command("iptables", "-w", "-t", "filter", "-S", "FORWARD").CombinedOutput()
	if strings.Contains(string(filterFwdAfterOut), "-j "+cutoverGuardFwdChain) {
		t.Fatalf("expected %s released after ReconcileRecovery, filter FORWARD:\n%s", cutoverGuardFwdChain, string(filterFwdAfterOut))
	}
	preAfterOut, _ := exec.Command("iptables", "-w", "-t", "mangle", "-S", "PREROUTING").CombinedOutput()
	if strings.Contains(string(preAfterOut), "-j "+cutoverGuardPreChain) {
		t.Fatalf("expected %s released after ReconcileRecovery, mangle PREROUTING:\n%s", cutoverGuardPreChain, string(preAfterOut))
	}
	probeOne(fmt.Sprintf("%s:%d", netnsCloudIP1, cutoverNetnsPort), "TUN")
	probeOne(fmt.Sprintf("%s:%d", cutoverFakeIP, cutoverNetnsPort), "TUN")
}

func extractDstPacketCount(iptablesLOutput, dstIP string) uint64 {
	for _, line := range strings.Split(iptablesLOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		matched := false
		for _, f := range fields {
			if f == dstIP || f == dstIP+"/32" {
				matched = true
				break
			}
		}
		if matched {
			if n, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
				return n
			}
		}
	}
	return 0
}
