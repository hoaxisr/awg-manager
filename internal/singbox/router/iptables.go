package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
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
	// BlackholeChain is the fail-closed DROP chain (mangle). It is engaged
	// ONLY while sing-box is dead AND the PREROUTING interception jumps were
	// wiped (e.g. an NDMS firewall reload): without it, policy-marked traffic
	// would fall through to the normal Keenetic routing table and LEAK to WAN
	// unencrypted, even though the user expects it to go through proxy/AWG.
	// The chain carries the SAME LAN/router/WAN RETURN exclusions as the
	// interception chains, then a terminal DROP, and is removed the moment the
	// engine recovers. Traffic is dropped, never leaked.
	BlackholeChain = "AWGM-BLACKHOLE"
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
	netfilterHookPath      = "/opt/etc/ndm/netfilter.d/50-awgm-tproxy.sh"
	netfilterRulesPath     = "/opt/etc/awg-manager/singbox/router-netfilter.rules"
	netfilterBlackholePath = "/opt/etc/awg-manager/singbox/router-blackhole.rules"

	// ipBinary — Entware-овский iproute2, тем же абсолютным путём, что и
	// sysiptables.Binary рядом. Раньше здесь стояло голое "ip": какой бинарь
	// возьмётся, зависело от PATH демона, а в прошивке есть свой /sbin/ip от
	// busybox с другим набором возможностей. Любое утверждение о поведении
	// `ip` на роутере при таком вызове недоказуемо — неизвестно, чьё поведение
	// наблюдали.
	ipBinary = "/opt/sbin/ip"
	// Per-table copies of the rules blob (issue #627): the netfilter.d hook
	// restores ONLY the table NDMS actually wiped, so a routine mangle-only
	// reload (DHCP renew) never tears down the working nat chain and vice
	// versa. The combined file above stays as the full-rebuild fallback.
	netfilterMangleRulesPath = "/opt/etc/awg-manager/singbox/router-netfilter-mangle.rules"
	netfilterNatRulesPath    = "/opt/etc/awg-manager/singbox/router-netfilter-nat.rules"
	// netfilterCtCleanPath is the poisoned-flow eviction script (issue #627),
	// invoked by the hook after a TPROXY restore and by Install.
	netfilterCtCleanPath = "/opt/etc/awg-manager/singbox/awgm-ctclean.sh"
	// bypassSavePath is the `ipset save` dump of AWGM-BYPASS written after every
	// rebuild. ipsets live in RAM, so it is the only way the set survives a
	// reboot: the netfilter.d hook restores it before any iptables-restore.
	bypassSavePath = "/opt/etc/awg-manager/singbox/bypass.ipset"
	// netfilterPolicyTunDNSHookPath — хук перехвата DNS в policy-tun.
	// Отдельный от 50-го: тот в этом режиме существует только под классы
	// QoS, его детектор завязан на цепочку AWGM-REDIRECT (которой без QoS
	// нет) и он гейтится на pidof sing-box, что несовместимо с fail-closed.
	netfilterPolicyTunDNSHookPath = "/opt/etc/ndm/netfilter.d/52-awgm-policytun-dns.sh"
)

// bypassSetName is the ipset holding the geoip bypass ranges — aliased from
// the bypassset sub-package so the name has exactly one definition.
const bypassSetName = bypassset.SetName

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

// tproxyTargetProbe — шов над пробой iptables: тесты подменяют, прод зовёт
// IsTProxyTargetAvailable.
var tproxyTargetProbe = IsTProxyTargetAvailable

// EnsureXtDscpModule best-effort loads xt_dscp so the QoS `-m dscp` dispatch
// rules can be accepted at iptables-restore COMMIT time. Same soft-fail
// contract as EnsureCommentModule: an absent .ko (possibly built-in kernel
// support) returns nil and lets iptables-restore surface the real verdict.
func EnsureXtDscpModule(ctx context.Context) error {
	err := ensureKernelModuleFn(ctx, "xt_dscp")
	if errors.Is(err, ErrNetfilterComponentMissing) {
		return nil
	}
	return err
}

var (
	xtDscpMu        sync.Mutex
	xtDscpModuleOK  bool
	xtDscpMatchOK   bool
	xtDscpCheckedAt time.Time
	// xtDscpAvailabilityFn is the indirection point tests use to count/stub
	// raw probes; production always runs the real XtDscpAvailability.
	xtDscpAvailabilityFn = XtDscpAvailability
)

// xtDscpNegativeTTL bounds how long a NEGATIVE xt_dscp probe result is
// served from cache. The probe execs `iptables -m dscp -h` — running it on
// every reconcile tick forever while the module stays missing is pure waste,
// but installing the missing piece must still be picked up without a daemon
// restart, hence the re-probe after the TTL. Positive results are cached
// forever (availability never degrades within one daemon lifetime).
const xtDscpNegativeTTL = 10 * time.Minute

// XtDscpAvailability probes the two independent halves of `-m dscp` support
// separately so status/diagnostics can tell the failure causes apart:
//
//   - moduleOK: the xt_dscp KERNEL module is loaded (/proc/modules) or its
//     .ko exists at the standard /lib/modules/<uname -r>/xt_dscp.ko path —
//     the exact pattern ensureKernelModule targets. Keenetic ships the .ko
//     there even on 4.9-ndm kernels. (Some third-party setups also carry
//     modules under /opt/lib/modules or /opt/lib/system-modules/<uname -r>
//     — XKeen issue #94 — but AWGM's whole module stack, xt_TPROXY
//     included, keys on the single standard path; keep parity here.)
//   - matchOK: the iptables USERSPACE extension parses `-m dscp` (runtime
//     `iptables -m dscp -h` probe, mirroring IsTProxyTargetAvailable). This
//     is the realistic gap on some Entware arches — the kernel module can be
//     present while the iptables build lacks the extension.
func XtDscpAvailability(ctx context.Context) (moduleOK, matchOK bool) {
	moduleOK = isModuleLoaded("xt_dscp")
	if !moduleOK {
		if kernel := osdetect.KernelRelease(); kernel != "" {
			_, err := os.Stat(filepath.Join("/lib/modules", kernel, "xt_dscp.ko"))
			moduleOK = err == nil
		}
	}
	res, err := sysexec.Run(ctx, sysiptables.Binary, "-m", "dscp", "-h")
	matchOK = err == nil && res != nil &&
		strings.Contains(strings.ToLower(res.Stdout+res.Stderr), "dscp")
	return moduleOK, matchOK
}

// cachedXtDscpAvailability returns the (moduleOK, matchOK) pair through the
// probe cache: a fully-positive result is cached forever, anything else for
// xtDscpNegativeTTL (see the const above). All availability consumers
// (IsXtDscpAvailable, GetStatus diagnostics) go through this so a missing
// module costs at most one `iptables -m dscp -h` exec per TTL window instead
// of one per reconcile tick / status poll.
func cachedXtDscpAvailability(ctx context.Context) (moduleOK, matchOK bool) {
	xtDscpMu.Lock()
	if xtDscpModuleOK && xtDscpMatchOK {
		xtDscpMu.Unlock()
		return true, true
	}
	if !xtDscpCheckedAt.IsZero() && time.Since(xtDscpCheckedAt) < xtDscpNegativeTTL {
		m, x := xtDscpModuleOK, xtDscpMatchOK
		xtDscpMu.Unlock()
		return m, x
	}
	probe := xtDscpAvailabilityFn
	xtDscpMu.Unlock()

	m, x := probe(ctx)
	xtDscpMu.Lock()
	xtDscpModuleOK, xtDscpMatchOK = m, x
	xtDscpCheckedAt = time.Now()
	xtDscpMu.Unlock()
	return m, x
}

// IsXtDscpAvailable reports whether DSCP matching is usable end-to-end: BOTH
// the kernel module (loaded or .ko on disk) AND the iptables extension must
// pass. Positive results are cached forever (availability never degrades
// within one daemon lifetime); negative results are cached for
// xtDscpNegativeTTL and then re-probed so installing the missing piece is
// picked up without a restart. Surfaced to the UI as the status field
// `xtDscpAvailable`.
func IsXtDscpAvailable(ctx context.Context) bool {
	moduleOK, matchOK := cachedXtDscpAvailability(ctx)
	return moduleOK && matchOK
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

	// BypassUDPPorts lists UDP destination ports/ranges that should RETURN from
	// AWGM-TPROXY before the catch-all TPROXY rule — they bypass sing-box
	// entirely and route as if no policy were active.
	BypassUDPPorts []PortRange

	// BypassTCPPorts lists TCP destination ports/ranges that should RETURN from
	// AWGM-REDIRECT before the catch-all REDIRECT rule.
	// Note: port 79 (NDMS admin) is always excluded by a hardcoded rule;
	// including 79 here produces a harmless duplicate RETURN rule.
	BypassTCPPorts []PortRange

	// BypassCIDRs — пользовательские IPv4 IP/CIDR назначения, чей трафик
	// целиком (включая DNS/53) идёт мимо sing-box: ранний `-j RETURN` в начале
	// ОБЕИХ цепочек (в mangle — до правила --dport 53). Канонизированы
	// resolveBypassSubnets. Эмитятся отдельно от bypassCIDRs/WANIPs, у которых
	// другая семантика (RETURN после перехвата DNS).
	BypassCIDRs []string

	// IngressInterfaces — уже резолвленные kernel-имена (напр. "nwg3"),
	// чей ingress-трафик помечается policy-меткой в mangle PREROUTING до
	// connmark-jump'а. Пусто / MatchAll / пустой PolicyMark = no-op.
	IngressInterfaces []string

	// BypassGeoIPSet, when true, вставляет `-m set --match-set AWGM-BYPASS dst
	// -j RETURN` рядом с пользовательскими bypass-CIDR — в начале AWGM-TPROXY,
	// AWGM-REDIRECT и blackhole, ДО перехвата :53: полный обход, включая DNS
	// (та же семантика, что у BypassCIDRs). Включается при непустом списке
	// geoip-тегов. Требует загруженного модуля xt_set и живого набора —
	// иначе iptables-restore падает целиком.
	BypassGeoIPSet bool

	// DSCPOnly — режим policy-tun: netfilter нужен ТОЛЬКО для QoS-DSCP-классов,
	// основной трафик идёт NDMS-политикой в tun-интерфейс sing-box. Цепочки
	// содержат лишь bypass-RETURN'ы и dscp-диспатч: ни catch-all, ни перехвата
	// DNS (основных tproxy/redirect-инбаундов в этом режиме нет), ни
	// ingress-MARK/DNS-RESCUE. Install в этом режиме также не
	// оставляет blackhole (fail-closed тут не нужен — трафик и так уходит в
	// tun, а не мимо него).
	DSCPOnly bool

	// QoSClasses lists the active DSCP QoS classes (issue #371). Each entry
	// yields one `-m dscp --dscp N` dispatch rule per chain (mangle UDP
	// TPROXY --on-port TProxyPort, nat TCP REDIRECT --to-ports RedirectPort)
	// inserted immediately before the catch-all — see the emission sites in
	// buildRestoreInput for the full ordering rationale. Empty = feature off.
	// Requires the xt_dscp kernel module (preloaded by the netfilter.d hook
	// and EnsureRouterNetfilterModules).
	QoSClasses []QoSClassSpec
}

// QoSClassSpec is the iptables projection of one active QoS class: the DSCP
// codepoint to match and the per-class sing-box listen ports to dispatch to.
// Ports come from QoSClassPorts so iptables and inbound generation share one
// source of truth. Comparable — reconcile change-detection uses slices.Equal.
type QoSClassSpec struct {
	DSCP         int
	TProxyPort   int
	RedirectPort int
}

// equalInstalledSpec и equalBlackholeSpec сравнивают ЖЕЛАЕМЫЙ спек с
// ПРИМЕНЁННЫМ. nil означает «в netfilter ничего нашего не установлено» и равен
// только nil.
//
// Сравнивается не спек, а его РЕНДЕР — ровно тот текст, что уходит в
// iptables-restore и в файл правил для netfilter.d-хука. Отсюда два следствия,
// которых у сравнения поле-в-поле не было:
//
//   - Полнота по построению. Поле, которого нет в рендере, доказуемо не может
//     изменить netfilter, и игнорировать его правильно. Обратное — «забыли
//     поле в компараторе» — становится невыразимым: рукописный список полей,
//     который надо было сопровождать вручную, исчез.
//   - Ловится дрейф самого рендерера: правка bypassCIDRs или emitBypassReturns
//     теперь даёт переустановку, а не молчание до следующего рестарта демона.
//
// nil-слайс и пустой дают одинаковый текст, поэтому причина, по которой был
// отвергнут reflect.DeepEqual (первый же коллектор с []string{} вместо nil
// давал бы переустановку на каждом тике), снимается сама.
//
// Компаратора ДВА, потому что ресурса два и рендеры у них разные: перехват —
// mangle+nat целиком, блокировка — свой mangle-блоб без QoS-диспатча,
// ingress-MARK и DNS-RESCUE. Сравнивать блокировку рендером перехвата значило
// бы переустанавливать её на смену входов, которых в её правилах нет.
func equalInstalledSpec(a, b *RestoreInputSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return buildRestoreInput(*a) == buildRestoreInput(*b)
}

func equalBlackholeSpec(a, b *RestoreInputSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return buildBlackholeRestoreInput(*a) == buildBlackholeRestoreInput(*b)
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

// emitUserBypassReturns эмитит ранний `-A <chain> -d <cidr> -j RETURN` для
// каждого пользовательского CIDR. Вызывается в НАЧАЛЕ обеих цепочек (mangle до
// правила --dport 53, nat до catch-all), чтобы исключение было буквальным —
// весь трафик к подсети идёт мимо sing-box, включая DNS.
func emitUserBypassReturns(b *strings.Builder, chain string, cidrs []string) {
	for _, cidr := range cidrs {
		fmt.Fprintf(b, "-A %s -d %s -j RETURN\n", chain, cidr)
	}
}

// emitBypassSetReturn эмитит ранний `-j RETURN` для адресов из geoip-набора
// AWGM-BYPASS. Ставится сразу за пользовательскими bypass-CIDR (emitUserBypassReturns)
// — то есть в начале цепочки, ДО перехвата :53: обход полный, включая DNS.
// В режиме policy-tun (DSCPOnly) не эмитится — там нет ни catch-all, ни
// перехвата, обходить нечего.
func emitBypassSetReturn(b *strings.Builder, chain string, spec RestoreInputSpec) {
	if !spec.BypassGeoIPSet || spec.DSCPOnly {
		return
	}
	fmt.Fprintf(b, "-A %s -m set --match-set %s dst -j RETURN\n", chain, bypassSetName)
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

// buildBlackholeRestoreInput renders the mangle-only *AWGM-BLACKHOLE* blob:
// the SAME LAN/router/WAN RETURN exclusions as the interception chain, then a
// terminal DROP, entered from PREROUTING by the identical policy selector
// (emitPreroutingJump). So the set it drops is exactly the set that would have
// entered AWGM-TPROXY — nothing local is caught, and no policy traffic escapes.
// mangle (not nat) so every packet of a flow is matched while the tunnel is
// down, not only the first packet of a new conntrack.
func buildBlackholeRestoreInput(spec RestoreInputSpec) string {
	var b strings.Builder
	b.WriteString("*mangle\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", BlackholeChain)
	// User bypass first — an explicitly excluded subnet must never be dropped.
	emitUserBypassReturns(&b, BlackholeChain, spec.BypassCIDRs)
	// geoip-bypass: эти адреса и при живом движке идут мимо sing-box — мёртвый
	// движок тем более не должен их дропать.
	emitBypassSetReturn(&b, BlackholeChain, spec)
	// User bypass ports (BOTH protocols): traffic the user deliberately keeps off
	// the proxy (STUN/VoIP/WireGuard/games) must go direct, not be dropped. The
	// blackhole matches every protocol (connmark on the jump, no -p filter), so it
	// must honour the TCP ports too — even though real TCP interception lives in
	// the nat chain, the fail-closed DROP here is the one place TCP policy traffic
	// is dropped while the engine is down.
	for _, pr := range spec.BypassUDPPorts {
		fmt.Fprintf(&b, "-A %s -p udp --dport %s -j RETURN\n", BlackholeChain, pr.String())
	}
	for _, pr := range spec.BypassTCPPorts {
		fmt.Fprintf(&b, "-A %s -p tcp --dport %s -j RETURN\n", BlackholeChain, pr.String())
	}
	// LAN/loopback/CGNAT/multicast + router-owned WAN IPs: reused verbatim from
	// the interception chain so the exclusion set cannot drift and the blackhole
	// can never over-block router-local, LAN-to-LAN, or management traffic.
	emitBypassReturns(&b, BlackholeChain, spec.WANIPs)
	// Everything else that belongs to the policy: drop it (fail-closed).
	fmt.Fprintf(&b, "-A %s -j DROP\n", BlackholeChain)
	// Same PREROUTING selector as the real interception jump (connmark policy
	// filter, or MatchAll), so exactly the policy traffic is diverted here.
	emitPreroutingJump(&b, BlackholeChain, spec)
	b.WriteString("COMMIT\n")
	return b.String()
}

// buildRestoreInput renders the full iptables-restore blob — the mangle and
// nat sections concatenated. Install and the hook's full-rebuild fallback
// consume this; the hook's per-table fast heal consumes the sections
// individually (issue #627).
func buildRestoreInput(spec RestoreInputSpec) string {
	return buildMangleRestoreInput(spec) + buildNatRestoreInput(spec)
}

func buildMangleRestoreInput(spec RestoreInputSpec) string {
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

	// Пользовательский bypass — целиком мимо sing-box, ДО перехвата DNS.
	emitUserBypassReturns(&b, ChainName, spec.BypassCIDRs)
	emitBypassSetReturn(&b, ChainName, spec)

	// Bypass ports: RETURN first — before DNS intercept and catch-all so that
	// any explicitly excluded port skips sing-box entirely (including port 53).
	for _, pr := range spec.BypassUDPPorts {
		fmt.Fprintf(&b, "-A %s -p udp --dport %s -j RETURN\n", ChainName, pr.String())
	}

	// policy-tun QoS-гибрид: DNS никогда не входит в QoS-диспатчинг (основных
	// tproxy/redirect-инбаундов в этом режиме НЕТ — перехваченный DNS ушёл бы
	// в никуда), catch-all отсутствует — основной трафик идёт NDMS-политикой в
	// tun. Bypass-RETURN'ы обязательны и здесь: без них DSCP-меченный
	// LAN-to-LAN (или трафик на WAN-IP роутера) уехал бы в sing-box.
	if spec.DSCPOnly {
		fmt.Fprintf(&b, "-A %s -p udp --dport 53 -j RETURN\n", ChainName)
		emitBypassReturns(&b, ChainName, spec.WANIPs)
		for _, q := range spec.QoSClasses {
			fmt.Fprintf(&b, "-A %s -p udp -m dscp --dscp %d -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
				ChainName, q.DSCP, q.TProxyPort, Fwmark)
		}
		emitPreroutingJump(&b, ChainName, spec)
		b.WriteString("COMMIT\n")
		return b.String()
	}

	// set_chain_rules: DNS first (when INTERCEPT_DNS_ENABLE=1)
	fmt.Fprintf(&b, "-A %s -p udp --dport 53 -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
		ChainName, TPROXYPort, Fwmark)

	// set_chain_rules: bypass set. SKeen uses one ipset rule; we render
	// the same destinations as discrete CIDR rules (semantically equal).
	emitBypassReturns(&b, ChainName, spec.WANIPs)

	// QoS-by-DSCP dispatch (issue #371): per-class TPROXY onto the class
	// port, immediately BEFORE the catch-all. Ordering rationale:
	//   - AFTER the DNS intercept: UDP/53 must keep landing on the MAIN
	//     tproxy port regardless of DSCP marks — hijack-dns lives there, so
	//     DNS handling never depends on the managed QoS route rules (the nat
	//     chain mirrors this with its own TCP/53 carve-out before the class
	//     rules).
	//   - AFTER user/builtin bypass RETURNs and WAN-IP exclusions: an
	//     explicit bypass always wins; DSCP marks must not re-capture
	//     traffic the user excluded (or loop router-WAN-IP traffic back in).
	//   - BEFORE the catch-all: otherwise the unconditional TPROXY eats the
	//     packet first and the class rule is dead.
	for _, q := range spec.QoSClasses {
		fmt.Fprintf(&b, "-A %s -p udp -m dscp --dscp %d -j TPROXY --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x\n",
			ChainName, q.DSCP, q.TProxyPort, Fwmark)
	}

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
	return b.String()
}

func buildNatRestoreInput(spec RestoreInputSpec) string {
	var b strings.Builder

	// ---- *nat table: TCP via REDIRECT ----
	// Literal port of `add_redirect_rules` from reference/SKeen/skeen.sh
	// (hybrid mode, nat table). SKeen's nat chain has ONLY the bypass set
	// + catch-all `-p tcp -j REDIRECT`, which already covers TCP/53. The
	// QoS DSCP dispatch needs its own TCP/53 carve-out (a class REDIRECT
	// sits before the catch-all and would otherwise swallow marked TCP/53
	// onto a class port), emitted with the class rules below.
	b.WriteString("*nat\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", RedirectChain)

	emitUserBypassReturns(&b, RedirectChain, spec.BypassCIDRs)
	emitBypassSetReturn(&b, RedirectChain, spec)

	// policy-tun QoS-гибрид: зеркало mangle-ветки — только bypass и dscp.
	// Без catch-all REDIRECT'а, без перехвата DNS и DNS-RESCUE, без правила
	// на порт 79: REDIRECT здесь делает лишь dscp-диспатч, а трафик к веб-морде
	// роутера DSCP-классов не несёт.
	if spec.DSCPOnly {
		for _, pr := range spec.BypassTCPPorts {
			fmt.Fprintf(&b, "-A %s -p tcp --dport %s -j RETURN\n", RedirectChain, pr.String())
		}
		fmt.Fprintf(&b, "-A %s -p tcp --dport 53 -j RETURN\n", RedirectChain)
		emitBypassReturns(&b, RedirectChain, spec.WANIPs)
		for _, q := range spec.QoSClasses {
			fmt.Fprintf(&b, "-A %s -p tcp -m dscp --dscp %d -j REDIRECT --to-ports %d\n",
				RedirectChain, q.DSCP, q.RedirectPort)
		}
		emitPreroutingJump(&b, RedirectChain, spec)
		b.WriteString("COMMIT\n")
		return b.String()
	}

	emitBypassReturns(&b, RedirectChain, spec.WANIPs)
	// Bypass router admin port so we don't redirect our own UI traffic.
	// (SKeen has equivalent dynamic admin-port discovery — same intent.)
	fmt.Fprintf(&b, "-A %s -p tcp --dport 79 -j RETURN\n", RedirectChain)

	// Bypass ports: RETURN before catch-all TCP REDIRECT.
	for _, pr := range spec.BypassTCPPorts {
		fmt.Fprintf(&b, "-A %s -p tcp --dport %s -j RETURN\n", RedirectChain, pr.String())
	}

	// QoS-by-DSCP dispatch for TCP — mirrors the mangle block above (same
	// ordering rationale: after bypasses, before the catch-all). DNS
	// carve-out first: without it, DSCP-marked DNS-over-TCP (or a
	// truncated-UDP retry) would land on a CLASS redirect inbound and only
	// get hijacked if the managed route rules happened to order right —
	// intercepting TCP/53 onto the MAIN redirect port here kills that whole
	// leak class at the netfilter level, exactly like the mangle chain's
	// unconditional UDP/53 intercept above the UDP class rules.
	if len(spec.QoSClasses) > 0 {
		fmt.Fprintf(&b, "-A %s -p tcp --dport 53 -j REDIRECT --to-ports %d\n",
			RedirectChain, RedirectPort)
		for _, q := range spec.QoSClasses {
			fmt.Fprintf(&b, "-A %s -p tcp -m dscp --dscp %d -j REDIRECT --to-ports %d\n",
				RedirectChain, q.DSCP, q.RedirectPort)
		}
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
type persistRulesFn func(combined, mangle, nat string) error

type IPTables struct {
	restoreNoflush restoreNoflushFn
	runIPTables    runFn
	runIPTablesOut runOutFn
	runIP          runFn
	runIPOut       runOutFn
	persistRules   persistRulesFn
	persistHook    func(includeBlackhole bool) error
	cleanupHook    func()
	// persistPolicyTunDNSHook / cleanupPolicyTunDNSHook — доставка и снос
	// хука перехвата DNS в policy-tun.
	persistPolicyTunDNSHook func(script string) error
	cleanupPolicyTunDNSHook func()
	persistBlackhole        persistFn
	cleanupBlackhole        func()
	runCtClean              func(ctx context.Context)
}

func NewIPTables() *IPTables {
	return &IPTables{
		restoreNoflush: sysiptables.RestoreNoflush,
		runIPTables:    sysiptables.Run,
		runIPTablesOut: sysiptables.RunOutput,
		runIP: func(ctx context.Context, args ...string) error {
			result, err := sysexec.Run(ctx, ipBinary, args...)
			return sysexec.FormatError(result, err)
		},
		runIPOut: func(ctx context.Context, args ...string) (string, error) {
			result, err := sysexec.Run(ctx, ipBinary, args...)
			if err != nil {
				return "", sysexec.FormatError(result, err)
			}
			if result == nil {
				return "", nil
			}
			return result.Stdout, nil
		},
		persistRules: writeNetfilterRulesFiles,
		persistHook:  writeNetfilterHook,
		cleanupHook:  removeNetfilterRulesFile,

		persistPolicyTunDNSHook: writePolicyTunDNSHook,
		cleanupPolicyTunDNSHook: removePolicyTunDNSHook,

		persistBlackhole: writeNetfilterBlackholeRulesFile,
		cleanupBlackhole: removeNetfilterBlackholeRulesFile,
		runCtClean: func(ctx context.Context) {
			// Best-effort: the script self-logs via logger; a failure here
			// must never fail Install (interception is already up).
			_, _ = sysexec.Run(ctx, netfilterCtCleanPath)
		},
	}
}

// InstallBlackhole engages the fail-closed DROP chain: scrub any stale jump,
// persist the blackhole rules (so the netfilter.d hook can re-assert it after
// an NDMS reload while the engine is still dead), then restore it via
// iptables-restore --noflush. It reuses the same hook it.persistHook already
// wrote — only the blackhole rules file is blackhole-specific. No fwmark/ip
// rule: the blackhole only drops, it never delivers to a table. Idempotent.
func (it *IPTables) InstallBlackhole(ctx context.Context, spec RestoreInputSpec) error {
	it.removeSourceHooksFromTable(ctx, "mangle", BlackholeChain)
	input := buildBlackholeRestoreInput(spec)
	if it.persistBlackhole != nil {
		if err := it.persistBlackhole(input); err != nil {
			return fmt.Errorf("write blackhole rules: %w", err)
		}
	}
	if err := it.restoreNoflush(ctx, input); err != nil {
		return fmt.Errorf("iptables-restore blackhole: %w", err)
	}
	return nil
}

// RemoveBlackhole tears down the fail-closed DROP chain: delete the rules file
// (so the hook stops re-asserting it), scrub the PREROUTING jump, then flush +
// delete the chain. Idempotent — safe to call when no blackhole is present.
func (it *IPTables) RemoveBlackhole(ctx context.Context) {
	if it.cleanupBlackhole != nil {
		it.cleanupBlackhole()
	}
	it.removeSourceHooksFromTable(ctx, "mangle", BlackholeChain)
	_ = it.runIPTables(ctx, "-t", "mangle", "-F", BlackholeChain)
	_ = it.runIPTables(ctx, "-t", "mangle", "-X", BlackholeChain)
}

// drainFwmarkRules deletes every `ip rule` for our fwmark/table, looping until
// ENOENT (capped by maxIPRuleDrainPasses). Install historically accumulated
// duplicate rules at auto-assigned priorities, so a single del would leave the
// rest — both Install (pre-add cleanup) and Uninstall drain via this.
func (it *IPTables) drainFwmarkRules(ctx context.Context) {
	for i := 0; i < maxIPRuleDrainPasses; i++ {
		if err := it.runIP(ctx, "rule", "del", "fwmark", fmt.Sprintf("0x%x", Fwmark),
			"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
			break
		}
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

	mangle := buildMangleRestoreInput(spec)
	nat := buildNatRestoreInput(spec)
	input := mangle + nat
	if it.persistRules != nil {
		if err := it.persistRules(input, mangle, nat); err != nil {
			return fmt.Errorf("write netfilter rules: %w", err)
		}
	}
	if it.persistHook != nil {
		if err := it.persistHook(!spec.DSCPOnly); err != nil {
			return fmt.Errorf("write netfilter hook: %w", err)
		}
	}
	// policy-tun: blackhole в этом режиме не применяется — снести файл правил,
	// оставшийся от прежнего режима, иначе хук прежней установки продолжил бы
	// поднимать fail-closed DROP на каждой перезагрузке netfilter.
	if spec.DSCPOnly && it.cleanupBlackhole != nil {
		it.cleanupBlackhole()
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
	it.drainFwmarkRules(ctx)
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
	// The window between engine start and this Install is fail-open exactly
	// like an NDMS reload: UDP flows whose first packet slipped past the
	// absent TPROXY jump carry a direct WAN NAT in conntrack and would
	// bypass tproxy for life (issue #627) — evict them now that rules are up.
	if it.runCtClean != nil {
		it.runCtClean(ctx)
	}
	return nil
}

// Both files are consumed by OTHER software at arbitrary times (NDMS executes
// the hook on every firewall reload; the hook feeds the rules file to
// iptables-restore), so they must never be observable half-written — use the
// fsync'ed temp-file+rename writer, not a truncate-in-place WriteFile.
func writeNetfilterRulesFiles(combined, mangle, nat string) error {
	if err := storage.AtomicWritePerm(netfilterRulesPath, []byte(combined), 0644); err != nil {
		return err
	}
	if err := storage.AtomicWritePerm(netfilterMangleRulesPath, []byte(mangle), 0644); err != nil {
		return err
	}
	return storage.AtomicWritePerm(netfilterNatRulesPath, []byte(nat), 0644)
}

func writeNetfilterBlackholeRulesFile(input string) error {
	return storage.AtomicWritePerm(netfilterBlackholePath, []byte(input), 0644)
}

// removeNetfilterBlackholeRulesFile deletes the persisted blackhole rules so
// the netfilter.d hook stops re-asserting the fail-closed DROP on the next NDMS
// reload. Called when the engine recovers (blackhole removed). Idempotent.
func removeNetfilterBlackholeRulesFile() {
	_ = os.Remove(netfilterBlackholePath)
}

// blackholeRulesFilePresent — шов наблюдения ДОЛГОВЕЧНОЙ половины блокировки.
// Цепочку и джамп при живом sing-box сносит ALIVE-ветка netfilter.d-хука, а
// файл правил переживает и её, и рестарт демона: удаляет его только
// RemoveBlackhole. Наблюдать одни цепочки значит оставлять протухший файл, из
// которого DEAD-ветка хука поднимет блокировку со СТАРЫМИ исключениями при
// следующей смерти движка. Один stat на тик — дешевле безусловного
// RemoveBlackhole, который стоит четырёх вызовов iptables.
var blackholeRulesFilePresent = func() bool {
	_, err := os.Stat(netfilterBlackholePath)
	return err == nil
}

func writeNetfilterHook(includeBlackhole bool) error {
	if err := storage.AtomicWritePerm(netfilterHookPath, []byte(netfilterHookScript(includeBlackhole)), 0755); err != nil {
		return err
	}
	return storage.AtomicWritePerm(netfilterCtCleanPath, []byte(ctCleanScript()), 0755)
}

// policyTunDNSHookScript рендерит хук восстановления перехвата DNS в
// policy-tun. Pure (без I/O) — как и netfilterHookScript, чтобы тест мог
// проверить шелл через `sh -n`.
//
// Правила выписаны по одному на строку, без циклов по списку: BusyBox sh не
// умеет массивов, а склейка селекторов в строку с последующим разбиением по
// словам развалилась бы на первом же значении с пробелом.
//
// Гейта `pidof sing-box` НЕТ сознательно (спека §7.3): нет движка — трафик
// этой политики работать не должен, и DNS в том числе. Не «забытая проверка».
func policyTunDNSHookScript(spec FakeIPIngressSpec) string {
	var rules strings.Builder
	for _, sel := range spec.dnatSelectors() {
		for _, proto := range []string{"udp", "tcp"} {
			fmt.Fprintf(&rules,
				"/opt/sbin/iptables -w -t nat -I PREROUTING 1 %s -p %s --dport 53 -m comment --comment %s -j DNAT --to-destination %s || ok=0\n",
				strings.Join(sel, " "), proto, PolicyTunDNSTag, net.JoinHostPort(spec.TunDNS, "53"))
		}
	}
	return fmt.Sprintf(`#!/bin/sh
# awg-manager: восстановление перехвата DNS в policy-tun после перестройки
# firewall ndm. Инверсия проверки $type — как в остальных хуках: при пустом
# $type в какой-нибудь прошивке положительная проверка молча убила бы хук.
[ "$type" = "ip6tables" ] && exit 0
[ "$table" = "nat" ] || exit 0
# Преднагрузка модулей: в policy-tun БЕЗ классов QoS полного хука
# 50-awgm-tproxy.sh не существует, а он единственный, кто их грузит.
KREL="$(uname -r)"
for mod in xt_comment xt_connmark; do
  grep -q "^${mod} " /proc/modules 2>/dev/null && continue
  [ -f "/lib/modules/${KREL}/${mod}.ko" ] && insmod "/lib/modules/${KREL}/${mod}.ko" 2>/dev/null || true
done
# Guard от дублей: событие nat приходит и без фактического wipe, а вставка
# идёт -I PREROUTING 1.
/opt/sbin/iptables -w -t nat -S PREROUTING 2>/dev/null | grep -q %[1]s && exit 0
ok=1
%[2]sif [ "$ok" = 1 ]; then
  logger -t awgm-policytun-dns "restored policy DNS hijack"
else
  logger -t awgm-policytun-dns "FAILED to restore policy DNS hijack"
fi
exit 0
`, PolicyTunDNSTag, rules.String())
}

// writePolicyTunDNSHook пишет хук ТОЛЬКО при отличии от лежащего на диске.
// Сравнение по содержимому — весь механизм отслеживания изменений: набор
// марок меняется в NDMS без нашего участия, и запись «один раз на enable»
// оставила бы навсегда протухший файл.
func writePolicyTunDNSHook(script string) error {
	// Сверяем И содержимое, И права: файл со сбитым снаружи режимом NDMS
	// молча не исполнит, а по одному лишь совпадению байтов мы бы его не
	// переписали.
	if st, err := os.Stat(netfilterPolicyTunDNSHookPath); err == nil && st.Mode().Perm() == 0755 {
		if cur, rerr := os.ReadFile(netfilterPolicyTunDNSHookPath); rerr == nil && string(cur) == script {
			return nil
		}
	}
	return storage.AtomicWritePerm(netfilterPolicyTunDNSHookPath, []byte(script), 0755)
}

func removePolicyTunDNSHook() {
	_ = os.Remove(netfilterPolicyTunDNSHookPath)
}

// RemovePolicyTunDNSHook снимает файл хука перехвата DNS. Идемпотентно.
func (it *IPTables) RemovePolicyTunDNSHook() {
	if it.cleanupPolicyTunDNSHook != nil {
		it.cleanupPolicyTunDNSHook()
	}
}

// netfilterHookScript renders the netfilter.d hook with all placeholders
// substituted. Pure (no I/O) so a test can validate the generated shell with
// `sh -n`.
//
// Two mutually-exclusive paths, chosen by whether sing-box is alive:
//
//   - ALIVE: real interception governs. Scrub any lingering fail-closed
//     blackhole, then restore the AWGM chains if NDMS wiped their PREROUTING
//     jumps. Per-table fast heal (issue #627): a routine reload wipes ONE
//     table (DHCP renew → mangle), so only the broken table is scrubbed and
//     restored — the intact table's working interception is never torn down.
//     The full both-table rebuild stays as fallback for the upgrade window
//     when the per-table rules files don't exist yet. Scrub-before-restore:
//     restoring --noflush over a surviving jump would append a SECOND one.
//     The `-[jg]` regex covers both legacy `-j` and current `-g` syntax so
//     upgrades don't leave duplicates. After any TPROXY restore the
//     poisoned-flow eviction script runs (see ctCleanScript).
//   - DEAD: fail-closed. Re-assert the blackhole DROP (if its rules file exists
//     and the jump was wiped) so policy traffic can NEVER reach WAN while the
//     engine is down — the old hook simply exited here, leaving a leak window
//     until the next reconcile tick.
//
// includeBlackhole=false (режим policy-tun) убирает DEAD-ветку целиком: там
// blackhole не применяется — трафик уходит в tun по NDMS-политике, а не мимо
// netfilter, так что ронять его при мёртвом движке нечем и незачем.
func netfilterHookScript(includeBlackhole bool) string {
	deadBranch := "  exit 0\n"
	if includeBlackhole {
		deadBranch = fmt.Sprintf(`  # sing-box DEAD — fail-closed. Re-assert the blackhole DROP if its rules file
  # exists and the PREROUTING jump was wiped, so policy traffic cannot leak to
  # WAN while the engine is down.
  [ -f %[1]q ] || exit 0
  if ! /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[2]s($| )'; then
    /opt/sbin/iptables-restore --noflush < %[1]q
    logger -t awgm-tproxy "netfilter.d: re-asserted fail-closed blackhole (sing-box down)"
  fi
`, netfilterBlackholePath, BlackholeChain)
	}
	return fmt.Sprintf(`#!/bin/sh
[ "$type" = "ip6tables" ] && exit 0
case "$table" in mangle|nat) ;; *) exit 0 ;; esac
# Best-effort kernel module preload (both paths need these). Absent .ko or
# built-in modules are silently skipped — iptables-restore surfaces the verdict.
KREL="$(uname -r)"
for mod in xt_TPROXY xt_comment xt_mark xt_connmark xt_conntrack xt_pkttype xt_dscp xt_set; do
  grep -q "^${mod} " /proc/modules 2>/dev/null && continue
  [ -f "/lib/modules/${KREL}/${mod}.ko" ] && insmod "/lib/modules/${KREL}/${mod}.ko" 2>/dev/null || true
done
# Восстановление набора AWGM-BYPASS ДО любого iptables-restore: правила с
# "-m set" роняют ВЕСЬ restore (включая fail-closed blackhole), пока набора
# нет — а ipset'ы живут в RAM и после ребута пусты. Гейт по успеху create
# БЕЗ -exist: он проходит только когда набора не было (ребут) — тогда
# заливаем дамп; при живом наборе (NDMS дёргает хук до 18-21 раза за один
# flap) create падает "already exists" и restore пропускается, живой набор
# не перезаливается. Счётчик записей для гейта не годится: ядра Keenetic
# работают на ipset kernel protocol 6, где list не печатает "Number of
# entries" вовсе. Сам матч "-m set" — отдельный модуль xt_set, он в
# прерольном списке выше: набора без матча (и наоборот) мало.
if [ -f %[15]q ]; then
  if /opt/sbin/ipset create %[16]s hash:net maxelem %[17]d family inet 2>/dev/null; then
    /opt/sbin/ipset restore -exist < %[15]q
  fi
fi
# scrub_jumps <table> <chain>: delete every PREROUTING jump into <chain>.
scrub_jumps() {
  /opt/sbin/iptables -w -t "$1" -S PREROUTING 2>/dev/null \
    | grep -E -- "-[jg] $2(\$| )" \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t "$1" $line 2>/dev/null; done
}
# scrub_tagged <table> <tag>: delete comment-tagged direct PREROUTING rules.
scrub_tagged() {
  /opt/sbin/iptables -w -t "$1" -S PREROUTING 2>/dev/null \
    | grep -E -- "--comment \"?$2" \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do /opt/sbin/iptables -w -t "$1" $line 2>/dev/null; done
}
if pidof sing-box >/dev/null 2>&1; then
  # sing-box ALIVE — real interception governs. Drop any lingering fail-closed
  # blackhole first so a stale DROP never sits in front of the interception jump.
  scrub_jumps mangle %[11]s
  /opt/sbin/iptables -w -t mangle -F %[11]s 2>/dev/null
  /opt/sbin/iptables -w -t mangle -X %[11]s 2>/dev/null
  [ -f %[1]q ] || exit 0
  # A chain is "ok" only if it EXISTS and PREROUTING actually jumps into it.
  # NDMS rebuilds PREROUTING and wipes our AWGM jumps while leaving the custom
  # chains intact; gating on chain existence alone would skip the restore.
  mangle_ok=0; nat_ok=0
  /opt/sbin/iptables -w -t mangle -nL %[2]s >/dev/null 2>&1 \
    && /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[2]s($| )' \
    && mangle_ok=1
  /opt/sbin/iptables -w -t nat -nL %[6]s >/dev/null 2>&1 \
    && /opt/sbin/iptables -w -t nat -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[6]s($| )' \
    && nat_ok=1
  if [ "$mangle_ok" -eq 0 ] || [ "$nat_ok" -eq 0 ]; then
    if { [ "$mangle_ok" -eq 1 ] || [ -f %[12]q ]; } && { [ "$nat_ok" -eq 1 ] || [ -f %[13]q ]; }; then
      # Per-table fast heal (issue #627): restore only the broken table so a
      # routine mangle-only reload never interrupts the working nat chain
      # (and vice versa) for the whole heavy rebuild.
      if [ "$mangle_ok" -eq 0 ]; then
        scrub_jumps mangle %[2]s
        # Legacy DNS-NOPOLICY MARK rules (dead code from earlier builds).
        scrub_tagged mangle %[8]s
        # Ingress-scope MARK/CONNMARK rules (comment-tagged).
        scrub_tagged mangle %[9]s
        /opt/sbin/iptables-restore --noflush < %[12]q
        /opt/sbin/ip rule add fwmark 0x%[3]x table %[4]d priority %[5]d 2>/dev/null || true
        /opt/sbin/ip route add local 0.0.0.0/0 dev lo table %[4]d 2>/dev/null || true
        logger -t awgm-tproxy "netfilter.d: fast-restored mangle AWGM chain after NDMS reload"
      fi
      if [ "$nat_ok" -eq 0 ]; then
        scrub_jumps nat %[6]s
        # DNS-RESCUE direct PREROUTING rules in nat (comment-tagged -j REDIRECT).
        scrub_tagged nat %[7]s
        /opt/sbin/iptables-restore --noflush < %[13]q
        logger -t awgm-tproxy "netfilter.d: fast-restored nat AWGM chain after NDMS reload"
      fi
    else
      scrub_jumps mangle %[2]s
      scrub_jumps nat %[6]s
      scrub_tagged nat %[7]s
      scrub_tagged mangle %[8]s
      scrub_tagged mangle %[9]s
      /opt/sbin/iptables-restore --noflush < %[1]q
      /opt/sbin/ip rule add fwmark 0x%[3]x table %[4]d priority %[5]d 2>/dev/null || true
      /opt/sbin/ip route add local 0.0.0.0/0 dev lo table %[4]d 2>/dev/null || true
      logger -t awgm-tproxy "netfilter.d: restored AWGM chains after NDMS reload"
    fi
    # TPROXY was down: UDP flows whose first packet hit the no-jump window got
    # a direct WAN NAT in conntrack and bypass tproxy for life — evict them.
    [ "$mangle_ok" -eq 0 ] && [ -x %[14]q ] && %[14]q
  fi
else
%[10]sfi
exit 0
`, netfilterRulesPath, ChainName, Fwmark, RoutingTable, IPRulePriority, RedirectChain, DNSRescueTag, DNSNoPolicyTag, IngressTag, deadBranch, BlackholeChain, netfilterMangleRulesPath, netfilterNatRulesPath, netfilterCtCleanPath, bypassSavePath, bypassSetName, bypassset.SetMaxElem)
}

// ctCleanScript renders the poisoned-flow eviction script (issue #627). While
// the TPROXY PREROUTING jump is absent (NDMS netfilter reload, engine start),
// a UDP flow whose FIRST packet slips through gets a direct WAN SNAT in
// conntrack and is picked up by FASTNAT/hwnat offload — after which its
// packets never reach netfilter again, so restoring the rules does not heal
// it: the flow bypasses tproxy until it dies (for a WhatsApp call: "they
// stopped hearing me until I hang up"). Deleting the conntrack entry costs
// one packet — the next one re-creates the flow through tproxy, and the
// offload shortcut dies with the entry. UDP only: killing established TCP
// mid-stream breaks it hard, while new TCP connects self-heal on the next SYN.
//
// Scope self-adapts to the device mode by reading the live PREROUTING jump:
//   - policy mode (connmark on the jump): one kernel-side `conntrack -D
//     --mark <policy> --reply-dst <WAN IP>` per WAN IP — cost independent of
//     conntrack table size (matters on mips with big P2P tables), and the
//     mark excludes the router's own flows;
//   - all-devices mode: awk pass over /proc/net/nf_conntrack scoped by mac=
//     (present on every LAN-originated flow on Keenetic, absent on the
//     router's own), with a cheap grep pre-filter.
//
// WAN IPv4s are read from the default-route interfaces at call time — the
// trigger IS often a DHCP renew, so baked-in addresses would go stale.
//
// The conntrack pass above only reaches flows BORN in the window (they carry a
// direct WAN NAT). Flows that were ALREADY established and intercepted have no
// NAT at all — their reply-dst is the LAN client — yet the window hands them to
// the MTK PPE engine as a plain forward, after which the hardware switches them
// and they never enter netfilter again: neither restored rules nor `conntrack
// -D` heal them (issue #684, measured on NC-1812: 8444 conntrack packets against
// 0 on the matching mangle rules). One write to /proc/sys/net/hwnat/ppe_flush
// makes every offloaded flow take the slow path once and re-learn. The node is
// absent on non-MTK platforms, hence the writability guard.
//
// Pure (no I/O) so a test can validate the generated shell with `sh -n`.
func ctCleanScript() string {
	return fmt.Sprintf(`#!/bin/sh
# Hardware offload (MTK PPE) flush — issue #684. Runs before the conntrack work
# and independently of it: the flows it heals carry no NAT, so no conntrack
# lookup would find them. Only once the TPROXY jump is really back — flushing
# while it is absent just re-teaches the same flows through the same hole.
if /opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[1]s($| )' \
  && [ -w /proc/sys/net/hwnat/ppe_flush ] && echo 1 > /proc/sys/net/hwnat/ppe_flush 2>/dev/null; then
  logger -t awgm-ctclean "flushed PPE offload table after TPROXY restore"
fi
CT=/opt/sbin/conntrack
[ -x "$CT" ] || { logger -t awgm-ctclean "conntrack tool missing — poisoned-flow eviction skipped"; exit 0; }
wan_ips=""
for dev in $(/opt/sbin/ip -4 route show default 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | sort -u); do
  wan_ips="$wan_ips $(/opt/sbin/ip -4 addr show dev "$dev" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p')"
done
set -- $wan_ips
[ $# -gt 0 ] || exit 0
pmark="$(/opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null \
  | sed -n 's/.*-m connmark --mark \(0x[0-9a-fA-F]*\).*-[jg] %[1]s.*/\1/p' | head -n 1)"
[ -n "$pmark" ] && pmark="$(printf '%%d' "$pmark" 2>/dev/null)"
n=0
if [ -n "$pmark" ]; then
  for ip in "$@"; do
    d="$("$CT" -D -p udp --mark "$pmark" --reply-dst "$ip" 2>&1 >/dev/null \
      | sed -n 's/.* \([0-9][0-9]*\) flow entries.*/\1/p')"
    [ -n "$d" ] && n=$((n + d))
  done
else
  hit=0
  for ip in "$@"; do
    grep -q "dst=$ip " /proc/net/nf_conntrack 2>/dev/null && { hit=1; break; }
  done
  if [ "$hit" -eq 1 ]; then
    list="$(awk -v wans="$*" '
      BEGIN { split(wans, a, " "); for (i in a) wan[a[i]] = 1 }
      $3 == "udp" {
        src = ""; dst = ""; sp = ""; dp = ""; nd = 0; rdst = ""; hasmac = 0;
        for (i = 1; i <= NF; i++) {
          if ($i ~ /^src=/ && src == "") src = substr($i, 5);
          else if ($i ~ /^dst=/) { nd++; if (nd == 1) dst = substr($i, 5); else if (nd == 2) rdst = substr($i, 5); }
          else if ($i ~ /^sport=/ && sp == "") sp = substr($i, 7);
          else if ($i ~ /^dport=/ && dp == "") dp = substr($i, 7);
          else if ($i ~ /^mac=/) hasmac = 1;
        }
        if (hasmac && (rdst in wan)) print src, dst, sp, dp;
      }' /proc/net/nf_conntrack 2>/dev/null)"
    if [ -n "$list" ]; then
      n="$(printf '%%s\n' "$list" | wc -l)"
      printf '%%s\n' "$list" | while read -r s d sp dp; do
        "$CT" -D -p udp -s "$s" -d "$d" --sport "$sp" --dport "$dp" >/dev/null 2>&1
      done
    fi
  fi
fi
[ "$n" -gt 0 ] && logger -t awgm-ctclean "evicted $n direct-NAT UDP flow(s) after TPROXY restore"
exit 0
`, ChainName)
}

// removeNetfilterRulesFile deletes the persisted rules files so the
// netfilter.d hook becomes a no-op on the next NDMS reload. Called on
// engine Disable. Idempotent.
func removeNetfilterRulesFile() {
	_ = os.Remove(netfilterRulesPath)
	_ = os.Remove(netfilterMangleRulesPath)
	_ = os.Remove(netfilterNatRulesPath)
}

// refreshNetfilterHookIfPresent rewrites the netfilter.d hook script
// when one is already installed, so older versions get the current
// pidof guard on daemon startup. No-op when the file is absent —
// Install creates it on first Enable.
func refreshNetfilterHookIfPresent() {
	if _, err := os.Stat(netfilterHookPath); err != nil {
		return
	}
	// true: обычный (tproxy) хук с fail-closed веткой. В policy-tun это
	// БЕЗВРЕДНО и режим не гейтится: DEAD-ветка хука первым делом проверяет
	// наличие файла blackhole-правил, а его снимает и Install(DSCPOnly)
	// (cleanupBlackhole), и Uninstall (RemoveBlackhole) на выходе из tproxy —
	// без файла ветка no-op. Ближайший Install(DSCPOnly) перепишет хук без неё.
	_ = writeNetfilterHook(true)
}

// Uninstall is best-effort by design (F79): every step is idempotent
// teardown whose "nothing to remove" is indistinguishable from failure
// without parsing iptables stderr; callers get no error.
func (it *IPTables) Uninstall(ctx context.Context) {
	if it.cleanupHook != nil {
		it.cleanupHook()
	}
	// Also tear down the fail-closed blackhole if one lingers (engine died,
	// blackhole engaged, then the user disabled the router): delete its rules
	// file, scrub the jump, drop the chain.
	it.RemoveBlackhole(ctx)
	it.removeSourceHooks(ctx)
	_ = it.runIPTables(ctx, "-t", "mangle", "-F", ChainName)
	_ = it.runIPTables(ctx, "-t", "mangle", "-X", ChainName)
	_ = it.runIPTables(ctx, "-t", "nat", "-F", RedirectChain)
	_ = it.runIPTables(ctx, "-t", "nat", "-X", RedirectChain)
	// Drain ALL fwmark rules — historically Install accumulated
	// duplicates at priorities 0-N (auto-assigned), so a single `del`
	// would leave the rest. Loop until ENOENT, capped defensively.
	it.drainFwmarkRules(ctx)
	_ = it.runIP(ctx, "route", "flush", "table", fmt.Sprintf("%d", RoutingTable))
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
	// Разбор идёт через ШОВ роли (runIPTablesOut), а не прямым sysexec: иначе
	// снос невидим тесту, и его выпил остаётся зелёным. "-w" добавляет сам
	// sysiptables.RunOutput.
	out, err := it.runIPTablesOut(ctx, "-t", table, "-S", chain)
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `--comment "`+tag+`"`) && !strings.Contains(line, `--comment `+tag) {
			continue
		}
		if !strings.HasPrefix(line, "-A "+chain+" ") {
			continue
		}
		delLine := "-D " + strings.TrimPrefix(line, "-A ")
		args := append([]string{"-t", table}, strings.Fields(delLine)...)
		_ = it.runIPTables(ctx, args...)
	}
}

func (it *IPTables) removeSourceHooksFromTable(ctx context.Context, table, chain string) {
	out, err := it.runIPTablesOut(ctx, "-t", table, "-S", "PREROUTING")
	if err != nil {
		return
	}
	// Install рендерит `-j` (:519, :521); `-g` принимается на случай ручных
	// правок и старых версий — пином не покрыт.
	jumpJ := "-j " + chain
	gotoG := "-g " + chain
	for _, line := range strings.Split(out, "\n") {
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
// does not cover: comment, mark, connmark, conntrack, pkttype, dscp.
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
		"xt_dscp",
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
	installed, jumps, _, err = it.probeAll(ctx)
	return installed, jumps, err
}

// probeAll is Probe plus the fail-closed blackhole's liveness. The blackhole
// lives in the SAME mangle table, so both its `-N` declaration and its
// PREROUTING jump are already in the dump Probe fetches: observing it costs
// zero extra iptables calls. Live means chain AND jump, by the same logic as
// `jumps` above — a chain nobody enters drops nothing, and that is fail-OPEN.
// Observing the live rules beats remembering that we installed them, because a
// manual `iptables -F -t mangle` or a failed netfilter.d hook wipes them with
// no NDMS event to react to.
//
// On a query error probeAll returns (false, false, false, err) — same contract
// as Probe: the zeros are "unknown", NOT "absent". A caller must gate on err
// before acting on any of the three, or a transient `-S` failure will read as
// "nothing is installed". In particular blackhole reads false even when the
// mangle dump already showed it, because the nat dump failed afterwards.
func (it *IPTables) probeAll(ctx context.Context) (installed, jumps, blackhole bool, err error) {
	mangleDump, err := it.runIPTablesOut(ctx, "-t", "mangle", "-S")
	if err != nil {
		return false, false, false, err
	}
	mChain, mJump := scanChain(mangleDump, ChainName)
	bChain, bJump := scanChain(mangleDump, BlackholeChain)
	blackhole = bChain && bJump
	natDump, err := it.runIPTablesOut(ctx, "-t", "nat", "-S")
	if err != nil {
		return false, false, false, err
	}
	nChain, nJump := scanChain(natDump, RedirectChain)
	installed = mChain && nChain
	jumps = installed && mJump && nJump
	return installed, jumps, blackhole, nil
}

// scanChain reports the chain's declaration and its PREROUTING jump in an
// `iptables -S <table>` dump.
func scanChain(dump, chain string) (chainExists, jumpExists bool) {
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if line == "-N "+chain {
			chainExists = true
		}
		if strings.HasPrefix(line, "-A PREROUTING ") && jumpToken(line, chain) {
			jumpExists = true
		}
	}
	return chainExists, jumpExists
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
