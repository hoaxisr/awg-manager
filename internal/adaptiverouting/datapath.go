package adaptiverouting

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const (
	ChainSusanin      = "SUSANIN"
	ChainSusaninDNS   = "SUSANIN-DNS"
	DefaultTableID    = 105
	DefaultFwmarkMask = "0x30000000"
	DefaultFwmarkTest = "0x10000000"
	DefaultFwmarkOk   = "0x20000000"
	DefaultPriOk      = 95
	DefaultPriTest    = 96
	NetfilterHookPath = "/opt/etc/ndm/netfilter.d/63-awgm-susanin.sh"

	SetOkTcp   = "susanin_ok_tcp"
	SetOkUdp   = "susanin_ok_udp"
	SetTestTcp = "susanin_test_tcp"
	SetTestUdp = "susanin_test_udp"
	SetOkNet   = "susanin_ok_net"
	SetNever   = "susanin_never"
)

// CmdRunner executes an arbitrary system command and returns stdout, stderr, or error.
type CmdRunner func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error)

type DatapathStats struct {
	OkTcpCount   int
	OkUdpCount   int
	TestTcpCount int
	TestUdpCount int
	OkNetCount   int
	NeverCount   int
	OkPackets    uint64
	TestPackets  uint64
	FailOpen     bool
}

type PolicyMarkResolver interface {
	Get(ctx context.Context, policyName string) (string, error)
}

type DatapathController struct {
	runner CmdRunner
	mu     sync.Mutex

	// Cached binary paths
	iptablesBin string
	ipsetBin    string
	ipBin       string

	policyResolver   PolicyMarkResolver
	failOpenActive   bool
	failClosedActive bool
}

func NewDatapathController(customRunner CmdRunner) *DatapathController {
	c := &DatapathController{
		runner:      customRunner,
		iptablesBin: sysiptables.Binary,
		ipsetBin:    "/opt/sbin/ipset",
		ipBin:       "/opt/sbin/ip",
	}
	if c.runner == nil {
		c.runner = func(ctx context.Context, bin string, args ...string) (*sysexec.Result, error) {
			return sysexec.Run(ctx, bin, args...)
		}
	}
	return c
}

func (d *DatapathController) SetBinaryPaths(iptables, ipset, ip string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if iptables != "" {
		d.iptablesBin = iptables
	}
	if ipset != "" {
		d.ipsetBin = ipset
	}
	if ip != "" {
		d.ipBin = ip
	}
}

func (d *DatapathController) SetPolicyMarkResolver(r PolicyMarkResolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.policyResolver = r
}

// EnsureSets ensures all 6 ipset tables exist.
func (d *DatapathController) EnsureSets(ctx context.Context) error {
	sets := []struct {
		name    string
		setType string
	}{
		{SetOkTcp, "hash:ip"},
		{SetOkUdp, "hash:ip"},
		{SetTestTcp, "hash:ip"},
		{SetTestUdp, "hash:ip"},
		{SetOkNet, "hash:net"},
		{SetNever, "hash:net"},
	}

	for _, s := range sets {
		// ipset create <name> <type> -exist
		res, err := d.runner(ctx, d.ipsetBin, "create", s.name, s.setType, "-exist")
		if err != nil {
			// If set already exists (e.g. created by datapath.sh with different options), ignore error
			if res != nil && (strings.Contains(res.Stderr, "already exists") || res.ExitCode == 1) {
				if checkRes, _ := d.runner(ctx, d.ipsetBin, "list", s.name, "-name"); checkRes != nil && checkRes.ExitCode == 0 {
					continue
				}
			}
			return fmt.Errorf("create ipset %s (%s): %w", s.name, s.setType, sysexec.FormatError(res, err))
		}
	}

	return nil
}

// EnsureChain provisions the AWGM-SUSANIN chain in mangle PREROUTING and bypass hooks.
func (d *DatapathController) EnsureChain(
	ctx context.Context,
	settings Settings,
	lanInterfaces []string,
	routerIPs []string,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	mask := settings.FwmarkMask
	if mask == "" {
		mask = DefaultFwmarkMask
	}
	markOk := settings.FwmarkOk
	if markOk == "" {
		markOk = DefaultFwmarkOk
	}
	markTest := settings.FwmarkTest
	if markTest == "" {
		markTest = DefaultFwmarkTest
	}

	// 1. Create chain if not exists: iptables -t mangle -N AWGM-SUSANIN
	_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-N", ChainSusanin)

	// 2. Flush chain rules: iptables -t mangle -F AWGM-SUSANIN
	res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-F", ChainSusanin)
	if err != nil {
		return fmt.Errorf("flush %s: %w", ChainSusanin, sysexec.FormatError(res, err))
	}

	// 2.1 Configure DNS rules in ChainSusanin if DNS is enabled
	if settings.DNS.Enabled && len(settings.DNS.Servers) > 0 {
		if settings.DNS.RouteViaTunnel {
			if settings.DNS.InterceptPort53 {
				// Intercepted DNS queries (e.g. sent by LAN clients to router IP 192.168.x.1:53 before DNAT)
				// must be marked for tunnel routing so the subsequent routing decision directs them to table 105.
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "--dport", "53", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask))
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "--dport", "53", "-j", "CONNMARK", "--save-mark", "--mask", mask)
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "--dport", "53", "-j", "ACCEPT")

				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "--dport", "53", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask))
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "--dport", "53", "-j", "CONNMARK", "--save-mark", "--mask", mask)
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "--dport", "53", "-j", "ACCEPT")
			}
			for _, s := range settings.DNS.Servers {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				// Also mark direct traffic to upstream server to route through table 105
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "-d", s, "--dport", "53", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask))
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "-d", s, "--dport", "53", "-j", "CONNMARK", "--save-mark", "--mask", mask)
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "-d", s, "--dport", "53", "-j", "ACCEPT")

				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "-d", s, "--dport", "53", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask))
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "-d", s, "--dport", "53", "-j", "CONNMARK", "--save-mark", "--mask", mask)
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "-d", s, "--dport", "53", "-j", "ACCEPT")

				// Router-local DNS queries (e.g. susanin-agent vpn_always resolver) to upstream DNS
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", "OUTPUT", "-p", "udp", "-d", s, "--dport", "53", "-m", "comment", "--comment", "SUSANIN-DNS-OUT", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask))
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", "OUTPUT", "-p", "tcp", "-d", s, "--dport", "53", "-m", "comment", "--comment", "SUSANIN-DNS-OUT", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask))
			}
		} else {
			for _, s := range settings.DNS.Servers {
				s = strings.TrimSpace(s)
				if s == "" {
					continue
				}
				// Explicitly RETURN DNS traffic to upstream server so it goes DIRECT via WAN
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "-d", s, "--dport", "53", "-j", "RETURN")
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "-d", s, "--dport", "53", "-j", "RETURN")
			}
			if settings.DNS.InterceptPort53 {
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "udp", "--dport", "53", "-j", "RETURN")
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-A", ChainSusanin, "-p", "tcp", "--dport", "53", "-j", "RETURN")
			}
		}
	}

	// 3. Populate bypass rules
	bypasses := [][]string{}
	var policyMarkWithMask string
	if settings.Source.Type == "policy" {
		if settings.Source.PolicyID == "" {
			return errors.New("policy traffic source requires policyId")
		}
		if d.policyResolver == nil {
			return fmt.Errorf("policy traffic source %q cannot be resolved: resolver is not configured", settings.Source.PolicyID)
		}
		mark, err := d.policyResolver.Get(ctx, settings.Source.PolicyID)
		if err != nil {
			return fmt.Errorf("resolve policy traffic source %q: %w", settings.Source.PolicyID, err)
		}
		if strings.TrimSpace(mark) == "" {
			return fmt.Errorf("policy traffic source %q has no firewall mark", settings.Source.PolicyID)
		}
		policyMarkWithMask = fmt.Sprintf("%s/0x0fffffff", strings.TrimSpace(mark))
		// Fail-closed guard: if packet mark does not match the policy mark, return immediately
		bypasses = append(bypasses, []string{"-m", "mark", "!", "--mark", policyMarkWithMask, "-j", "RETURN"})
	} else {
		// When not scoped to a specific policy, do NOT steal traffic from NDMS policies (0xffffaa0/0xffffff0)
		bypasses = append(bypasses, []string{"-m", "mark", "--mark", "0xffffaa0/0xffffff0", "-j", "RETURN"})
	}

	bypasses = append(bypasses, [][]string{
		{"-d", "127.0.0.0/8", "-j", "RETURN"},
		{"-d", "224.0.0.0/4", "-j", "RETURN"},
		{"-d", "255.255.255.255/32", "-j", "RETURN"},
		{"-d", "192.168.0.0/16", "-j", "RETURN"},
		{"-d", "10.0.0.0/8", "-j", "RETURN"},
		{"-d", "172.16.0.0/12", "-j", "RETURN"},
		{"-m", "set", "--match-set", SetNever, "dst", "-j", "RETURN"},
	}...)

	// Bypass router ports (DNS, Web UI, Clash API, TProxy ports)
	for _, rip := range routerIPs {
		if rip == "" {
			continue
		}
		bypasses = append(bypasses, []string{"-d", rip, "-j", "RETURN"})
	}

	for _, rule := range bypasses {
		args := append([]string{"-w", "-t", "mangle", "-A", ChainSusanin}, rule...)
		if r, err := d.runner(ctx, d.iptablesBin, args...); err != nil {
			return fmt.Errorf("add bypass rule %v: %w", rule, sysexec.FormatError(r, err))
		}
	}

	// 4. Marking rules for OK targets (TCP, UDP, CIDRs)
	okSpecs := [][]string{
		{"-p", "tcp", "-m", "set", "--match-set", SetOkTcp, "dst", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask)},
		{"-p", "tcp", "-m", "set", "--match-set", SetOkTcp, "dst", "-j", "CONNMARK", "--save-mark", "--mask", mask},
		{"-p", "tcp", "-m", "set", "--match-set", SetOkTcp, "dst", "-j", "ACCEPT"},

		{"-p", "udp", "-m", "set", "--match-set", SetOkUdp, "dst", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask)},
		{"-p", "udp", "-m", "set", "--match-set", SetOkUdp, "dst", "-j", "CONNMARK", "--save-mark", "--mask", mask},
		{"-p", "udp", "-m", "set", "--match-set", SetOkUdp, "dst", "-j", "ACCEPT"},

		{"-m", "set", "--match-set", SetOkNet, "dst", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markOk, mask)},
		{"-m", "set", "--match-set", SetOkNet, "dst", "-j", "CONNMARK", "--save-mark", "--mask", mask},
		{"-m", "set", "--match-set", SetOkNet, "dst", "-j", "ACCEPT"},
	}
	for _, rule := range okSpecs {
		args := append([]string{"-w", "-t", "mangle", "-A", ChainSusanin}, rule...)
		if r, err := d.runner(ctx, d.iptablesBin, args...); err != nil {
			return fmt.Errorf("add ok mark rule: %w", sysexec.FormatError(r, err))
		}
	}

	// 5. Marking rules for TEST targets (TCP, UDP)
	testSpecs := [][]string{
		{"-p", "tcp", "-m", "set", "--match-set", SetTestTcp, "dst", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markTest, mask)},
		{"-p", "tcp", "-m", "set", "--match-set", SetTestTcp, "dst", "-j", "CONNMARK", "--save-mark", "--mask", mask},
		{"-p", "tcp", "-m", "set", "--match-set", SetTestTcp, "dst", "-j", "ACCEPT"},

		{"-p", "udp", "-m", "set", "--match-set", SetTestUdp, "dst", "-j", "MARK", "--set-xmark", fmt.Sprintf("%s/%s", markTest, mask)},
		{"-p", "udp", "-m", "set", "--match-set", SetTestUdp, "dst", "-j", "CONNMARK", "--save-mark", "--mask", mask},
		{"-p", "udp", "-m", "set", "--match-set", SetTestUdp, "dst", "-j", "ACCEPT"},
	}
	for _, rule := range testSpecs {
		args := append([]string{"-w", "-t", "mangle", "-A", ChainSusanin}, rule...)
		if r, err := d.runner(ctx, d.iptablesBin, args...); err != nil {
			return fmt.Errorf("add test mark rule: %w", sysexec.FormatError(r, err))
		}
	}

	// 6. Ensure PREROUTING jump based on SourceScope
	// First remove any existing jumps to avoid duplication
	d.removeJumpsLocked(ctx, lanInterfaces)

	sourceInterfaces := lanInterfaces
	switch settings.Source.Type {
	case "", "all_lan":
		if len(sourceInterfaces) == 0 {
			sourceInterfaces = []string{"br0"}
		}
	case "policy":
		args := []string{"-w", "-t", "mangle", "-A", "PREROUTING", "-m", "mark", "--mark", policyMarkWithMask, "-j", ChainSusanin}
		if r, err := d.runner(ctx, d.iptablesBin, args...); err != nil {
			return fmt.Errorf("add PREROUTING policy jump for %s (%s): %w", settings.Source.PolicyID, policyMarkWithMask, sysexec.FormatError(r, err))
		}
	case "interfaces":
		sourceInterfaces = settings.Source.Interfaces
		if len(sourceInterfaces) == 0 {
			return errors.New("interfaces traffic source requires at least one interface")
		}
	case "server_tunnel":
		if strings.TrimSpace(settings.Source.TunnelTag) == "" {
			return errors.New("server_tunnel traffic source requires tunnelTag")
		}
		sourceInterfaces = []string{settings.Source.TunnelTag}
	default:
		return fmt.Errorf("unsupported traffic source type %q", settings.Source.Type)
	}

	if settings.Source.Type != "policy" {
		for _, iface := range sourceInterfaces {
			if iface == "" {
				continue
			}
			// iptables -t mangle -A PREROUTING -i <iface> -j AWGM-SUSANIN
			args := []string{"-w", "-t", "mangle", "-A", "PREROUTING", "-i", iface, "-j", ChainSusanin}
			if r, err := d.runner(ctx, d.iptablesBin, args...); err != nil {
				return fmt.Errorf("add PREROUTING jump for %s: %w", iface, sysexec.FormatError(r, err))
			}
		}
	}

	// 7. Manage DNS Interception (DNAT port 53) in nat table
	d.cleanDnsNatLocked(ctx, lanInterfaces)
	if settings.DNS.Enabled && settings.DNS.InterceptPort53 && len(settings.DNS.Servers) > 0 {
		primaryDNS := strings.TrimSpace(settings.DNS.Servers[0])
		if primaryDNS != "" {
			_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-N", ChainSusaninDNS)
			_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-F", ChainSusaninDNS)
			_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-A", ChainSusaninDNS, "-p", "udp", "--dport", "53", "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:53", primaryDNS))
			_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-A", ChainSusaninDNS, "-p", "tcp", "--dport", "53", "-j", "DNAT", "--to-destination", fmt.Sprintf("%s:53", primaryDNS))

			if settings.Source.Type == "policy" {
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-I", "PREROUTING", "1", "-m", "mark", "--mark", policyMarkWithMask, "-p", "udp", "--dport", "53", "-j", ChainSusaninDNS)
				_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-I", "PREROUTING", "2", "-m", "mark", "--mark", policyMarkWithMask, "-p", "tcp", "--dport", "53", "-j", ChainSusaninDNS)
			} else {
				idx := 1
				for _, iface := range sourceInterfaces {
					if iface != "" {
						_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-I", "PREROUTING", strconv.Itoa(idx), "-i", iface, "-p", "udp", "--dport", "53", "-j", ChainSusaninDNS)
						idx++
						_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-I", "PREROUTING", strconv.Itoa(idx), "-i", iface, "-p", "tcp", "--dport", "53", "-j", ChainSusaninDNS)
						idx++
					}
				}
			}
		}
	}

	return nil
}

// EnsureRules configures ip rule 95, 96 and routing table 105.
func (d *DatapathController) EnsureRules(ctx context.Context, settings Settings, egressDev string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	table := settings.RoutingTableID
	if table <= 0 {
		table = DefaultTableID
	}
	priOk := settings.RulePriorityOk
	if priOk <= 0 {
		priOk = DefaultPriOk
	}
	priTest := settings.RulePriorityTest
	if priTest <= 0 {
		priTest = DefaultPriTest
	}
	mask := settings.FwmarkMask
	if mask == "" {
		mask = DefaultFwmarkMask
	}
	markOk := settings.FwmarkOk
	if markOk == "" {
		markOk = DefaultFwmarkOk
	}
	markTest := settings.FwmarkTest
	if markTest == "" {
		markTest = DefaultFwmarkTest
	}

	// 1. Drain existing rules for table
	d.drainRulesLocked(ctx, table, priOk, priTest)

	// 2. Add ip rule for OK: ip rule add pref 95 fwmark 0x20000000/0x30000000 table 105
	res, err := d.runner(ctx, d.ipBin, "rule", "add", "pref", strconv.Itoa(priOk),
		"fwmark", fmt.Sprintf("%s/%s", markOk, mask), "table", strconv.Itoa(table))
	if err != nil && !strings.Contains(err.Error(), "File exists") {
		return fmt.Errorf("add ip rule ok (pri %d): %w", priOk, sysexec.FormatError(res, err))
	}

	// 3. Add ip rule for TEST: ip rule add pref 96 fwmark 0x10000000/0x30000000 table 105
	res, err = d.runner(ctx, d.ipBin, "rule", "add", "pref", strconv.Itoa(priTest),
		"fwmark", fmt.Sprintf("%s/%s", markTest, mask), "table", strconv.Itoa(table))
	if err != nil && !strings.Contains(err.Error(), "File exists") {
		return fmt.Errorf("add ip rule test (pri %d): %w", priTest, sysexec.FormatError(res, err))
	}

	// 4. Configure default route in table: ip route replace default dev <egressDev> table 105
	if egressDev != "" {
		realDev := resolveKernelDev(egressDev)
		res, err = d.runner(ctx, d.ipBin, "route", "replace", "default", "dev", realDev, "table", strconv.Itoa(table))
		if err != nil {
			return fmt.Errorf("replace default route dev %s table %d: %w", realDev, table, sysexec.FormatError(res, err))
		}

		// 5. Ensure FORWARD, INPUT, TCPMSS, and MASQUERADE for egressDev
		d.cleanEgressFirewallLocked(ctx, realDev)
		_, _ = d.runner(ctx, d.iptablesBin, "-w", "-I", "FORWARD", "1", "-i", realDev, "-j", "ACCEPT")
		_, _ = d.runner(ctx, d.iptablesBin, "-w", "-I", "FORWARD", "1", "-o", realDev, "-j", "ACCEPT")
		_, _ = d.runner(ctx, d.iptablesBin, "-w", "-I", "INPUT", "1", "-i", realDev, "-j", "ACCEPT")
		_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-I", "FORWARD", "1", "-o", realDev,
			"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu")
		_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-I", "POSTROUTING", "1", "-o", realDev, "-j", "MASQUERADE")

		d.ensureNetfilterHook(realDev)
	}

	d.failOpenActive = false
	d.failClosedActive = false
	return nil
}

// SetFailOpen enables or disables fail-open mode. When enabled, IP rules 95 & 96 are dropped so traffic flows DIRECT.
func (d *DatapathController) SetFailOpen(ctx context.Context, failOpen bool, settings Settings) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	table := settings.RoutingTableID
	if table <= 0 {
		table = DefaultTableID
	}
	priOk := settings.RulePriorityOk
	if priOk <= 0 {
		priOk = DefaultPriOk
	}
	priTest := settings.RulePriorityTest
	if priTest <= 0 {
		priTest = DefaultPriTest
	}

	if failOpen {
		if !d.failOpenActive {
			d.drainRulesLocked(ctx, table, priOk, priTest)
			d.failOpenActive = true
			d.failClosedActive = false
		}
		return nil
	}

	// Restore
	if d.failOpenActive {
		mask := settings.FwmarkMask
		if mask == "" {
			mask = DefaultFwmarkMask
		}
		markOk := settings.FwmarkOk
		if markOk == "" {
			markOk = DefaultFwmarkOk
		}
		markTest := settings.FwmarkTest
		if markTest == "" {
			markTest = DefaultFwmarkTest
		}

		_, _ = d.runner(ctx, d.ipBin, "rule", "add", "pref", strconv.Itoa(priOk),
			"fwmark", fmt.Sprintf("%s/%s", markOk, mask), "table", strconv.Itoa(table))
		_, _ = d.runner(ctx, d.ipBin, "rule", "add", "pref", strconv.Itoa(priTest),
			"fwmark", fmt.Sprintf("%s/%s", markTest, mask), "table", strconv.Itoa(table))
		d.failOpenActive = false
	}
	return nil
}

// SetFailClosed enables or disables kill-switch mode. The policy rules stay
// installed, but their routing table points to a blackhole route so marked
// traffic cannot leak to the router's main table while the selected egress is
// unavailable. Disabling this mode only clears the marker; EnsureRules must be
// called with the live egress interface to restore the actual default route.
func (d *DatapathController) SetFailClosed(ctx context.Context, failClosed bool, settings Settings) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	table := settings.RoutingTableID
	if table <= 0 {
		table = DefaultTableID
	}
	priOk := settings.RulePriorityOk
	if priOk <= 0 {
		priOk = DefaultPriOk
	}
	priTest := settings.RulePriorityTest
	if priTest <= 0 {
		priTest = DefaultPriTest
	}
	mask := settings.FwmarkMask
	if mask == "" {
		mask = DefaultFwmarkMask
	}
	markOk := settings.FwmarkOk
	if markOk == "" {
		markOk = DefaultFwmarkOk
	}
	markTest := settings.FwmarkTest
	if markTest == "" {
		markTest = DefaultFwmarkTest
	}

	if !failClosed {
		d.failClosedActive = false
		return nil
	}

	// A previous fail-open state removed the rules; restore them before
	// installing the blackhole route.
	if d.failOpenActive {
		d.drainRulesLocked(ctx, table, priOk, priTest)
		if res, err := d.runner(ctx, d.ipBin, "rule", "add", "pref", strconv.Itoa(priOk),
			"fwmark", fmt.Sprintf("%s/%s", markOk, mask), "table", strconv.Itoa(table)); err != nil && !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("restore kill-switch ip rule ok: %w", sysexec.FormatError(res, err))
		}
		if res, err := d.runner(ctx, d.ipBin, "rule", "add", "pref", strconv.Itoa(priTest),
			"fwmark", fmt.Sprintf("%s/%s", markTest, mask), "table", strconv.Itoa(table)); err != nil && !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("restore kill-switch ip rule test: %w", sysexec.FormatError(res, err))
		}
		d.failOpenActive = false
	}

	if !d.failClosedActive {
		res, err := d.runner(ctx, d.ipBin, "route", "replace", "blackhole", "default", "table", strconv.Itoa(table))
		if err != nil {
			return fmt.Errorf("install kill-switch blackhole route table %d: %w", table, sysexec.FormatError(res, err))
		}
		d.failClosedActive = true
	}
	return nil
}

// Teardown cleanly removes the chain, jumps, and ip rules.
func (d *DatapathController) Teardown(ctx context.Context, settings Settings, lanInterfaces []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	table := settings.RoutingTableID
	if table <= 0 {
		table = DefaultTableID
	}
	priOk := settings.RulePriorityOk
	if priOk <= 0 {
		priOk = DefaultPriOk
	}
	priTest := settings.RulePriorityTest
	if priTest <= 0 {
		priTest = DefaultPriTest
	}

	// 1. Drain IP rules
	d.drainRulesLocked(ctx, table, priOk, priTest)

	// 2. Flush table route: ip route flush table <tableID>
	_, _ = d.runner(ctx, d.ipBin, "route", "flush", "table", strconv.Itoa(table))

	// 3. Remove PREROUTING jumps
	d.removeJumpsLocked(ctx, lanInterfaces)

	// 4. Flush and delete chain: iptables -t mangle -F SUSANIN; iptables -t mangle -X SUSANIN
	_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-F", ChainSusanin)
	_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-X", ChainSusanin)

	// 5. Clean egress firewall rules and remove netfilter hook
	d.cleanEgressFirewallLocked(ctx, "awgsus0")
	_ = os.Remove(NetfilterHookPath)

	// 6. Flush conntrack marks for Susanin
	markOk := settings.FwmarkOk
	if markOk == "" {
		markOk = DefaultFwmarkOk
	}
	markTest := settings.FwmarkTest
	if markTest == "" {
		markTest = DefaultFwmarkTest
	}
	_, _ = d.runner(ctx, "/opt/sbin/conntrack", "-D", "-m", markOk)
	_, _ = d.runner(ctx, "/opt/sbin/conntrack", "-D", "-m", markTest)

	d.failOpenActive = false
	return nil
}

// FlushSets flushes the test and ok ipsets.
func (d *DatapathController) FlushSets(ctx context.Context) error {
	for _, setName := range []string{SetOkTcp, SetOkUdp, SetTestTcp, SetTestUdp, SetOkNet} {
		res, err := d.runner(ctx, d.ipsetBin, "flush", setName)
		if err != nil && !strings.Contains(err.Error(), "does not exist") {
			return fmt.Errorf("flush ipset %s: %w", setName, sysexec.FormatError(res, err))
		}
	}
	return nil
}

// AddLearned adds an IP to the ok or test ipset.
func (d *DatapathController) AddLearned(ctx context.Context, ip string, proto string, kind string) error {
	targetSet := SetOkTcp
	if strings.ToLower(proto) == "udp" {
		if kind == "test" {
			targetSet = SetTestUdp
		} else {
			targetSet = SetOkUdp
		}
	} else {
		if kind == "test" {
			targetSet = SetTestTcp
		} else {
			targetSet = SetOkTcp
		}
	}

	res, err := d.runner(ctx, d.ipsetBin, "add", targetSet, ip, "-exist")
	if err != nil {
		return fmt.Errorf("ipset add %s %s: %w", targetSet, ip, sysexec.FormatError(res, err))
	}
	return nil
}

// RemoveLearned removes an IP from test and ok sets.
func (d *DatapathController) RemoveLearned(ctx context.Context, ip string, proto string) error {
	sets := []string{SetOkTcp, SetTestTcp}
	if strings.ToLower(proto) == "udp" {
		sets = []string{SetOkUdp, SetTestUdp}
	} else if proto == "all" || proto == "" {
		sets = []string{SetOkTcp, SetOkUdp, SetTestTcp, SetTestUdp}
	}

	for _, s := range sets {
		_, _ = d.runner(ctx, d.ipsetBin, "del", s, ip)
	}
	return nil
}

// GetStats parses counts from ipsets and packet counts from iptables.
func (d *DatapathController) GetStats(ctx context.Context) (DatapathStats, error) {
	stats := DatapathStats{
		FailOpen: d.failOpenActive,
	}

	sets := map[string]*int{
		SetOkTcp:   &stats.OkTcpCount,
		SetOkUdp:   &stats.OkUdpCount,
		SetTestTcp: &stats.TestTcpCount,
		SetTestUdp: &stats.TestUdpCount,
		SetOkNet:   &stats.OkNetCount,
		SetNever:   &stats.NeverCount,
	}

	for name, target := range sets {
		// Keenetic's ipset build omits "Number of entries" in terse output.
		// Use the regular listing: it is supported by both the Entware build on
		// the router and upstream ipset, and is the only portable source for the
		// counters shown in the Susanin UI.
		res, err := d.runner(ctx, d.ipsetBin, "list", name)
		if err == nil && res != nil {
			members := false
			memberCount := 0
			countFound := false
			for _, line := range strings.Split(res.Stdout, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "Number of entries:") {
					parts := strings.Split(line, ":")
					if len(parts) == 2 {
						if n, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
							*target = n
							countFound = true
						}
					}
					continue
				}
				// Several Keenetic ipset builds omit "Number of entries" even
				// from the regular listing.  In that format every non-empty line
				// following the Members: header is one set member.
				if line == "Members:" {
					members = true
					continue
				}
				if members && line != "" {
					memberCount++
				}
			}
			if !countFound {
				*target = memberCount
			}
		}
	}

	return stats, nil
}

// GetLearnedEntries returns the active members of learned and configured ipsets.
func (d *DatapathController) GetLearnedEntries(ctx context.Context) (map[string][]string, error) {
	result := make(map[string][]string)
	sets := []string{SetOkTcp, SetOkUdp, SetTestTcp, SetTestUdp, SetOkNet, SetNever}

	for _, name := range sets {
		result[name] = []string{}
		res, err := d.runner(ctx, d.ipsetBin, "list", name)
		if err != nil || res == nil || res.ExitCode != 0 {
			continue
		}

		members := false
		var entries []string
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line == "Members:" {
				members = true
				continue
			}
			if members && line != "" {
				parts := strings.Fields(line)
				if len(parts) > 0 {
					entries = append(entries, parts[0])
				}
			}
		}
		result[name] = entries
	}

	return result, nil
}

func (d *DatapathController) drainRulesLocked(ctx context.Context, table, priOk, priTest int) {
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.ipBin, "rule", "del", "pref", strconv.Itoa(priOk))
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.ipBin, "rule", "del", "pref", strconv.Itoa(priTest))
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.ipBin, "rule", "del", "table", strconv.Itoa(table))
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
}

func (d *DatapathController) removeJumpsLocked(ctx context.Context, lanInterfaces []string) {
	// First scan PREROUTING and delete any jump pointing to ChainSusanin
	if res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-S", "PREROUTING"); err == nil && res != nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if (strings.Contains(line, "-j "+ChainSusanin) || strings.Contains(line, "-g "+ChainSusanin)) && strings.HasPrefix(line, "-A PREROUTING") {
				fields := strings.Fields(line)
				if len(fields) > 2 {
					delArgs := append([]string{"-w", "-t", "mangle", "-D", "PREROUTING"}, fields[2:]...)
					_, _ = d.runner(ctx, d.iptablesBin, delArgs...)
				}
			}
		}
	}

	// Clean any Susanin OUTPUT rules in mangle
	if res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-S", "OUTPUT"); err == nil && res != nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "SUSANIN-DNS-OUT") && strings.HasPrefix(line, "-A OUTPUT") {
				fields := strings.Fields(line)
				if len(fields) > 2 {
					delArgs := append([]string{"-w", "-t", "mangle", "-D", "OUTPUT"}, fields[2:]...)
					_, _ = d.runner(ctx, d.iptablesBin, delArgs...)
				}
			}
		}
	}

	if len(lanInterfaces) == 0 {
		lanInterfaces = []string{"br0", "br1"}
	}
	for _, iface := range lanInterfaces {
		for i := 0; i < 5; i++ {
			res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-D", "PREROUTING", "-i", iface, "-j", ChainSusanin)
			if err != nil || (res != nil && res.ExitCode != 0) {
				break
			}
		}
	}

	d.cleanDnsNatLocked(ctx, lanInterfaces)
}

func (d *DatapathController) cleanDnsNatLocked(ctx context.Context, lanInterfaces []string) {
	if res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-S", "PREROUTING"); err == nil && res != nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "-j "+ChainSusaninDNS) && strings.HasPrefix(line, "-A PREROUTING") {
				fields := strings.Fields(line)
				if len(fields) > 2 {
					delArgs := append([]string{"-w", "-t", "nat", "-D", "PREROUTING"}, fields[2:]...)
					_, _ = d.runner(ctx, d.iptablesBin, delArgs...)
				}
			}
		}
	}
	_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-F", ChainSusaninDNS)
	_, _ = d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-X", ChainSusaninDNS)
}

// ReconcileDatapath verifies that ChainSusanin jump and route 105 are in place and restores them if missing.
func (d *DatapathController) ReconcileDatapath(
	ctx context.Context,
	settings Settings,
	lanInterfaces []string,
	routerIPs []string,
	egressDev string,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Check if jump exists in PREROUTING
	jumpMissing := true
	if res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-S", "PREROUTING"); err == nil && res != nil {
		for _, line := range strings.Split(res.Stdout, "\n") {
			if (strings.Contains(line, "-j "+ChainSusanin) || strings.Contains(line, "-g "+ChainSusanin)) && strings.HasPrefix(line, "-A PREROUTING") {
				if settings.Source.Type == "policy" && !strings.Contains(line, "--mark") {
					continue
				}
				jumpMissing = false
				break
			}
		}
	}

	if jumpMissing {
		d.mu.Unlock()
		err := d.EnsureChain(ctx, settings, lanInterfaces, routerIPs)
		d.mu.Lock()
		if err != nil {
			return fmt.Errorf("reconcile susanin chain: %w", err)
		}
	}

	// Check if route exists in table
	table := settings.RoutingTableID
	if table <= 0 {
		table = DefaultTableID
	}
	if egressDev != "" {
		routeMissing := true
		if res, err := d.runner(ctx, d.ipBin, "route", "show", "table", strconv.Itoa(table)); err == nil && res != nil {
			if strings.Contains(res.Stdout, "default") && strings.Contains(res.Stdout, egressDev) {
				routeMissing = false
			}
		}
		if routeMissing {
			d.mu.Unlock()
			err := d.EnsureRules(ctx, settings, egressDev)
			d.mu.Lock()
			if err != nil {
				return fmt.Errorf("reconcile susanin rules: %w", err)
			}
		}
	}

	return nil
}

func resolveKernelDev(dev string) string {
	if dev == "" {
		return ""
	}
	if strings.HasPrefix(dev, "Wireguard") {
		return "nwg" + strings.TrimPrefix(dev, "Wireguard")
	}
	if strings.HasPrefix(dev, "OpkgTun") {
		return strings.ToLower(dev)
	}
	return dev
}

func (d *DatapathController) cleanEgressFirewallLocked(ctx context.Context, iface string) {
	if iface == "" {
		return
	}
	iface = resolveKernelDev(iface)
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.iptablesBin, "-w", "-D", "FORWARD", "-i", iface, "-j", "ACCEPT")
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.iptablesBin, "-w", "-D", "FORWARD", "-o", iface, "-j", "ACCEPT")
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.iptablesBin, "-w", "-D", "INPUT", "-i", iface, "-j", "ACCEPT")
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "mangle", "-D", "FORWARD", "-o", iface,
			"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu")
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
	for i := 0; i < 5; i++ {
		res, err := d.runner(ctx, d.iptablesBin, "-w", "-t", "nat", "-D", "POSTROUTING", "-o", iface, "-j", "MASQUERADE")
		if err != nil || (res != nil && res.ExitCode != 0) {
			break
		}
	}
}

func (d *DatapathController) ensureNetfilterHook(iface string) {
	if iface == "" {
		return
	}
	hookDir := filepath.Dir(NetfilterHookPath)
	if fi, err := os.Stat(hookDir); err == nil && fi.IsDir() {
		script := fmt.Sprintf(`#!/bin/sh
[ "${type:-}" = "ip6tables" ] && exit 0
IPT=$(which iptables 2>/dev/null || echo "/opt/sbin/iptables")
[ -x "$IPT" ] || exit 0
IFACE="%s"
if [ -d "/sys/class/net/$IFACE" ]; then
    $IPT -w -D FORWARD -i "$IFACE" -j ACCEPT 2>/dev/null || true
    $IPT -w -D FORWARD -o "$IFACE" -j ACCEPT 2>/dev/null || true
    $IPT -w -I FORWARD 1 -i "$IFACE" -j ACCEPT
    $IPT -w -I FORWARD 1 -o "$IFACE" -j ACCEPT
    $IPT -w -D INPUT -i "$IFACE" -j ACCEPT 2>/dev/null || true
    $IPT -w -I INPUT 1 -i "$IFACE" -j ACCEPT
    $IPT -w -t mangle -C FORWARD -o "$IFACE" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null || \
        $IPT -w -t mangle -I FORWARD 1 -o "$IFACE" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
    $IPT -w -t nat -C POSTROUTING -o "$IFACE" -j MASQUERADE 2>/dev/null || \
        $IPT -w -t nat -I POSTROUTING 1 -o "$IFACE" -j MASQUERADE
fi
`, iface)
		_ = os.WriteFile(NetfilterHookPath, []byte(script), 0755)
	}
}
