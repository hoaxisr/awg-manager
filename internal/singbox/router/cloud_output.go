package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/cloudagent"
)

var errKeenDNSCloudUnchanged = errors.New("keendns cloud outbound unchanged")

var (
	cloudOutputReloadWait          = 10 * time.Second
	cloudOutputReloadPoll          = 100 * time.Millisecond
	netfilterCloudOutputHookPath   = "/opt/etc/ndm/netfilter.d/53-awgm-keendns-cloud.sh"
	netfilterCloudOutputHookCustom bool
)

const (
	cloudOutputInbound        = orchestrator.KeenDNSCloudInboundTag
	cloudOutputUDPInbound     = orchestrator.KeenDNSCloudUDPInboundTag
	cloudOutputChain          = "AWGM-KEENDNS-OUT"
	cloudOutputMangleChain    = "AWGM-KEENDNS-OUT-M"
	cloudOutputPreChain       = "AWGM-KEENDNS-PRE"
	cloudOutputPort           = 51390
	cloudOutputUDPPort        = 51391
	cloudOutputMark           = 0x10000000
	cloudOutputUDPMark        = 0x20000000
	cloudOutputRoutingTable   = 101
	cloudOutputIPRulePrio     = 1090
	cloudOutputIpsetName      = "AWGM-SB-CLOUD"
	cloudOutputStagingSet     = "AWGM-SB-CLOUD-TMP"
	cloudOutputIpsetMaxElem   = 4096
	cloudOutputStaticTCPPorts = "9,3478,3479,4044,5683,5684,5685,8082,8090"
	cloudOutputStaticUDPPorts = "9,3478,3479,4044,5683,5684,5685"

	cloudIpsetSyncInterval  = 60 * time.Second
	cloudIpsetRetryInterval = 15 * time.Second
	cloudIpsetDNSTimeout    = 3 * time.Second
	cloudIpsetCmdTimeout    = 5 * time.Second
	cloudIpsetDNSWorkers    = 4
	cloudIpsetStaleTTL      = 30 * time.Minute
	cloudIpsetMaxLKGTTL     = 2 * time.Hour
	cloudIpsetMaxCachedIPs  = 512

	cloudAgentResetCooldown = 30 * time.Second
	cloudOutboundWatchEvery = 5 * time.Second
)

// KeenDNSCloudSnapshot holds active NDMS /show/ndns state consumed by sing-box
// cloud OUTPUT interception.
type KeenDNSCloudSnapshot struct {
	Enabled        bool
	RelayIPs       []string
	RelayDomains   []string
	RelayEndpoints []netip.AddrPort
}

var cloudSharedIPv4 = netip.MustParsePrefix("100.64.0.0/10")

// cloudOutputBaseDomains lists Keenetic/Netcraze cloud broker and master
// hostnames resolved via the router's standard DNS resolver (never FakeIP).
var cloudOutputBaseDomains = []string{
	"broker.keenetic.cloud",
	"broker.netcraze.cloud",
	"cloud.keenetic.net",
	"master.keenetic.cloud",
	"ea.master.keenetic.cloud",
	"master.netcraze.cloud",
	"ea.master.netcraze.cloud",
}

// cloudOutputLocalDirectDomains lists Keenetic/Netcraze local access portals
// that must stay DIRECT ahead of broad cloud suffix rules.
var cloudOutputLocalDirectDomains = []string{
	"my.keenetic.net",
	"my.netcraze.net",
}

// cloudOutputStaticSubnets lists static Keenetic/Netcraze cloud subnets whose
// control/relay ports (9, 3478, 3479, 4044, 5683, 5684, 5685 TCP/UDP, and
// 8082, 8090 TCP for NDNS dialback and coalagent master) must traverse the
// selected cloud outbound alongside dynamic broker/relay IPs.
var cloudOutputStaticSubnets = []string{
	"185.162.93.0/24",
	"95.213.212.0/24",
	"95.213.181.0/24",
	"87.228.71.0/24",
	"91.92.241.0/24",
	"193.107.216.0/24",
	"178.250.154.0/24",
	"178.72.134.0/24",
	"85.198.119.0/24",
	"37.0.127.0/24",
	"5.35.2.0/24",
	"84.38.177.0/24",
	"49.12.59.0/24",
	"167.233.7.0/24",
	"82.202.218.0/24",
	"82.202.0.0/16",
}

// disallowedCloudIPv4Prefixes rejects bogon, loopback, RFC1918, CGNAT, link-local,
// FakeIP (198.18.0.0/15), KeenDNS local portal (198.51.100.0/24), and multicast/reserved ranges.
var disallowedCloudIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"),
}

// cloudOutputChainBypassCIDRs are early RETURN destinations inside AWGM-KEENDNS-OUT(-M)
// so router-local, LAN, loopback, CGNAT, FakeIP, KeenDNS local portals, and WAN reply
// traffic never enter cloud interception or loop back through TPROXY.
var cloudOutputChainBypassCIDRs = []string{
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.88.99.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"224.0.0.0/4",
	"255.255.255.255/32",
}

var cloudNegBackoffSteps = []time.Duration{
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

type cloudOutputSourceEntry struct {
	ips          map[string]time.Time
	lkgExpiresAt time.Time
}

type cloudOutputNegBackoff struct {
	step         int
	nextLookupAt time.Time
}

type cloudOutputIpsetRunnerFn func(ctx context.Context, stdin string, args ...string) ([]byte, error)

var (
	cloudIpsetMu         sync.RWMutex
	cloudIpsetRunner     cloudOutputIpsetRunnerFn = defaultCloudOutputIpsetRunner
	cloudIpsetCustom     bool
	cloudDNSMu           sync.RWMutex
	cloudDNSFn           = defaultCloudOutputDNSLookupIPv4
	cloudClockMu         sync.RWMutex
	cloudNowFn           = time.Now
	pkgCloudOutIpsetLock sync.Mutex
)

func defaultCloudOutputIpsetRunner(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	bin := bypassset.IPSetBinary()
	if bin == "" {
		return nil, bypassset.ErrIPSetNotAvailable
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func defaultCloudOutputDNSLookupIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

func setCloudOutputIpsetRunner(fn cloudOutputIpsetRunnerFn) {
	cloudIpsetMu.Lock()
	if fn == nil {
		cloudIpsetRunner = defaultCloudOutputIpsetRunner
		cloudIpsetCustom = false
	} else {
		cloudIpsetRunner = fn
		cloudIpsetCustom = true
	}
	cloudIpsetMu.Unlock()
}

func hasCustomCloudOutputIpsetRunner() bool {
	cloudIpsetMu.RLock()
	custom := cloudIpsetCustom
	cloudIpsetMu.RUnlock()
	return custom
}

func runCloudOutputIpset(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cloudIpsetMu.RLock()
	fn := cloudIpsetRunner
	cloudIpsetMu.RUnlock()
	return fn(ctx, stdin, args...)
}

func setCloudOutputDNSLookupIPv4(fn func(ctx context.Context, host string) ([]netip.Addr, error)) {
	cloudDNSMu.Lock()
	if fn == nil {
		cloudDNSFn = defaultCloudOutputDNSLookupIPv4
	} else {
		cloudDNSFn = fn
	}
	cloudDNSMu.Unlock()
}

func lookupCloudOutputIPv4(ctx context.Context, host string) ([]netip.Addr, error) {
	cloudDNSMu.RLock()
	fn := cloudDNSFn
	cloudDNSMu.RUnlock()
	return fn(ctx, host)
}

func setCloudOutputNow(fn func() time.Time) {
	cloudClockMu.Lock()
	if fn == nil {
		cloudNowFn = time.Now
	} else {
		cloudNowFn = fn
	}
	cloudClockMu.Unlock()
}

func cloudOutputNow() time.Time {
	cloudClockMu.RLock()
	fn := cloudNowFn
	cloudClockMu.RUnlock()
	return fn()
}

func isCloudOutputIpsetRuntimeEnabled(ipt *IPTables) bool {
	if hasCustomCloudOutputIpsetRunner() {
		return true
	}
	return ipt != nil && ipt.realKernel && runtime.GOOS == "linux" && bypassset.IPSetBinary() != ""
}

func isMissingCloudIpsetErr(out []byte, err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(string(out)))
	return strings.Contains(s, "does not exist") ||
		strings.Contains(s, "no such set") ||
		strings.Contains(s, "cannot find")
}

func cloudOutputIpsetExistsLocked(ctx context.Context, ipt *IPTables) bool {
	if !isCloudOutputIpsetRuntimeEnabled(ipt) {
		return false
	}
	_, err := runCloudOutputIpset(ctx, "", "list", cloudOutputIpsetName, "-n")
	return err == nil
}

// validateCloudPublicIPv4Entry validates and canonicalizes a single public IPv4
// host or CIDR suitable for AWGM-SB-CLOUD.
func validateCloudPublicIPv4Entry(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 32 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && c != '.' && c != '/' {
			return "", false
		}
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.IsValid() || !p.Addr().Is4() || p.Addr().Is4In6() {
			return "", false
		}
		if p.Bits() < 8 || p.Bits() > 32 {
			return "", false
		}
		p = p.Masked()
		if prefixOverlapsDisallowedCloudIPv4(p) {
			return "", false
		}
		if p.Bits() == 32 {
			return p.Addr().String(), true
		}
		return p.String(), true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.IsValid() || !addr.Is4() || addr.Is4In6() {
		return "", false
	}
	p := netip.PrefixFrom(addr, 32)
	if prefixOverlapsDisallowedCloudIPv4(p) {
		return "", false
	}
	return addr.String(), true
}

func prefixOverlapsDisallowedCloudIPv4(p netip.Prefix) bool {
	for _, bad := range disallowedCloudIPv4Prefixes {
		if p.Overlaps(bad) {
			return true
		}
	}
	return false
}

func validateCloudHostnameStrict(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(raw, ".")))
	if s == "" || len(s) > 253 || !strings.Contains(s, ".") {
		return "", false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return "", false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return "", false
			}
		}
	}
	tld := labels[len(labels)-1]
	allDigits := true
	for i := 0; i < len(tld); i++ {
		if tld[i] < '0' || tld[i] > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return "", false
	}
	return s, true
}

func normalizeCloudWANIPs(raw []string) []string {
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		s := strings.TrimSpace(entry)
		if s == "" {
			continue
		}
		var cidr string
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil || !p.IsValid() || !p.Addr().Is4() || p.Addr().Is4In6() {
				continue
			}
			cidr = p.Masked().String()
		} else {
			a, err := netip.ParseAddr(s)
			if err != nil || !a.IsValid() || !a.Is4() || a.Is4In6() {
				continue
			}
			cidr = netip.PrefixFrom(a.Unmap(), 32).String()
		}
		if _, ok := seen[cidr]; !ok {
			seen[cidr] = struct{}{}
			out = append(out, cidr)
		}
	}
	slices.Sort(out)
	return out
}

// cloudOutputPlan pairs dedicated TCP REDIRECT + UDP TPROXY listeners with the
// router OUTPUT / PREROUTING netfilter rules for KeenDNS & Netcraze Cloud.
// Callers must apply listeners before netfilter rules and detach rules before stopping.
type cloudOutputPlan struct {
	inbound    Inbound
	udpInbound Inbound
	rules      []Rule
	endpoints  []netip.AddrPort
	wanIPs     []string
	mark       uint32
	udpMark    uint32
	useIpset   bool
	enableUDP  bool
}

type cloudOutputPlanOpts struct {
	tcpPort   int
	udpPort   int
	outbound  string
	endpoints []netip.AddrPort
	wanIPs    []string
	mark      uint32
	udpMark   uint32
	useIpset  bool
	enableUDP bool
}

func newCloudOutputPlan(port int, outbound string, endpoints []netip.AddrPort, engineMark uint32) (cloudOutputPlan, error) {
	return newCloudOutputPlanWithOpts(cloudOutputPlanOpts{
		tcpPort:   port,
		udpPort:   cloudOutputUDPPort,
		outbound:  outbound,
		endpoints: endpoints,
		mark:      engineMark,
		udpMark:   cloudOutputUDPMark,
		enableUDP: true,
	})
}

func newCloudOutputPlanWithOpts(opts cloudOutputPlanOpts) (cloudOutputPlan, error) {
	outbound := strings.TrimSpace(opts.outbound)
	if opts.tcpPort < 1 || opts.tcpPort > 65535 || outbound == "" || opts.mark == 0 {
		return cloudOutputPlan{}, fmt.Errorf("cloud output requires a valid listener port, outbound and engine bypass mark")
	}
	if opts.enableUDP {
		if opts.udpPort < 1 || opts.udpPort > 65535 || opts.udpPort == opts.tcpPort || opts.udpMark == 0 || opts.udpMark == opts.mark {
			return cloudOutputPlan{}, fmt.Errorf("cloud UDP output requires a distinct listener port and fwmark")
		}
	}
	peers := slices.Clone(opts.endpoints)
	for i, endpoint := range peers {
		ip := endpoint.Addr().Unmap()
		if !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || endpoint.Port() == 0 {
			return cloudOutputPlan{}, fmt.Errorf("cloud relay requires a public IPv4 TCP endpoint")
		}
		firstOctet := ip.As4()[0]
		if firstOctet == 0 || firstOctet >= 240 || cloudSharedIPv4.Contains(ip) || prefixOverlapsDisallowedCloudIPv4(netip.PrefixFrom(ip, 32)) {
			return cloudOutputPlan{}, fmt.Errorf("cloud relay requires a public IPv4 TCP endpoint")
		}
		peers[i] = netip.AddrPortFrom(ip, endpoint.Port())
	}
	slices.SortFunc(peers, netip.AddrPort.Compare)

	inboundTags := []string{cloudOutputInbound}
	var udpIn Inbound
	if opts.enableUDP {
		inboundTags = append(inboundTags, cloudOutputUDPInbound)
		udpIn = Inbound{
			Type:       "tproxy",
			Tag:        cloudOutputUDPInbound,
			Listen:     "127.0.0.1",
			ListenPort: opts.udpPort,
			Network:    "udp",
			UDPTimeout: "5m",
		}
	}

	rules := []Rule{
		{
			Domain:   slices.Clone(cloudOutputLocalDirectDomains),
			Action:   "route",
			Outbound: "direct",
		},
		{
			Inbound:  inboundTags,
			Action:   "route",
			Outbound: outbound,
		},
	}

	return cloudOutputPlan{
		inbound: Inbound{
			Type:       "redirect",
			Tag:        cloudOutputInbound,
			Listen:     "127.0.0.1",
			ListenPort: opts.tcpPort,
		},
		udpInbound: udpIn,
		rules:      rules,
		endpoints:  slices.Compact(peers),
		wanIPs:     normalizeCloudWANIPs(opts.wanIPs),
		mark:       opts.mark,
		udpMark:    opts.udpMark,
		useIpset:   opts.useIpset,
		enableUDP:  opts.enableUDP,
	}, nil
}

func (p cloudOutputPlan) inboundList() []Inbound {
	if p.enableUDP && p.udpInbound.Tag != "" {
		return []Inbound{p.inbound, p.udpInbound}
	}
	return []Inbound{p.inbound}
}

func (p cloudOutputPlan) targetOutbound() string {
	for _, r := range p.rules {
		if len(r.Inbound) > 0 {
			return r.Outbound
		}
	}
	return ""
}

// configure adds the cloud inbound(s), local portal direct exception, and
// cloud inbound route rule to a copy of the slot.
func (p cloudOutputPlan) configure(cfg *RouterConfig, outboundExists func(string) bool) (*RouterConfig, error) {
	targetOut := p.targetOutbound()
	if cfg == nil || outboundExists == nil || targetOut == "" || !outboundExists(targetOut) {
		return nil, fmt.Errorf("KeenDNS Cloud outbound is unavailable")
	}
	if len(cfg.Route.DefaultMark) > 0 && orchestrator.ParseRoutingMark(cfg.Route.DefaultMark) != p.mark {
		return nil, fmt.Errorf("KeenDNS Cloud bypass mark conflicts with existing route.default_mark")
	}
	addedInbounds := p.inboundList()
	for _, in := range cfg.Inbounds {
		for _, want := range addedInbounds {
			if in.Tag == want.Tag || in.ListenPort == want.ListenPort {
				return nil, fmt.Errorf("KeenDNS Cloud listener conflicts with existing inbound %q", in.Tag)
			}
		}
	}
	next, err := cloneRouterConfig(cfg)
	if err != nil {
		return nil, err
	}
	next.Inbounds = append(next.Inbounds, addedInbounds...)
	next.Route.DefaultMark = []byte(strconv.FormatUint(uint64(p.mark), 10))
	next.Route.Rules = append(slices.Clone(p.rules), next.Route.Rules...)
	return next, nil
}

func (p cloudOutputPlan) commonBypassRules(dnsProto string) [][]string {
	rules := make([][]string, 0, 3+len(cloudOutputChainBypassCIDRs)+len(p.wanIPs))
	rules = append(rules,
		[]string{"-o", "lo", "-j", "RETURN"},
		[]string{"-p", dnsProto, "--dport", "53", "-j", "RETURN"},
		[]string{"-m", "mark", "!", "--mark", "0x0", "-j", "RETURN"},
	)
	for _, cidr := range cloudOutputChainBypassCIDRs {
		rules = append(rules, []string{"-d", cidr, "-j", "RETURN"})
	}
	for _, wan := range p.wanIPs {
		rules = append(rules, []string{"-d", wan, "-j", "RETURN"})
	}
	return rules
}

// redirectRules targets router-originated TCP cloud traffic (dynamic ipset on all ports,
// active RCI relay endpoints, and static cloud subnets on cloud control ports).
func (p cloudOutputPlan) redirectRules() ([][]string, error) {
	if p.mark == 0 {
		return nil, fmt.Errorf("cloud output requires a dedicated engine bypass mark")
	}
	portStr := strconv.Itoa(p.inbound.ListenPort)
	rules := p.commonBypassRules("tcp")
	if p.useIpset {
		rules = append(rules, []string{
			"-p", "tcp", "-m", "set", "--match-set", cloudOutputIpsetName, "dst",
			"-m", "conntrack", "--ctstate", "NEW", "--ctdir", "ORIGINAL",
			"-m", "mark", "--mark", "0x0",
			"-j", "REDIRECT", "--to-ports", portStr,
		})
	}
	for _, peer := range p.endpoints {
		rules = append(rules, []string{
			"-p", "tcp", "-d", peer.Addr().String() + "/32", "--dport", strconv.Itoa(int(peer.Port())),
			"-m", "conntrack", "--ctstate", "NEW", "--ctdir", "ORIGINAL",
			"-m", "mark", "--mark", "0x0",
			"-j", "REDIRECT", "--to-ports", portStr,
		})
	}
	for _, cidr := range cloudOutputStaticSubnets {
		rules = append(rules, []string{
			"-p", "tcp", "-d", cidr, "-m", "multiport", "--dports", cloudOutputStaticTCPPorts,
			"-m", "conntrack", "--ctstate", "NEW", "--ctdir", "ORIGINAL",
			"-m", "mark", "--mark", "0x0",
			"-j", "REDIRECT", "--to-ports", portStr,
		})
	}
	return rules, nil
}

// mangleOutputRules targets router-originated UDP cloud traffic without --ctstate NEW so
// long-lived system UDP sockets (such as CoAP :5683 and :4044) are adopted immediately.
func (p cloudOutputPlan) mangleOutputRules() ([][]string, error) {
	if !p.enableUDP {
		return nil, nil
	}
	if p.mark == 0 || p.udpMark == 0 {
		return nil, fmt.Errorf("cloud UDP output requires dedicated marks")
	}
	markSpec := fmt.Sprintf("0x%x/0x%x", p.udpMark, p.udpMark)
	rules := p.commonBypassRules("udp")
	if p.useIpset {
		rules = append(rules, []string{
			"-p", "udp", "-m", "set", "--match-set", cloudOutputIpsetName, "dst",
			"-m", "mark", "--mark", "0x0",
			"-j", "MARK", "--set-xmark", markSpec,
		})
	}
	for _, cidr := range cloudOutputStaticSubnets {
		rules = append(rules, []string{
			"-p", "udp", "-d", cidr, "-m", "multiport", "--dports", cloudOutputStaticUDPPorts,
			"-m", "mark", "--mark", "0x0",
			"-j", "MARK", "--set-xmark", markSpec,
		})
	}
	return rules, nil
}

func (p cloudOutputPlan) manglePreroutingRules() [][]string {
	if !p.enableUDP || p.udpMark == 0 || p.udpInbound.ListenPort == 0 {
		return nil
	}
	markSpec := fmt.Sprintf("0x%x/0x%x", p.udpMark, p.udpMark)
	return [][]string{
		{
			"-i", "lo", "-p", "udp", "-m", "mark", "--mark", markSpec,
			"-j", "TPROXY", "--on-port", strconv.Itoa(p.udpInbound.ListenPort),
			"--on-ip", "127.0.0.1", "--tproxy-mark", markSpec,
		},
	}
}

type parsedCloudIPTRule struct {
	proto      string
	inIface    string
	outIface   string
	dst        string
	matchSet   string
	dport      string
	multiports string
	ctstate    string
	ctdir      string
	mark       string
	markNeg    bool
	target     string
	toPorts    string
	setXmark   string
	onPort     string
	onIP       string
	tproxyMark string
}

func normalizeIPTDst(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "/") {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			return a.String() + "/32"
		}
	} else if p, err := netip.ParsePrefix(s); err == nil && p.IsValid() && p.Addr().Is4() {
		return p.Masked().String()
	}
	return s
}

func normalizeIPTMark(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	valPart, maskPart, hasMask := strings.Cut(s, "/")
	v, err := strconv.ParseUint(valPart, 0, 32)
	if err != nil {
		return s
	}
	var m uint64 = 0xffffffff
	if hasMask {
		parsedMask, merr := strconv.ParseUint(maskPart, 0, 32)
		if merr != nil {
			return s
		}
		m = parsedMask
	}
	return fmt.Sprintf("0x%x/0x%x", v, m)
}

func parseCloudIPTRule(fields []string) parsedCloudIPTRule {
	var r parsedCloudIPTRule
	for i := 0; i < len(fields); i++ {
		if i+1 >= len(fields) {
			break
		}
		neg := i > 0 && fields[i-1] == "!"
		switch fields[i] {
		case "-p":
			r.proto = fields[i+1]
			i++
		case "-i":
			r.inIface = fields[i+1]
			i++
		case "-o":
			r.outIface = fields[i+1]
			i++
		case "-d":
			r.dst = normalizeIPTDst(fields[i+1])
			i++
		case "--match-set":
			r.matchSet = fields[i+1]
			if i+2 < len(fields) {
				r.matchSet += " " + fields[i+2]
				i++
			}
			i++
		case "--dport":
			r.dport = fields[i+1]
			i++
		case "--dports", "--destination-ports":
			r.multiports = fields[i+1]
			i++
		case "--ctstate":
			r.ctstate = fields[i+1]
			i++
		case "--ctdir":
			r.ctdir = fields[i+1]
			i++
		case "--mark":
			r.markNeg = neg
			r.mark = normalizeIPTMark(fields[i+1])
			i++
		case "-j":
			r.target = fields[i+1]
			i++
		case "--to-ports":
			r.toPorts = fields[i+1]
			i++
		case "--set-xmark":
			r.setXmark = normalizeIPTMark(fields[i+1])
			i++
		case "--on-port":
			r.onPort = fields[i+1]
			i++
		case "--on-ip":
			r.onIP = fields[i+1]
			i++
		case "--tproxy-mark":
			r.tproxyMark = normalizeIPTMark(fields[i+1])
			i++
		}
	}
	return r
}

func cloudRulesEqual(existingLine string, desiredArgs []string) bool {
	ef := strings.Fields(strings.TrimSpace(existingLine))
	if len(ef) >= 2 && ef[0] == "-A" {
		ef = ef[2:]
	}
	return parseCloudIPTRule(ef) == parseCloudIPTRule(desiredArgs)
}

func scanCloudChainSnapshot(snapshot, parentChain, childChain string) (chainExists bool, firstParentRule string, hooks int, existingRules []string) {
	hook := "-A " + parentChain + " -j " + childChain
	rulePrefix := "-A " + childChain + " "
	parentPrefix := "-A " + parentChain + " "
	for _, rawLine := range strings.Split(snapshot, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "-N "+childChain {
			chainExists = true
		}
		if strings.HasPrefix(line, parentPrefix) && firstParentRule == "" {
			firstParentRule = line
		}
		if line == hook {
			hooks++
		}
		if strings.HasPrefix(line, rulePrefix) {
			existingRules = append(existingRules, line)
		}
	}
	return chainExists, firstParentRule, hooks, existingRules
}

func emitCloudChainRestore(b *strings.Builder, parentChain, childChain string, existingLines []string, chainExists bool, firstParentRule string, hooks int, desiredRules [][]string) (changed bool) {
	wantPresent := desiredRules != nil
	hook := "-A " + parentChain + " -j " + childChain
	if !wantPresent && !chainExists && hooks == 0 {
		return false
	}
	if wantPresent && chainExists && hooks == 1 && firstParentRule == hook && len(existingLines) == len(desiredRules) {
		allMatch := true
		for i, want := range desiredRules {
			if !cloudRulesEqual(existingLines[i], want) {
				allMatch = false
				break
			}
		}
		if allMatch {
			return false
		}
	}
	for range hooks {
		fmt.Fprintln(b, "-D "+parentChain+" -j "+childChain)
	}
	if chainExists {
		fmt.Fprintln(b, "-F "+childChain)
	} else if wantPresent {
		fmt.Fprintln(b, "-N "+childChain)
	}
	if !wantPresent {
		if chainExists {
			fmt.Fprintln(b, "-X "+childChain)
		}
	} else {
		for _, rule := range desiredRules {
			fmt.Fprintln(b, "-A "+childChain+" "+strings.Join(rule, " "))
		}
		fmt.Fprintln(b, "-I "+parentChain+" 1 -j "+childChain)
	}
	return true
}

// cloudOutputRestore builds a single *nat transaction from an `iptables -t nat -S`
// snapshot. A nil plan removes this feature's hook and chain.
func cloudOutputRestore(snapshot string, plan *cloudOutputPlan) (string, error) {
	var rules [][]string
	if plan != nil {
		var err error
		rules, err = plan.redirectRules()
		if err != nil {
			return "", err
		}
	}
	chainExists, firstOutputRule, hooks, existingRules := scanCloudChainSnapshot(snapshot, "OUTPUT", cloudOutputChain)
	var b strings.Builder
	b.WriteString("*nat\n")
	if !emitCloudChainRestore(&b, "OUTPUT", cloudOutputChain, existingRules, chainExists, firstOutputRule, hooks, rules) {
		return "", nil
	}
	b.WriteString("COMMIT\n")
	return b.String(), nil
}

// cloudOutputMangleRestore builds a single *mangle transaction from an `iptables -t mangle -S`
// snapshot covering both AWGM-KEENDNS-OUT-M (OUTPUT) and AWGM-KEENDNS-PRE (PREROUTING).
// Passing plan == nil or !plan.enableUDP removes both mangle hooks and chains.
func cloudOutputMangleRestore(snapshot string, plan *cloudOutputPlan) (string, error) {
	var outRules, preRules [][]string
	if plan != nil && plan.enableUDP {
		var err error
		outRules, err = plan.mangleOutputRules()
		if err != nil {
			return "", err
		}
		preRules = plan.manglePreroutingRules()
	}
	outExists, outFirst, outHooks, outExisting := scanCloudChainSnapshot(snapshot, "OUTPUT", cloudOutputMangleChain)
	preExists, preFirst, preHooks, preExisting := scanCloudChainSnapshot(snapshot, "PREROUTING", cloudOutputPreChain)

	var b strings.Builder
	b.WriteString("*mangle\n")
	outChanged := emitCloudChainRestore(&b, "OUTPUT", cloudOutputMangleChain, outExisting, outExists, outFirst, outHooks, outRules)
	preChanged := emitCloudChainRestore(&b, "PREROUTING", cloudOutputPreChain, preExisting, preExists, preFirst, preHooks, preRules)
	if !outChanged && !preChanged {
		return "", nil
	}
	b.WriteString("COMMIT\n")
	return b.String(), nil
}

func cloudRuleDumpMatches(dump string, mark uint32, table, prio int) (exactSingle, anyMatching bool) {
	if dump == "" {
		return false, false
	}
	exactHex := fmt.Sprintf("fwmark 0x%x", mark)
	prioPrefix := strconv.Itoa(prio) + ":"
	lookupSuffix := "lookup " + strconv.Itoa(table)
	matches := 0
	exactOK := false
	for _, rawLine := range strings.Split(dump, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.Contains(line, exactHex) && strings.Contains(line, lookupSuffix) {
			matches++
			if strings.HasPrefix(line, prioPrefix) {
				exactOK = true
			}
		}
	}
	return matches == 1 && exactOK, matches > 0
}

func cloudRouteDumpReady(dump string) bool {
	return strings.Contains(dump, "local default dev lo") ||
		strings.Contains(dump, "local 0.0.0.0/0 dev lo")
}

func (it *IPTables) ensureCloudUDPRoutingLocked(ctx context.Context, plan *cloudOutputPlan) error {
	if it.runIP == nil {
		return nil
	}
	wantUDP := plan != nil && plan.enableUDP
	markSpec := fmt.Sprintf("0x%x/0x%x", cloudOutputUDPMark, cloudOutputUDPMark)
	if plan != nil && plan.udpMark != 0 {
		markSpec = fmt.Sprintf("0x%x/0x%x", plan.udpMark, plan.udpMark)
	}
	tableStr := strconv.Itoa(cloudOutputRoutingTable)
	prioStr := strconv.Itoa(cloudOutputIPRulePrio)

	var ruleDump, routeDump string
	var ruleErr, routeErr error
	if it.runIPOut != nil {
		ruleDump, ruleErr = it.runIPOut(ctx, "-4", "rule", "show")
		routeDump, routeErr = it.runIPOut(ctx, "-4", "route", "show", "table", tableStr)
	}

	if !wantUDP {
		_, hasRules := cloudRuleDumpMatches(ruleDump, cloudOutputUDPMark, cloudOutputRoutingTable, cloudOutputIPRulePrio)
		if it.runIPOut == nil || ruleErr != nil || hasRules {
			for range maxIPRuleDrainPasses {
				if err := it.runIP(ctx, "-4", "rule", "del", "fwmark", markSpec, "table", tableStr); err != nil {
					break
				}
			}
		}
		if it.runIPOut == nil || routeErr != nil || strings.TrimSpace(routeDump) != "" {
			_ = it.runIP(ctx, "-4", "route", "flush", "table", tableStr)
		}
		return nil
	}

	exactSingle, _ := cloudRuleDumpMatches(ruleDump, plan.udpMark, cloudOutputRoutingTable, cloudOutputIPRulePrio)
	if !exactSingle {
		for range maxIPRuleDrainPasses {
			if err := it.runIP(ctx, "-4", "rule", "del", "fwmark", markSpec, "table", tableStr); err != nil {
				break
			}
		}
		if err := it.runIP(ctx, "-4", "rule", "add", "fwmark", markSpec, "table", tableStr, "priority", prioStr); err != nil {
			if !strings.Contains(err.Error(), "File exists") {
				return fmt.Errorf("ip rule add cloud UDP: %w", err)
			}
		}
	}
	if !cloudRouteDumpReady(routeDump) {
		if err := it.runIP(ctx, "-4", "route", "replace", "local", "0.0.0.0/0", "dev", "lo", "table", tableStr); err != nil {
			if !strings.Contains(err.Error(), "File exists") {
				return fmt.Errorf("ip route replace cloud UDP table %s: %w", tableStr, err)
			}
		}
	}
	return nil
}

func shouldManageCloudOutputNetfilterHook(it *IPTables) bool {
	return it != nil && (it.realKernel || netfilterCloudOutputHookCustom)
}

// cloudOutputNetfilterHookScript renders the netfilter.d hook that re-attaches
// KeenDNS Cloud OUTPUT/PREROUTING jumps and policy routing after NDMS firewall
// rebuilds while awgm-box is alive. Pure (no I/O) so tests can verify syntax via sh -n.
func cloudOutputNetfilterHookScript() string {
	return fmt.Sprintf(`#!/bin/sh
[ "$type" = "ip6tables" ] && exit 0
case "$table" in nat|mangle) ;; *) exit 0 ;; esac
LOCKFILE=%[1]q
LOCKDIR_FB=""
cleanup_nfhook_lock() {
  [ -n "$LOCKDIR_FB" ] && rmdir "$LOCKDIR_FB" 2>/dev/null || true
  LOCKDIR_FB=""
}
if [ -n "$LOCKFILE" ]; then
  if command -v flock >/dev/null 2>&1; then
    exec 9>>"$LOCKFILE" 2>/dev/null || true
    _lock_tries=0
    while [ "$_lock_tries" -lt 500 ]; do
      if flock -nx 9 2>/dev/null; then
        break
      fi
      _lock_tries=$((_lock_tries + 1))
      sleep 0.02 2>/dev/null || sleep 1
    done
  else
    _lock_tries=0
    while [ "$_lock_tries" -lt 500 ]; do
      if mkdir "${LOCKFILE}.d" 2>/dev/null; then
        LOCKDIR_FB="${LOCKFILE}.d"
        trap 'cleanup_nfhook_lock' EXIT INT TERM
        break
      fi
      _lock_tries=$((_lock_tries + 1))
      sleep 0.02 2>/dev/null || sleep 1
    done
  fi
fi
pidof awgm-box >/dev/null 2>&1 || exit 0
IPT=/opt/sbin/iptables
[ -x "$IPT" ] || IPT=iptables
IPBIN=/opt/sbin/ip
[ -x "$IPBIN" ] || IPBIN=ip
KREL="$(uname -r)"
for mod in xt_TPROXY xt_set xt_multiport xt_mark xt_conntrack; do
  grep -q "^${mod} " /proc/modules 2>/dev/null && continue
  [ -f "/lib/modules/${KREL}/${mod}.ko" ] && insmod "/lib/modules/${KREL}/${mod}.ko" 2>/dev/null || true
done
ensure_first_jump() {
  _tbl="$1"
  _parent="$2"
  _chain="$3"
  "$IPT" -w -t "$_tbl" -nL "$_chain" >/dev/null 2>&1 || return 0
  _first="$("$IPT" -w -t "$_tbl" -S "$_parent" 2>/dev/null | grep -E '^-A ' | head -n 1)"
  _cnt="$("$IPT" -w -t "$_tbl" -S "$_parent" 2>/dev/null | grep -cE -- "^-A $_parent -[jg] $_chain(\$| )" || true)"
  if [ "${_cnt:-0}" -eq 1 ] && [ "$_first" = "-A $_parent -j $_chain" ]; then
    return 0
  fi
  while "$IPT" -w -t "$_tbl" -D "$_parent" -j "$_chain" 2>/dev/null; do :; done
  "$IPT" -w -t "$_tbl" -I "$_parent" 1 -j "$_chain" 2>/dev/null || true
}
case "$table" in
nat)
  ensure_first_jump nat OUTPUT %[2]s
  ;;
mangle)
  ensure_first_jump mangle OUTPUT %[3]s
  if "$IPT" -w -t mangle -nL %[4]s >/dev/null 2>&1; then
    ensure_first_jump mangle PREROUTING %[4]s
    _rule_line="$("$IPBIN" -4 rule show 2>/dev/null | grep -E '^[0-9]+:.*fwmark 0x%[5]x(/0x%[5]x)? .*lookup %[6]d$' || true)"
    if ! printf '%%s\n' "$_rule_line" | grep -qE '^%[7]d:'; then
      "$IPBIN" -4 rule del fwmark 0x%[5]x/0x%[5]x table %[6]d 2>/dev/null || true
      "$IPBIN" -4 rule add fwmark 0x%[5]x/0x%[5]x table %[6]d priority %[7]d 2>/dev/null || true
    fi
    if ! "$IPBIN" -4 route show table %[6]d 2>/dev/null | grep -qE '^local (default|0\.0\.0\.0/0) dev lo'; then
      "$IPBIN" -4 route replace local 0.0.0.0/0 dev lo table %[6]d 2>/dev/null || true
    fi
  fi
  ;;
esac
exit 0
`, netfilterHookLockDir, cloudOutputChain, cloudOutputMangleChain, cloudOutputPreChain, cloudOutputUDPMark, cloudOutputRoutingTable, cloudOutputIPRulePrio)
}

func writeCloudOutputNetfilterHook(it *IPTables) error {
	if !shouldManageCloudOutputNetfilterHook(it) {
		return nil
	}
	script := cloudOutputNetfilterHookScript()
	if st, err := os.Stat(netfilterCloudOutputHookPath); err == nil && st.Mode().Perm() == 0755 {
		if cur, rerr := os.ReadFile(netfilterCloudOutputHookPath); rerr == nil && string(cur) == script {
			return nil
		}
	}
	return storage.AtomicWritePerm(netfilterCloudOutputHookPath, []byte(script), 0755)
}

func removeCloudOutputNetfilterHook(it *IPTables) {
	if !shouldManageCloudOutputNetfilterHook(it) {
		return
	}
	_ = os.Remove(netfilterCloudOutputHookPath)
}

func (it *IPTables) restoreCloudOutputLocked(ctx context.Context, plan *cloudOutputPlan) error {
	if plan == nil {
		// Remove the netfilter.d hook before tearing down rules so a racing NDMS
		// event cannot re-attach a jump while teardown is in progress.
		removeCloudOutputNetfilterHook(it)
	}
	if it.runIPTablesOut == nil || it.restoreNoflush == nil {
		if plan == nil {
			return nil
		}
		return fmt.Errorf("iptables runner is unavailable")
	}
	natSnapshot, err := it.runIPTablesOut(ctx, "-t", "nat", "-S")
	if err != nil {
		return fmt.Errorf("read cloud nat OUTPUT rules: %w", err)
	}
	natInput, err := cloudOutputRestore(natSnapshot, plan)
	if err != nil {
		return err
	}
	if natInput != "" {
		if err := it.restoreNoflush(ctx, natInput); err != nil {
			return fmt.Errorf("restore cloud nat OUTPUT rules: %w", err)
		}
	}

	mangleSnapshot, err := it.runIPTablesOut(ctx, "-t", "mangle", "-S")
	if err != nil {
		return fmt.Errorf("read cloud mangle OUTPUT/PREROUTING rules: %w", err)
	}
	mangleInput, err := cloudOutputMangleRestore(mangleSnapshot, plan)
	if err != nil {
		return err
	}
	if mangleInput != "" {
		if err := it.restoreNoflush(ctx, mangleInput); err != nil {
			return fmt.Errorf("restore cloud mangle OUTPUT/PREROUTING rules: %w", err)
		}
	}

	if err := it.ensureCloudUDPRoutingLocked(ctx, plan); err != nil {
		return err
	}
	if plan != nil {
		if err := writeCloudOutputNetfilterHook(it); err != nil {
			return fmt.Errorf("write cloud netfilter.d hook: %w", err)
		}
	}
	return nil
}

func destroyCloudOutputIpsets(ctx context.Context, ipt *IPTables) {
	if !isCloudOutputIpsetRuntimeEnabled(ipt) {
		return
	}
	pkgCloudOutIpsetLock.Lock()
	defer pkgCloudOutIpsetLock.Unlock()
	for _, name := range []string{cloudOutputStagingSet, cloudOutputIpsetName} {
		_, _ = runCloudOutputIpset(ctx, "", "destroy", name)
	}
}

// applyCloudOutput serializes listener verification and netfilter updates under
// the router hook lock. Callers must hold transitionMu.
func (it *IPTables) applyCloudOutput(ctx context.Context, plan *cloudOutputPlan, listening func(context.Context, int) bool, listeningUDP func(context.Context, int) bool) error {
	defer acquireNetfilterHookLock()()
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan != nil {
		if listening == nil || !listening(ctx, plan.inbound.ListenPort) {
			return fmt.Errorf("KeenDNS Cloud TCP listener is unavailable on port %d", plan.inbound.ListenPort)
		}
		if plan.enableUDP && (listeningUDP == nil || !listeningUDP(ctx, plan.udpInbound.ListenPort)) {
			return fmt.Errorf("KeenDNS Cloud UDP listener is unavailable on port %d", plan.udpInbound.ListenPort)
		}
	}
	return it.restoreCloudOutputLocked(ctx, plan)
}

// UninstallCloudOutput removes KeenDNS Cloud netfilter chains, hooks, policy
// routing, and ipsets during package removal (ReleaseNetfilterForRemoval).
func (it *IPTables) UninstallCloudOutput(ctx context.Context) {
	if it == nil {
		destroyCloudOutputIpsets(ctx, nil)
		return
	}
	func() {
		defer acquireNetfilterHookLock()()
		if it.runIPTablesOut != nil && it.restoreNoflush != nil {
			_ = it.restoreCloudOutputLocked(ctx, nil)
		}
	}()
	destroyCloudOutputIpsets(ctx, it)
}

func (s *ServiceImpl) clearCloudOutputSyncState() {
	if s == nil {
		return
	}
	s.cloudStateMu.Lock()
	s.cloudSetReady = false
	s.cloudLastAttemptAt = time.Time{}
	s.cloudLastSyncAt = time.Time{}
	s.cloudLastSyncErr = ""
	s.cloudLastRelayKey = ""
	s.cloudLastEntries = nil
	s.cloudSourceCache = nil
	s.cloudNegBackoff = nil
	s.cloudSyncedOutboundTag = ""
	s.cloudSyncedActiveMember = ""
	s.cloudStateMu.Unlock()
}

func (s *ServiceImpl) resetKeeneticCloudAgent(ctx context.Context) error {
	if s.deps.ResetCloudAgent != nil {
		return s.deps.ResetCloudAgent(ctx)
	}
	if s.deps.IPTables != nil && s.deps.IPTables.realKernel {
		_, err := cloudagent.Reset(ctx)
		return err
	}
	return nil
}

// CheckCloudOutboundSwitch polls the active member of the KeenDNS Cloud
// composite outbound (via Clash API) and resets coalagent when the selected
// outbound member changes, with deferred 30s cooldown protection so switches
// occurring inside the cooldown window still trigger after cooldown expires.
func (s *ServiceImpl) CheckCloudOutboundSwitch(ctx context.Context) {
	if s == nil || s.deps.Settings == nil || s.deps.CloudOutboundActive == nil {
		return
	}
	settings, err := s.deps.Settings.Get()
	if err != nil || settings == nil {
		return
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return
	}
	tag := strings.TrimSpace(sr.KeenDNSCloudOutbound)
	if !sr.Enabled || settings.SingboxManuallyStopped || isFircRoutingEngine(sr) || tag == "" {
		s.cloudStateMu.Lock()
		s.cloudSyncedOutboundTag = ""
		s.cloudSyncedActiveMember = ""
		s.cloudStateMu.Unlock()
		return
	}
	if s.deps.Singbox != nil {
		running, _ := s.deps.Singbox.IsRunning()
		if !running {
			return
		}
	}
	active, err := s.deps.CloudOutboundActive(ctx, tag)
	if err != nil {
		return
	}
	active = strings.TrimSpace(active)
	if active == "" {
		return
	}

	s.cloudStateMu.Lock()
	if s.cloudSyncedActiveMember == "" {
		s.cloudSyncedOutboundTag = tag
		s.cloudSyncedActiveMember = active
		s.cloudStateMu.Unlock()
		return
	}
	if s.cloudSyncedOutboundTag == tag && s.cloudSyncedActiveMember == active {
		s.cloudStateMu.Unlock()
		return
	}
	now := cloudOutputNow()
	if !s.cloudLastAgentResetAt.IsZero() && now.Sub(s.cloudLastAgentResetAt) < cloudAgentResetCooldown {
		s.cloudStateMu.Unlock()
		return
	}
	prevTag := s.cloudSyncedOutboundTag
	prevActive := s.cloudSyncedActiveMember
	s.cloudSyncedOutboundTag = tag
	s.cloudSyncedActiveMember = active
	s.cloudLastAgentResetAt = now
	s.cloudStateMu.Unlock()

	if err := s.resetKeeneticCloudAgent(ctx); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("keendns-cloud", "agent-reset", err.Error())
		}
		return
	}
	if s.appLog != nil {
		fromDesc := prevActive
		if prevTag != "" && prevTag != tag {
			fromDesc = prevTag + "/" + prevActive
		}
		s.appLog.Info(
			"keendns-cloud",
			"agent-reset",
			fmt.Sprintf("outbound %s switched (%s -> %s); signaled coalagent for fast reconnect", tag, fromDesc, active),
		)
	}
}

func (s *ServiceImpl) detachCloudOutputLocked(ctx context.Context, disableSlot bool) error {
	if s == nil {
		return nil
	}
	if s.deps.IPTables != nil {
		if err := s.deps.IPTables.applyCloudOutput(ctx, nil, nil, nil); err != nil {
			return err
		}
		destroyCloudOutputIpsets(ctx, s.deps.IPTables)
	} else {
		destroyCloudOutputIpsets(ctx, nil)
	}
	s.clearCloudOutputSyncState()
	if disableSlot && s.deps.Orch != nil && s.slotEnabled(orchestrator.SlotKeenDNSCloud) {
		return s.deps.Orch.SetEnabled(orchestrator.SlotKeenDNSCloud, false)
	}
	return nil
}

// DetachCloudOutput removes KeenDNS Cloud OUTPUT redirection before sing-box
// stops so local cloud traffic never blackholes against a dead listener.
func (s *ServiceImpl) DetachCloudOutput(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	return s.detachCloudOutputLocked(ctx, false)
}

// reconcileCloudOutputFromStoreLocked reconciles KeenDNS Cloud interception
// from current persisted settings. Caller must hold s.transitionMu (and NOT s.mu).
func (s *ServiceImpl) reconcileCloudOutputFromStoreLocked(ctx context.Context, scope string) {
	if s == nil || s.deps.Settings == nil {
		return
	}
	settings, err := s.deps.Settings.Get()
	if err != nil || settings == nil {
		return
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return
	}
	if !sr.Enabled || settings.SingboxManuallyStopped || isFircRoutingEngine(sr) {
		return
	}
	if cerr := s.reconcileCloudOutput(ctx, sr, false); cerr != nil && s.appLog != nil {
		s.appLog.Warn("keendns-cloud", scope, cerr.Error())
	}
}

// ReconcileCloudOutput reconciles KeenDNS Cloud OUTPUT/PREROUTING interception
// against current persisted settings and sing-box process state (e.g. right after
// manual Control("start") or Control("restart")).
func (s *ServiceImpl) ReconcileCloudOutput(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if s.deps.Settings == nil {
		return nil
	}
	settings, err := s.deps.Settings.Get()
	if err != nil || settings == nil {
		return err
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return err
	}
	if isFircRoutingEngine(sr) {
		return nil
	}
	return s.reconcileCloudOutput(ctx, sr, settings.SingboxManuallyStopped)
}

func (s *ServiceImpl) fetchCloudSnapshot(ctx context.Context) (KeenDNSCloudSnapshot, error) {
	if s.deps.KeenDNSCloudSnapshot != nil {
		return s.deps.KeenDNSCloudSnapshot(ctx)
	}
	if s.deps.KeenDNSRelayEndpoints == nil {
		return KeenDNSCloudSnapshot{}, nil
	}
	endpoints, err := s.deps.KeenDNSRelayEndpoints(ctx)
	if err != nil {
		return KeenDNSCloudSnapshot{}, err
	}
	relayIPs := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.Addr().Is4() {
			relayIPs = append(relayIPs, ep.Addr().String())
		}
	}
	return KeenDNSCloudSnapshot{
		Enabled:        len(endpoints) > 0,
		RelayIPs:       relayIPs,
		RelayEndpoints: endpoints,
	}, nil
}

func cloneCloudOutputSourceCache(src map[string]*cloudOutputSourceEntry) map[string]*cloudOutputSourceEntry {
	out := make(map[string]*cloudOutputSourceEntry, len(src)+8)
	for k, v := range src {
		if v == nil {
			continue
		}
		ips := make(map[string]time.Time, len(v.ips))
		for ip, exp := range v.ips {
			ips[ip] = exp
		}
		out[k] = &cloudOutputSourceEntry{ips: ips, lkgExpiresAt: v.lkgExpiresAt}
	}
	return out
}

func isNegativeCloudOutputDNSError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	low := strings.ToLower(err.Error())
	return strings.Contains(low, "no such host") ||
		strings.Contains(low, "nxdomain") ||
		strings.Contains(low, "nodata")
}

func updateCloudOutputSourceIPs(cache map[string]*cloudOutputSourceEntry, srcKey string, validIPs []string, now time.Time) {
	entry := cache[srcKey]
	if entry == nil {
		if len(validIPs) == 0 {
			return
		}
		entry = &cloudOutputSourceEntry{ips: make(map[string]time.Time, len(validIPs))}
		cache[srcKey] = entry
	}
	for ip, exp := range entry.ips {
		if !exp.After(now) {
			delete(entry.ips, ip)
		}
	}
	normalExp := now.Add(cloudIpsetStaleTTL)
	for _, ip := range validIPs {
		entry.ips[ip] = normalExp
	}
	if len(validIPs) > 0 {
		entry.lkgExpiresAt = now.Add(cloudIpsetMaxLKGTTL)
	}
	if len(entry.ips) == 0 {
		delete(cache, srcKey)
	}
}

func pruneCloudOutputNormalTTL(cache map[string]*cloudOutputSourceEntry, srcKey string, now time.Time) {
	entry := cache[srcKey]
	if entry == nil {
		return
	}
	for ip, exp := range entry.ips {
		if !exp.After(now) {
			delete(entry.ips, ip)
		}
	}
	if len(entry.ips) == 0 {
		delete(cache, srcKey)
	}
}

func pruneCloudOutputBoundedLKG(cache map[string]*cloudOutputSourceEntry, srcKey string, now time.Time) {
	entry := cache[srcKey]
	if entry == nil {
		return
	}
	if !entry.lkgExpiresAt.After(now) {
		delete(cache, srcKey)
	}
}

func trimCloudOutputSourceCache(cache map[string]*cloudOutputSourceEntry, maxEntries int) {
	type item struct {
		srcKey string
		ip     string
		exp    time.Time
	}
	var all []item
	for srcKey, entry := range cache {
		if entry == nil {
			continue
		}
		for ip, exp := range entry.ips {
			all = append(all, item{srcKey: srcKey, ip: ip, exp: exp})
		}
	}
	if len(all) <= maxEntries {
		return
	}
	slices.SortFunc(all, func(a, b item) int {
		if !a.exp.Equal(b.exp) {
			if a.exp.Before(b.exp) {
				return -1
			}
			return 1
		}
		if a.srcKey != b.srcKey {
			return strings.Compare(a.srcKey, b.srcKey)
		}
		return strings.Compare(a.ip, b.ip)
	})
	drop := len(all) - maxEntries
	for i := 0; i < drop; i++ {
		if entry := cache[all[i].srcKey]; entry != nil {
			delete(entry.ips, all[i].ip)
			if len(entry.ips) == 0 {
				delete(cache, all[i].srcKey)
			}
		}
	}
}

type cloudOutputDomainTarget struct {
	host       string
	sourceKey  string
	isOptional bool
}

type cloudOutputDomainResult struct {
	target cloudOutputDomainTarget
	ips    []string
	err    error
}

func resolveCloudOutputDomainTargetsIPv4(ctx context.Context, targets []cloudOutputDomainTarget) []cloudOutputDomainResult {
	if len(targets) == 0 {
		return nil
	}
	jobs := make(chan cloudOutputDomainTarget, len(targets))
	results := make(chan cloudOutputDomainResult, len(targets))
	workers := min(cloudIpsetDNSWorkers, len(targets))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range jobs {
				if ctx.Err() != nil {
					results <- cloudOutputDomainResult{target: target, err: ctx.Err()}
					continue
				}
				dCtx, cancel := context.WithTimeout(ctx, cloudIpsetDNSTimeout)
				addrs, err := lookupCloudOutputIPv4(dCtx, target.host)
				cancel()
				if err != nil {
					results <- cloudOutputDomainResult{target: target, err: err}
					continue
				}
				var valid []string
				for _, addr := range addrs {
					if !addr.IsValid() || !addr.Is4() || addr.Is4In6() {
						continue
					}
					if entry, ok := validateCloudPublicIPv4Entry(addr.String()); ok {
						valid = append(valid, entry)
					}
				}
				results <- cloudOutputDomainResult{target: target, ips: valid}
			}
		}()
	}
	for _, t := range targets {
		jobs <- t
	}
	close(jobs)
	wg.Wait()
	close(results)

	out := make([]cloudOutputDomainResult, 0, len(targets))
	for res := range results {
		out = append(out, res)
	}
	return out
}

func buildCloudRelayKey(snap KeenDNSCloudSnapshot) (validRelayIPs []string, relayKey string) {
	ipSeen := make(map[string]struct{})
	addIP := func(raw string) {
		if entry, ok := validateCloudPublicIPv4Entry(raw); ok {
			if _, ok := ipSeen[entry]; !ok {
				ipSeen[entry] = struct{}{}
				validRelayIPs = append(validRelayIPs, entry)
			}
		}
	}
	for _, ip := range snap.RelayIPs {
		addIP(ip)
	}
	for _, ep := range snap.RelayEndpoints {
		if ep.Addr().Is4() {
			addIP(ep.Addr().Unmap().String())
		}
	}
	var domains []string
	domSeen := make(map[string]struct{})
	for _, d := range snap.RelayDomains {
		if host, ok := validateCloudHostnameStrict(d); ok {
			if _, ok := domSeen[host]; !ok {
				domSeen[host] = struct{}{}
				domains = append(domains, host)
			}
		}
	}
	slices.Sort(validRelayIPs)
	slices.Sort(domains)
	return validRelayIPs, fmt.Sprintf("en=%t;ips=%s;doms=%s", snap.Enabled, strings.Join(validRelayIPs, ","), strings.Join(domains, ","))
}

func applyCloudOutputIpsetAtomicLocked(ctx context.Context, entries []string) error {
	ipsetCtx, cancel := context.WithTimeout(ctx, cloudIpsetCmdTimeout)
	defer cancel()

	var script strings.Builder
	fmt.Fprintf(&script, "create %s hash:net family inet hashsize 1024 maxelem %d -exist\n", cloudOutputStagingSet, cloudOutputIpsetMaxElem)
	fmt.Fprintf(&script, "flush %s\n", cloudOutputStagingSet)
	for _, entry := range entries {
		if valid, ok := validateCloudPublicIPv4Entry(entry); ok {
			fmt.Fprintf(&script, "add %s %s -exist\n", cloudOutputStagingSet, valid)
		}
	}
	cleanupStaging := func() {
		cCtx, cCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cCancel()
		_, _ = runCloudOutputIpset(cCtx, "", "destroy", cloudOutputStagingSet)
	}

	if out, err := runCloudOutputIpset(ipsetCtx, script.String(), "restore", "-exist"); err != nil {
		cleanupStaging()
		return fmt.Errorf("restore %s: %w (%s)", cloudOutputStagingSet, err, strings.TrimSpace(string(out)))
	}
	if out, err := runCloudOutputIpset(ipsetCtx, "", "create", cloudOutputIpsetName,
		"hash:net", "family", "inet", "hashsize", "1024", "maxelem", strconv.Itoa(cloudOutputIpsetMaxElem), "-exist"); err != nil {
		cleanupStaging()
		return fmt.Errorf("create %s: %w (%s)", cloudOutputIpsetName, err, strings.TrimSpace(string(out)))
	}
	if out, err := runCloudOutputIpset(ipsetCtx, "", "swap", cloudOutputStagingSet, cloudOutputIpsetName); err != nil {
		cleanupStaging()
		return fmt.Errorf("swap %s -> %s: %w (%s)", cloudOutputStagingSet, cloudOutputIpsetName, err, strings.TrimSpace(string(out)))
	}
	cleanupStaging()
	return nil
}

// syncCloudOutputIpset maintains the AWGM-SB-CLOUD dynamic IPv4 ipset from
// active RCI relay IPs/domains and cloud broker/master hostnames using bounded
// LKG/TTL caching and atomic staging+swap. Returns true if AWGM-SB-CLOUD exists
// in the kernel and can be matched by iptables.
func (s *ServiceImpl) syncCloudOutputIpset(ctx context.Context, snap KeenDNSCloudSnapshot, rciErr error) (bool, error) {
	if !isCloudOutputIpsetRuntimeEnabled(s.deps.IPTables) {
		return false, nil
	}

	now := cloudOutputNow()
	validRelayIPs, relayKey := buildCloudRelayKey(snap)

	s.cloudStateMu.Lock()
	lastAttempt := s.cloudLastAttemptAt
	lastErr := s.cloudLastSyncErr
	lastRelayKey := s.cloudLastRelayKey
	hadPriorState := s.cloudSetReady || len(s.cloudSourceCache) > 0
	lastEntries := slices.Clone(s.cloudLastEntries)
	nextCache := cloneCloudOutputSourceCache(s.cloudSourceCache)
	nextNeg := make(map[string]cloudOutputNegBackoff, len(s.cloudNegBackoff))
	for k, v := range s.cloudNegBackoff {
		nextNeg[k] = v
	}
	s.cloudStateMu.Unlock()

	pkgCloudOutIpsetLock.Lock()
	existsInKernel := cloudOutputIpsetExistsLocked(ctx, s.deps.IPTables)
	pkgCloudOutIpsetLock.Unlock()

	minInterval := cloudIpsetSyncInterval
	if lastErr != "" {
		minInterval = cloudIpsetRetryInterval
	}
	due := !existsInKernel || lastAttempt.IsZero() || now.Sub(lastAttempt) >= minInterval || relayKey != lastRelayKey
	if !due {
		if existsInKernel {
			return true, nil
		}
		return false, nil
	}

	domainTargets := make([]cloudOutputDomainTarget, 0, len(cloudOutputBaseDomains)+len(snap.RelayDomains))
	domainSeen := make(map[string]struct{}, len(cloudOutputBaseDomains)+len(snap.RelayDomains))
	addDomainTarget := func(raw string, isOptional bool) {
		if entry, ok := validateCloudPublicIPv4Entry(raw); ok {
			if rciErr == nil {
				if !slices.Contains(validRelayIPs, entry) {
					validRelayIPs = append(validRelayIPs, entry)
				}
			}
			return
		}
		if host, ok := validateCloudHostnameStrict(raw); ok {
			if _, exists := domainSeen[host]; !exists {
				domainSeen[host] = struct{}{}
				prefix := "relay:"
				if isOptional {
					prefix = "base:"
				}
				domainTargets = append(domainTargets, cloudOutputDomainTarget{
					host:       host,
					sourceKey:  prefix + host,
					isOptional: isOptional,
				})
			}
		}
	}
	if rciErr == nil && snap.Enabled {
		for _, d := range snap.RelayDomains {
			addDomainTarget(d, false)
		}
	}
	for _, d := range cloudOutputBaseDomains {
		addDomainTarget(d, true)
	}

	lookupTargets := make([]cloudOutputDomainTarget, 0, len(domainTargets))
	for _, target := range domainTargets {
		if !target.isOptional {
			delete(nextNeg, target.host)
			lookupTargets = append(lookupTargets, target)
			continue
		}
		if neg, exists := nextNeg[target.host]; exists && now.Before(neg.nextLookupAt) {
			continue
		}
		lookupTargets = append(lookupTargets, target)
	}

	results := resolveCloudOutputDomainTargetsIPv4(ctx, lookupTargets)
	if err := ctx.Err(); err != nil {
		return existsInKernel, err
	}

	var transientErrs []error
	if rciErr != nil {
		transientErrs = append(transientErrs, fmt.Errorf("ndms keendns query: %w", rciErr))
	}

	dnsSuccessCount := 0
	dnsTransientCount := 0
	touchedSources := make(map[string]struct{}, len(domainTargets)+1)
	for _, res := range results {
		target := res.target
		touchedSources[target.sourceKey] = struct{}{}
		if target.isOptional {
			isNeg := (res.err == nil && len(res.ips) == 0) || isNegativeCloudOutputDNSError(res.err)
			if res.err == nil && len(res.ips) > 0 {
				dnsSuccessCount++
				delete(nextNeg, target.host)
				updateCloudOutputSourceIPs(nextCache, target.sourceKey, res.ips, now)
				continue
			}
			if isNeg {
				prev := nextNeg[target.host]
				idx := min(max(prev.step, 0), len(cloudNegBackoffSteps)-1)
				nextNeg[target.host] = cloudOutputNegBackoff{
					step:         prev.step + 1,
					nextLookupAt: now.Add(cloudNegBackoffSteps[idx]),
				}
				pruneCloudOutputNormalTTL(nextCache, target.sourceKey, now)
				continue
			}
			dnsTransientCount++
			transientErrs = append(transientErrs, fmt.Errorf("%s: %w", target.host, res.err))
			pruneCloudOutputBoundedLKG(nextCache, target.sourceKey, now)
			continue
		}

		if res.err == nil && len(res.ips) > 0 {
			dnsSuccessCount++
			updateCloudOutputSourceIPs(nextCache, target.sourceKey, res.ips, now)
			continue
		}
		dnsTransientCount++
		errVal := res.err
		if errVal == nil {
			errVal = errors.New("no valid IPv4 addresses")
		}
		transientErrs = append(transientErrs, fmt.Errorf("%s: %w", target.host, errVal))
		pruneCloudOutputBoundedLKG(nextCache, target.sourceKey, now)
	}

	touchedSources["rci"] = struct{}{}
	if rciErr == nil {
		updateCloudOutputSourceIPs(nextCache, "rci", validRelayIPs, now)
	} else {
		pruneCloudOutputBoundedLKG(nextCache, "rci", now)
	}
	for srcKey := range nextCache {
		if _, ok := touchedSources[srcKey]; ok {
			continue
		}
		if rciErr != nil && strings.HasPrefix(srcKey, "relay:") {
			pruneCloudOutputBoundedLKG(nextCache, srcKey, now)
		} else {
			pruneCloudOutputNormalTTL(nextCache, srcKey, now)
		}
	}

	trimCloudOutputSourceCache(nextCache, cloudIpsetMaxCachedIPs)
	desiredSet := make(map[string]struct{}, 32)
	for _, entry := range nextCache {
		for ip := range entry.ips {
			desiredSet[ip] = struct{}{}
		}
	}
	entries := make([]string, 0, len(desiredSet))
	for ip := range desiredSet {
		entries = append(entries, ip)
	}
	slices.Sort(entries)

	partialErr := errors.Join(transientErrs...)
	errMsg := ""
	if partialErr != nil {
		errMsg = partialErr.Error()
	}

	// Preserve any existing kernel set unmodified when cold start had zero dynamic
	// resolution and an outage occurred, though still create an empty set if needed
	// so static iptables rules stay consistent on clean cold starts.
	skipSwap := existsInKernel && slices.Equal(entries, lastEntries) && (partialErr != nil || hadPriorState)

	if !skipSwap {
		pkgCloudOutIpsetLock.Lock()
		swapErr := applyCloudOutputIpsetAtomicLocked(ctx, entries)
		existsNow := cloudOutputIpsetExistsLocked(ctx, s.deps.IPTables)
		pkgCloudOutIpsetLock.Unlock()
		if swapErr != nil {
			s.cloudStateMu.Lock()
			s.cloudSetReady = existsNow
			s.cloudLastAttemptAt = now
			s.cloudLastSyncErr = swapErr.Error()
			s.cloudNegBackoff = nextNeg
			s.cloudStateMu.Unlock()
			return existsNow, swapErr
		}
		existsInKernel = existsNow
	}

	s.cloudStateMu.Lock()
	s.cloudLastAttemptAt = now
	s.cloudLastRelayKey = relayKey
	s.cloudSourceCache = nextCache
	s.cloudNegBackoff = nextNeg
	s.cloudSetReady = existsInKernel
	s.cloudLastEntries = slices.Clone(entries)
	if dnsSuccessCount > 0 || (rciErr == nil && len(validRelayIPs) > 0) || !existsInKernel {
		s.cloudLastSyncAt = now
	}
	s.cloudLastSyncErr = errMsg
	s.cloudStateMu.Unlock()

	if partialErr != nil && dnsSuccessCount == 0 && len(validRelayIPs) == 0 && dnsTransientCount > 0 {
		return existsInKernel, partialErr
	}
	return existsInKernel, nil
}

type cloudOutputSlotRouteJSON struct {
	Rules       []Rule          `json:"rules"`
	DefaultMark json.RawMessage `json:"default_mark,omitempty"`
}

type cloudOutputSlotJSON struct {
	Inbounds []Inbound                `json:"inbounds"`
	Route    cloudOutputSlotRouteJSON `json:"route"`
}

func marshalCloudOutputSlot(cfg *RouterConfig) ([]byte, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cloud output slot config is nil")
	}
	return json.Marshal(cloudOutputSlotJSON{
		Inbounds: cfg.Inbounds,
		Route: cloudOutputSlotRouteJSON{
			Rules:       cfg.Route.Rules,
			DefaultMark: cfg.Route.DefaultMark,
		},
	})
}

// stageCloudOutputSlotBeforeEnable pre-populates and enables 09-keendns-cloud.json
// right before enableLocked / enablePolicyTun / enableFakeIPTun runs
// orchestratorApplyNow(), avoiding a second sing-box reload (and tun restart)
// when reconcileCloudOutput attaches netfilter rules after enable completes.
func (s *ServiceImpl) stageCloudOutputSlotBeforeEnable(ctx context.Context, sr storage.SingboxRouterSettings) {
	if s == nil || s.deps.Orch == nil || !sr.Enabled || isFircRoutingEngine(sr) {
		return
	}
	if s.deps.Settings != nil {
		if cur, err := s.deps.Settings.Get(); err == nil && cur != nil && cur.SingboxManuallyStopped {
			return
		}
	}
	outbound := strings.TrimSpace(sr.KeenDNSCloudOutbound)
	if outbound == "" {
		return
	}
	enableUDP := s.prepareNetfilter(ctx) == nil
	plan, err := newCloudOutputPlanWithOpts(cloudOutputPlanOpts{
		tcpPort:   cloudOutputPort,
		udpPort:   cloudOutputUDPPort,
		outbound:  outbound,
		mark:      cloudOutputMark,
		udpMark:   cloudOutputUDPMark,
		enableUDP: enableUDP,
	})
	if err != nil {
		return
	}
	cfg, err := plan.configure(&RouterConfig{}, func(string) bool { return true })
	if err != nil {
		return
	}
	want, err := marshalCloudOutputSlot(cfg)
	if err != nil {
		return
	}
	if res := s.deps.Orch.ValidateDraft(orchestrator.SlotKeenDNSCloud, want); !res.Ok() {
		return
	}
	if s.deps.IPTables != nil {
		_ = s.deps.IPTables.applyCloudOutput(ctx, nil, nil, nil)
	}
	if err := s.deps.Orch.SaveSilent(orchestrator.SlotKeenDNSCloud, want); err != nil {
		return
	}
	_ = s.deps.Orch.SetEnabledSilent(orchestrator.SlotKeenDNSCloud, true)
}

// reconcileCloudOutput runs under transitionMu. A changed slot is loaded
// with interception detached, so a rejected configuration leaves direct
// cloud connectivity available and can be retried on the next tick.
func (s *ServiceImpl) reconcileCloudOutput(ctx context.Context, sr storage.SingboxRouterSettings, stopped bool) error {
	orch, ipt := s.deps.Orch, s.deps.IPTables
	if orch == nil || ipt == nil {
		return nil
	}
	slot := orchestrator.SlotKeenDNSCloud
	enabled := s.slotEnabled(slot)
	if !sr.Enabled || stopped || strings.TrimSpace(sr.KeenDNSCloudOutbound) == "" {
		// Always detach kernel rules and ipsets even when the slot is already
		// disabled so a stale rule set left by an unclean exit is cleaned up.
		if err := s.detachCloudOutputLocked(ctx, enabled); err != nil {
			return err
		}
		return nil
	}
	running, pid := true, 0
	if s.deps.Singbox != nil {
		running, pid = s.deps.Singbox.IsRunning()
	}
	if !running {
		return s.detachCloudOutputLocked(ctx, false)
	}

	snap, rciErr := s.fetchCloudSnapshot(ctx)
	useIpset, ipsetErr := s.syncCloudOutputIpset(ctx, snap, rciErr)
	if ipsetErr != nil && s.appLog != nil {
		s.appLog.Warn("keendns-cloud", "ipset", ipsetErr.Error())
	} else if rciErr != nil && s.appLog != nil {
		s.appLog.Warn("keendns-cloud", "ndns", rciErr.Error())
	}

	var validEndpoints []netip.AddrPort
	for _, ep := range snap.RelayEndpoints {
		if ep.Addr().Is4() {
			validEndpoints = append(validEndpoints, ep)
		}
	}

	var wanIPs []string
	if s.deps.WANIPCollector != nil {
		if collected, cerr := s.deps.WANIPCollector.Collect(ctx); cerr == nil {
			wanIPs = collected
		}
	}

	enableUDP := s.prepareNetfilter(ctx) == nil

	plan, err := newCloudOutputPlanWithOpts(cloudOutputPlanOpts{
		tcpPort:   cloudOutputPort,
		udpPort:   cloudOutputUDPPort,
		outbound:  sr.KeenDNSCloudOutbound,
		endpoints: validEndpoints,
		wanIPs:    wanIPs,
		mark:      cloudOutputMark,
		udpMark:   cloudOutputUDPMark,
		useIpset:  useIpset,
		enableUDP: enableUDP,
	})
	if err != nil {
		return err
	}

	// The orchestrator checks the reference against all enabled slots.
	cfg, err := plan.configure(&RouterConfig{}, func(string) bool { return true })
	if err != nil {
		return err
	}
	want, err := marshalCloudOutputSlot(cfg)
	if err != nil {
		return err
	}
	current, err := orch.LoadApplied(slot)
	if err != nil {
		return err
	}
	changed := !enabled || !bytes.Equal(current, want)
	if changed {
		if err := ipt.applyCloudOutput(ctx, nil, nil, nil); err != nil {
			return err
		}
		if err := orch.SaveSilent(slot, want); err != nil {
			return err
		}
		if err := orch.SetEnabledSilent(slot, true); err != nil {
			return err
		}
	}
	tcpListening := cloudOutputListenProbe(ctx, pid, cloudOutputPort)
	udpListening := !plan.enableUDP || cloudOutputUDPListenProbe(ctx, pid, cloudOutputUDPPort)
	if changed || !tcpListening || !udpListening {
		if err := orch.ReloadNow(); err != nil {
			_ = s.detachCloudOutputLocked(ctx, true)
			return err
		}
		deadline := time.Now().Add(cloudOutputReloadWait)
		for {
			if s.deps.Singbox != nil {
				runningNow, currentPID := s.deps.Singbox.IsRunning()
				if !runningNow {
					_ = s.detachCloudOutputLocked(ctx, false)
					return fmt.Errorf("sing-box stopped during KeenDNS Cloud reload")
				}
				pid = currentPID
			}
			tcpReady := cloudOutputListenProbe(ctx, pid, cloudOutputPort)
			udpReady := !plan.enableUDP || cloudOutputUDPListenProbe(ctx, pid, cloudOutputUDPPort)
			if tcpReady && udpReady {
				break
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
				_ = s.detachCloudOutputLocked(ctx, false)
				return ctx.Err()
			case <-time.After(cloudOutputReloadPoll):
			}
		}
	}
	if !s.slotEnabled(slot) {
		return fmt.Errorf("KeenDNS Cloud slot was not applied")
	}
	err = ipt.applyCloudOutput(
		ctx,
		&plan,
		func(pctx context.Context, port int) bool {
			return cloudOutputListenProbe(pctx, pid, port)
		},
		func(pctx context.Context, port int) bool {
			return cloudOutputUDPListenProbe(pctx, pid, port)
		},
	)
	if err != nil {
		_ = s.detachCloudOutputLocked(ctx, false)
	}
	return err
}

// keendnsCloudReferencing reports whether persisted singbox router settings
// bind KeenDNS Cloud OUTPUT redirection to tag.
func (s *ServiceImpl) keendnsCloudReferencing(tag string) []string {
	tag = strings.TrimSpace(tag)
	if s.deps.Settings == nil || tag == "" {
		return nil
	}
	settings, err := s.deps.Settings.Load()
	if err != nil || settings == nil {
		return nil
	}
	if strings.TrimSpace(settings.SingboxRouter.KeenDNSCloudOutbound) == tag {
		return []string{"keendnsCloudOutbound"}
	}
	return nil
}

// renameKeenDNSCloudOutbound updates persisted KeenDNSCloudOutbound when an
// external or composite outbound tag is renamed.
func (s *ServiceImpl) renameKeenDNSCloudOutbound(oldTag, newTag string) error {
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if s.deps.Settings == nil || oldTag == "" || newTag == "" || oldTag == newTag {
		return nil
	}
	err := s.deps.Settings.Update(func(cur *storage.Settings) error {
		if strings.TrimSpace(cur.SingboxRouter.KeenDNSCloudOutbound) != oldTag {
			return errKeenDNSCloudUnchanged
		}
		cur.SingboxRouter.KeenDNSCloudOutbound = newTag
		return nil
	})
	if errors.Is(err, errKeenDNSCloudUnchanged) {
		return nil
	}
	return err
}

// clearKeenDNSCloudOutbound clears KeenDNSCloudOutbound on force-delete of its
// target outbound and immediately detaches the active slot and OUTPUT redirect.
func (s *ServiceImpl) clearKeenDNSCloudOutbound(ctx context.Context, tag string) error {
	tag = strings.TrimSpace(tag)
	if s.deps.Settings == nil || tag == "" {
		return nil
	}
	cleared := false
	err := s.deps.Settings.Update(func(cur *storage.Settings) error {
		if strings.TrimSpace(cur.SingboxRouter.KeenDNSCloudOutbound) != tag {
			return errKeenDNSCloudUnchanged
		}
		cur.SingboxRouter.KeenDNSCloudOutbound = ""
		cleared = true
		return nil
	})
	if errors.Is(err, errKeenDNSCloudUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	if cleared {
		s.transitionMu.Lock()
		defer s.transitionMu.Unlock()
		return s.detachCloudOutputLocked(ctx, true)
	}
	return nil
}

// rewriteCloudOutputSlotOutbound updates active and disabled 09-keendns-cloud.json
// files when an external tunnel or proxy outbound tag is renamed.
func (s *ServiceImpl) rewriteCloudOutputSlotOutbound(oldTag, newTag string) (bool, error) {
	if s.deps.Orch == nil {
		return false, nil
	}
	configDir := s.deps.Orch.ConfigDir()
	activePath := filepath.Join(configDir, "09-keendns-cloud.json")
	disabledPath := filepath.Join(configDir, "disabled", "09-keendns-cloud.json")
	changed := false
	if data, ok, err := rewriteCloudSlotOutboundRefs(activePath, oldTag, newTag); err != nil {
		return false, err
	} else if ok {
		if err := s.deps.Orch.Save(orchestrator.SlotKeenDNSCloud, data); err != nil {
			return false, err
		}
		changed = true
	}
	if data, ok, err := rewriteCloudSlotOutboundRefs(disabledPath, oldTag, newTag); err != nil {
		return false, err
	} else if ok {
		if err := storage.AtomicWrite(disabledPath, data); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func rewriteCloudSlotOutboundRefs(path, oldTag, newTag string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var doc cloudOutputSlotJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	updated := false
	for i := range doc.Route.Rules {
		if doc.Route.Rules[i].Outbound == oldTag {
			doc.Route.Rules[i].Outbound = newTag
			updated = true
		}
	}
	if !updated {
		return nil, false, nil
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
