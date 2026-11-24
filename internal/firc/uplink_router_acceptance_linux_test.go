//go:build linux

package firc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"gopkg.in/yaml.v3"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type hardwareFIRCProviders struct{}

func (hardwareFIRCProviders) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "ethernet", Device: "eth3", Up: true}, {ID: "wisp", Device: "apcli0", Up: true}}
}

// This test runs only inside a supervised maintenance window with automatic
// restoration of the original daemon. It never installs/replaces binaries.
func TestRouterFIRCUplinkAcceptance(t *testing.T) {
	mode := os.Getenv("FIRC_ROUTER_ACCEPTANCE")
	if mode != "prepare" && mode != "run" {
		t.Skip("explicit supervised router acceptance")
	}
	dir := os.Getenv("FIRC_ROUTER_DIRECTORY")
	if !strings.HasPrefix(dir, "/opt/var/uplink-firc-acceptance-") || filepath.Clean(dir) != dir {
		t.Fatal("private directory required")
	}
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	read := func(name string) []byte { b, e := os.ReadFile(filepath.Join(dir, name)); must(e); return b }
	write := func(name string, b []byte) { must(os.WriteFile(filepath.Join(dir, name), b, 0600)) }
	if mode == "prepare" {
		if _, e := net.InterfaceByName("tunvless19"); e == nil {
			t.Fatal("test device already exists")
		}
		must(os.MkdirAll(filepath.Join(dir, "config"), 0700))
		must(os.MkdirAll(filepath.Join(dir, "cache"), 0700))
		must(os.MkdirAll(filepath.Join(dir, "update"), 0700))
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		must(e)
		port := uint16(l.Addr().(*net.TCPAddr).Port)
		l.Close()
		var original []Tunnel
		must(json.Unmarshal(read("before/tunnels-api.json"), &original))
		var source Tunnel
		for _, v := range original {
			if v.ID == "tunnel1" {
				source = v
			}
		}
		if len(source.Sources) != 1 || source.Sources[0].URL == "" {
			t.Fatal("expected one existing URL subscription source")
		}
		var state []TunnelState
		must(json.Unmarshal(read("before/tunnels-state.json"), &state))
		active := ""
		for _, s := range state {
			if s.ID == source.ID && len(s.Active) > 0 {
				active = s.Active[0]
			}
		}
		if active == "" {
			t.Fatal("baseline has no active subscription member")
		}
		tun := Tunnel{ID: "uplink-hardware", Device: "tunvless19", Enable: true, Uplink: TunnelUplink{Kind: "iface", Ref: "eth3"}, EndpointDNS: []string{"1.1.1.1"}, Sources: []TunnelSource{{Kind: "link", Link: strings.TrimSpace(string(read("standalone-links.txt")))}}, Inbounds: []TunnelInbound{{Listen: "lo", Port: port, Node: TunnelNodePool}}, Advanced: TunnelAdvanced{KernelTCP: false, KernelTCPSet: true, Timeout: 8}}
		b, e := yaml.Marshal(map[string]any{"tunnels": []Tunnel{tun}})
		must(e)
		write("config/tunnels.yaml", b)
		source.ID = tun.ID
		source.Device = tun.Device
		source.Uplink = tun.Uplink
		source.EndpointDNS = tun.EndpointDNS
		source.Inbounds = tun.Inbounds
		source.Advanced = tun.Advanced
		source.Filter = "^" + regexp.QuoteMeta(active) + "$"
		source.Order = nil
		source.Exclude = nil
		b, e = json.Marshal(source)
		must(e)
		write("subscription-target.json", b)
		write("config/firc.conf", []byte("configVersion: 0.99.0\napp:\n  httpWeb:\n    enabled: false\n  dnsProxy:\n    host: {address: 127.0.0.1, port: 43553}\n    disableRemap53: true\n  netfilter:\n    disableIPv4: true\n    disableIPv6: true\n  update:\n    check: false\n  link: [lo]\ngroups: []\n"))
		t.Log("private standalone and URL-subscription configs prepared; no daemon or network mutation")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	client := NewClient("/tmp/firc/firc.sock")
	h, e := client.Ping(ctx)
	must(e)
	if h.UplinkDNSVersion < 1 {
		t.Fatal("candidate WAN DNS capability absent")
	}
	tunnels, e := client.GetTunnels(ctx)
	must(e)
	if len(tunnels) != 1 || tunnels[0].ID != "uplink-hardware" {
		t.Fatal("candidate does not own exactly the private test target")
	}
	op := NewOperator(Options{ConfigDir: filepath.Join(dir, "config"), SocketPath: "/tmp/firc/firc.sock", FircdBinary: filepath.Join(dir, "runtime/fircd"), TunvlessBin: filepath.Join(dir, "runtime/tunvless")})
	op.SetClient(client)
	adapter := &UplinkAdapter{Operator: op, Active: func() bool { return true }, DNSForDevice: func(context.Context, string) ([]string, error) { return []string{"1.1.1.1"}, nil }}
	store, e := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	must(e)
	c := uplink.New(store, hardwareFIRCProviders{}, map[string]uplink.Adapter{"firc": adapter})
	op.SetManagedUplinkConfig(func(ctx context.Context, ts []Tunnel) ([]Tunnel, error) {
		return MaterializeOwnedUplinks(ctx, ts, store.MaterializationPolicies(), c.Providers(), adapter.DNSForDevice, client)
	})
	command := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "/opt/sbin/iptables", args...).CombinedOutput()
	}
	run := func(label string) {
		t.Helper()
		p, e := c.Put(ctx, uplink.Policy{ID: "hardware", Target: uplink.Target{Engine: "firc", Ref: "uplink-hardware"}, Providers: []string{"ethernet", "wisp"}, Enabled: true, IntervalSeconds: 8, TimeoutSeconds: 8, FailureThreshold: 1, HoldDownSeconds: 10})
		must(e)
		healthy := func(want string) {
			t.Helper()
			s := c.List()[0].State
			if s.Phase != "healthy" || s.Provider != want {
				t.Fatalf("%s expected %s healthy: %+v", label, want, s)
			}
			t.Log(label, "DNS/HTTPS healthy", want)
		}
		c.Tick(ctx)
		healthy("ethernet")
		// Discover the actual candidate child's outer mark, rather than guessing
		// the daemon's allocation or touching a physical WAN.
		mark := ""
		entries, e := os.ReadDir("/proc")
		must(e)
		for _, entry := range entries {
			if _, e := strconv.Atoi(entry.Name()); e != nil {
				continue
			}
			exe, _ := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
			if exe != filepath.Join(dir, "runtime/tunvless") {
				continue
			}
			raw, _ := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
			args := strings.Split(string(raw), "\x00")
			for i := 1; i+1 < len(args); i++ {
				if args[i] == "-m" {
					mark = args[i+1]
				}
			}
		}
		n, e := strconv.ParseUint(mark, 0, 32)
		must(e)
		if n == 0 {
			t.Fatal("candidate has no exclusive outer mark")
		}
		mark = fmt.Sprintf("0x%x", n)
		rule := []string{"OUTPUT", "-o", "eth3", "-p", "tcp", "-m", "mark", "--mark", mark, "-m", "comment", "--comment", "awgm-uplink-firc-acceptance", "-j", "DROP"}
		write("block-mark", []byte(mark+"\n"))
		unblock := func() { command(append([]string{"-D"}, rule...)...) }
		defer unblock()
		_, e = command(append([]string{"-I"}, rule...)...)
		must(e)
		witness, e := exec.CommandContext(ctx, "/opt/bin/curl", "-4", "--interface", "eth3", "--noproxy", "*", "--connect-timeout", "4", "--max-time", "10", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "https://cp.cloudflare.com/generate_204").CombinedOutput()
		must(e)
		if string(witness) != "204" {
			t.Fatal("direct Ethernet witness failed")
		}
		time.Sleep(11 * time.Second)
		c.Tick(ctx)
		healthy("wisp")
		counters, e := command("-L", "OUTPUT", "-n", "-v", "-x")
		must(e)
		hits := uint64(0)
		for _, line := range strings.Split(string(counters), "\n") {
			if strings.Contains(line, "awgm-uplink-firc-acceptance") {
				f := strings.Fields(line)
				if len(f) > 0 {
					v, _ := strconv.ParseUint(f[0], 10, 64)
					hits += v
				}
			}
		}
		if hits == 0 {
			t.Fatal("no selective failure packets")
		}
		t.Log(label, "target-only DROP packets", hits)
		if label == "URL-subscription" {
			readSubscription := func() SubState {
				states, err := client.GetTunnelsState(ctx)
				must(err)
				for _, state := range states {
					if state.ID == p.Target.Ref && len(state.Subscriptions) == 1 {
						return state.Subscriptions[0]
					}
				}
				t.Fatal("one subscription source state required")
				return SubState{}
			}
			previous := readSubscription()
			time.Sleep(1100 * time.Millisecond) // source telemetry timestamps have second precision
			queued, e := client.RefreshTunnel(ctx, "uplink-hardware")
			must(e)
			if !queued {
				t.Fatal("URL refresh not queued")
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				fresh := readSubscription()
				if fresh.LastTry > previous.LastTry && fresh.LastOk >= fresh.LastTry && !fresh.Fetching && fresh.Error == "" && fresh.Nodes > 0 {
					data, err := json.Marshal(fresh)
					must(err)
					write("subscription-refresh-state.json", data)
					t.Logf("URL source fetch completed on WISP: nodes=%d lastTry=%d lastOk=%d", fresh.Nodes, fresh.LastTry, fresh.LastOk)
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("URL refresh did not confirm a new successful completed fetch")
				}
				time.Sleep(200 * time.Millisecond)
			}
			must(adapter.Probe(ctx, p.Target, "apcli0"))
			t.Log("fresh URL refresh retained WISP DNS/HTTPS")
		}
		// Restart the actual private daemon, not only the Go controller, while
		// WISP is selected. The supervisor reads the updated PID for rollback.
		oldPID, e := strconv.Atoi(strings.TrimSpace(string(read("candidate.pid"))))
		must(e)
		ownedExe, e := os.Readlink(fmt.Sprintf("/proc/%d/exe", oldPID))
		must(e)
		if ownedExe != filepath.Join(dir, "runtime/fircd") {
			t.Fatal("candidate daemon ownership changed")
		}
		oldProcess, e := os.FindProcess(oldPID)
		must(e)
		must(oldProcess.Signal(syscall.SIGTERM))
		for deadline := time.Now().Add(10 * time.Second); ; {
			current, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", oldPID))
			if current != ownedExe {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("candidate did not stop")
			}
			time.Sleep(100 * time.Millisecond)
		}
		logFile, e := os.Create(filepath.Join(dir, "restart-"+label+".log"))
		must(e)
		cmd := exec.Command(filepath.Join(dir, "runtime/fircd"), "--config", filepath.Join(dir, "config/firc.conf"), "--output-floor", "error")
		cmd.Env = append(os.Environ(), "FIRC_TUNVLESS_BIN="+filepath.Join(dir, "runtime/tunvless"), "FIRC_TUNNELS_PATH="+filepath.Join(dir, "config/tunnels.yaml"), "FIRC_TUNNELS_CACHE_DIR="+filepath.Join(dir, "cache"), "FIRC_UPDATE_DIR="+filepath.Join(dir, "update"), "FIRC_HWID_PATH="+filepath.Join(dir, "hwid"))
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		must(cmd.Start())
		logFile.Close()
		write("candidate.pid", []byte(strconv.Itoa(cmd.Process.Pid)+"\n"))
		t.Cleanup(func() {
			cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
			}
		})
		for deadline := time.Now().Add(12 * time.Second); ; {
			_, err := client.Ping(ctx)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("restarted candidate unavailable")
			}
			time.Sleep(100 * time.Millisecond)
		}
		store, e = uplink.OpenStore(filepath.Join(dir, "policies.json"))
		must(e)
		c = uplink.New(store, hardwareFIRCProviders{}, map[string]uplink.Adapter{"firc": adapter})
		c.Tick(ctx)
		healthy("wisp")
		t.Log(label, "actual fircd restart and new controller retained WISP")
		unblock()
		must(c.Switch(ctx, p.ID, "ethernet", p.Revision))
		healthy("ethernet")
		must(c.Delete(ctx, p.ID, p.Revision))
	}
	run("standalone")
	var sub Tunnel
	must(json.Unmarshal(read("subscription-target.json"), &sub))
	_, e = op.SyncTunnels(ctx, []Tunnel{sub}, false)
	must(e)
	run("URL-subscription")
	t.Log("FIRC actual ARM64 runtime: standalone and URL-source WAN fallback, controller restart, manual return and release passed")
}
