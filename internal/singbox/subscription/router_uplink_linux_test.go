//go:build linux

package subscription

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/sbconfig"
)

const routerProxyUID = 60010

type routerProxyProviders struct{}

func (routerProxyProviders) Providers() []uplink.Provider {
	return []uplink.Provider{{ID: "ethernet", Device: "eth3", Up: true}, {ID: "wisp", Device: "apcli0", Up: true}}
}

type routerPortAllocator struct {
	*OperatorAdapter
	port uint16
}

func (a routerPortAllocator) AllocListenPort() (uint16, error) { return a.port, nil }

// Explicit hardware gate. All writable paths, listeners and policies belong to
// the acceptance directory. A private UID scopes failure injection to this
// test's actual sing-box sockets; production daemons are never stopped.
func TestRouterProxyUplinkAcceptance(t *testing.T) {
	if os.Getenv("UPLINK_ROUTER_PROXY") != "1" {
		t.Skip("explicit router acceptance only")
	}
	dir := os.Getenv("UPLINK_ROUTER_PROXY_DIR")
	if !strings.HasPrefix(dir, "/opt/var/uplink-proxy-acceptance-") || filepath.Clean(dir) != dir {
		t.Fatal("invalid private directory")
	}
	if os.Getenv("UPLINK_ROUTER_PROXY_CHILD") != "1" {
		if os.Geteuid() != 0 {
			t.Fatal("root supervisor required")
		}
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			if _, err := strconv.Atoi(e.Name()); err != nil {
				continue
			}
			b, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
			if strings.Contains(string(b), fmt.Sprintf("Uid:\t%d\t", routerProxyUID)) {
				t.Fatal("acceptance UID already in use")
			}
		}
		if err := filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Chown(p, routerProxyUID, routerProxyUID)
		}); err != nil {
			t.Fatal(err)
		}
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(self, "-test.run", "^TestRouterProxyUplinkAcceptance$", "-test.v", "-test.timeout", "265s")
		cmd.Env = append(os.Environ(), "UPLINK_ROUTER_PROXY_CHILD=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: routerProxyUID, Gid: routerProxyUID}, AmbientCaps: []uintptr{12, 13}}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			t.Fatal("isolated hardware child:", err)
		}
		return
	}
	if os.Geteuid() != routerProxyUID {
		t.Fatal("UID isolation missing")
	}
	binary := os.Getenv("UPLINK_ROUTER_PROXY_BINARY")
	if binary == "" {
		binary = "/opt/etc/awg-manager/singbox/sing-box"
	}
	if binary != "/opt/etc/awg-manager/singbox/sing-box" && binary != "/opt/etc/awg-manager/singbox/awgm-box" {
		t.Fatal("unsupported installed test binary path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Second)
	defer cancel()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "outbound.json"))
	check(err)
	var ob map[string]any
	check(json.Unmarshal(raw, &ob))
	check(sbconfig.ValidateProxyOptions(ob))
	port := func() int {
		l, e := net.Listen("tcp4", "127.0.0.1:0")
		check(e)
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		return p
	}
	clashPort, socksPort, subPort := port(), port(), port()
	work := filepath.Join(dir, "work")
	check(os.MkdirAll(filepath.Join(work, "config.d"), 0700))
	base := map[string]any{"log": map[string]any{"level": "error"}, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}, "experimental": map[string]any{"clash_api": map[string]any{"external_controller": fmt.Sprintf("127.0.0.1:%d", clashPort)}, "cache_file": map[string]any{"enabled": false, "path": filepath.Join(work, "cache.db")}}}
	base["dns"] = map[string]any{"servers": []any{map[string]any{"type": "udp", "tag": "dns-bootstrap", "server": "1.1.1.1", "bind_interface": "eth3"}}, "strategy": "ipv4_only"}
	b, _ := json.Marshal(base)
	check(os.WriteFile(filepath.Join(work, "config.d/00-base.json"), b, 0600))
	op := singbox.NewOperator(singbox.OperatorDeps{Dir: work, Binary: binary, ClashPort: func() int { return clashPort }, IsNDMSProxyEnabled: func() bool { return false }})
	t.Cleanup(op.Process().Close)
	// Constructor normalizes the base. Restore the deliberately isolated base
	// before Bootstrap: no shared cache, API, DNS listener, tun or interception.
	check(os.WriteFile(filepath.Join(work, "config.d/00-base.json"), b, 0600))
	orch := orchestrator.NewWithAppliedPath(op.ConfigDir(), op.Process(), filepath.Join(work, "applied.json"))
	t.Cleanup(orch.Close)
	for _, m := range orchestrator.KnownSlots() {
		if m.Slot == orchestrator.SlotBase || m.Slot == orchestrator.SlotTunnels || m.Slot == orchestrator.SlotSubscriptions {
			if m.Slot == orchestrator.SlotTunnels {
				m.HasContent = op.HasUserTunnels
			}
			if m.Slot == orchestrator.SlotSubscriptions {
				m.HasContent = func() bool {
					_, err := os.Stat(filepath.Join(op.ConfigDir(), SlotSubscriptionsMeta.Filename))
					return err == nil
				}
			}
			check(orch.Register(m))
		}
	}
	check(orch.Bootstrap())
	op.SetOrch(orch)
	t.Cleanup(func() { op.Process().Stop() })
	binding, err := uplink.InspectBinding("eth3")
	check(err)
	binding.DNSServers = []string{"1.1.1.1"}
	ob["tag"] = "acceptance-proxy"
	doc, err := sbconfig.Apply(map[string]any{"outbounds": []any{ob}, "inbounds": []any{map[string]any{"type": "socks", "tag": "acceptance-proxy-in", "listen": "127.0.0.1", "listen_port": socksPort}}, "route": map[string]any{"rules": []any{map[string]any{"inbound": []any{"acceptance-proxy-in"}, "outbound": "acceptance-proxy"}}}}, []sbconfig.Spec{{Tag: "acceptance-proxy", Binding: binding}})
	check(err)
	data, _ := json.Marshal(doc)
	cfg := singbox.NewConfig()
	check(cfg.UnmarshalJSON(data))
	check(op.ApplyConfig(ctx, cfg))
	check(orch.ReloadNow())
	check(op.Start())
	dns := func(context.Context, string) ([]string, error) { return []string{"1.1.1.1"}, nil }
	standalone := &singbox.UplinkAdapter{Operator: op, Active: func() bool { return true }, DNSForDevice: dns}
	policies, err := uplink.OpenStore(filepath.Join(work, "policies.json"))
	check(err)
	mut := NewOperatorAdapter(orch, nil, op.Clash())
	subscriptions, err := NewStore(filepath.Join(work, "subscriptions.json"))
	check(err)
	service := NewService(subscriptions, routerPortAllocator{mut, uint16(subPort)})
	service.SetNDMSProxyEnabled(func() bool { return false })
	mut.SetUplinkBindingsFn(service.UplinkBindings)
	group := &UplinkAdapter{Service: service, Operator: op, Active: func() bool { return true }, DNSForDevice: dns}
	controller := uplink.New(policies, routerProxyProviders{}, map[string]uplink.Adapter{"sing-box": standalone})
	op.SetManagedUplinkConfig(func(ctx context.Context, d map[string]any) (map[string]any, error) {
		return sbconfig.MaterializeOwned(ctx, d, policies.MaterializationPolicies(), controller.Providers(), dns)
	})
	// Resolve through the provider-bound resolver only, to scope DROP to the
	// endpoint addresses without exposing them or affecting other processes.
	resolver := net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return binding.DialContext(ctx, "tcp", "1.1.1.1:53")
	}}
	host, _ := ob["server"].(string)
	ips, err := resolver.LookupIP(ctx, "ip4", host)
	check(err)
	if len(ips) == 0 {
		t.Fatal("no IPv4 endpoint")
	}
	endpointPort := fmt.Sprint(ob["server_port"])
	rulesFor := func(endpoint map[string]any) [][]string {
		t.Helper()
		server, _ := endpoint["server"].(string)
		addresses, err := resolver.LookupIP(ctx, "ip4", server)
		check(err)
		if len(addresses) == 0 {
			t.Fatal("no IPv4 address for selected subscription member")
		}
		result := [][]string{}
		for _, ip := range addresses {
			for _, protocol := range []string{"tcp", "udp"} {
				result = append(result, []string{"OUTPUT", "-o", "eth3", "-p", protocol, "-d", ip.String(), "--dport", fmt.Sprint(endpoint["server_port"]), "-m", "owner", "--uid-owner", strconv.Itoa(routerProxyUID), "-m", "comment", "--comment", "awgm-uplink-proxy-acceptance", "-j", "DROP"})
			}
		}
		return result
	}
	command := func(name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).CombinedOutput()
	}
	rules := [][]string{}
	for _, ip := range ips {
		for _, protocol := range []string{"tcp", "udp"} {
			rules = append(rules, []string{"OUTPUT", "-o", "eth3", "-p", protocol, "-d", ip.String(), "--dport", endpointPort, "-m", "owner", "--uid-owner", strconv.Itoa(routerProxyUID), "-m", "comment", "--comment", "awgm-uplink-proxy-acceptance", "-j", "DROP"})
		}
	}
	unblock := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, r := range rules {
			exec.CommandContext(cleanupCtx, "iptables", append([]string{"-D"}, r...)...).CombinedOutput()
		}
	}
	t.Cleanup(unblock)
	runScenario := func(target uplink.Target) {
		t.Helper()
		expectedMember := ""
		var adapter uplink.Adapter = standalone
		if target.Kind == "subscription" {
			adapter = group
			sub, err := service.Get(target.Ref)
			check(err)
			expectedMember = sub.ActiveMember
			found := false
			for _, member := range mut.SubscriptionOutbounds() {
				if member["tag"] == sub.ActiveMember {
					rules = rulesFor(member)
					found = true
					break
				}
			}
			if !found {
				t.Fatal("selected subscription endpoint not found")
			}
		}
		adapters := map[string]uplink.Adapter{"sing-box": adapter}
		controller = uplink.New(policies, routerProxyProviders{}, adapters)
		p, e := controller.Put(ctx, uplink.Policy{ID: "hardware", Target: target, Providers: []string{"ethernet", "wisp"}, Enabled: true, IntervalSeconds: 8, TimeoutSeconds: 8, FailureThreshold: 1, HoldDownSeconds: 10})
		check(e)
		assertHealthy := func(want string) {
			t.Helper()
			s := controller.List()[0].State
			if s.Phase != "healthy" || s.Provider != want {
				t.Fatalf("expected %s healthy, got %+v", want, s)
			}
			if expectedMember != "" {
				fresh, err := service.Get(target.Ref)
				check(err)
				if fresh.ActiveMember != expectedMember {
					t.Fatal("WAN transition changed the stored subscription member")
				}
				for deadline := time.Now().Add(5 * time.Second); ; {
					live, err := op.Clash().SelectorActive(fresh.SelectorTag)
					if err == nil && live != "" {
						if live != expectedMember {
							t.Fatal("WAN transition changed the live subscription member")
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("private selector identity unavailable after probe")
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
			t.Log(target.Engine, "DNS/HTTPS healthy:", want)
		}
		controller.Tick(ctx)
		assertHealthy("ethernet")
		for _, r := range rules {
			_, e = command("iptables", append([]string{"-I"}, r...)...)
			check(e)
		}
		out, e := command("curl", "-4", "--interface", "eth3", "--noproxy", "*", "--connect-timeout", "4", "--max-time", "10", "-sS", "-o", "/dev/null", "-w", "%{http_code}", "https://cp.cloudflare.com/generate_204")
		check(e)
		if string(out) != "204" {
			t.Fatal("direct Ethernet witness failed")
		}
		time.Sleep(11 * time.Second)
		controller.Tick(ctx)
		assertHealthy("wisp")
		counters, e := command("iptables", "-L", "OUTPUT", "-n", "-v", "-x")
		check(e)
		hits := uint64(0)
		for _, line := range strings.Split(string(counters), "\n") {
			if strings.Contains(line, "awgm-uplink-proxy-acceptance") {
				f := strings.Fields(line)
				if len(f) > 0 {
					n, _ := strconv.ParseUint(f[0], 10, 64)
					hits += n
				}
			}
		}
		if hits == 0 {
			t.Fatal("no selective failure packets")
		}
		t.Log("UID-scoped endpoint DROP packets:", hits)
		if target.Kind == "subscription" {
			previous, err := service.Get(target.Ref)
			check(err)
			_, e = service.Refresh(ctx, target.Ref)
			check(e)
			fresh, err := service.Get(target.Ref)
			check(err)
			if fresh.URL != "" {
				if !fresh.LastFetched.After(previous.LastFetched) || fresh.LastError != "" || len(fresh.MemberTags) < 2 {
					t.Fatal("URL refresh did not confirm successful multi-member fetch")
				}
				t.Log("URL subscription fetched on WISP; all members retain strict binding:", len(fresh.MemberTags))
			}
			check(op.RestartForUplink(ctx))
			check(group.Probe(ctx, target, "apcli0"))
			t.Log("subscription refresh retained strict WISP binding and DNS")
		}
		reopened, e := uplink.OpenStore(filepath.Join(work, "policies.json"))
		check(e)
		policies = reopened
		controller = uplink.New(reopened, routerProxyProviders{}, adapters)
		controller.Tick(ctx)
		assertHealthy("wisp")
		unblock()
		check(controller.Switch(ctx, p.ID, "ethernet", p.Revision))
		assertHealthy("ethernet")
		check(controller.Delete(p.ID, p.Revision))
	}
	runScenario(uplink.Target{Engine: "sing-box", Ref: "acceptance-proxy"})
	// Import a real one-member inline subscription through the actual service,
	// parser, operator adapter and subscription slot. No provider URL is fetched.
	delete(ob, "bind_interface")
	delete(ob, "inet4_bind_address")
	delete(ob, "inet6_bind_address")
	delete(ob, "domain_resolver")
	ob["tag"] = "acceptance-member"
	inline, _ := json.Marshal(map[string]any{"outbounds": []any{ob}})
	if ob["type"] == "trusttunnel" {
		// TrustTunnel's existing subscription import accepts endpoint TOML;
		// raw sing-box JSON import currently accepts a narrower protocol set.
		tls, _ := ob["tls"].(map[string]any)
		hostname, _ := tls["server_name"].(string)
		if hostname == "" {
			hostname = host
		}
		protocol := "http2"
		if ob["quic"] == true {
			protocol = "http3"
		}
		var encoded bytes.Buffer
		check(toml.NewEncoder(&encoded).Encode(map[string]any{"hostname": hostname, "addresses": []string{net.JoinHostPort(host, endpointPort)}, "username": ob["username"], "password": ob["password"], "skip_verification": tls["insecure"] == true, "upstream_protocol": protocol}))
		inline = encoded.Bytes()
	}
	input := CreateInput{Label: "isolated WAN acceptance", Inline: string(inline), Enabled: true, Mode: ModeSelector}
	var urlInput struct {
		URL             string   `json:"url"`
		Headers         []Header `json:"headers"`
		PreferredSuffix string   `json:"preferredSuffix"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "subscription-input.json")); err == nil {
		check(json.Unmarshal(data, &urlInput))
		if urlInput.URL == "" {
			t.Fatal("URL source required")
		}
		input.Inline = ""
		input.URL = urlInput.URL
		input.Headers = urlInput.Headers
		input.BindInterface = "eth3"
	} else if !os.IsNotExist(err) {
		check(err)
	}
	sub, err := service.Create(ctx, input)
	check(err)
	if input.URL != "" {
		check(op.RestartForUplink(ctx)) // publish the new selector before its live selection API
		for deadline := time.Now().Add(15 * time.Second); ; {
			if op.Clash().HasOutbound(sub.SelectorTag) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("private Clash API did not publish the subscription selector")
			}
			time.Sleep(100 * time.Millisecond)
		}
		if len(sub.MemberTags) < 2 {
			t.Fatal("multi-member URL source required")
		}
		if urlInput.PreferredSuffix != "" {
			found := false
			for _, tag := range sub.MemberTags {
				if strings.HasSuffix(tag, "-"+urlInput.PreferredSuffix) {
					check(service.SetActiveMember(ctx, sub.ID, tag))
					found = true
					break
				}
			}
			if !found {
				t.Fatal("previously working member identity not present in refreshed URL source")
			}
		}
		t.Log("real URL subscription created with members:", len(sub.MemberTags))
	}
	if input.URL == "" {
		check(op.RestartForUplink(ctx))
	}
	runScenario(uplink.Target{Engine: "sing-box", Kind: "subscription", Ref: sub.ID})
	t.Log("PASS: actual router standalone and subscription fallback, strict refresh, controller restart, manual return and policy release")
}
