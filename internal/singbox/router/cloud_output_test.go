package router

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestApplyCloudOutputRequiresListenerBeforeNetfilter(t *testing.T) {
	plan, err := newCloudOutputPlan(51390, "cloud-fallback", []netip.AddrPort{netip.MustParseAddrPort("87.228.71.67:8090")}, 666)
	if err != nil {
		t.Fatal(err)
	}
	// The zero value has no runner. Reaching netfilter would panic.
	var ipt IPTables
	err = ipt.applyCloudOutput(
		context.Background(),
		&plan,
		func(_ context.Context, port int) bool {
			if port != plan.inbound.ListenPort {
				t.Fatalf("checked wrong TCP port: %d", port)
			}
			return false
		},
		func(context.Context, int) bool { return true },
	)
	if err == nil || !strings.Contains(err.Error(), "TCP listener is unavailable") {
		t.Fatalf("unexpected TCP listener error: %v", err)
	}

	err = ipt.applyCloudOutput(
		context.Background(),
		&plan,
		func(context.Context, int) bool { return true },
		func(_ context.Context, port int) bool {
			if port != plan.udpInbound.ListenPort {
				t.Fatalf("checked wrong UDP port: %d", port)
			}
			return false
		},
	)
	if err == nil || !strings.Contains(err.Error(), "UDP listener is unavailable") {
		t.Fatalf("unexpected UDP listener error: %v", err)
	}
}

func TestCloudOutputRejectsNonPublicRelay(t *testing.T) {
	for _, address := range []string{"0.1.2.3:8090", "10.0.0.1:8090", "100.64.0.1:8090", "127.0.0.1:8090", "198.18.0.10:8090", "198.51.100.11:8090", "240.0.0.1:8090"} {
		if _, err := newCloudOutputPlan(51390, "cloud-fallback", []netip.AddrPort{netip.MustParseAddrPort(address)}, 666); err == nil {
			t.Errorf("accepted a nonpublic relay endpoint %s", address)
		}
	}
}

func TestCloudOutputPlanColdStartLocalDirectAndStaticPorts(t *testing.T) {
	plan, err := newCloudOutputPlanWithOpts(cloudOutputPlanOpts{
		tcpPort:   cloudOutputPort,
		udpPort:   cloudOutputUDPPort,
		outbound:  "cloud-fallback",
		endpoints: nil, // cold start: zero active RCI tunnels
		wanIPs:    []string{"198.51.100.2/32"},
		mark:      cloudOutputMark,
		udpMark:   cloudOutputUDPMark,
		useIpset:  true,
		enableUDP: true,
	})
	if err != nil {
		t.Fatalf("expected cold-start plan without RCI endpoints to succeed, got %v", err)
	}
	if plan.inbound.Type != "redirect" || plan.inbound.ListenPort != cloudOutputPort ||
		plan.udpInbound.Type != "tproxy" || plan.udpInbound.Network != "udp" || plan.udpInbound.ListenPort != cloudOutputUDPPort {
		t.Fatalf("unexpected inbounds: tcp=%+v udp=%+v", plan.inbound, plan.udpInbound)
	}
	if len(plan.rules) != 2 || !slices.Equal(plan.rules[0].Domain, cloudOutputLocalDirectDomains) || plan.rules[0].Outbound != "direct" ||
		!slices.Equal(plan.rules[1].Inbound, []string{cloudOutputInbound, cloudOutputUDPInbound}) || plan.rules[1].Outbound != "cloud-fallback" {
		t.Fatalf("unexpected cloud slot rules: %+v", plan.rules)
	}

	natRules, err := plan.redirectRules()
	if err != nil {
		t.Fatal(err)
	}
	var natJoined []string
	for _, r := range natRules {
		natJoined = append(natJoined, strings.Join(r, " "))
	}
	natAll := strings.Join(natJoined, "\n")
	for _, want := range []string{
		"-o lo -j RETURN",
		"-p tcp --dport 53 -j RETURN",
		"-m mark ! --mark 0x0 -j RETURN",
		"-d 198.51.100.0/24 -j RETURN",
		"-d 198.51.100.2/32 -j RETURN",
		"-p tcp -m set --match-set AWGM-SB-CLOUD dst -m conntrack --ctstate NEW --ctdir ORIGINAL -m mark --mark 0x0 -j REDIRECT --to-ports 51390",
		"-p tcp -d 185.162.93.0/24 -m multiport --dports 9,3478,3479,4044,5683,5684,5685,8082,8090 -m conntrack --ctstate NEW --ctdir ORIGINAL -m mark --mark 0x0 -j REDIRECT --to-ports 51390",
	} {
		if !strings.Contains(natAll, want) {
			t.Errorf("NAT redirect rules missing %q:\n%s", want, natAll)
		}
	}

	mangleOutRules, err := plan.mangleOutputRules()
	if err != nil {
		t.Fatal(err)
	}
	var mangleJoined []string
	for _, r := range mangleOutRules {
		mangleJoined = append(mangleJoined, strings.Join(r, " "))
	}
	mangleAll := strings.Join(mangleJoined, "\n")
	for _, want := range []string{
		"-o lo -j RETURN",
		"-p udp --dport 53 -j RETURN",
		"-m mark ! --mark 0x0 -j RETURN",
		"-p udp -m set --match-set AWGM-SB-CLOUD dst -m mark --mark 0x0 -j MARK --set-xmark 0x20000000/0x20000000",
		"-p udp -d 82.202.0.0/16 -m multiport --dports 9,3478,3479,4044,5683,5684,5685 -m mark --mark 0x0 -j MARK --set-xmark 0x20000000/0x20000000",
	} {
		if !strings.Contains(mangleAll, want) {
			t.Errorf("mangle OUTPUT rules missing %q:\n%s", want, mangleAll)
		}
	}
	if strings.Contains(mangleAll, "--ctstate NEW") {
		t.Errorf("mangle OUTPUT UDP rules must NOT require --ctstate NEW so pre-existing sockets are adopted: %s", mangleAll)
	}

	preRules := plan.manglePreroutingRules()
	if len(preRules) != 1 || !strings.Contains(strings.Join(preRules[0], " "), "-i lo -p udp -m mark --mark 0x20000000/0x20000000 -j TPROXY --on-port 51391 --on-ip 127.0.0.1 --tproxy-mark 0x20000000/0x20000000") {
		t.Fatalf("unexpected mangle PREROUTING TPROXY rules: %v", preRules)
	}
}

func TestCloudOutputConfigRequiresTargetAndPreservesExistingRules(t *testing.T) {
	p, err := newCloudOutputPlan(51390, "cloud-fallback", []netip.AddrPort{netip.MustParseAddrPort("87.228.71.67:8090")}, 666)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &RouterConfig{
		Inbounds: []Inbound{{Tag: "existing", ListenPort: 51272}},
		Route:    Route{Rules: []Rule{{Action: "route", Outbound: "ordinary"}}},
	}
	if _, err := p.configure(cfg, func(string) bool { return false }); err == nil {
		t.Fatal("missing cloud outbound accepted")
	}
	exists := func(tag string) bool { return tag == "cloud-fallback" }
	next, err := p.configure(cfg, exists)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.DefaultMark) != 0 || string(next.Route.DefaultMark) != "666" || len(cfg.Inbounds) != 1 || len(cfg.Route.Rules) != 1 || len(next.Inbounds) != 3 ||
		len(next.Route.Rules) != 3 || next.Route.Rules[0].Outbound != "direct" ||
		next.Route.Rules[1].Outbound != "cloud-fallback" || next.Route.Rules[2].Outbound != "ordinary" {
		t.Fatalf("unexpected cloud slot configuration: %+v", next)
	}
	cfg.Route.DefaultMark = []byte(`"0x29b"`)
	if _, err := p.configure(cfg, exists); err == nil {
		t.Fatal("existing route.default_mark was silently replaced")
	}
	cfg.Route.DefaultMark = nil
	cfg.Inbounds[0].ListenPort = 51390
	if _, err := p.configure(cfg, exists); err == nil {
		t.Fatal("TCP listener port collision accepted")
	}
	cfg.Inbounds[0].ListenPort = 51391
	if _, err := p.configure(cfg, exists); err == nil {
		t.Fatal("UDP listener port collision accepted")
	}
}

func TestCloudOutputRestoreReplacesOnlyOwnedRules(t *testing.T) {
	const natSnapshot = "-N AWGM-KEENDNS-OUT\n-A OUTPUT -j USER-CHAIN\n-A OUTPUT -j AWGM-KEENDNS-OUT\n-A OUTPUT -j AWGM-KEENDNS-OUT\n"
	const mangleSnapshot = "-N AWGM-KEENDNS-OUT-M\n-N AWGM-KEENDNS-PRE\n-A OUTPUT -j USER-MANGLE\n-A OUTPUT -j AWGM-KEENDNS-OUT-M\n-A PREROUTING -j USER-PRE\n-A PREROUTING -j AWGM-KEENDNS-PRE\n"
	p, err := newCloudOutputPlan(51390, "cloud-fallback", []netip.AddrPort{netip.MustParseAddrPort("87.228.71.67:8090")}, 666)
	if err != nil {
		t.Fatal(err)
	}
	restoreNAT, err := cloudOutputRestore(natSnapshot, &p)
	if err != nil || strings.Count(restoreNAT, "-D OUTPUT -j AWGM-KEENDNS-OUT") != 2 ||
		strings.Count(restoreNAT, "-I OUTPUT 1 -j AWGM-KEENDNS-OUT") != 1 ||
		strings.Contains(restoreNAT, "USER-CHAIN") {
		t.Fatalf("unexpected NAT update: %q (%v)", restoreNAT, err)
	}
	removeNAT, err := cloudOutputRestore(natSnapshot, nil)
	if err != nil || !strings.Contains(removeNAT, "-F AWGM-KEENDNS-OUT\n-X AWGM-KEENDNS-OUT\nCOMMIT") ||
		strings.Contains(removeNAT, "USER-CHAIN") {
		t.Fatalf("unexpected NAT teardown: %q (%v)", removeNAT, err)
	}

	restoreMangle, err := cloudOutputMangleRestore(mangleSnapshot, &p)
	if err != nil || !strings.Contains(restoreMangle, "-I OUTPUT 1 -j AWGM-KEENDNS-OUT-M") ||
		!strings.Contains(restoreMangle, "-I PREROUTING 1 -j AWGM-KEENDNS-PRE") ||
		strings.Contains(restoreMangle, "USER-MANGLE") || strings.Contains(restoreMangle, "USER-PRE") {
		t.Fatalf("unexpected mangle update: %q (%v)", restoreMangle, err)
	}
	removeMangle, err := cloudOutputMangleRestore(mangleSnapshot, nil)
	if err != nil || !strings.Contains(removeMangle, "-F AWGM-KEENDNS-OUT-M\n-X AWGM-KEENDNS-OUT-M\n") ||
		!strings.Contains(removeMangle, "-F AWGM-KEENDNS-PRE\n-X AWGM-KEENDNS-PRE\nCOMMIT") {
		t.Fatalf("unexpected mangle teardown: %q (%v)", removeMangle, err)
	}
}

// statefulCloudTables simulates Linux nat+mangle iptables -S, ip rule/route, and
// ipset state for hermetic idempotency and teardown tests.
type statefulCloudTables struct {
	mu           sync.Mutex
	natChains    map[string][]string
	mangleChains map[string][]string
	ipRules      []string
	table101     []string
	ipsets       map[string]map[string]bool
	restoreCalls int
	ipsetSwaps   int
	ipRuleWrites int
}

func newStatefulCloudTables() *statefulCloudTables {
	return &statefulCloudTables{
		natChains:    map[string][]string{"PREROUTING": {}, "INPUT": {}, "OUTPUT": {}, "POSTROUTING": {}},
		mangleChains: map[string][]string{"PREROUTING": {}, "INPUT": {}, "OUTPUT": {}, "POSTROUTING": {}},
		ipsets:       make(map[string]map[string]bool),
	}
}

func canonicalizeLinuxIPTRule(line string) string {
	line = strings.ReplaceAll(line, " -p tcp ", " -p tcp -m tcp ")
	line = strings.ReplaceAll(line, "--dport ", "--dport ")
	line = strings.ReplaceAll(line, " -p udp ", " -p udp -m udp ")
	line = strings.ReplaceAll(line, "--mark 0x0 ", "--mark 0x0/0xffffffff ")
	if strings.HasSuffix(line, "--mark 0x0") {
		line = strings.TrimSuffix(line, "--mark 0x0") + "--mark 0x0/0xffffffff"
	}
	return line
}

func (n *statefulCloudTables) dumpTable(table string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	src := n.natChains
	if table == "mangle" {
		src = n.mangleChains
	}
	var b strings.Builder
	b.WriteString("-P PREROUTING ACCEPT\n-P INPUT ACCEPT\n-P OUTPUT ACCEPT\n-P POSTROUTING ACCEPT\n")
	var custom []string
	for ch := range src {
		if ch != "PREROUTING" && ch != "INPUT" && ch != "OUTPUT" && ch != "POSTROUTING" {
			custom = append(custom, ch)
		}
	}
	sort.Strings(custom)
	for _, ch := range custom {
		b.WriteString("-N " + ch + "\n")
	}
	for _, base := range []string{"PREROUTING", "INPUT", "OUTPUT", "POSTROUTING"} {
		for _, r := range src[base] {
			b.WriteString("-A " + base + " " + canonicalizeLinuxIPTRule(r) + "\n")
		}
	}
	for _, ch := range custom {
		for _, r := range src[ch] {
			b.WriteString("-A " + ch + " " + canonicalizeLinuxIPTRule(r) + "\n")
		}
	}
	return b.String()
}

func (n *statefulCloudTables) restoreNoflush(_ context.Context, input string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.restoreCalls++
	var cur map[string][]string
	for _, rawLine := range strings.Split(input, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case line == "*nat":
			cur = n.natChains
		case line == "*mangle":
			cur = n.mangleChains
		case line == "COMMIT":
			cur = nil
		case strings.HasPrefix(line, "-N "):
			ch := strings.TrimSpace(strings.TrimPrefix(line, "-N "))
			if _, ok := cur[ch]; !ok {
				cur[ch] = nil
			}
		case strings.HasPrefix(line, "-F "):
			ch := strings.TrimSpace(strings.TrimPrefix(line, "-F "))
			cur[ch] = nil
		case strings.HasPrefix(line, "-X "):
			ch := strings.TrimSpace(strings.TrimPrefix(line, "-X "))
			delete(cur, ch)
		case strings.HasPrefix(line, "-D "):
			rest := strings.TrimPrefix(line, "-D ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				ch, rule := parts[0], parts[1]
				existing := cur[ch]
				filtered := existing[:0]
				removed := false
				for _, r := range existing {
					if !removed && r == rule {
						removed = true
						continue
					}
					filtered = append(filtered, r)
				}
				cur[ch] = filtered
			}
		case strings.HasPrefix(line, "-I "):
			rest := strings.TrimPrefix(line, "-I ")
			// Expect: -I <chain> 1 <rule>
			parts := strings.SplitN(rest, " ", 3)
			if len(parts) == 3 {
				ch, rule := parts[0], parts[2]
				cur[ch] = append([]string{rule}, cur[ch]...)
			}
		case strings.HasPrefix(line, "-A "):
			rest := strings.TrimPrefix(line, "-A ")
			parts := strings.SplitN(rest, " ", 2)
			if len(parts) == 2 {
				ch, rule := parts[0], parts[1]
				cur[ch] = append(cur[ch], rule)
			}
		}
	}
	return nil
}

func (n *statefulCloudTables) runIP(_ context.Context, args ...string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(args) >= 2 && args[0] == "-4" {
		args = args[1:]
	}
	cmd := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(cmd, "rule del "):
		if len(n.ipRules) == 0 {
			return errENOENT
		}
		n.ipRuleWrites++
		n.ipRules = nil
		return nil
	case strings.HasPrefix(cmd, "rule add "):
		n.ipRuleWrites++
		n.ipRules = append(n.ipRules, "1090:\tfrom all fwmark 0x20000000/0x20000000 lookup 101")
		return nil
	case strings.HasPrefix(cmd, "route replace local 0.0.0.0/0 dev lo table 101"):
		n.ipRuleWrites++
		n.table101 = []string{"local default dev lo scope host"}
		return nil
	case strings.HasPrefix(cmd, "route flush table 101"):
		n.ipRuleWrites++
		n.table101 = nil
		return nil
	}
	return nil
}

func (n *statefulCloudTables) runIPOut(_ context.Context, args ...string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(args) >= 2 && args[0] == "-4" {
		args = args[1:]
	}
	cmd := strings.Join(args, " ")
	switch cmd {
	case "rule show":
		return strings.Join(n.ipRules, "\n"), nil
	case "route show table 101":
		return strings.Join(n.table101, "\n"), nil
	}
	return "", nil
}

func (n *statefulCloudTables) ipsetRunner(_ context.Context, stdin string, args ...string) ([]byte, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(args) == 0 {
		return nil, errors.New("empty ipset args")
	}
	switch args[0] {
	case "list":
		setName := args[1]
		if _, ok := n.ipsets[setName]; !ok {
			return []byte("The set with the given name does not exist"), errors.New("exit status 1")
		}
		return []byte(setName + "\n"), nil
	case "create":
		setName := args[1]
		if _, ok := n.ipsets[setName]; !ok {
			n.ipsets[setName] = make(map[string]bool)
		}
		return nil, nil
	case "destroy":
		setName := args[1]
		if _, ok := n.ipsets[setName]; !ok {
			return []byte("The set with the given name does not exist"), errors.New("exit status 1")
		}
		delete(n.ipsets, setName)
		return nil, nil
	case "swap":
		a, b := args[1], args[2]
		if _, ok := n.ipsets[a]; !ok {
			return []byte("The set with the given name does not exist"), errors.New("exit status 1")
		}
		if _, ok := n.ipsets[b]; !ok {
			return []byte("The set with the given name does not exist"), errors.New("exit status 1")
		}
		n.ipsets[a], n.ipsets[b] = n.ipsets[b], n.ipsets[a]
		n.ipsetSwaps++
		return nil, nil
	case "restore":
		for _, line := range strings.Split(stdin, "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			switch f[0] {
			case "create":
				if _, ok := n.ipsets[f[1]]; !ok {
					n.ipsets[f[1]] = make(map[string]bool)
				}
			case "flush":
				n.ipsets[f[1]] = make(map[string]bool)
			case "add":
				if n.ipsets[f[1]] == nil {
					n.ipsets[f[1]] = make(map[string]bool)
				}
				n.ipsets[f[1]][f[2]] = true
			}
		}
		return nil, nil
	}
	return nil, nil
}

func (n *statefulCloudTables) snapshotSet(name string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.ipsets[name]
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (n *statefulCloudTables) hasCloudHooks() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, natExists := n.natChains[cloudOutputChain]
	_, mangleExists := n.mangleChains[cloudOutputMangleChain]
	_, preExists := n.mangleChains[cloudOutputPreChain]
	return natExists && mangleExists && preExists && len(n.ipRules) == 1 && len(n.table101) == 1
}

func (n *statefulCloudTables) anyCloudState() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.natChains[cloudOutputChain]; ok {
		return true
	}
	if _, ok := n.mangleChains[cloudOutputMangleChain]; ok {
		return true
	}
	if _, ok := n.mangleChains[cloudOutputPreChain]; ok {
		return true
	}
	return len(n.ipRules) > 0 || len(n.table101) > 0 || len(n.ipsets) > 0
}

func (n *statefulCloudTables) ipt(fe *fakeExec) *IPTables {
	ipt := newFakeIPTables(fe)
	ipt.restoreNoflush = func(ctx context.Context, input string) error {
		if err := fe.restoreNoflush(ctx, input); err != nil {
			return err
		}
		return n.restoreNoflush(ctx, input)
	}
	ipt.runIPTablesOut = func(_ context.Context, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-t" && args[1] == "nat" && args[2] == "-S" {
			return n.dumpTable("nat"), nil
		}
		if len(args) >= 3 && args[0] == "-t" && args[1] == "mangle" && args[2] == "-S" {
			return n.dumpTable("mangle") + "-N " + ChainName + "\n-A PREROUTING -m conntrack ! --ctstate INVALID -j " + ChainName + "\n", nil
		}
		return jumpsPresentDump(), nil
	}
	ipt.runIP = n.runIP
	ipt.runIPOut = n.runIPOut
	return ipt
}

type countingCloudProc struct {
	*fakeSingbox
	mu          sync.Mutex
	reloadCalls int
}

func (p *countingCloudProc) Reload() error {
	p.mu.Lock()
	p.reloadCalls++
	p.mu.Unlock()
	return p.fakeSingbox.Reload()
}

func (p *countingCloudProc) reloads() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reloadCalls
}

func seedProcNetSockets(t *testing.T, pid, tcpPort, tcpInode, udpPort, udpInode, ownerPID int) string {
	t.Helper()
	root := t.TempDir()
	old := procFSRoot
	procFSRoot = root
	t.Cleanup(func() { procFSRoot = old })

	netDir := filepath.Join(root, "net")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tcpLine := fmt.Sprintf("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n   0: 0100007F:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 00000000 100 0 0 10 0\n", tcpPort, tcpInode)
	if err := os.WriteFile(filepath.Join(netDir, "tcp"), []byte(tcpLine), 0o644); err != nil {
		t.Fatal(err)
	}
	udpLine := fmt.Sprintf("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n   0: 0100007F:%04X 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 %d 1 00000000 100 0 0 10 0\n", udpPort, udpInode)
	if err := os.WriteFile(filepath.Join(netDir, "udp"), []byte(udpLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if ownerPID > 0 {
		fdDir := filepath.Join(root, fmt.Sprintf("%d", ownerPID), "fd")
		if err := os.MkdirAll(fdDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if tcpInode > 0 {
			if err := os.Symlink(fmt.Sprintf("socket:[%d]", tcpInode), filepath.Join(fdDir, "7")); err != nil {
				t.Fatal(err)
			}
		}
		if udpInode > 0 {
			if err := os.Symlink(fmt.Sprintf("socket:[%d]", udpInode), filepath.Join(fdDir, "8")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pid > 0 && pid != ownerPID {
		otherFdDir := filepath.Join(root, fmt.Sprintf("%d", pid), "fd")
		if err := os.MkdirAll(otherFdDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("socket:[999999]", filepath.Join(otherFdDir, "7")); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newCloudOutputTestService(t *testing.T, obTag string) (*ServiceImpl, *statefulCloudTables, *countingCloudProc, string) {
	t.Helper()
	stubTProxyProbe(t, func(context.Context) bool { return true })
	stubXtDscpProbe(t, false, false)
	stubEnsureKernelModule(t)
	stubLinkAbsent(t)

	dir := t.TempDir()
	sb := newReadyTestSingbox(t)
	sb.dir = dir
	sb.isRunningFn = func() (bool, int) { return true, 4242 }
	proc := &countingCloudProc{fakeSingbox: sb}

	orch := orchestrator.NewWithAppliedPath(dir, proc, filepath.Join(t.TempDir(), "singbox-applied.json"))
	t.Cleanup(orch.Close)
	for _, meta := range []orchestrator.SlotMeta{
		{Slot: orchestrator.SlotKeenDNSCloud, Filename: "09-keendns-cloud.json"},
		{Slot: orchestrator.SlotRouter, Filename: "20-router.json"},
		{Slot: orchestrator.SlotFakeIP, Filename: "21-fakeip.json"},
	} {
		if err := orch.Register(meta); err != nil {
			t.Fatalf("orch.Register(%s): %v", meta.Slot, err)
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("orch.Bootstrap: %v", err)
	}
	routerCfg := fmt.Sprintf(`{"outbounds":[{"tag":%q,"type":"selector","outbounds":["direct"]}],"route":{"rules":[]}}`, obTag)
	if err := orch.SaveSilent(orchestrator.SlotRouter, []byte(routerCfg)); err != nil {
		t.Fatal(err)
	}
	if err := orch.SetEnabledSilent(orchestrator.SlotRouter, true); err != nil {
		t.Fatal(err)
	}

	tables := newStatefulCloudTables()
	fe := newFakeExec()
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled:              true,
		RoutingMode:          "tproxy",
		KeenDNSCloudOutbound: obTag,
	})
	svc := &ServiceImpl{
		deps: Deps{
			Settings:       store,
			Singbox:        proc,
			Orch:           orch,
			IPTables:       tables.ipt(fe),
			WANIPCollector: &fakeWANIPCollector{ips: []string{"203.0.113.10/32"}},
			Bus:            &mockBus{},
			SwapGate:       &netdev.SwapGate{},
			KeenDNSCloudSnapshot: func(context.Context) (KeenDNSCloudSnapshot, error) {
				ep := netip.MustParseAddrPort("87.228.71.67:8090")
				return KeenDNSCloudSnapshot{
					Enabled:        true,
					RelayIPs:       []string{"87.228.71.67"},
					RelayEndpoints: []netip.AddrPort{ep},
				}, nil
			},
		},
	}
	svc.deps.OpkgTunPool = testOpkgTunPool(svc)
	return svc, tables, proc, dir
}

func TestCloudOutputRestore_LinuxCanonicalIdempotent(t *testing.T) {
	p, err := newCloudOutputPlanWithOpts(cloudOutputPlanOpts{
		tcpPort:   cloudOutputPort,
		udpPort:   cloudOutputUDPPort,
		outbound:  "cloud-fallback",
		endpoints: []netip.AddrPort{netip.MustParseAddrPort("87.228.71.67:8090")},
		wanIPs:    []string{"203.0.113.10/32"},
		mark:      cloudOutputMark,
		udpMark:   cloudOutputUDPMark,
		useIpset:  true,
		enableUDP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	natRules, err := p.redirectRules()
	if err != nil {
		t.Fatal(err)
	}
	var natLines []string
	natLines = append(natLines, "-N "+cloudOutputChain, "-A OUTPUT -j "+cloudOutputChain)
	for _, r := range natRules {
		natLines = append(natLines, canonicalizeLinuxIPTRule("-A "+cloudOutputChain+" "+strings.Join(r, " ")))
	}
	canonicalNAT := strings.Join(natLines, "\n") + "\n"
	patch, err := cloudOutputRestore(canonicalNAT, &p)
	if err != nil || patch != "" {
		t.Fatalf("expected canonical NAT dump to be idempotent, got patch=%q err=%v", patch, err)
	}

	mangleOutRules, err := p.mangleOutputRules()
	if err != nil {
		t.Fatal(err)
	}
	preRules := p.manglePreroutingRules()
	var mLines []string
	mLines = append(mLines, "-N "+cloudOutputMangleChain, "-N "+cloudOutputPreChain, "-A OUTPUT -j "+cloudOutputMangleChain, "-A PREROUTING -j "+cloudOutputPreChain)
	for _, r := range mangleOutRules {
		mLines = append(mLines, canonicalizeLinuxIPTRule("-A "+cloudOutputMangleChain+" "+strings.Join(r, " ")))
	}
	for _, r := range preRules {
		mLines = append(mLines, canonicalizeLinuxIPTRule("-A "+cloudOutputPreChain+" "+strings.Join(r, " ")))
	}
	canonicalMangle := strings.Join(mLines, "\n") + "\n"
	mPatch, err := cloudOutputMangleRestore(canonicalMangle, &p)
	if err != nil || mPatch != "" {
		t.Fatalf("expected canonical mangle dump to be idempotent, got patch=%q err=%v", mPatch, err)
	}

	driftedNAT := strings.Replace(canonicalNAT, "--to-ports 51390", "--to-ports 51399", 1)
	patch, err = cloudOutputRestore(driftedNAT, &p)
	if err != nil || !strings.Contains(patch, "-F "+cloudOutputChain) || !strings.Contains(patch, "--to-ports 51390") {
		t.Fatalf("expected drifted NAT rule to emit replacement patch, got patch=%q err=%v", patch, err)
	}
}

func TestCloudOutputListenProbe_PidSocketOwnership(t *testing.T) {
	seedProcNetSockets(t, 9999, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	if !cloudOutputListenProbe(context.Background(), 4242, cloudOutputPort) {
		t.Fatal("expected owning PID 4242 with TCP socket:[54321] to pass listen probe")
	}
	if !cloudOutputUDPListenProbe(context.Background(), 4242, cloudOutputUDPPort) {
		t.Fatal("expected owning PID 4242 with UDP socket:[54322] to pass listen probe")
	}
	if cloudOutputListenProbe(context.Background(), 9999, cloudOutputPort) {
		t.Fatal("expected non-owning PID 9999 on bound TCP port to fail listen probe")
	}
	if cloudOutputUDPListenProbe(context.Background(), 9999, cloudOutputUDPPort) {
		t.Fatal("expected non-owning PID 9999 on bound UDP port to fail listen probe")
	}
	if cloudOutputListenProbe(context.Background(), 4242, cloudOutputPort+5) {
		t.Fatal("expected unbound TCP port to fail listen probe")
	}
	if cloudOutputUDPListenProbe(context.Background(), 4242, cloudOutputUDPPort+5) {
		t.Fatal("expected unbound UDP port to fail listen probe")
	}
}

func TestReconcileCloudOutput_SteadyStateZeroReloadsAndZeroRestore(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	svc, tables, proc, _ := newCloudOutputTestService(t, "cloud-fallback")
	setCloudOutputIpsetRunner(tables.ipsetRunner)
	defer setCloudOutputIpsetRunner(nil)
	setCloudOutputDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("185.10.184.178"),
			netip.MustParseAddr("198.51.100.11"), // rejected local KeenDNS portal
			netip.MustParseAddr("198.18.0.10"),   // rejected FakeIP
		}, nil
	})
	defer setCloudOutputDNSLookupIPv4(nil)

	st, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileCloudOutput(context.Background(), st.SingboxRouter, false); err != nil {
		t.Fatalf("initial reconcileCloudOutput: %v", err)
	}
	// Expect 1 reload, 2 restores (1 nat + 1 mangle), 1 atomic ipset swap, and ip rule+route setup.
	if proc.reloads() != 1 || tables.restoreCalls != 2 || tables.ipsetSwaps != 1 {
		t.Fatalf("after initial reconcile: reloads=%d restores=%d swaps=%d, want (1, 2, 1)", proc.reloads(), tables.restoreCalls, tables.ipsetSwaps)
	}
	if !tables.hasCloudHooks() {
		t.Fatal("expected NAT + mangle chains + ip rule 1090 + table 101 active after initial reconcile")
	}
	gotSet := tables.snapshotSet(cloudOutputIpsetName)
	if !slices.Contains(gotSet, "87.228.71.67") || !slices.Contains(gotSet, "185.10.184.178") {
		t.Fatalf("AWGM-SB-CLOUD missing expected IPs: %v", gotSet)
	}
	if slices.Contains(gotSet, "198.51.100.11") || slices.Contains(gotSet, "198.18.0.10") {
		t.Fatalf("AWGM-SB-CLOUD must reject 198.51.100.0/24 and 198.18.0.0/15: %v", gotSet)
	}

	initIPWrites := tables.ipRuleWrites
	for i := 0; i < 3; i++ {
		if err := svc.reconcileCloudOutput(context.Background(), st.SingboxRouter, false); err != nil {
			t.Fatalf("steady tick %d: %v", i, err)
		}
	}
	if proc.reloads() != 1 || tables.restoreCalls != 2 || tables.ipsetSwaps != 1 || tables.ipRuleWrites != initIPWrites {
		t.Fatalf("steady ticks triggered churn: reloads=%d restores=%d swaps=%d ipWrites=%d (init=%d)",
			proc.reloads(), tables.restoreCalls, tables.ipsetSwaps, tables.ipRuleWrites, initIPWrites)
	}
}

func TestReconcileCloudOutput_ColdStartAndLKGRetention(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	svc, tables, _, _ := newCloudOutputTestService(t, "cloud-fallback")
	setCloudOutputIpsetRunner(tables.ipsetRunner)
	defer setCloudOutputIpsetRunner(nil)
	defer setCloudOutputDNSLookupIPv4(nil)
	defer setCloudOutputNow(nil)

	now := time.Unix(1_700_000_000, 0)
	setCloudOutputNow(func() time.Time { return now })

	rciSnap := KeenDNSCloudSnapshot{Enabled: true} // zero relay endpoints -> cold start
	var rciErr error
	svc.deps.KeenDNSCloudSnapshot = func(context.Context) (KeenDNSCloudSnapshot, error) {
		return rciSnap, rciErr
	}
	dnsFailHost := ""
	setCloudOutputDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		if dnsFailHost == host {
			return nil, errors.New("dns timeout")
		}
		if host == "broker.netcraze.cloud" {
			return []netip.Addr{netip.MustParseAddr("185.10.184.178")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("82.202.218.226")}, nil
	})

	st, _ := svc.deps.Settings.Load()
	if err := svc.reconcileCloudOutput(context.Background(), st.SingboxRouter, false); err != nil {
		t.Fatalf("cold-start reconcile failed: %v", err)
	}
	if !tables.hasCloudHooks() || !slices.Contains(tables.snapshotSet(cloudOutputIpsetName), "185.10.184.178") {
		t.Fatalf("expected cold-start hooks + resolved broker IP active, set=%v", tables.snapshotSet(cloudOutputIpsetName))
	}

	// Simulate broker DNS transient failure + RCI error after 35 minutes (>30m stale, <2h LKG): must retain LKG IP.
	now = now.Add(35 * time.Minute)
	rciErr = errors.New("rci 503")
	dnsFailHost = "broker.netcraze.cloud"
	if err := svc.reconcileCloudOutput(context.Background(), st.SingboxRouter, false); err != nil {
		t.Fatalf("reconcile during transient outage failed: %v", err)
	}
	if !slices.Contains(tables.snapshotSet(cloudOutputIpsetName), "185.10.184.178") {
		t.Fatalf("expected bounded LKG retention for 185.10.184.178, got %v", tables.snapshotSet(cloudOutputIpsetName))
	}
}

func TestReconcileCloudOutput_DetachesStaleWhenDisabledOrStopped(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	svc, tables, proc, _ := newCloudOutputTestService(t, "cloud-fallback")
	setCloudOutputIpsetRunner(tables.ipsetRunner)
	defer setCloudOutputIpsetRunner(nil)
	setCloudOutputDNSLookupIPv4(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("185.10.184.178")}, nil
	})
	defer setCloudOutputDNSLookupIPv4(nil)

	tables.mu.Lock()
	tables.natChains[cloudOutputChain] = []string{"-o lo -j RETURN"}
	tables.natChains["OUTPUT"] = []string{"-j " + cloudOutputChain}
	tables.mangleChains[cloudOutputMangleChain] = []string{"-o lo -j RETURN"}
	tables.mangleChains["OUTPUT"] = []string{"-j " + cloudOutputMangleChain}
	tables.mangleChains[cloudOutputPreChain] = []string{"-i lo -p udp -j ACCEPT"}
	tables.mangleChains["PREROUTING"] = []string{"-j " + cloudOutputPreChain}
	tables.ipRules = []string{"1090:\tfrom all fwmark 0x20000000/0x20000000 lookup 101"}
	tables.table101 = []string{"local default dev lo scope host"}
	tables.ipsets[cloudOutputIpsetName] = map[string]bool{"87.228.71.67": true}
	tables.mu.Unlock()

	// Case A: disabled in settings while slot already disabled -> strips NAT, mangle, ip rule/route, and ipset.
	if err := svc.reconcileCloudOutput(context.Background(), storage.SingboxRouterSettings{Enabled: false, KeenDNSCloudOutbound: "cloud-fallback"}, false); err != nil {
		t.Fatalf("reconcile disabled with stale chain: %v", err)
	}
	if tables.anyCloudState() {
		t.Fatalf("expected all stale cloud chains/rules/ipsets stripped, nat=%v mangle=%v ipsets=%v", tables.natChains, tables.mangleChains, tables.ipsets)
	}

	// Re-attach, then stop sing-box -> detach cleanly.
	if err := svc.reconcileCloudOutput(context.Background(), storage.SingboxRouterSettings{Enabled: true, KeenDNSCloudOutbound: "cloud-fallback"}, false); err != nil {
		t.Fatalf("attach while running: %v", err)
	}
	if !tables.hasCloudHooks() {
		t.Fatal("expected active cloud hooks after attach")
	}
	proc.isRunningFn = func() (bool, int) { return false, 0 }
	if err := svc.reconcileCloudOutput(context.Background(), storage.SingboxRouterSettings{Enabled: true, KeenDNSCloudOutbound: "cloud-fallback"}, false); err != nil {
		t.Fatalf("detach when stopped: %v", err)
	}
	if tables.anyCloudState() {
		t.Fatalf("expected all cloud hooks and ipsets stripped when sing-box is stopped")
	}
}

func TestDisableModes_DetachCloudOutput(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	ctx := context.Background()

	attachAndVerify := func(t *testing.T, svc *ServiceImpl, tables *statefulCloudTables) {
		t.Helper()
		st, err := svc.deps.Settings.Load()
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.reconcileCloudOutput(ctx, st.SingboxRouter, false); err != nil {
			t.Fatalf("attach cloud output: %v", err)
		}
		if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
			t.Fatalf("precondition failed: hooks=%v slotEnabled=%v", tables.hasCloudHooks(), svc.slotEnabled(orchestrator.SlotKeenDNSCloud))
		}
	}

	t.Run("disableTProxyLocked", func(t *testing.T) {
		svc, tables, _, _ := newCloudOutputTestService(t, "cloud-fallback")
		attachAndVerify(t, svc, tables)
		if err := svc.disableTProxyLocked(ctx, false); err != nil {
			t.Fatalf("disableTProxyLocked: %v", err)
		}
		if tables.anyCloudState() || svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
			t.Fatalf("expected cloud output detached after tproxy disable")
		}
	})

	t.Run("disableFakeIPTunLocked_unprovisioned", func(t *testing.T) {
		svc, tables, _, _ := newCloudOutputTestService(t, "cloud-fallback")
		attachAndVerify(t, svc, tables)
		if err := svc.disableFakeIPTunLocked(ctx, &storage.Settings{}, false); err != nil {
			t.Fatalf("disableFakeIPTunLocked: %v", err)
		}
		if tables.anyCloudState() || svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
			t.Fatalf("expected cloud output detached after fakeip disable")
		}
	})

	t.Run("disablePolicyTunLocked_nilState", func(t *testing.T) {
		svc, tables, _, _ := newCloudOutputTestService(t, "cloud-fallback")
		attachAndVerify(t, svc, tables)
		if err := svc.disablePolicyTunLocked(ctx, &storage.Settings{}, false); err != nil {
			t.Fatalf("disablePolicyTunLocked: %v", err)
		}
		if tables.anyCloudState() || svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
			t.Fatalf("expected cloud output detached after policy-tun disable")
		}
	})
}

func TestKeenDNSCloudOutbound_RenameDeleteAndStaging(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	ctx := context.Background()
	svc, tables, _, dir := newCloudOutputTestService(t, "cloud-fallback")

	st, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileCloudOutput(ctx, st.SingboxRouter, false); err != nil {
		t.Fatalf("attach cloud output: %v", err)
	}

	locs := svc.OutboundReferenceLocations("cloud-fallback")
	if len(locs) == 0 || locs[len(locs)-1] != "keendnsCloudOutbound" {
		t.Fatalf("OutboundReferenceLocations = %v, want keendnsCloudOutbound included", locs)
	}

	if err := svc.DeleteCompositeOutbound(ctx, "cloud-fallback", false); err == nil || !strings.Contains(err.Error(), "keendnsCloudOutbound") {
		t.Fatalf("expected non-force delete to be rejected due to keendnsCloudOutbound, got %v", err)
	}

	// Rename composite outbound: updates settings and detaches 09-keendns-cloud.json until ApplyStaging.
	if err := svc.UpdateCompositeOutbound(ctx, "cloud-fallback", Outbound{
		Tag:       "cloud-fallback-v2",
		Type:      "selector",
		Outbounds: []string{"direct"},
	}); err != nil {
		t.Fatalf("UpdateCompositeOutbound rename: %v", err)
	}
	st, err = svc.deps.Settings.Load()
	if err != nil || st.SingboxRouter.KeenDNSCloudOutbound != "cloud-fallback-v2" {
		t.Fatalf("after rename KeenDNSCloudOutbound = %q (err=%v), want cloud-fallback-v2", st.SingboxRouter.KeenDNSCloudOutbound, err)
	}
	if svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected SlotKeenDNSCloud to be detached while renamed router outbound is still staged")
	}

	// ApplyStaging must validate cleanly and immediately re-attach 09-keendns-cloud.json with the new tag.
	res, err := svc.ApplyStaging(ctx)
	if err != nil || !res.Ok() {
		t.Fatalf("ApplyStaging after rename: res=%v err=%v", res, err)
	}
	if !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) || !tables.hasCloudHooks() {
		t.Fatalf("expected cloud slot and hooks to re-attach after ApplyStaging: slot=%v hooks=%v", svc.slotEnabled(orchestrator.SlotKeenDNSCloud), tables.hasCloudHooks())
	}
	slotBytes, err := os.ReadFile(filepath.Join(dir, "09-keendns-cloud.json"))
	if err != nil || !strings.Contains(string(slotBytes), `"cloud-fallback-v2"`) {
		t.Fatalf("09-keendns-cloud.json does not target cloud-fallback-v2: %s (%v)", slotBytes, err)
	}

	// External tunnel rename must rewrite settings + active 09-keendns-cloud.json directly, leaving local portal direct rule intact.
	if err := svc.RenameExternalOutboundTag(ctx, "cloud-fallback-v2", "cloud-fallback-v3"); err != nil {
		t.Fatalf("RenameExternalOutboundTag: %v", err)
	}
	st, _ = svc.deps.Settings.Load()
	if st.SingboxRouter.KeenDNSCloudOutbound != "cloud-fallback-v3" {
		t.Fatalf("after external rename KeenDNSCloudOutbound = %q, want cloud-fallback-v3", st.SingboxRouter.KeenDNSCloudOutbound)
	}
	slotBytes, _ = os.ReadFile(filepath.Join(dir, "09-keendns-cloud.json"))
	if !strings.Contains(string(slotBytes), `"cloud-fallback-v3"`) || !strings.Contains(string(slotBytes), `"direct"`) {
		t.Fatalf("09-keendns-cloud.json lost target or local direct rule on external rename: %s", slotBytes)
	}

	// Force-delete clears settings, disables cloud slot, and detaches OUTPUT/PREROUTING hooks.
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.KeenDNSCloudOutbound = "target-ob"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SaveSilent(orchestrator.SlotRouter, []byte(`{"outbounds":[{"tag":"target-ob","type":"selector","outbounds":["direct"]}],"route":{"rules":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCompositeOutbound(ctx, "target-ob", true); err != nil {
		t.Fatalf("DeleteCompositeOutbound force: %v", err)
	}
	st, _ = svc.deps.Settings.Load()
	if st.SingboxRouter.KeenDNSCloudOutbound != "" {
		t.Fatalf("after force delete KeenDNSCloudOutbound = %q, want empty", st.SingboxRouter.KeenDNSCloudOutbound)
	}
	if svc.slotEnabled(orchestrator.SlotKeenDNSCloud) || tables.anyCloudState() {
		t.Fatalf("expected cloud slot disabled and hooks detached on force delete: slot=%v", svc.slotEnabled(orchestrator.SlotKeenDNSCloud))
	}
}

func TestCloudOutputNetfilterHookLifecycleAndSyntax(t *testing.T) {
	script := cloudOutputNetfilterHookScript()
	if shPath, err := exec.LookPath("sh"); err == nil {
		cmd := exec.Command(shPath, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, serr := cmd.CombinedOutput(); serr != nil {
			t.Fatalf("cloud netfilter.d hook failed sh -n: %v\n%s\n%s", serr, string(out), script)
		}
	}
	for _, want := range []string{
		"pidof awgm-box",
		cloudOutputChain,
		cloudOutputMangleChain,
		cloudOutputPreChain,
		"0x20000000",
		"1090",
		"table 101",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("cloud netfilter.d hook missing %q:\n%s", want, script)
		}
	}

	tmpDir := t.TempDir()
	origPath := netfilterCloudOutputHookPath
	origCustom := netfilterCloudOutputHookCustom
	netfilterCloudOutputHookPath = filepath.Join(tmpDir, "53-awgm-keendns-cloud.sh")
	netfilterCloudOutputHookCustom = true
	t.Cleanup(func() {
		netfilterCloudOutputHookPath = origPath
		netfilterCloudOutputHookCustom = origCustom
	})

	tables := newStatefulCloudTables()
	ipt := tables.ipt(newFakeExec())
	p, err := newCloudOutputPlan(cloudOutputPort, "cloud-fallback", []netip.AddrPort{netip.MustParseAddrPort("87.228.71.67:8090")}, cloudOutputMark)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ipt.applyCloudOutput(ctx, &p, func(context.Context, int) bool { return true }, func(context.Context, int) bool { return true }); err != nil {
		t.Fatalf("attach with hook: %v", err)
	}
	st, err := os.Stat(netfilterCloudOutputHookPath)
	if err != nil {
		t.Fatalf("expected netfilter.d hook file written: %v", err)
	}
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("hook file mode = %v, want 0755", st.Mode().Perm())
	}

	// Idempotent rewrite when mode drifts from 0755 -> 0644.
	if err := os.Chmod(netfilterCloudOutputHookPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ipt.applyCloudOutput(ctx, &p, func(context.Context, int) bool { return true }, func(context.Context, int) bool { return true }); err != nil {
		t.Fatalf("reapply after chmod: %v", err)
	}
	if st2, serr := os.Stat(netfilterCloudOutputHookPath); serr != nil || st2.Mode().Perm() != 0o755 {
		t.Fatalf("expected chmod 0644 repaired to 0755, got mode=%v err=%v", st2, serr)
	}

	// Detach removes the netfilter.d hook file cleanly.
	if err := ipt.applyCloudOutput(ctx, nil, nil, nil); err != nil {
		t.Fatalf("detach with hook: %v", err)
	}
	if _, err := os.Stat(netfilterCloudOutputHookPath); !os.IsNotExist(err) {
		t.Fatalf("expected hook file removed on detach, stat err=%v", err)
	}
}

func TestReconcileCloudOutput_ImmediateAttachOnModeSwitchResumeAndControlStart(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	stubListeningProbe(t, func() bool { return true })
	stubTunReadyProbe(t, func(string) bool { return true })
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool { return true })

	svc, tables, proc, dir := newCloudOutputTestService(t, "cloud-fallback")
	svc.reapNetfilterSettleDelay = 0
	log := &callLog{}
	svc.deps.OpkgTun = &recOpkgTun{log: log}
	svc.deps.StaticRoutes = &recStaticRoutes{log: log}
	svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	svc.deps.DefaultRoute = &recDefaultRoute{log: log}
	svc.deps.FakeIPTun = DefaultFakeIPTunParams()
	svc.deps.Policies = &fakeAccessPolicyProvider{mark: "0xffffaaa"}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	if err := svc.deps.Settings.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.DeviceMode = "all"
		cur.SingboxRouter.WANAutoDetect = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	setCloudOutputIpsetRunner(tables.ipsetRunner)
	defer setCloudOutputIpsetRunner(nil)
	setCloudOutputDNSLookupIPv4(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("185.10.184.178")}, nil
	})
	defer setCloudOutputDNSLookupIPv4(nil)

	ctx := context.Background()
	st, err := svc.deps.Settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.reconcileCloudOutput(ctx, st.SingboxRouter, false); err != nil {
		t.Fatalf("initial reconcileCloudOutput: %v", err)
	}
	if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected initial cloud hooks and slot active")
	}
	slotBytes, err := os.ReadFile(filepath.Join(dir, "09-keendns-cloud.json"))
	if err != nil {
		t.Fatalf("read 09-keendns-cloud.json: %v", err)
	}
	if strings.Contains(string(slotBytes), `"final"`) {
		t.Fatalf("09-keendns-cloud.json must not emit route.final: %s", slotBytes)
	}

	// SwitchRoutingMode(tproxy -> policy-tun -> tproxy) must stage 09-keendns-cloud.json
	// before orchestratorApplyNow so sing-box starts once per tun toggle (0 extra reloads after enable),
	// and retain cloud hooks across Reconcile() in policy-tun (where IPTables.Uninstall runs).
	reloadsBeforePolicy := proc.reloads()
	startsBeforePolicy := proc.startCalls
	if err := svc.SwitchRoutingMode(ctx, statePolicyTun); err != nil {
		t.Fatalf("SwitchRoutingMode(policy-tun): %v", err)
	}
	if gotStarts, gotReloads := proc.startCalls-startsBeforePolicy, proc.reloads()-reloadsBeforePolicy; gotStarts != 1 || gotReloads != 0 {
		t.Fatalf("SwitchRoutingMode(tproxy->policy-tun) starts=%d reloads=%d, want (1, 0)", gotStarts, gotReloads)
	}
	if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatalf("expected cloud hooks and slot active immediately after tproxy->policy-tun (dir=%s)", dir)
	}
	if err := svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile in policy-tun: %v", err)
	}
	if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected Reconcile in policy-tun (IPTables.Uninstall sweep) to preserve KeenDNS Cloud hooks and slot")
	}
	if err := svc.SwitchRoutingMode(ctx, stateTProxy); err != nil {
		t.Fatalf("SwitchRoutingMode(tproxy): %v", err)
	}
	if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected cloud hooks and slot active immediately after policy-tun->tproxy")
	}

	// Simulate manual Control("stop") -> BeforeStop -> DetachCloudOutput, then Control("start") -> AfterStart -> ReconcileCloudOutput.
	if err := svc.DetachCloudOutput(ctx); err != nil {
		t.Fatalf("DetachCloudOutput: %v", err)
	}
	if tables.anyCloudState() {
		t.Fatal("expected all cloud hooks detached after DetachCloudOutput")
	}
	if err := svc.ReconcileCloudOutput(ctx); err != nil {
		t.Fatalf("ReconcileCloudOutput: %v", err)
	}
	if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected ReconcileCloudOutput to immediately re-attach cloud hooks and slot")
	}

	// UninstallCloudOutput (package removal cleanup) must strip all cloud chains and ipsets.
	tables.ipt(newFakeExec()).UninstallCloudOutput(ctx)
	if tables.anyCloudState() {
		t.Fatal("expected UninstallCloudOutput to remove all cloud chains, rules, and ipsets")
	}
	if err := svc.ReconcileCloudOutput(ctx); err != nil {
		t.Fatalf("re-attach after UninstallCloudOutput check: %v", err)
	}

	// ParkForExternalEngine -> ResumeFromExternalEngine must immediately re-attach cloud hooks and slot
	// using a single reload (skip-gate bypassed via fresh PID 4343 simulating daemon restart).
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4343)
	proc.isRunningFn = func() (bool, int) { return true, 4343 }
	reloadsBeforeResume := proc.reloads()
	if err := svc.ParkForExternalEngine(ctx); err != nil {
		t.Fatalf("ParkForExternalEngine: %v", err)
	}
	if tables.anyCloudState() || svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected cloud hooks and slot parked when external engine takes over")
	}
	if err := svc.ResumeFromExternalEngine(ctx); err != nil {
		t.Fatalf("ResumeFromExternalEngine: %v", err)
	}
	if got := proc.reloads() - reloadsBeforeResume; got != 1 {
		t.Fatalf("Park+Resume triggered %d reloads, want 1", got)
	}
	if !tables.hasCloudHooks() || !svc.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		t.Fatal("expected cloud hooks and slot immediately re-attached on ResumeFromExternalEngine")
	}
}

func TestCheckCloudOutboundSwitch_FailoverDeferredCooldownAndFailback(t *testing.T) {
	svc, _, proc, _ := newCloudOutputTestService(t, "bench-cloud-fb")
	now := time.Unix(1700000000, 0)
	setCloudOutputNow(func() time.Time { return now })
	t.Cleanup(func() { setCloudOutputNow(nil) })

	var activeErr error
	activeMember := "bench-direct-eth3"
	var resets int
	svc.deps.CloudOutboundActive = func(context.Context, string) (string, error) {
		if activeErr != nil {
			return "", activeErr
		}
		return activeMember, nil
	}
	svc.deps.ResetCloudAgent = func(context.Context) error {
		resets++
		return nil
	}

	ctx := context.Background()
	// 1. Initial observation only records baseline (zero reset).
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 0 {
		t.Fatalf("initial observation triggered reset (%d)", resets)
	}

	// 2. Failover direct -> proxy triggers 1 reset.
	activeMember = "lat-premium"
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 1 {
		t.Fatalf("failover expected 1 reset, got %d", resets)
	}

	// 3. Transient Clash error must NOT clear synced tracker.
	activeErr = errors.New("clash timeout")
	svc.CheckCloudOutboundSwitch(ctx)
	activeErr = nil
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 1 {
		t.Fatalf("transient Clash error caused spurious reset: %d", resets)
	}

	// 4. Switch inside 30s cooldown is deferred (does not reset immediately, does not lose switch).
	now = now.Add(10 * time.Second)
	activeMember = "bench-direct-eth3"
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 1 {
		t.Fatalf("switch inside 30s cooldown triggered reset early: %d", resets)
	}

	// 5. Once cooldown expires, the deferred switch fires automatically.
	now = now.Add(21 * time.Second)
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 2 {
		t.Fatalf("deferred switch after cooldown expected 2 resets, got %d", resets)
	}

	// 6. Round-trip direct -> proxy -> direct completely inside cooldown leaves synced==active -> no extra reset.
	now = now.Add(5 * time.Second)
	activeMember = "lat-premium"
	svc.CheckCloudOutboundSwitch(ctx)
	now = now.Add(5 * time.Second)
	activeMember = "bench-direct-eth3"
	svc.CheckCloudOutboundSwitch(ctx)
	now = now.Add(25 * time.Second)
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 2 {
		t.Fatalf("completed round-trip inside cooldown caused unexpected reset: %d", resets)
	}

	// 7. Disabling KeenDNS outbound clears tracker so re-enabling does not reset on first observation.
	if err := svc.deps.Settings.Update(func(s *storage.Settings) error {
		s.SingboxRouter.KeenDNSCloudOutbound = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	svc.CheckCloudOutboundSwitch(ctx)
	if err := svc.deps.Settings.Update(func(s *storage.Settings) error {
		s.SingboxRouter.KeenDNSCloudOutbound = "bench-cloud-fb"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	activeMember = "lat-premium"
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 2 {
		t.Fatalf("re-enable first observation triggered reset: %d", resets)
	}

	// While sing-box is down, switch check is a no-op.
	proc.isRunningFn = func() (bool, int) { return false, 0 }
	now = now.Add(40 * time.Second)
	activeMember = "bench-direct-eth3"
	svc.CheckCloudOutboundSwitch(ctx)
	if resets != 2 {
		t.Fatalf("stopped sing-box triggered reset: %d", resets)
	}
}

// F809: в выключенном состоянии полное снятие (дампы iptables, ip rule, ipset)
// идёт один раз, а не на каждом тике; попытка включения возвращает его.
func TestReconcileCloudOutput_DetachedSteadyStateNoExec(t *testing.T) {
	seedProcNetSockets(t, 0, cloudOutputPort, 54321, cloudOutputUDPPort, 54322, 4242)
	svc, tables, proc, _ := newCloudOutputTestService(t, "cloud-fallback")
	setCloudOutputIpsetRunner(tables.ipsetRunner)
	defer setCloudOutputIpsetRunner(nil)
	setCloudOutputDNSLookupIPv4(func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("185.10.184.178")}, nil
	})
	defer setCloudOutputDNSLookupIPv4(nil)

	var dumps int
	inner := svc.deps.IPTables.runIPTablesOut
	svc.deps.IPTables.runIPTablesOut = func(ctx context.Context, args ...string) (string, error) {
		dumps++
		return inner(ctx, args...)
	}
	ctx := context.Background()
	off := storage.SingboxRouterSettings{Enabled: true}

	if err := svc.reconcileCloudOutput(ctx, off, false); err != nil {
		t.Fatal(err)
	}
	if dumps == 0 {
		t.Fatal("first reconcile must detach (stale rules after unclean exit)")
	}
	dumps = 0
	for i := 0; i < 3; i++ {
		if err := svc.reconcileCloudOutput(ctx, off, false); err != nil {
			t.Fatal(err)
		}
	}
	if dumps != 0 {
		t.Fatalf("steady detached state ran %d iptables dumps, want 0", dumps)
	}

	on := storage.SingboxRouterSettings{Enabled: true, KeenDNSCloudOutbound: "cloud-fallback"}
	if err := svc.reconcileCloudOutput(ctx, on, false); err != nil {
		t.Fatal(err)
	}
	if !tables.hasCloudHooks() {
		t.Fatal("expected cloud hooks after attach")
	}
	if err := svc.reconcileCloudOutput(ctx, off, false); err != nil {
		t.Fatal(err)
	}
	if tables.anyCloudState() {
		t.Fatal("detach after attach must strip cloud state")
	}

	// Настроено, но sing-box не запущен: тот же один раз, не каждый тик.
	if err := svc.reconcileCloudOutput(ctx, on, false); err != nil {
		t.Fatal(err)
	}
	proc.isRunningFn = func() (bool, int) { return false, 0 }
	if err := svc.reconcileCloudOutput(ctx, on, false); err != nil {
		t.Fatal(err)
	}
	if tables.anyCloudState() {
		t.Fatal("stopped sing-box must strip cloud state")
	}
	dumps = 0
	for i := 0; i < 3; i++ {
		if err := svc.reconcileCloudOutput(ctx, on, false); err != nil {
			t.Fatal(err)
		}
	}
	if dumps != 0 {
		t.Fatalf("stopped sing-box ran %d iptables dumps per steady ticks, want 0", dumps)
	}
}
