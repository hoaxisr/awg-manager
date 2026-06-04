package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

const (
	TPROXYPort = 51271
	// RedirectPort is sing-box's REDIRECT inbound for TCP. We split TCP
	// onto NAT REDIRECT (instead of mangle TPROXY) because TPROXY for
	// TCP requires a working `-m socket --transparent` bypass to deliver
	// ACK/data on already-established connections — Keenetic's 4.9-ndm-5
	// kernel evaluates that match as 0 hits, so ACKs would re-enter the
	// TPROXY rule, get redirected to 127.0.0.1:51271 with the listener
	// destination, and the kernel would emit RST because no socket
	// matches that 4-tuple. NAT REDIRECT sidesteps the issue entirely:
	// conntrack tracks the DNAT for established flows, ACKs are
	// auto-translated, and sing-box's accept()ed socket handles them
	// like any normal TCP connection. SKeen ships the same split.
	RedirectPort  = 51272
	Fwmark        = 0x1
	RoutingTable  = 100
	ChainName     = "AWGM-TPROXY"
	RedirectChain = "AWGM-REDIRECT"
	// DNSRescueTag identifies our short-circuit REDIRECT rules in nat
	// PREROUTING that bypass NDMS's _NDM_DNS_FLT_REDIR catch-all
	// (which would unconditionally REDIRECT DNS to :53, where
	// sing-box's hijack-dns transparent listener catches it and
	// silently drops). The rules are inserted at PREROUTING position 1
	// per LAN bridge and target the per-policy ndnproxy port
	// discovered from _NDM_HOTSPOT_DNSREDIR (see lanbridges.go).
	// Issue #132.
	DNSRescueTag = "AWGM-DNS-RESCUE"
	// IngressTag identifies our MARK/CONNMARK rules in mangle PREROUTING
	// that force selected interfaces' connections to carry the policy
	// mark (ingress-scope feature). Comment-tagged for idempotent cleanup.
	IngressTag = "AWGM-INGRESS"
	// DNSNoPolicyTag is the legacy tag for the previous (failed)
	// attempt: re-mark mark=0 DNS in mangle PREROUTING up to an NDMS
	// catch-all mark, expecting _NDM_HOTSPOT_DNSREDIR to forward to
	// the per-policy ndnproxy. That path is dead — _NDM_DNS_FLT_REDIR
	// REDIRECTs to :53 before _NDM_HOTSPOT_DNSREDIR ever runs, so the
	// mark we'd elevate is never consulted. We still scrub these on
	// Install to clean up the rules on upgrade from any prior AWGM
	// build that installed the mangle-MARK form.
	DNSNoPolicyTag = "AWGM-DNS-NOPOLICY"
	// IPRulePriority is the fixed `ip rule` priority for our fwmark rule.
	// Above NDMS policy rules (~100-200) and below system main/default
	// (32766/32767). Hard-coded so Install is fully idempotent and so
	// our rule never accidentally displaces the kernel's local-table
	// rule at priority 0.
	IPRulePriority = 30000
	// maxIPRuleDrainPasses caps the drain loop in Install/Uninstall.
	// Defensive bound — `ip rule del` should return ENOENT quickly when
	// nothing matches, but a buggy kernel returning success forever
	// would otherwise hang Install. 32 is well above any realistic
	// duplicate count (the worst observed was 10 in the wild).
	maxIPRuleDrainPasses = 32
)

// Mutable in tests via t.Cleanup so they can redirect into a tmp dir.
// Production code reads these at call time.
var (
	netfilterHookPath  = "/opt/etc/ndm/netfilter.d/50-awgm-tproxy.sh"
	netfilterRulesPath = "/opt/etc/awg-manager/singbox/router-netfilter.rules"
)

func kernelModuleName() string { return "xt_TPROXY" }

func buildTProxyModulePath(kernelVersion string) string {
	return filepath.Join("/lib/modules", kernelVersion, "xt_TPROXY.ko")
}

func isModuleLoaded(name string) bool {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return false
	}
	prefix := name + " "
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

var (
	netfilterOnce      sync.Once
	netfilterAvailable bool
)

func IsNetfilterAvailable() bool {
	netfilterOnce.Do(func() {
		if isModuleLoaded(kernelModuleName()) {
			netfilterAvailable = true
			return
		}
		kernel := osdetect.KernelRelease()
		if kernel == "" {
			return
		}
		_, err := os.Stat(buildTProxyModulePath(kernel))
		netfilterAvailable = err == nil
	})
	return netfilterAvailable
}

func EnsureTProxyModule(ctx context.Context) error {
	return ensureKernelModuleFn(ctx, kernelModuleName())
}

// EnsureCommentModule loads xt_comment when available as a .ko module
// so DNS-NOPOLICY rules (which use `-m comment --comment "..."` as
// their scrub identifier in netfilter.d) can be accepted by the kernel
// at iptables-restore COMMIT time.
//
// Soft-fail by design: if the .ko file is absent we return nil and let
// iptables-restore surface a concrete error later. Reason — the module
// may be built into the kernel (no .ko on disk), in which case the
// rules apply normally and a hard "missing component" error here would
// be a false positive. The harder failure path (genuinely missing both
// as module and built-in) shows up as "iptables-restore: line N failed"
// at Install time, which is the same error path the user already
// observes today.
//
// Why this is needed: NDMS on some Keenetic OS 5.x EA firmwares
// (observed on NC-1812 / MT7988 / OS 5.00.C.11.0-0 EA) does not use
// `-m comment` anywhere, so xt_comment isn't auto-loaded at boot.
// Without an explicit insmod our DNS-NOPOLICY rules (added by commit
// ad5ad113) are rejected at COMMIT and the whole mangle install fails.
// See issue #130.
func EnsureCommentModule(ctx context.Context) error {
	err := ensureKernelModuleFn(ctx, "xt_comment")
	if errors.Is(err, ErrNetfilterComponentMissing) {
		return nil
	}
	return err
}

// ensureKernelModuleFn is the indirection point that tests redirect to
// inject deterministic outcomes for EnsureCommentModule. Production
// callers use the real ensureKernelModule below.
var ensureKernelModuleFn = ensureKernelModule

func ensureKernelModule(ctx context.Context, name string) error {
	if isModuleLoaded(name) {
		return nil
	}
	kernel := osdetect.KernelRelease()
	if kernel == "" {
		return ErrNetfilterComponentMissing
	}
	path := filepath.Join("/lib/modules", kernel, name+".ko")
	if _, err := os.Stat(path); err != nil {
		return ErrNetfilterComponentMissing
	}
	if _, err := sysexec.Run(ctx, "insmod", path); err != nil {
		return fmt.Errorf("insmod %s: %w", path, err)
	}
	return nil
}

var (
	tproxyTargetMu     sync.Mutex
	tproxyTargetResult bool
)

func IsTProxyTargetAvailable(ctx context.Context) bool {
	tproxyTargetMu.Lock()
	if tproxyTargetResult {
		tproxyTargetMu.Unlock()
		return true
	}
	tproxyTargetMu.Unlock()

	res, err := sysexec.Run(ctx, sysiptables.Binary, "-j", "TPROXY", "--help")
	ok := err == nil && res != nil && strings.Contains(res.Stdout+res.Stderr, "TPROXY")
	if ok {
		tproxyTargetMu.Lock()
		tproxyTargetResult = true
		tproxyTargetMu.Unlock()
	}
	return ok
}

type RestoreInputSpec struct {
	// PolicyMark is the NDMS-assigned mark (hex, e.g. "0xffffaaa") that
	// NDMS sets on connections from devices bound to the chosen access
	// policy. Empty means no PREROUTING jump (defensive — caller should
	// never reach Install with empty mark, but iptables doesn't panic).
	PolicyMark string

	// MatchAll installs PREROUTING jumps without an NDMS policy-mark
	// filter. This is the "all devices" mode. Keep it separate from an
	// empty PolicyMark so legacy defensive behavior (empty mark = no
	// PREROUTING jumps) stays intact.
	MatchAll bool

	// WANIPs is a list of router-owned IP addresses (in "X.X.X.X/32" form)
	// that must NOT be TPROXY'd: traffic from LAN to the router's own
	// public-WAN or tunnel-endpoint IPs would otherwise loop back into
	// sing-box. Collected dynamically by WANIPCollector before Install.
	// Empty list = no extra exclusions (router still works, just exposes
	// the WAN-IP loop edge case).
	WANIPs []string

	// LANBridges enumerates (bridge, ndnproxy-port) pairs for the
	// DNS-RESCUE nat-PREROUTING REDIRECT rules. Discovered by
	// DiscoverLANBridges() — see lanbridges.go for the discovery
	// algorithm and the why. Empty list = skip DNS-RESCUE entirely
	// (no bridges with usable _NDM_HOTSPOT_DNSREDIR entries on this
	// router).
	LANBridges []LANBridgeDNSRedir

	// BypassUDPPorts lists UDP destination ports that should RETURN from
	// AWGM-TPROXY before the catch-all TPROXY rule — they bypass sing-box
	// entirely and route as if no policy were active.
	BypassUDPPorts []int

	// BypassTCPPorts lists TCP destination ports that should RETURN from
	// AWGM-REDIRECT before the catch-all REDIRECT rule.
	// Note: port 79 (NDMS admin) is always excluded by a hardcoded rule;
	// including 79 here produces a harmless duplicate RETURN rule.
	BypassTCPPorts []int

	// IngressInterfaces — уже резолвленные kernel-имена (напр. "nwg3"),
	// чей ingress-трафик помечается policy-меткой в mangle PREROUTING до
	// connmark-jump'а. Пусто / MatchAll / пустой PolicyMark = no-op.
	IngressInterfaces []string
}

var bypassCIDRs = []string{
	"127.0.0.0/8",
	"169.254.0.0/16",
	"100.64.0.0/10", // CGNAT (RFC 6598)
	"0.0.0.0/8",     // this network (RFC 1122)
	"192.0.0.0/24",  // IETF Protocol Assignments (NAT64 well-known)
	"224.0.0.0/4",
	"255.255.255.255/32",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
}

// emitBypassReturns appends "-A <chain> -d <dst> -j RETURN" for every bypass
// CIDR and router-owned WAN IP. mangle (UDP/TPROXY) and nat (TCP/REDIRECT) must
// carry an IDENTICAL bypass set — both tables call this so the set can't drift,
// which would let traffic enter sing-box on one protocol and bypass on the
// other (asymmetric loop/leak).
func emitBypassReturns(b *strings.Builder, chain string, wanIPs []string) {
	for _, cidr := range bypassCIDRs {
		fmt.Fprintf(b, "-A %s -d %s -j RETURN\n", chain, cidr)
	}
	for _, ip := range wanIPs {
		fmt.Fprintf(b, "-A %s -d %s -j RETURN\n", chain, ip)
	}
}

// emitPreroutingJump appends the PREROUTING jump into chain, gated by the same
// policy-mark condition for mangle (UDP) and nat (TCP) so a device is proxied
// by identical criteria on both protocols (drift = "half-broken tunnel").
func emitPreroutingJump(b *strings.Builder, chain string, spec RestoreInputSpec) {
	if spec.MatchAll {
		fmt.Fprintf(b, "-A PREROUTING -m conntrack ! --ctstate INVALID -j %s\n", chain)
	} else if spec.PolicyMark != "" {
		fmt.Fprintf(b, "-A PREROUTING -m connmark --mark %s -m conntrack ! --ctstate INVALID -j %s\n",
			spec.PolicyMark, chain)
	}
}

func buildRestoreInput(spec RestoreInputSpec) string {
	var b strings.Builder

	// SKeen-style layout (`reference/SKeen/skeen.sh`, set_chain_rules /
	// set_prerouting_rules / add_tproxy_rules / add_redirect_rules):
	//
	//   - one chain per table (mangle: AWGM-TPROXY, nat: AWGM-REDIRECT)
	//   - policy connmark filter lives ON THE PREROUTING JUMP, not inside
	//     the chain — non-policy traffic never enters the chain at all
	//   - jump uses `-j` (not `-g`) so bypasses can `-j RETURN` cleanly
	//     back into PREROUTING; the catch-all TPROXY/REDIRECT at the end
	//     of the chain handles everything else
	//   - jump is `-A PREROUTING` (append) so it runs AFTER NDMS
	//     _NDM_*_PREROUTING_* chains have a chance to set the connmark;
	//     this also keeps the fast_nat-cache issue (FORWARD MASQUERADE
	//     poisoning conntrack with WAN-IP source) from coming back
	//   - NO AWGM-DNS-OFFLOAD chain: with policy filter on the jump,
	//     non-policy DNS never reaches sing-box via these rules. (If the
	//     `hijack-dns` side-effect listener turns out to still grab
	//     non-policy DNS at the kernel socket-lookup level, that's a
	//     sing-box inbound/dns config matter — see Уровень Б discussion
	//     in commit log)
	//   - no `-m addrtype` or `-i br+` matchers anywhere: zero kernel
	//     module surface beyond xt_TPROXY

	// ---- *mangle table: UDP via TPROXY ----
	// Literal port of `add_tproxy_rules` from reference/SKeen/skeen.sh
	// (hybrid mode, mangle table) plus the DNS interception rule from
	// set_chain_rules (`INTERCEPT_DNS_ENABLE=1` branch). No extras.
	b.WriteString("*mangle\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", ChainName)

	// Bypass ports: RETURN first — before DNS intercept and catch-all so that
	// any explicitly excluded port skips sing-box entirely (including port 53).
	for _, port := range spec.BypassUDPPorts {
		fmt.Fprintf(&b, "-A %s -p udp --dport %d -j RETURN\n", ChainName, port)
	}

	// set_chain_rules: DNS first (when INTERCEPT_DNS_ENABLE=1)
	fmt.Fprintf(&b, "-A %s -p udp --dport 53 -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
		ChainName, TPROXYPort, Fwmark)

	// set_chain_rules: bypass set. SKeen uses one ipset rule; we render
	// the same destinations as discrete CIDR rules (semantically equal).
	emitBypassReturns(&b, ChainName, spec.WANIPs)

	// add_tproxy_rules: catch-all TPROXY for UDP.
	fmt.Fprintf(&b, "-A %s -p udp -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
		ChainName, TPROXYPort, Fwmark)

	// Ingress-scope: пометить выбранные интерфейсы policy-меткой ДО jump'а,
	// чтобы connmark-jump (ниже в mangle и в nat) принял их за членов
	// политики. Эмитится перед jump'ом → в PREROUTING MARK/save сработают
	// раньше. Skip в MatchAll / при пустой метке (там и так всё проксируется).
	if !spec.MatchAll && spec.PolicyMark != "" {
		for _, iface := range spec.IngressInterfaces {
			fmt.Fprintf(&b, "-A PREROUTING -i %s -m comment --comment %s -j MARK --set-xmark %s/0xffffffff\n",
				iface, IngressTag, spec.PolicyMark)
			fmt.Fprintf(&b, "-A PREROUTING -i %s -m comment --comment %s -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff\n",
				iface, IngressTag)
		}
	}

	// set_prerouting_rules: policy connmark filter ON THE JUMP, no `-p`
	// matcher (SKeen jumps unconditionally; per-proto matching happens
	// inside the chain).
	emitPreroutingJump(&b, ChainName, spec)

	b.WriteString("COMMIT\n")

	// ---- *nat table: TCP via REDIRECT ----
	// Literal port of `add_redirect_rules` from reference/SKeen/skeen.sh
	// (hybrid mode, nat table). SKeen's nat chain has ONLY the bypass set
	// + catch-all `-p tcp -j REDIRECT`; there is no DNS-specific TCP rule
	// because the catch-all already covers TCP/53.
	b.WriteString("*nat\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", RedirectChain)

	emitBypassReturns(&b, RedirectChain, spec.WANIPs)
	// Bypass router admin port so we don't redirect our own UI traffic.
	// (SKeen has equivalent dynamic admin-port discovery — same intent.)
	fmt.Fprintf(&b, "-A %s -p tcp --dport 79 -j RETURN\n", RedirectChain)

	// Bypass ports: RETURN before catch-all TCP REDIRECT.
	for _, port := range spec.BypassTCPPorts {
		fmt.Fprintf(&b, "-A %s -p tcp --dport %d -j RETURN\n", RedirectChain, port)
	}

	// add_redirect_rules: catch-all REDIRECT for TCP.
	fmt.Fprintf(&b, "-A %s -p tcp -j REDIRECT --to-ports %d\n", RedirectChain, RedirectPort)

	emitPreroutingJump(&b, RedirectChain, spec)

	// ---- DNS-RESCUE: per-bridge short-circuit REDIRECT to ndnproxy ----
	// For each (bridge, ndnproxy-port) discovered from
	// _NDM_HOTSPOT_DNSREDIR (see lanbridges.go), insert at position 1
	// in nat PREROUTING — BEFORE NDMS's own `-A PREROUTING -j
	// _NDM_DNS_REDIRECT`, whose first sub-chain (_NDM_DNS_FLT_REDIR)
	// unconditionally REDIRECTs DNS to :53 (terminating, mark not
	// consulted). Our rule fires first, REDIRECTs the packet to the
	// per-policy ndnproxy that sing-box doesn't touch, and skips the
	// :53 hijack edge case entirely.
	//
	// Filters:
	//   - -i <bridge>: only LAN bridges where NDMS knows ndnproxy port,
	//   - -m mark --mark 0x0: only mark=0 (default-policy) packets —
	//     devices in any access policy already get NDMS's mark-aware
	//     _NDM_HOTSPOT_DNSREDIR via _NDM_DNS_BYPS-style mechanisms or
	//     don't go through FLT_REDIR at all,
	//   - -m pkttype --pkt-type unicast: don't touch mDNS/multicast,
	//   - --dport 53 + REDIRECT --to-ports <port>: DNS only, target
	//     ndnproxy.
	//
	// All rules use -I PREROUTING 1 so they land in front of the NDMS
	// jumps; their relative order amongst themselves doesn't matter
	// (per-bridge, per-protocol independent matches).
	if !spec.MatchAll {
		for _, bm := range spec.LANBridges {
			fmt.Fprintf(&b, "-I PREROUTING 1 -i %s -m mark --mark 0x0 -m pkttype --pkt-type unicast -p udp --dport 53 -m comment --comment %q -j REDIRECT --to-ports %d\n",
				bm.Bridge, DNSRescueTag, bm.Port)
			fmt.Fprintf(&b, "-I PREROUTING 1 -i %s -m mark --mark 0x0 -m pkttype --pkt-type unicast -p tcp --dport 53 -m comment --comment %q -j REDIRECT --to-ports %d\n",
				bm.Bridge, DNSRescueTag, bm.Port)
		}
	}

	b.WriteString("COMMIT\n")
	return b.String()
}

type restoreNoflushFn func(ctx context.Context, input string) error
type runFn func(ctx context.Context, args ...string) error
type runOutFn func(ctx context.Context, args ...string) (string, error)

type persistFn func(input string) error

type IPTables struct {
	restoreNoflush restoreNoflushFn
	runIPTables    runFn
	runIPTablesOut runOutFn
	runIP          runFn
	persistRules   persistFn
	persistHook    func() error
	cleanupHook    func()
}

func NewIPTables() *IPTables {
	return &IPTables{
		restoreNoflush: sysiptables.RestoreNoflush,
		runIPTables:    sysiptables.Run,
		runIPTablesOut: sysiptables.RunOutput,
		runIP: func(ctx context.Context, args ...string) error {
			result, err := sysexec.Run(ctx, "ip", args...)
			return sysexec.FormatError(result, err)
		},
		persistRules: writeNetfilterRulesFile,
		persistHook:  writeNetfilterHook,
		cleanupHook:  removeNetfilterRulesFile,
	}
}

func (it *IPTables) Install(ctx context.Context, spec RestoreInputSpec) error {
	// Scrub any existing PREROUTING jumps to AWGM-TPROXY before inserting
	// the new one. iptables-restore --noflush + -I PREROUTING 1 would
	// otherwise stack a duplicate jump on every restart / mark-change /
	// re-Enable: the stale rule from the previous policy/mark survives
	// because mangle isn't flushed, and the new rule lands in front of it.
	// Idempotent: a no-op when no prior jumps exist.
	it.removeSourceHooks(ctx)

	input := buildRestoreInput(spec)
	if it.persistRules != nil {
		if err := it.persistRules(input); err != nil {
			return fmt.Errorf("write netfilter rules: %w", err)
		}
	}
	if it.persistHook != nil {
		if err := it.persistHook(); err != nil {
			return fmt.Errorf("write netfilter hook: %w", err)
		}
	}
	if err := it.restoreNoflush(ctx, input); err != nil {
		return fmt.Errorf("iptables-restore: %w", err)
	}
	// Drain ALL existing fwmark rules pointing at our table before
	// adding a fresh one. Without this, every Install (Reconcile,
	// daemon restart, mark-change, re-Enable) leaves a duplicate
	// because `ip rule add` without explicit priority lands at
	// previous_priority + 1 instead of being deduped — and a stack of
	// rules at priorities 0-N displaces the kernel's `from all lookup
	// local` rule (normally at prio 0), breaking router-local routing
	// (sing-box outbounds to direct silently fail).
	for i := 0; i < maxIPRuleDrainPasses; i++ {
		if err := it.runIP(ctx, "rule", "del", "fwmark", fmt.Sprintf("0x%x", Fwmark),
			"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
			break
		}
	}
	// Use an explicit priority well above NDMS policy rules (100-200)
	// and well below the system main/default tables (32766/32767), so
	// our rule is identifiable and idempotent.
	if err := it.runIP(ctx, "rule", "add", "fwmark", fmt.Sprintf("0x%x", Fwmark),
		"table", fmt.Sprintf("%d", RoutingTable),
		"priority", fmt.Sprintf("%d", IPRulePriority)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip rule add: %w", err)
		}
	}
	if err := it.runIP(ctx, "route", "add", "local", "0.0.0.0/0", "dev", "lo",
		"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip route add: %w", err)
		}
	}
	return nil
}

func writeNetfilterRulesFile(input string) error {
	if err := os.MkdirAll(filepath.Dir(netfilterRulesPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(netfilterRulesPath, []byte(input), 0644)
}

func writeNetfilterHook() error {
	if err := os.MkdirAll(filepath.Dir(netfilterHookPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(netfilterHookPath, []byte(netfilterHookScript()), 0755)
}

// netfilterHookScript renders the netfilter.d hook with all placeholders
// substituted. Pure (no I/O) so a test can validate the generated shell with
// `sh -n`.
//
// Scrub block before restore: when NDMS reloads only one table (e.g. nat) but
// leaves mangle intact, restoring --noflush would append a SECOND PREROUTING
// jump on top of the surviving one. The `-[jg]` regex covers both legacy `-j`
// and current `-g` syntax so the upgrade path doesn't leave duplicates around.
func netfilterHookScript() string {
	return fmt.Sprintf(`#!/bin/sh
[ "$type" = "ip6tables" ] && exit 0
case "$table" in mangle|nat) ;; *) exit 0 ;; esac
[ -f %[1]q ] || exit 0
pidof sing-box >/dev/null 2>&1 || exit 0
# Best-effort kernel module preload. Absent .ko or built-in modules are
# silently skipped — iptables-restore will surface the final verdict anyway.
KREL="$(uname -r)"
for mod in xt_TPROXY xt_comment xt_mark xt_connmark xt_conntrack xt_pkttype; do
  grep -q "^${mod} " /proc/modules 2>/dev/null && continue
  [ -f "/lib/modules/${KREL}/${mod}.ko" ] && insmod "/lib/modules/${KREL}/${mod}.ko" 2>/dev/null || true
done
# A chain is "ok" only if it EXISTS and PREROUTING actually jumps into it.
# NDMS rebuilds PREROUTING and wipes our AWGM PREROUTING jumps while leaving
# the custom chains intact; gating on chain existence alone would skip the
# restore in exactly that case, leaving interception silently dead.
mangle_ok=0; nat_ok=0
/opt/sbin/iptables -w -t mangle -nL %[2]s >/dev/null 2>&1 \
  && /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[2]s($| )' \
  && mangle_ok=1
/opt/sbin/iptables -w -t nat -nL %[6]s >/dev/null 2>&1 \
  && /opt/sbin/iptables -w -t nat -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[6]s($| )' \
  && nat_ok=1
if [ "$mangle_ok" -eq 0 ] || [ "$nat_ok" -eq 0 ]; then
  /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null \
    | grep -E -- '-[jg] %[2]s($| )' \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t mangle $line 2>/dev/null; done
  /opt/sbin/iptables -w -t nat -S PREROUTING 2>/dev/null \
    | grep -E -- '-[jg] %[6]s($| )' \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t nat $line 2>/dev/null; done
  # Scrub DNS-RESCUE direct PREROUTING rules in nat (identified by
  # comment tag, not by jump target — these are -j REDIRECT rules,
  # not chain jumps). Same drop-and-restore approach as above, just
  # matched via the iptables-save comment serialisation.
  /opt/sbin/iptables -w -t nat -S PREROUTING 2>/dev/null \
    | grep -E -- '--comment "?%[7]s' \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t nat $line 2>/dev/null; done
  # Legacy DNS-NOPOLICY MARK rules in mangle (dead code from earlier
  # AWGM builds). Always scrub so upgrades don't leave dangling rules
  # accumulating across NDMS reloads.
  /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null \
    | grep -E -- '--comment "?%[8]s' \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t mangle $line 2>/dev/null; done
  # Ingress-scope MARK/CONNMARK rules in mangle (comment-tagged).
  /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null \
    | grep -E -- '--comment "?%[9]s' \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t mangle $line 2>/dev/null; done
  /opt/sbin/iptables-restore --noflush < %[1]q
  /opt/sbin/ip rule add fwmark 0x%[3]x table %[4]d priority %[5]d 2>/dev/null || true
  /opt/sbin/ip route add local 0.0.0.0/0 dev lo table %[4]d 2>/dev/null || true
  logger -t awgm-tproxy "netfilter.d: restored AWGM chains after NDMS reload"
fi
`, netfilterRulesPath, ChainName, Fwmark, RoutingTable, IPRulePriority, RedirectChain, DNSRescueTag, DNSNoPolicyTag, IngressTag)
}

// removeNetfilterRulesFile deletes the persisted rules file so the
// netfilter.d hook becomes a no-op on the next NDMS reload. Called on
// engine Disable. Idempotent.
func removeNetfilterRulesFile() {
	_ = os.Remove(netfilterRulesPath)
}

// refreshNetfilterHookIfPresent rewrites the netfilter.d hook script
// when one is already installed, so older versions get the current
// pidof guard on daemon startup. No-op when the file is absent —
// Install creates it on first Enable.
func refreshNetfilterHookIfPresent() {
	if _, err := os.Stat(netfilterHookPath); err != nil {
		return
	}
	_ = writeNetfilterHook()
}

func (it *IPTables) Uninstall(ctx context.Context) error {
	if it.cleanupHook != nil {
		it.cleanupHook()
	}
	it.removeSourceHooks(ctx)
	_ = it.runIPTables(ctx, "-t", "mangle", "-F", ChainName)
	_ = it.runIPTables(ctx, "-t", "mangle", "-X", ChainName)
	_ = it.runIPTables(ctx, "-t", "nat", "-F", RedirectChain)
	_ = it.runIPTables(ctx, "-t", "nat", "-X", RedirectChain)
	// Drain ALL fwmark rules — historically Install accumulated
	// duplicates at priorities 0-N (auto-assigned), so a single `del`
	// would leave the rest. Loop until ENOENT, capped defensively.
	for i := 0; i < maxIPRuleDrainPasses; i++ {
		if err := it.runIP(ctx, "rule", "del", "fwmark", fmt.Sprintf("0x%x", Fwmark),
			"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
			break
		}
	}
	_ = it.runIP(ctx, "route", "flush", "table", fmt.Sprintf("%d", RoutingTable))
	return nil
}

func (it *IPTables) removeSourceHooks(ctx context.Context) {
	it.removeSourceHooksFromTable(ctx, "mangle", ChainName)
	it.removeSourceHooksFromTable(ctx, "nat", RedirectChain)
	// DNS-RESCUE: direct PREROUTING REDIRECT rules in nat, tagged with
	// `-m comment --comment AWGM-DNS-RESCUE`. Scrub before re-install
	// so we don't accumulate duplicates and so port changes (e.g. NDMS
	// reassigned ndnproxy:41100→41200) propagate cleanly.
	it.removeCommentTaggedRulesFromTable(ctx, "nat", "PREROUTING", DNSRescueTag)
	// Legacy: DNS-NOPOLICY MARK rules in mangle from 2.10.x and
	// earlier. Always scrub on Install for upgrade migration — the
	// rules are dead code now, but if left in place they'd accumulate
	// across upgrades.
	it.removeCommentTaggedRulesFromTable(ctx, "mangle", "PREROUTING", DNSNoPolicyTag)
	// Ingress-scope: direct MARK/CONNMARK rules in mangle PREROUTING,
	// tagged AWGM-INGRESS. Scrub before re-install so the list stays
	// idempotent and removed interfaces don't leave dangling marks.
	it.removeCommentTaggedRulesFromTable(ctx, "mangle", "PREROUTING", IngressTag)
}

// removeCommentTaggedRulesFromTable scrubs every rule in `chain` whose
// iptables-save output contains the given comment tag. Used for
// DNS-NOPOLICY where rules are direct PREROUTING entries (not jumps
// to a custom chain). The grep+sed pattern is the same approach as
// XKeen's removal logic — robust to rule ordering and matcher changes
// as long as the `-m comment --comment <tag>` survives serialisation.
func (it *IPTables) removeCommentTaggedRulesFromTable(ctx context.Context, table, chain, tag string) {
	result, err := sysexec.Run(ctx, sysiptables.Binary, "-w", "-t", table, "-S", chain)
	if err != nil || result == nil {
		return
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if !strings.Contains(line, `--comment "`+tag+`"`) && !strings.Contains(line, `--comment `+tag) {
			continue
		}
		if !strings.HasPrefix(line, "-A "+chain+" ") {
			continue
		}
		delLine := "-D " + strings.TrimPrefix(line, "-A ")
		args := append([]string{"-w", "-t", table}, strings.Fields(delLine)...)
		_, _ = sysexec.Run(ctx, sysiptables.Binary, args...)
	}
}

func (it *IPTables) removeSourceHooksFromTable(ctx context.Context, table, chain string) {
	result, err := sysexec.Run(ctx, sysiptables.Binary, "-w", "-t", table, "-S", "PREROUTING")
	if err != nil || result == nil {
		return
	}
	// Match both `-j chain` (old jump syntax pre-fastnat-fix) and
	// `-g chain` (current goto syntax) so upgrading installs scrub
	// stale jumps from previous versions before we re-append the new one.
	jumpJ := "-j " + chain
	gotoG := "-g " + chain
	for _, line := range strings.Split(result.Stdout, "\n") {
		if !strings.Contains(line, jumpJ) && !strings.Contains(line, gotoG) {
			continue
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A PREROUTING") {
			continue
		}
		deleteLine := strings.Replace(line, "-A PREROUTING", "-D PREROUTING", 1)
		args := append([]string{"-t", table}, strings.Fields(deleteLine)...)
		_ = it.runIPTables(ctx, args...)
	}
}

// EnsureRouterNetfilterModules best-effort preloads the remaining xt_*
// modules that our iptables rules reference but that TPROXY preflight
// does not cover: comment, mark, connmark, conntrack, pkttype.
// ErrNetfilterComponentMissing (module absent or built-in) is silently
// skipped. All other insmod errors are collected and returned without
// blocking — a hard failure here would prevent a working install on
// systems where the module is built-in or named differently. The caller
// can log the warnings and proceed.
func EnsureRouterNetfilterModules(ctx context.Context) []error {
	var errs []error
	for _, name := range []string{
		"xt_comment",
		"xt_mark",
		"xt_connmark",
		"xt_conntrack",
		"xt_pkttype",
	} {
		err := ensureKernelModuleFn(ctx, name)
		if err == nil || errors.Is(err, ErrNetfilterComponentMissing) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
	}
	return errs
}

// HasAnyInstalled returns true if at least one of the AWGM chains exists
// in the kernel. Used for the disabled-cleanup path: even a partial install
// (e.g. mangle chain present but nat chain missing after a failed upgrade)
// must trigger Uninstall so no stale remnants survive.
func (it *IPTables) HasAnyInstalled(ctx context.Context) bool {
	return it.runIPTables(ctx, "-t", "mangle", "-nL", ChainName) == nil ||
		it.runIPTables(ctx, "-t", "nat", "-nL", RedirectChain) == nil
}

// IsInstalled returns true only when both AWGM chains exist. Used for the
// enabled-reconcile path: if either chain is missing a full re-install is
// needed to reach a known-good state.
func (it *IPTables) IsInstalled(ctx context.Context) bool {
	if it.runIPTables(ctx, "-t", "mangle", "-nL", ChainName) != nil {
		return false
	}
	if it.runIPTables(ctx, "-t", "nat", "-nL", RedirectChain) != nil {
		return false
	}
	return true
}

// Probe reports the live interception state in two booleans, from a single
// `iptables -S <table>` per table (one exec each instead of separate -nL +
// -S PREROUTING calls):
//
//   - installed: both AWGM chains exist (the `-N AWGM-*` declarations).
//   - jumps: both chains are actually entered from PREROUTING.
//
// Chain existence is necessary but NOT sufficient: NDMS rebuilds PREROUTING on
// reconfig and can wipe our `-A PREROUTING ... -j AWGM-*` jumps while the
// custom chains survive, silently disabling interception. The jump match is
// anchored (`-j`/`-g` + chain + boundary), mirroring the netfilter.d hook, so
// it is mark-agnostic and not fooled by a substring.
//
// On a query error Probe returns (false, false, err); callers must treat that
// as "unknown" (do NOT reinstall) rather than "broken".
func (it *IPTables) Probe(ctx context.Context) (installed, jumps bool, err error) {
	mChain, mJump, err := it.probeTable(ctx, "mangle", ChainName)
	if err != nil {
		return false, false, err
	}
	nChain, nJump, err := it.probeTable(ctx, "nat", RedirectChain)
	if err != nil {
		return false, false, err
	}
	installed = mChain && nChain
	jumps = installed && mJump && nJump
	return installed, jumps, nil
}

func (it *IPTables) probeTable(ctx context.Context, table, chain string) (chainExists, jumpExists bool, err error) {
	out, err := it.runIPTablesOut(ctx, "-t", table, "-S")
	if err != nil {
		return false, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "-N "+chain {
			chainExists = true
		}
		if strings.HasPrefix(line, "-A PREROUTING ") && jumpToken(line, chain) {
			jumpExists = true
		}
	}
	return chainExists, jumpExists, nil
}

// jumpToken reports whether line jumps to chain via `-j chain` or `-g chain`
// as a whole token (followed by a space or end-of-line) — the Go equivalent of
// the hook's `-[jg] CHAIN($| )` regex, so `AWGM-TPROXY` never matches a longer
// `AWGM-TPROXY-X`.
func jumpToken(line, chain string) bool {
	for _, tok := range []string{"-j " + chain, "-g " + chain} {
		i := strings.Index(line, tok)
		if i < 0 {
			continue
		}
		if end := i + len(tok); end == len(line) || line[end] == ' ' {
			return true
		}
	}
	return false
}
