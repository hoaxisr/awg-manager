package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/awgmbackend"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/selective"
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
	// BlackholeChain is the fail-closed DROP chain (в таблице UDP-перехвата,
	// см. udpChainTable). It is engaged
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
	// PPETag помечает правило `-j PPE`, чтобы скраб мог его найти. Само по
	// себе PPE не является джампом в AWGM-цепочку, поэтому под общий шаблон
	// removeSourceHooks (поиск джампов в наши цепочки) не попадает — без тега
	// restore --noflush добавлял бы ещё одно такое правило на каждый Install.
	PPETag = "AWGM-PPE"
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

// chainRef — пара «таблица/цепочка». Отдельный тип, потому что раскладка
// перехвата зависит от режима и её приходится передавать целиком.
type chainRef struct{ table, chain string }

const (
	// AwgmTable — собственная таблица xtables. ndm чужие таблицы не трогает
	// (проверено на железе), поэтому правила в ней переживают перестройку
	// firewall без хука netfilter.d.
	AwgmTable = "awgm"
	// AwgmTProxyTarget / AwgmPPETarget — клоны TPROXY/PPE, привязанные к
	// таблице awgm (.table в модуле). Штатные имена в awgm ядро отвергает,
	// наши — в mangle: затенения нет в обе стороны (стенд, фаза B §2).
	AwgmTProxyTarget = "AWGMTPROXY"
	AwgmPPETarget    = "AWGMPPE"
)

// udpChainTable / tcpChainTable — ЕДИНСТВЕННЫЙ источник правды о раскладке.
// В awgm-режиме обе цепочки перехвата (и blackhole) живут в таблице awgm;
// в legacy UDP — mangle, TCP — nat (REDIRECT'у нужен conntrack DNAT).
func udpChainTable(awgmLayout bool) string {
	if awgmLayout {
		return AwgmTable
	}
	return "mangle"
}

func tcpChainTable(awgmLayout bool) string {
	if awgmLayout {
		return AwgmTable
	}
	return "nat"
}

func interceptionChains(awgmLayout bool) []chainRef {
	return []chainRef{
		{udpChainTable(awgmLayout), ChainName},
		{tcpChainTable(awgmLayout), RedirectChain},
	}
}

// Mutable in tests via t.Cleanup so they can redirect into a tmp dir.
// Production code reads these at call time.
var (
	netfilterHookPath = "/opt/etc/ndm/netfilter.d/50-awgm-tproxy.sh"
	// netfilterDNSHookPath — узкий хук awgm-режима. Восстанавливает ТОЛЬКО
	// nat-блоб с DNS-RESCUE: перехват в таблице awgm ndm не стирает, а
	// DNS-RESCUE обязан лежать в nat-таблице ndm и потому переставляется
	// после каждой перестройки firewall. Отдельное имя, чтобы не путать с
	// полным 50-awgm-tproxy.sh.
	netfilterDNSHookPath   = "/opt/etc/ndm/netfilter.d/51-awgm-dnsrescue.sh"
	netfilterRulesPath     = "/opt/etc/awg-manager/singbox/router-netfilter.rules"
	netfilterBlackholePath = "/opt/etc/awg-manager/singbox/router-blackhole.rules"
	// Per-table copies of the rules blob (issue #627): the netfilter.d hook
	// restores ONLY the table NDMS actually wiped, so a routine mangle-only
	// reload (DHCP renew) never tears down the working nat chain and vice
	// versa. The combined file above stays as the full-rebuild fallback.
	netfilterMangleRulesPath = "/opt/etc/awg-manager/singbox/router-netfilter-mangle.rules"
	netfilterNatRulesPath    = "/opt/etc/awg-manager/singbox/router-netfilter-nat.rules"
	// netfilterCtCleanPath is the poisoned-flow eviction script (issue #627),
	// invoked by the hook after a TPROXY restore and by Install.
	netfilterCtCleanPath = "/opt/etc/awg-manager/singbox/awgm-ctclean.sh"
)

// selectiveSetName is the ipset name used for selective bypass — aliased
// from the selective sub-package so the name has exactly one definition.
const selectiveSetName = selective.SetName

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

// IsTProxyTargetAvailable спрашивает АКТИВНЫЙ бинарь, знает ли он таргет
// TPROXY: штатный бинарь и бандл-бинарь — разные сборки с разным набором
// расширений.
func (it *IPTables) IsTProxyTargetAvailable(ctx context.Context) bool {
	runOut := it.activeRunOut()
	if runOut == nil {
		// Частично собранный IPTables (тесты соседних путей): спросить
		// некого, отвечаем «недоступно».
		return false
	}
	it.probeMu.Lock()
	if it.tproxyTargetResult {
		it.probeMu.Unlock()
		return true
	}
	it.probeMu.Unlock()

	// Вердикт выносит код возврата: без расширения iptables выходит ненулевым,
	// а справку печатает в stdout. Поэтому stderr здесь не читается осознанно —
	// на решение он не влияет.
	out, err := runOut(ctx, "-j", "TPROXY", "--help")
	ok := err == nil && strings.Contains(out, "TPROXY")
	if ok {
		it.probeMu.Lock()
		it.tproxyTargetResult = true
		it.probeMu.Unlock()
	}
	return ok
}

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
func (it *IPTables) XtDscpAvailability(ctx context.Context) (moduleOK, matchOK bool) {
	moduleOK = isModuleLoaded("xt_dscp")
	if !moduleOK {
		if kernel := osdetect.KernelRelease(); kernel != "" {
			_, err := os.Stat(filepath.Join("/lib/modules", kernel, "xt_dscp.ko"))
			moduleOK = err == nil
		}
	}
	runOut := it.activeRunOut()
	if runOut == nil {
		return moduleOK, false // см. IsTProxyTargetAvailable
	}
	// Как и в IsTProxyTargetAvailable: решает код возврата, stderr на вердикт
	// не влияет.
	out, err := runOut(ctx, "-m", "dscp", "-h")
	matchOK = err == nil && strings.Contains(strings.ToLower(out), "dscp")
	return moduleOK, matchOK
}

// cachedXtDscpAvailability returns the (moduleOK, matchOK) pair through the
// probe cache: a fully-positive result is cached forever, anything else for
// xtDscpNegativeTTL (see the const above). All availability consumers
// (IsXtDscpAvailable, GetStatus diagnostics) go through this so a missing
// module costs at most one `iptables -m dscp -h` exec per TTL window instead
// of one per reconcile tick / status poll.
func (it *IPTables) cachedXtDscpAvailability(ctx context.Context) (moduleOK, matchOK bool) {
	it.probeMu.Lock()
	if it.xtDscpModuleOK && it.xtDscpMatchOK {
		it.probeMu.Unlock()
		return true, true
	}
	if !it.xtDscpCheckedAt.IsZero() && time.Since(it.xtDscpCheckedAt) < xtDscpNegativeTTL {
		m, x := it.xtDscpModuleOK, it.xtDscpMatchOK
		it.probeMu.Unlock()
		return m, x
	}
	probe := it.xtDscpAvailabilityFn
	it.probeMu.Unlock()

	if probe == nil {
		probe = it.XtDscpAvailability
	}
	m, x := probe(ctx)
	it.probeMu.Lock()
	it.xtDscpModuleOK, it.xtDscpMatchOK = m, x
	it.xtDscpCheckedAt = time.Now()
	it.probeMu.Unlock()
	return m, x
}

// IsXtDscpAvailable reports whether DSCP matching is usable end-to-end: BOTH
// the kernel module (loaded or .ko on disk) AND the iptables extension must
// pass. Positive results are cached forever (availability never degrades
// within one daemon lifetime); negative results are cached for
// xtDscpNegativeTTL and then re-probed so installing the missing piece is
// picked up without a restart. Surfaced to the UI as the status field
// `xtDscpAvailable`.
func (it *IPTables) IsXtDscpAvailable(ctx context.Context) bool {
	moduleOK, matchOK := it.cachedXtDscpAvailability(ctx)
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

	// SelectiveIPSet, when true, inserts an iptables -m set guard rule in
	// both AWGM-TPROXY (mangle) and AWGM-REDIRECT (nat) chains so that
	// only traffic whose destination IP is listed in AWGM-SELECTIVE reaches
	// sing-box. All other traffic gets an early RETURN and bypasses sing-box
	// entirely (going straight to WAN). The guard is placed after user bypass
	// RETURN rules (port/CIDR exclusions) but before the catch-all TPROXY /
	// REDIRECT rule, so explicit bypass rules still take precedence.
	// Only meaningful when the xt_set kernel module is loaded.
	SelectiveIPSet bool

	// DSCPOnly — режим policy-tun: netfilter нужен ТОЛЬКО для QoS-DSCP-классов,
	// основной трафик идёт NDMS-политикой в tun-интерфейс sing-box. Цепочки
	// содержат лишь bypass-RETURN'ы и dscp-диспатч: ни catch-all, ни перехвата
	// DNS (основных tproxy/redirect-инбаундов в этом режиме нет), ни
	// selective/ingress-MARK/DNS-RESCUE. Install в этом режиме также не
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

	// AwgmMode — правила применяются бандл-бинарём в таблицу awgm; обе цепочки
	// перехвата и PPE-гард переезжают туда, в nat остаётся только DNS-RESCUE.
	// Терминальные действия становятся клонами AWGMTPROXY/AWGMPPE: штатные
	// имена таргетов ядро в таблице awgm отвергает.
	//
	// Причина переезда — ndm перестраивает mangle/nat целиком и стирает наши
	// правила, а чужую таблицу не трогает: перехват в awgm переживает
	// перестройку firewall без хука netfilter.d.
	//
	// --on-ip 127.0.0.1 ОБЯЗАТЕЛЕН в каждом AWGMTPROXY-правиле: без него
	// клон-модуль читает net_device.ip_ptr (смещение 744) и перехват мёртв.
	//
	// PPE-гард нужен потому, что программный fastpath роутера уносит уже
	// установившийся поток мимо всего перехвата: без этого правила до
	// forward-хука доходило 48 пакетов из 192, с ним — 205 из 205.
	AwgmMode bool
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

// buildBlackholeRestoreInput renders the single-table *AWGM-BLACKHOLE* blob:
// the SAME LAN/router/WAN RETURN exclusions as the interception chain, then a
// terminal DROP, entered from PREROUTING by the identical policy selector
// (emitPreroutingJump). So the set it drops is exactly the set that would have
// entered AWGM-TPROXY — nothing local is caught, and no policy traffic escapes.
// Таблица — та же, что у UDP-цепочки перехвата (udpChainTable): не nat, чтобы
// матчился КАЖДЫЙ пакет потока, пока туннель лежит, а не только первый пакет
// нового conntrack.
func buildBlackholeRestoreInput(spec RestoreInputSpec) string {
	var b strings.Builder
	if spec.AwgmMode {
		b.WriteString("*awgm\n")
	} else {
		b.WriteString("*mangle\n")
	}
	fmt.Fprintf(&b, ":%s - [0:0]\n", BlackholeChain)
	// User bypass first — an explicitly excluded subnet must never be dropped.
	emitUserBypassReturns(&b, BlackholeChain, spec.BypassCIDRs)
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
	// Selective mode: only destinations in AWGM-SELECTIVE are proxied; everything
	// else is SUPPOSED to go direct to WAN. Mirror the interception guard so the
	// blackhole drops ONLY the selective subset — without it a dead engine would
	// blackhole the user's entire (mostly non-selective) traffic, taking policy
	// devices fully offline, which is worse than the fail-open it replaces.
	if spec.SelectiveIPSet {
		fmt.Fprintf(&b, "-A %s -m set ! --match-set %s dst -j RETURN\n", BlackholeChain, selectiveSetName)
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

// buildRestoreInput renders the full iptables-restore blob — the interception
// and nat sections concatenated. Install and the hook's full-rebuild fallback
// consume this; the hook's per-table fast heal consumes the sections
// individually (issue #627).
func buildRestoreInput(spec RestoreInputSpec) string {
	return buildInterceptRestoreInput(spec) + buildNatRestoreInput(spec)
}

func buildInterceptRestoreInput(spec RestoreInputSpec) string {
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

	// ---- interception table (mangle в legacy, awgm иначе): UDP via TPROXY ----
	// Literal port of `add_tproxy_rules` from reference/SKeen/skeen.sh
	// (hybrid mode, mangle table) plus the DNS interception rule from
	// set_chain_rules (`INTERCEPT_DNS_ENABLE=1` branch). No extras.
	if spec.AwgmMode {
		b.WriteString("*awgm\n")
	} else {
		b.WriteString("*mangle\n")
	}
	fmt.Fprintf(&b, ":%s - [0:0]\n", ChainName)

	// Пользовательский bypass — целиком мимо sing-box, ДО перехвата DNS.
	emitUserBypassReturns(&b, ChainName, spec.BypassCIDRs)

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
			fmt.Fprintf(&b, "-A %s -p udp -m dscp --dscp %d %s\n",
				ChainName, q.DSCP, udpDispatch(spec, q.TProxyPort))
		}
		// QoS-классы в awgm тоже идут через tproxy — без PPE установившиеся
		// потоки прошли бы мимо.
		if spec.AwgmMode {
			emitPPEGuard(&b, spec)
			emitTCPChain(&b, spec)
		}
		emitPreroutingJump(&b, ChainName, spec)
		b.WriteString("COMMIT\n")
		return b.String()
	}

	// set_chain_rules: DNS first (when INTERCEPT_DNS_ENABLE=1)
	fmt.Fprintf(&b, "-A %s -p udp --dport 53 %s\n", ChainName, udpDispatch(spec, TPROXYPort))

	// Selective-bypass guard: only traffic to IPs in AWGM-SELECTIVE reaches
	// sing-box; everything else returns to PREROUTING and goes to WAN.
	// Placed after DNS intercept so DNS still reaches sing-box regardless
	// of ipset membership (DNS must always be intercepted for hijack-dns).
	if spec.SelectiveIPSet {
		fmt.Fprintf(&b, "-A %s -m set ! --match-set %s dst -j RETURN\n",
			ChainName, selectiveSetName)
	}

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
	//   - AFTER the selective guard: in selective mode only ipset-listed
	//     destinations enter sing-box at all; QoS classifies within that
	//     scope, it does not widen it.
	//   - BEFORE the catch-all: otherwise the unconditional TPROXY eats the
	//     packet first and the class rule is dead.
	for _, q := range spec.QoSClasses {
		fmt.Fprintf(&b, "-A %s -p udp -m dscp --dscp %d %s\n",
			ChainName, q.DSCP, udpDispatch(spec, q.TProxyPort))
	}

	// add_tproxy_rules: catch-all TPROXY for UDP.
	fmt.Fprintf(&b, "-A %s -p udp %s\n", ChainName, udpDispatch(spec, TPROXYPort))

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

	// awgm-режим: PPE → цепочка TCP-перехвата → джамп UDP-цепочки. PPE строго
	// первым — иначе fastpath уносит установившийся поток мимо всего перехвата.
	if spec.AwgmMode {
		emitPPEGuard(&b, spec)
		emitTCPChain(&b, spec)
	}

	// set_prerouting_rules: policy connmark filter ON THE JUMP, no `-p`
	// matcher (SKeen jumps unconditionally; per-proto matching happens
	// inside the chain).
	emitPreroutingJump(&b, ChainName, spec)

	b.WriteString("COMMIT\n")
	return b.String()
}

// emitTCPChain строит цепочку TCP-перехвата (AWGM-REDIRECT) и джамп в неё из
// PREROUTING. Структура — bypass'ы, порт 79, selective-гард, DNS-карвауты,
// dscp-классы, catch-all — одинакова в обоих режимах; отличается только
// терминальное действие: REDIRECT в nat для legacy, AWGMTPROXY в таблице awgm
// для awgm-режима.
// COMMIT цепочка не пишет — это дело вызывающего билдера, у которого ниже
// может быть ещё код (DNS-RESCUE в nat).
//
// Порядок правил менять НЕЛЬЗЯ: каждая строка тут закрывает свой класс утечки,
// обоснования — в комментариях по месту.
func emitTCPChain(b *strings.Builder, spec RestoreInputSpec) {
	// Literal port of `add_redirect_rules` from reference/SKeen/skeen.sh
	// (hybrid mode, nat table). SKeen's chain has ONLY the bypass set
	// + catch-all `-p tcp` dispatch; without selective bypass the
	// catch-all already covers TCP/53. WITH the selective guard the
	// catch-all is no longer unconditional, so TCP/53 gets its own
	// intercept before the guard (see below) — otherwise a truncated-UDP
	// retry or DNS-over-TCP to a resolver outside the set escapes
	// hijack-dns and leaks real IPs of proxied domains. The QoS DSCP
	// dispatch needs the same carve-out (a class dispatch would otherwise
	// swallow marked TCP/53 onto a class port), emitted with the class
	// rules below when the selective intercept isn't already present.
	fmt.Fprintf(b, ":%s - [0:0]\n", RedirectChain)

	emitUserBypassReturns(b, RedirectChain, spec.BypassCIDRs)

	// policy-tun QoS-гибрид: зеркало mangle-ветки — только bypass и dscp.
	// Без catch-all, без перехвата DNS и DNS-RESCUE, без правила на порт 79:
	// перехват здесь делает лишь dscp-диспатч, а трафик к веб-морде роутера
	// DSCP-классов не несёт.
	if spec.DSCPOnly {
		for _, pr := range spec.BypassTCPPorts {
			fmt.Fprintf(b, "-A %s -p tcp --dport %s -j RETURN\n", RedirectChain, pr.String())
		}
		fmt.Fprintf(b, "-A %s -p tcp --dport 53 -j RETURN\n", RedirectChain)
		emitBypassReturns(b, RedirectChain, spec.WANIPs)
		for _, q := range spec.QoSClasses {
			fmt.Fprintf(b, "-A %s -p tcp -m dscp --dscp %d %s\n",
				RedirectChain, q.DSCP, tcpDispatch(spec, q.TProxyPort, q.RedirectPort))
		}
		emitPreroutingJump(b, RedirectChain, spec)
		return
	}

	emitBypassReturns(b, RedirectChain, spec.WANIPs)
	// Bypass router admin port so we don't redirect our own UI traffic.
	// (SKeen has equivalent dynamic admin-port discovery — same intent.)
	fmt.Fprintf(b, "-A %s -p tcp --dport 79 -j RETURN\n", RedirectChain)

	// Bypass ports: RETURN before the catch-all TCP dispatch.
	for _, pr := range spec.BypassTCPPorts {
		fmt.Fprintf(b, "-A %s -p tcp --dport %s -j RETURN\n", RedirectChain, pr.String())
	}

	// Selective-bypass guard for TCP: mirrors the mangle UDP guard.
	// TCP/53 is intercepted FIRST (mirroring the mangle UDP/53 rule and
	// honoring the same "DNS must always reach hijack-dns" invariant):
	// resolver IPs are typically NOT in AWGM-SELECTIVE, so without this
	// rule the guard would RETURN DNS-over-TCP straight to the upstream.
	if spec.SelectiveIPSet {
		fmt.Fprintf(b, "-A %s -p tcp --dport 53 %s\n",
			RedirectChain, tcpDispatch(spec, TPROXYPort, RedirectPort))
		fmt.Fprintf(b, "-A %s -m set ! --match-set %s dst -j RETURN\n",
			RedirectChain, selectiveSetName)
	}

	// QoS-by-DSCP dispatch for TCP — mirrors the mangle UDP block (same
	// ordering rationale: after bypasses and the selective guard, before the
	// catch-all). DNS carve-out first: without it, DSCP-marked DNS-over-TCP
	// (or a truncated-UDP retry) would land on a CLASS inbound and only get
	// hijacked if the managed route rules happened to order right —
	// intercepting TCP/53 onto the MAIN port here kills that whole leak class
	// at the netfilter level, exactly like the mangle chain's unconditional
	// UDP/53 intercept above the UDP class rules. Skipped when the selective
	// guard already emitted the identical intercept earlier in this chain.
	if len(spec.QoSClasses) > 0 {
		if !spec.SelectiveIPSet {
			fmt.Fprintf(b, "-A %s -p tcp --dport 53 %s\n",
				RedirectChain, tcpDispatch(spec, TPROXYPort, RedirectPort))
		}
		for _, q := range spec.QoSClasses {
			fmt.Fprintf(b, "-A %s -p tcp -m dscp --dscp %d %s\n",
				RedirectChain, q.DSCP, tcpDispatch(spec, q.TProxyPort, q.RedirectPort))
		}
	}

	// add_redirect_rules: catch-all dispatch for TCP.
	fmt.Fprintf(b, "-A %s -p tcp %s\n", RedirectChain, tcpDispatch(spec, TPROXYPort, RedirectPort))

	emitPreroutingJump(b, RedirectChain, spec)
}

// udpDispatch — терминальное действие UDP-перехвата. Имя таргета зависит от
// таблицы: TPROXY валиден только в mangle, AWGMTPROXY — только в awgm.
// --on-ip 127.0.0.1 ОБЯЗАТЕЛЕН: tproxy-инбаунд слушает именно этот адрес, а
// клон-модуль без него читает net_device.ip_ptr (смещение 744) — правило
// продукта, охраняется тестом.
func udpDispatch(spec RestoreInputSpec, port int) string {
	target := "TPROXY"
	if spec.AwgmMode {
		target = AwgmTProxyTarget
	}
	return fmt.Sprintf("-j %s --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x", target, port, Fwmark)
}

// tcpDispatch — терминальное действие для TCP: REDIRECT на redirect-инбаунд в
// legacy-режиме, AWGMTPROXY на tproxy-инбаунд в awgm-режиме.
//
// --on-ip 127.0.0.1 — см. udpDispatch: тот же адрес, та же причина.
func tcpDispatch(spec RestoreInputSpec, tproxyPort, redirectPort int) string {
	if spec.AwgmMode {
		return fmt.Sprintf("-j %s --on-port %d --on-ip 127.0.0.1 --tproxy-mark 0x%x",
			AwgmTProxyTarget, tproxyPort, Fwmark)
	}
	return fmt.Sprintf("-j REDIRECT --to-ports %d", redirectPort)
}

// emitPPEGuard эмитит ОДНО правило `-j AWGMPPE` в PREROUTING таблицы awgm с тем
// же селектором, что и джампы перехвата: программный fastpath роутера иначе
// уносит уже установившийся поток мимо всего перехвата, и тот работает лишь на
// первых пакетах соединения. Помечено comment-тегом, чтобы скраб
// (removeSourceHooks) мог снять правило перед переустановкой.
//
// Таргет — клон AWGMPPE, а не стоковый PPE: зовётся эмиссия только при
// AwgmMode, а штатное имя ядро в таблице awgm отвергает вместе со всем блобом.
func emitPPEGuard(b *strings.Builder, spec RestoreInputSpec) {
	if spec.MatchAll {
		fmt.Fprintf(b, "-A PREROUTING -m comment --comment %s -j %s\n", PPETag, AwgmPPETarget)
		return
	}
	if spec.PolicyMark == "" {
		return
	}
	fmt.Fprintf(b, "-A PREROUTING -m connmark --mark %s -m comment --comment %s -j %s\n",
		spec.PolicyMark, PPETag, AwgmPPETarget)
}

func buildNatRestoreInput(spec RestoreInputSpec) string {
	var b strings.Builder

	// ---- *nat table: TCP via REDIRECT ----
	// В awgm-режиме цепочки перехвата здесь нет вовсе — она уехала в таблицу
	// awgm (см. RestoreInputSpec.AwgmMode), в nat остаётся только DNS-RESCUE.
	b.WriteString("*nat\n")

	if !spec.AwgmMode {
		emitTCPChain(&b, spec)
	}

	// Ранний выход в DSCPOnly — ДО блока DNS-RESCUE: policy-tun сознательно
	// живёт без DNS-RESCUE (перехвата DNS в этом режиме нет вообще, см.
	// комментарий в emitTCPChain). Выход обязан остаться здесь: вынос цепочки
	// в хелпер стёр бы его, и билдер провалился бы в DNS-RESCUE-гейт. Сейчас
	// это латентно (оба DSCPOnly-вызывающих не передают LANBridges), но
	// семантику функции менять нельзя.
	if spec.DSCPOnly {
		b.WriteString("COMMIT\n")
		return b.String()
	}

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
	// legacyRun / legacyRunOut — вызовы, закреплённые за штатным бинарём
	// НАВСЕГДА, независимо от активного бэкенда. Сюда идут правила и чтения,
	// чьё место — таблицы ndm (nat/mangle): discovery портов ndnproxy из
	// _NDM_HOTSPOT_DNSREDIR, DNS-RESCUE, fakeip DNAT :53. Бандл-бинарь эти
	// таблицы тоже видит, но канал закреплён осознанно: правило чужой таблицы
	// не должно зависеть от того, каким бэкендом работает перехват.
	legacyRun    runFn
	legacyRunOut runOutFn
	runIP        runFn
	runIPOut     runOutFn
	persistRules persistRulesFn
	persistHook  func(includeBlackhole bool) error
	// legacyRestoreNoflush — второй канал Install в awgm-режиме: узкий
	// nat-блоб DNS-RESCUE применяется штатным iptables-restore, потому что
	// правило обязано лечь в nat-таблицу ndm, а не в нашу.
	legacyRestoreNoflush restoreNoflushFn
	// persistDNSHook пишет узкий хук при true и снимает при false.
	persistDNSHook   func(want bool) error
	cleanupHook      func()
	persistBlackhole persistFn
	cleanupBlackhole func()
	// persistCtClean доставляет скрипт вытеснения. Отдельная точка от
	// persistHook: скрипт нужен в обоих режимах, а полный хук — только в
	// legacy.
	persistCtClean func() error
	runCtClean     func(ctx context.Context)

	// awgmLayout — раскладка правил активного бэкенда: в каких таблицах лежат
	// цепочки перехвата (см. udpChainTable/tcpChainTable). Меняется вместе с
	// командами и той же парой UseAwgm/UseLegacy, поэтому живёт под тем же
	// runnerMu: снятый отдельно от команд, он мог бы описывать уже другой
	// бэкенд.
	awgmLayout bool

	// runnerMu защищает четыре поля выше, которые меняет смена бэкенда
	// (restoreNoflush / runIPTables / runIPTablesOut / awgmLayout). Писать их
	// некому, кроме UseAwgm/UseLegacy, а читают их и цикл сверки (свой поток), и
	// статус по HTTP — без замка это гонка за указателями на функции.
	// Читатели берут снимок через activeRunOut/activeRunLayout/
	// activeRunOutLayout/activeChannelLayout/activeRestoreLayout.
	runnerMu sync.Mutex

	// probeMu защищает кеш проб доступности (TPROXY-таргет, xt_dscp).
	// Кеш живёт здесь, а не в пакетных переменных, потому что пробы
	// спрашивают конкретный бинарь: после смены бэкенда прежний ответ
	// относится к другому набору расширений и стал бы враньём.
	probeMu            sync.Mutex
	tproxyTargetResult bool
	xtDscpModuleOK     bool
	xtDscpMatchOK      bool
	xtDscpCheckedAt    time.Time
	// xtDscpAvailabilityFn — точка подмены для тестов (счётчик/заглушка
	// сырых проб); nil означает настоящую XtDscpAvailability.
	xtDscpAvailabilityFn func(ctx context.Context) (moduleOK, matchOK bool)
}

func NewIPTables() *IPTables {
	return &IPTables{
		restoreNoflush:       sysiptables.RestoreNoflush,
		runIPTables:          sysiptables.Run,
		runIPTablesOut:       sysiptables.RunOutput,
		legacyRun:            sysiptables.Run,
		legacyRunOut:         sysiptables.RunOutput,
		legacyRestoreNoflush: sysiptables.RestoreNoflush,
		runIP: func(ctx context.Context, args ...string) error {
			result, err := sysexec.Run(ctx, "ip", args...)
			return sysexec.FormatError(result, err)
		},
		runIPOut: func(ctx context.Context, args ...string) (string, error) {
			result, err := sysexec.Run(ctx, "ip", args...)
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
		persistDNSHook: func(want bool) error {
			if !want {
				removeNetfilterDNSHook()
				return nil
			}
			return writeNetfilterDNSHook()
		},
		cleanupHook:      removeNetfilterRulesFile,
		persistBlackhole: writeNetfilterBlackholeRulesFile,
		cleanupBlackhole: removeNetfilterBlackholeRulesFile,
		persistCtClean:   writeCtCleanScript,
		runCtClean: func(ctx context.Context) {
			// Best-effort: the script self-logs via logger; a failure here
			// must never fail Install (interception is already up).
			_, _ = sysexec.Run(ctx, netfilterCtCleanPath)
		},
	}
}

// RuleRunner — то, что умеет применять правила. Интерфейс, а не конкретный
// *awgmbackend.Backend, чтобы тесты подсовывали заглушку без приведения типов.
type RuleRunner interface {
	RestoreNoflush(ctx context.Context, input string) error
	Run(ctx context.Context, args ...string) error
	RunOutput(ctx context.Context, args ...string) (string, error)
}

// UseAwgm переводит команды над НАШИМИ правилами на бандл-бинарь, знающий
// таблицу awgm. legacyRun/legacyRunOut не трогаются — см. комментарий к полям.
func (it *IPTables) UseAwgm(b RuleRunner) {
	it.runnerMu.Lock()
	it.restoreNoflush = b.RestoreNoflush
	it.runIPTables = b.Run
	it.runIPTablesOut = b.RunOutput
	it.awgmLayout = true
	it.runnerMu.Unlock()
	it.resetProbeCache()
}

// UseLegacy возвращает команды на штатный iptables. Вызывается при откате и
// при переключении режима обратно. Источник — legacy-канал ЭТОГО объекта
// (см. поля legacyRun/legacyRunOut/legacyRestoreNoflush): в проде это те же
// sysiptables, а объект с подменёнными командами возвращается к своим, а не
// начинает внезапно звать настоящий бинарь.
func (it *IPTables) UseLegacy() {
	it.runnerMu.Lock()
	it.restoreNoflush = sysiptables.RestoreNoflush
	if it.legacyRestoreNoflush != nil {
		it.restoreNoflush = it.legacyRestoreNoflush
	}
	it.runIPTables = sysiptables.Run
	if it.legacyRun != nil {
		it.runIPTables = it.legacyRun
	}
	it.runIPTablesOut = sysiptables.RunOutput
	if it.legacyRunOut != nil {
		it.runIPTablesOut = it.legacyRunOut
	}
	it.awgmLayout = false
	it.runnerMu.Unlock()
	it.resetProbeCache()
}

// activeRunOut отдаёт снимок команд активного бэкенда. Снимок берётся один
// раз на операцию: смена бэкенда посреди цикла удалений иначе отправила бы
// часть команд в один стек, часть в другой.
func (it *IPTables) activeRunOut() runOutFn {
	it.runnerMu.Lock()
	defer it.runnerMu.Unlock()
	return it.runIPTablesOut
}

// activeRunLayout / activeRunOutLayout отдают команду И раскладку активного
// бэкенда одним чтением. Порознь они могли бы разъехаться: команда нового
// бэкенда с раскладкой старого спрашивала бы цепочку не в той таблице и
// объявила бы живой перехват отсутствующим.
func (it *IPTables) activeRunLayout() (runFn, bool) {
	it.runnerMu.Lock()
	defer it.runnerMu.Unlock()
	return it.runIPTables, it.awgmLayout
}

func (it *IPTables) activeRunOutLayout() (runOutFn, bool) {
	it.runnerMu.Lock()
	defer it.runnerMu.Unlock()
	return it.runIPTablesOut, it.awgmLayout
}

// activeRestoreLayout отдаёт restore-функцию И раскладку одним снимком — как
// activeRunLayout, но для restore. Нужен там, где решение spec.AwgmMode уже
// принято РАНЬШЕ, на service-уровне (blob уже построен под конкретную
// раскладку), и обязано быть сверено с каналом ПРЯМО перед восстановлением:
// раздельные снимок restore-функции и проверка раскладки оставили бы окно
// между сверкой и восстановлением, в которое влезла бы демоция с
// несериализованного Enable-пути — тот же класс разъезда, что и у
// activeChannelLayout, только для restore, а не для скраба.
func (it *IPTables) activeRestoreLayout() (restoreNoflushFn, bool) {
	it.runnerMu.Lock()
	defer it.runnerMu.Unlock()
	return it.restoreNoflush, it.awgmLayout
}

// activeChannelLayout отдаёт run, runOut И раскладку ОДНИМ снимком под одним
// захватом runnerMu — как activeRunLayout, но для вызывающих, которым нужны
// обе команды сразу (иначе пришлось бы звать activeRunLayout()/
// activeRunOutLayout() отдельно, и конкурентный UseAwgm/UseLegacy между двумя
// захватами мог бы спарить канал одного бэкенда с раскладкой другого).
func (it *IPTables) activeChannelLayout() (runFn, runOutFn, bool) {
	it.runnerMu.Lock()
	defer it.runnerMu.Unlock()
	return it.runIPTables, it.runIPTablesOut, it.awgmLayout
}

// legacyChannel отдаёт команды штатного бинаря НЕЗАВИСИМО от активного
// бэкенда. Поля неизменны после сборки объекта (UseAwgm/UseLegacy их не
// трогают), поэтому замок не нужен. Нужен там, где правило заведомо лежит в
// таблицах firewall'а роутера (DNS-RESCUE, DNS-NOPOLICY, fakeip DNAT).
func (it *IPTables) legacyChannel() (runFn, runOutFn) {
	return it.legacyRun, it.legacyRunOut
}

// resetProbeCache забывает результаты проб доступности: они получены у
// прежнего бинаря, а положительный ответ кешируется навсегда.
func (it *IPTables) resetProbeCache() {
	it.probeMu.Lock()
	it.tproxyTargetResult = false
	it.xtDscpModuleOK, it.xtDscpMatchOK = false, false
	it.xtDscpCheckedAt = time.Time{}
	it.probeMu.Unlock()
}

// InstallBlackhole engages the fail-closed DROP chain: scrub any stale jump,
// persist the blackhole rules (so the netfilter.d hook can re-assert it after
// an NDMS reload while the engine is still dead), then restore it via
// iptables-restore --noflush. It reuses the same hook it.persistHook already
// wrote — only the blackhole rules file is blackhole-specific. No fwmark/ip
// rule: the blackhole only drops, it never delivers to a table. Idempotent.
func (it *IPTables) InstallBlackhole(ctx context.Context, spec RestoreInputSpec) error {
	run, runOut, layout := it.activeChannelLayout()
	it.removeSourceHooksFromTable(ctx, run, runOut, udpChainTable(layout), BlackholeChain)
	input := buildBlackholeRestoreInput(spec)
	if it.persistBlackhole != nil {
		if err := it.persistBlackhole(input); err != nil {
			return fmt.Errorf("write blackhole rules: %w", err)
		}
	}
	// restore и restoreLayout — ОДИН снимок, взятый ПРЯМО перед восстановлением
	// (а не тот, что уже устарел на момент scrub'а выше): spec.AwgmMode решён
	// раньше, на service-уровне, и несериализованная демоция с Enable-пути
	// могла успеть переключить канал уже ПОСЛЕ scrub'а. Расхождение — блоб
	// чужой раскладки в чужой канал — не применяем вовсе: громкий, типизированный
	// отказ отдаёт решение уже существующей демоции/повтору вместо тихого
	// провала или неопределённого поведения бинаря на незнакомой таблице.
	restore, restoreLayout := it.activeRestoreLayout()
	if restoreLayout != spec.AwgmMode {
		return fmt.Errorf("install заглушки: канал сменился между выбором режима и restore (канал awgm=%v, спецификация awgm=%v): %w",
			restoreLayout, spec.AwgmMode, ruleChannelError{errors.New("раскладка канала разошлась со спецификацией")})
	}
	if err := restore(ctx, input); err != nil {
		return fmt.Errorf("iptables-restore blackhole: %w", ruleChannelError{err})
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
	run, runOut, layout := it.activeChannelLayout()
	table := udpChainTable(layout)
	it.removeSourceHooksFromTable(ctx, run, runOut, table, BlackholeChain)
	_ = run(ctx, "-t", table, "-F", BlackholeChain)
	_ = run(ctx, "-t", table, "-X", BlackholeChain)
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

// ipRuleFwmarkPresent finds our fwmark rule in an `ip rule show` dump.
// Опознаём по приоритету и метке, а не по «lookup <номер таблицы>»: если имя
// таблицы заведено в /etc/iproute2/rt_tables, iproute2 печатает имя, и матч по
// номеру видел бы вечную пропажу, добавляя правило каждый тик (та же причина,
// что у fakeIPIngressOwnedRule). Приоритет Install задаёт явно, так что он —
// надёжная метка владения.
func ipRuleFwmarkPresent(dump string) bool {
	prefix := fmt.Sprintf("%d:", IPRulePriority)
	// Пробел на конце обязателен: без него "fwmark 0x1" совпало бы с
	// "fwmark 0x10".
	mark := fmt.Sprintf("fwmark 0x%x ", Fwmark)
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) && strings.Contains(line, mark) {
			return true
		}
	}
	return false
}

// ipRouteLocalDefaultPresent finds the local catch-all route in an
// `ip route show table <ours>` dump. Ставится маршрут как "local 0.0.0.0/0", а
// печатается как "local default" — синтаксис ввода и вывода у iproute2 разный,
// поэтому принимаем обе формы.
func ipRouteLocalDefaultPresent(dump string) bool {
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "local default") || strings.HasPrefix(line, "local 0.0.0.0/0") {
			return true
		}
	}
	return false
}

// EnsureIPRule restores the policy-routing half of the interception: our
// fwmark `ip rule` and the local catch-all route in our table. Ставит их
// Install, а переставлял после каждой перестройки firewall netfilter.d-хук —
// в awgm-режиме полного хука нет (перехват живёт в таблице awgm, которую ndm
// не трогает), и восстановить эту половину больше некому. Пропажа
// молчаливая: правила TPROXY срабатывают, счётчики растут, но помеченные
// пакеты не уходят в нашу таблицу — перехват слепнет, соединений ноль.
// Проверять каждый тик двумя чтениями дешевле, чем доказывать, что чужой код
// наши `ip rule` не трогает.
//
// Идемпотентно: обе половины на месте — ни одной мутации. Безусловный
// `ip rule add` плодил бы дубли на автоприоритетах (от них и лечит
// drainFwmarkRules), поэтому «есть — не трогать» здесь обязательно.
func (it *IPTables) EnsureIPRule(ctx context.Context) error {
	rules, err := it.runIPOut(ctx, "rule", "show")
	if err != nil {
		return fmt.Errorf("dump ip rules: %w", err)
	}
	if !ipRuleFwmarkPresent(rules) {
		if err := it.runIP(ctx, "rule", "add", "fwmark", fmt.Sprintf("0x%x", Fwmark),
			"table", fmt.Sprintf("%d", RoutingTable),
			"priority", fmt.Sprintf("%d", IPRulePriority)); err != nil {
			if !strings.Contains(err.Error(), "File exists") {
				return fmt.Errorf("ip rule add: %w", err)
			}
		}
	}
	routes, err := it.runIPOut(ctx, "route", "show", "table", fmt.Sprintf("%d", RoutingTable))
	if err != nil {
		return fmt.Errorf("dump table %d: %w", RoutingTable, err)
	}
	if ipRouteLocalDefaultPresent(routes) {
		return nil
	}
	// Без маршрута правило указывает в пустую таблицу, и перехват слеп ровно
	// так же, как без самого правила.
	if err := it.runIP(ctx, "route", "add", "local", "0.0.0.0/0", "dev", "lo",
		"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip route add: %w", err)
		}
	}
	return nil
}

func (it *IPTables) Install(ctx context.Context, spec RestoreInputSpec) error {
	// Scrub any existing PREROUTING jumps to AWGM-TPROXY before inserting
	// the new one. iptables-restore --noflush + -I PREROUTING 1 would
	// otherwise stack a duplicate jump on every restart / mark-change /
	// re-Enable: the stale rule from the previous policy/mark survives
	// because mangle isn't flushed, and the new rule lands in front of it.
	// Idempotent: a no-op when no prior jumps exist.
	it.removeSourceHooks(ctx, true)

	intercept := buildInterceptRestoreInput(spec)
	nat := buildNatRestoreInput(spec)
	input := intercept + nat

	if spec.AwgmMode {
		hasDNSRescue := strings.Contains(nat, DNSRescueTag)

		// 1. Снести артефакты прежнего (legacy) режима — ДО записи своих
		//    файлов: cleanupHook сносит ВСЕ три блоба, включая nat. Вызов
		//    после persistRules стёр бы nat-блоб, который узкий хук только
		//    что получил на чтение, и хук стал бы вечным no-op.
		removeNetfilterHookFile()
		if it.cleanupHook != nil {
			it.cleanupHook()
		}
		// 2. Записать только nat: перехват живёт в таблице awgm, которую
		//    ndm не трогает, и восстановления не требует. Без DNS-RESCUE
		//    блоб вырождается в пустую транзакцию — такой файл никто не
		//    читает, поэтому передаём пустую секцию и он сносится.
		natBlob := ""
		if hasDNSRescue {
			natBlob = nat
		}
		if it.persistRules != nil {
			if err := it.persistRules("", "", natBlob); err != nil {
				return fmt.Errorf("write netfilter rules: %w", err)
			}
		}
		// 3. Узкий хук — только когда DNS-RESCUE реально эмитится.
		if it.persistDNSHook != nil {
			if err := it.persistDNSHook(hasDNSRescue); err != nil {
				return fmt.Errorf("write dns hook: %w", err)
			}
		}
	} else {
		// Симметрично awgm-ветке (removeNetfilterHookFile выше): снести узкий
		// DNS-хук awgm-режима ДО записи своих файлов. Без этого сноса демоция
		// на рестарте демона (awgm был активен, SelectBackend откатил на legacy)
		// оставляет узкий хук жить рядом с полным: в режиме MatchAll его guard
		// не срабатывает никогда, и он на каждом nat-событии повторно скармливает
		// legacy nat-блоб через --noflush — правила AWGM-REDIRECT и джамп
		// копятся между стираниями таблиц.
		removeNetfilterDNSHook()
		if it.persistRules != nil {
			if err := it.persistRules(input, intercept, nat); err != nil {
				return fmt.Errorf("write netfilter rules: %w", err)
			}
		}
		if it.persistHook != nil {
			if err := it.persistHook(!spec.DSCPOnly); err != nil {
				return fmt.Errorf("write netfilter hook: %w", err)
			}
		}
	}
	// Скрипт вытеснения — в обоих режимах и на каждом Install: окно между
	// стартом движка и установкой правил фейл-опен одинаково, а в awgm-режиме
	// хука, с которым скрипт раньше ехал одним куском, нет. Безусловная
	// перезапись заодно освежает версию, оставшуюся от прежнего режима.
	if it.persistCtClean != nil {
		if err := it.persistCtClean(); err != nil {
			return fmt.Errorf("write ctclean script: %w", err)
		}
	}
	// policy-tun: blackhole в этом режиме не применяется — снести файл правил,
	// оставшийся от прежнего режима, иначе хук прежней установки продолжил бы
	// поднимать fail-closed DROP на каждой перезагрузке netfilter.
	if spec.DSCPOnly && it.cleanupBlackhole != nil {
		it.cleanupBlackhole()
	}
	// restore и restoreLayout — ОДИН снимок ПРЯМО перед восстановлением, взятый
	// заново: spec.AwgmMode решён на service-уровне ещё до входа в Install
	// (removeSourceHooks выше уже пробежал по своей,
	// отдельной раскладке), и несериализованная демоция с Enable-пути могла
	// успеть переключить канал уже ПОСЛЕ этого. Расхождение — блоб чужой
	// раскладки в чужой канал — не применяем: громкий, типизированный отказ
	// вместо тихого провала или неопределённого поведения бинаря на незнакомой
	// таблице.
	restore, restoreLayout := it.activeRestoreLayout()
	if restoreLayout != spec.AwgmMode {
		return fmt.Errorf("install: канал сменился между выбором режима и restore (канал awgm=%v, спецификация awgm=%v): %w",
			restoreLayout, spec.AwgmMode, ruleChannelError{errors.New("раскладка канала разошлась со спецификацией")})
	}
	if spec.AwgmMode {
		// Два канала: перехват — бандл-бинарём в таблицу awgm, DNS-RESCUE —
		// штатным iptables, потому что правило обязано попасть в nat ndm.
		if err := restore(ctx, intercept); err != nil {
			return fmt.Errorf("iptables-awgm-restore: %w", ruleChannelError{err})
		}
		if strings.Contains(nat, DNSRescueTag) && it.legacyRestoreNoflush != nil {
			if err := it.legacyRestoreNoflush(ctx, nat); err != nil {
				return fmt.Errorf("iptables-restore dns-rescue: %w", err)
			}
		}
	} else if err := restore(ctx, input); err != nil {
		return fmt.Errorf("iptables-restore: %w", ruleChannelError{err})
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
// Пустая секция означает «этот блоб в текущем режиме не нужен»: файл не
// создаётся, а оставшийся от прежнего режима сносится — иначе хук скормил бы
// iptables-restore устаревшие правила.
func writeNetfilterRulesFiles(combined, mangle, nat string) error {
	if err := writeOrRemoveRulesFile(netfilterRulesPath, combined); err != nil {
		return err
	}
	if err := writeOrRemoveRulesFile(netfilterMangleRulesPath, mangle); err != nil {
		return err
	}
	return writeOrRemoveRulesFile(netfilterNatRulesPath, nat)
}

func writeOrRemoveRulesFile(path, content string) error {
	if content == "" {
		_ = os.Remove(path)
		return nil
	}
	return storage.AtomicWritePerm(path, []byte(content), 0644)
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

// writeNetfilterDNSHook пишет узкий хук awgm-режима: восстанавливает только
// nat-блоб с DNS-RESCUE. Полный tproxy-хук здесь не годится — он скрабит и
// восстанавливает джампы из блобов, которых в awgm-режиме нет, то есть на
// каждом ndm-reload сносил бы работающий перехват и восстанавливал пустоту.
func writeNetfilterDNSHook() error {
	return storage.AtomicWritePerm(netfilterDNSHookPath, []byte(netfilterDNSHookScript()), 0755)
}

// netfilterDNSHookScript рендерит узкий хук. Pure (без I/O) — как и
// netfilterHookScript, чтобы тест мог проверить шелл через `sh -n`.
func netfilterDNSHookScript() string {
	return fmt.Sprintf(`#!/bin/sh
# awg-manager: восстановление DNS-RESCUE после перестройки firewall ndm.
# Только nat: перехват живёт в таблице awgm, которую ndm не стирает.
# Инверсия, как в полном хуке: при пустом $type в какой-нибудь прошивке
# положительная проверка молча убила бы хук.
[ "$type" = "ip6tables" ] && exit 0
[ "$table" = "nat" ] || exit 0
[ -f %[1]q ] || exit 0
# Guard от дублей: событие nat может прийти и без фактического wipe, а
# правила вставляются через -I PREROUTING 1.
if /opt/sbin/iptables -w -t nat -S PREROUTING 2>/dev/null | grep -q %[2]s; then
	exit 0
fi
/opt/sbin/iptables-restore --noflush < %[1]q
exit 0
`, netfilterNatRulesPath, DNSRescueTag)
}

func removeNetfilterDNSHook() {
	_ = os.Remove(netfilterDNSHookPath)
}

// removeNetfilterHookFile сносит полный tproxy-хук. Нужен при переходе в
// awgm-режим: этот хук скрабит наши джампы и восстанавливает из блобов,
// которых в awgm-режиме больше нет.
func removeNetfilterHookFile() {
	_ = os.Remove(netfilterHookPath)
}

func writeNetfilterHook(includeBlackhole bool) error {
	return storage.AtomicWritePerm(netfilterHookPath, []byte(netfilterHookScript(includeBlackhole)), 0755)
}

// writeCtCleanScript кладёт скрипт вытеснения отравленных потоков. Отдельно от
// writeNetfilterHook: в awgm-режиме полного хука нет, а вытеснение на Install
// нужно всё равно — окно между стартом движка и установкой правил существует в
// обоих режимах. Пока доставка ехала одним куском с хуком, свежая установка в
// awgm-режиме скрипта не получала вовсе, а установка после legacy-режима
// доставалась со старой версией скрипта на диске.
func writeCtCleanScript() error {
	return storage.AtomicWritePerm(netfilterCtCleanPath, []byte(ctCleanScript()), 0755)
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
for mod in xt_TPROXY xt_comment xt_mark xt_connmark xt_conntrack xt_pkttype xt_dscp; do
  grep -q "^${mod} " /proc/modules 2>/dev/null && continue
  [ -f "/lib/modules/${KREL}/${mod}.ko" ] && insmod "/lib/modules/${KREL}/${mod}.ko" 2>/dev/null || true
done
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
`, netfilterRulesPath, ChainName, Fwmark, RoutingTable, IPRulePriority, RedirectChain, DNSRescueTag, DNSNoPolicyTag, IngressTag, deadBranch, BlackholeChain, netfilterMangleRulesPath, netfilterNatRulesPath, netfilterCtCleanPath)
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
// Дамп PREROUTING читается из той таблицы, где стоят наши правила: mangle в
// обычном режиме, awgm в awgm-режиме. Штатный бинарь про таблицу awgm не знает,
// поэтому второй заход делает бандл-бинарь.
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
# Дамп PREROUTING из той таблицы, где реально стоят НАШИ правила: и проверка
# живого джампа, и марка читаются дальше только из него. В обычном режиме
# правила лежат в mangle и их видит штатный iptables; в awgm-режиме — в таблице
# awgm, про которую знает только бинарь из бандла.
# Штатный пробуем первым — это основной путь на большинстве моделей.
# Якорь на нашу цепочку обязателен: без него за свой сошёл бы любой непустой
# дамп, включая правила ndm, и марка ниже бралась бы из чужого правила.
AWGM_BIN=%[2]s
# Единственная форма якоря на нашу цепочку: её спрашивают и выбор таблицы, и
# гейт PPE ниже — два разных якоря на один вопрос разъехались бы. Голый
# substring схватил бы более длинное имя (%[1]s-X) или комментарий с этой
# подстрокой.
has_our_jump() { printf '%%s\n' "$1" | grep -qE -- '-[jg] %[1]s($| )'; }
awgm_mode=0
prerouting="$(/opt/sbin/iptables -w -t mangle -S PREROUTING 2>/dev/null)"
if ! has_our_jump "$prerouting" && [ -x "$AWGM_BIN" ]; then
  prerouting="$("$AWGM_BIN" -w -t awgm -S PREROUTING 2>/dev/null)"
  # Режим определяется по тому, ГДЕ нашлись наши правила, а не по настройке:
  # скрипт зовёт и netfilter.d-хук, которому настройка недоступна.
  has_our_jump "$prerouting" && awgm_mode=1
fi
# Hardware offload (MTK PPE) flush — issue #684. Runs before the conntrack work
# and independently of it: the flows it heals carry no NAT, so no conntrack
# lookup would find them. Only once the TPROXY jump is really back — flushing
# while it is absent just re-teaches the same flows through the same hole.
if has_our_jump "$prerouting" \
  && [ -w /proc/sys/net/hwnat/ppe_flush ] && echo 1 > /proc/sys/net/hwnat/ppe_flush 2>/dev/null; then
  logger -t awgm-ctclean "flushed PPE offload table after TPROXY restore"
fi
CT=/opt/sbin/conntrack
[ -x "$CT" ] || { logger -t awgm-ctclean "conntrack tool missing — poisoned-flow eviction skipped"; exit 0; }
# awgm-режим: гарантия «весь трафик члена политики идёт через sing-box».
# Уязвима запись, НЕ прошедшая через -j AWGMPPE: ядро печатает у неё [FASTNAT]
# (флаг ровно по !ct->fast_ext). Помеченные записи — те, что реально прошли
# перехват, — не трогаем. Оба протокола: TCP связывается с fastpath за доли
# секунды и раньше не вытеснялся вовсе.
#
# Метка политики читается тут же из живого джампа — своя, а не общая с
# UDP-ветками ниже: те стоят за выходом по отсутствию WAN-адреса, а этот проход
# обязан работать и без него.
if [ "$awgm_mode" -eq 1 ]; then
  fpm="$(printf '%%s\n' "$prerouting" \
    | sed -n 's/.*-m connmark --mark \(0x[0-9a-fA-F]*\).*-[jg] %[1]s.*/\1/p' | head -n 1)"
  [ -n "$fpm" ] && fpm="$(printf '%%d' "$fpm" 2>/dev/null)"
  fastnat="$(awk -v pm="$fpm" '
    # Локальные назначения — приближение списка RETURN-исключений перехвата
    # (emitBypassReturns): LAN-to-LAN, сам роутер, loopback, link-local, CGNAT,
    # multicast. Их перехват не трогает по дизайну, и вытеснять их нельзя —
    # оборвало бы в том числе веб-сессию, из которой движок включают.
    # Пользовательские bypass-подсети сюда не попадают: их потоки вытеснятся и
    # переустановятся сами, уже мимо прокси.
    function local_dst(d) {
      return d ~ /^(10\.|127\.|169\.254\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|100\.(6[4-9]|[7-9][0-9]|1[0-2][0-9])\.|22[4-9]\.|23[0-9]\.)/;
    }
    /\[FASTNAT\]/ && ($3 == "tcp" || $3 == "udp") {
      src = ""; dst = ""; sp = ""; dp = ""; mark = ""; hasmac = 0;
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^src=/ && src == "") src = substr($i, 5);
        else if ($i ~ /^dst=/ && dst == "") dst = substr($i, 5);
        else if ($i ~ /^sport=/ && sp == "") sp = substr($i, 7);
        else if ($i ~ /^dport=/ && dp == "") dp = substr($i, 7);
        else if ($i ~ /^mark=/) mark = substr($i, 6);
        else if ($i ~ /^mac=/) hasmac = 1;
      }
      # Скоуп: метка политики, а при MatchAll (метки нет) — любой поток,
      # рождённый в LAN. mac= есть на каждом LAN-потоке Keenetic и нет у
      # собственных потоков роутера, поэтому он же исключает роутер.
      if (pm != "" && mark != pm) next;
      if (pm == "" && !hasmac) next;
      if (src == "" || dst == "" || sp == "" || dp == "") next;
      if (local_dst(dst)) next;
      print $3, src, dst, sp, dp;
    }' /proc/net/nf_conntrack 2>/dev/null)"
  if [ -n "$fastnat" ]; then
    printf '%%s\n' "$fastnat" | while read -r pr s d sp dp; do
      "$CT" -D -p "$pr" -s "$s" -d "$d" --sport "$sp" --dport "$dp" >/dev/null 2>&1
    done
    logger -t awgm-ctclean "evicted $(printf '%%s\n' "$fastnat" | wc -l) unprotected flow(s) in awgm mode"
  fi
fi
wan_ips=""
for dev in $(/opt/sbin/ip -4 route show default 2>/dev/null | sed -n 's/.* dev \([^ ]*\).*/\1/p' | sort -u); do
  wan_ips="$wan_ips $(/opt/sbin/ip -4 addr show dev "$dev" 2>/dev/null | sed -n 's/.*inet \([0-9.]*\).*/\1/p')"
done
set -- $wan_ips
[ $# -gt 0 ] || exit 0
pmark="$(printf '%%s\n' "$prerouting" \
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
`, ChainName, filepath.Join(awgmbackend.BundleDir, "sbin", "iptables"))
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
	// Узкий DNS-хук awgm-режима трогать нельзя: полный tproxy-скрипт его
	// затрёт, и после первого же рестарта демона DNS-RESCUE перестанет
	// восстанавливаться.
	if _, err := os.Stat(netfilterDNSHookPath); err == nil {
		return
	}
	if _, err := os.Stat(netfilterHookPath); err != nil {
		return
	}
	// true: обычный (tproxy) хук с fail-closed веткой. В policy-tun это
	// БЕЗВРЕДНО и режим не гейтится: DEAD-ветка хука первым делом проверяет
	// наличие файла blackhole-правил, а его снимает и Install(DSCPOnly)
	// (cleanupBlackhole), и Uninstall (RemoveBlackhole) на выходе из tproxy —
	// без файла ветка no-op. Ближайший Install(DSCPOnly) перепишет хук без неё.
	_ = writeNetfilterHook(true)
	// Скрипт вытеснения раньше ехал одним куском с хуком и обновлялся вместе
	// с ним. Доставка разъехалась, а повод обновления остался тем же: хук
	// зовёт скрипт после каждого восстановления, и старая версия не должна
	// досидеть до ближайшего Install.
	_ = writeCtCleanScript()
}

func (it *IPTables) Uninstall(ctx context.Context) error {
	if it.cleanupHook != nil {
		it.cleanupHook()
	}
	// Узкий DNS-хук снимаем явно. Без блоба он и так no-op, но уцелевший
	// файл заставил бы refreshNetfilterHookIfPresent навсегда пропускать
	// обновление полного хука после возврата в обычный режим. В обычном
	// режиме файла нет — вызов no-op.
	removeNetfilterDNSHook()
	// Also tear down the fail-closed blackhole if one lingers (engine died,
	// blackhole engaged, then the user disabled the router): delete its rules
	// file, scrub the jump, drop the chain.
	it.RemoveBlackhole(ctx)
	it.removeSourceHooks(ctx, true)
	it.dropInterceptionChains(ctx)
	it.DrainPolicyRouting(ctx)
	return nil
}

// UninstallForeignRules снимает правила ЧУЖОЙ раскладки — той, в которой этот
// процесс не работает. «Чужая» задаётся раскладкой самого объекта, а не
// бинарём: стек один, бандл-бинарь видит и legacy-таблицы, поэтому зовущий
// (adoptAndClean) собирает scratch-фасад с раскладкой прежнего режима и все
// команды здесь автоматически уходят в её таблицы. Не Uninstall: тот снимает
// наше СОБСТВЕННОЕ хозяйство целиком, а здесь чужой только один слой — правила
// в цепочках чужой раскладки. Остальное принадлежит живому каналу и трогать его
// нельзя:
//
//   - DNS-RESCUE и узкий DNS-хук в awgm-режиме НАШИ. Правило обязано лежать в
//     nat-таблице роутера, и туда его кладёт legacy-канал при активном awgm —
//     это часть двухканального Install, а не мусор прошлой жизни. Снятое здесь,
//     оно осталось бы снятым до первого успешного Install: упади подъём движка
//     на RCI или на ожидании sing-box — и DNS-RESCUE пропал бы ровно в том
//     состоянии «движок мёртв», ради которого он существует.
//   - Файлы-воскрешатели (блобы netfilter.d, полный хук, узкий DNS-хук)
//     переписывает ближайший Install: он первым же шагом сносит артефакты
//     чужого режима сам — в обе стороны (removeNetfilterHookFile при переходе
//     в awgm, removeNetfilterDNSHook при переходе в legacy).
//   - Половина перехвата в policy-routing (`ip rule` + наша таблица) от канала
//     не зависит вовсе: снос оставил бы работающий перехват живого канала
//     слепым.
func (it *IPTables) UninstallForeignRules(ctx context.Context) {
	it.RemoveBlackhole(ctx)
	it.removeSourceHooks(ctx, false)
	it.dropInterceptionChains(ctx)
}

// dropInterceptionChains опустошает и удаляет цепочки перехвата СВОЕЙ
// раскладки. Только своей: стек тут ОДИН — бандл-бинарь видит ровно те же
// mangle/nat, что и штатный, — и флаш чужой раскладки «на всякий случай» снёс
// бы живой перехват другого режима. Чужую раскладку чистит тот, у кого на
// руках объект с ней, — см. UninstallForeignRules и adoptAndClean
// (scratch-фасад).
func (it *IPTables) dropInterceptionChains(ctx context.Context) {
	run, layout := it.activeRunLayout()
	for _, c := range interceptionChains(layout) {
		_ = run(ctx, "-t", c.table, "-F", c.chain)
		_ = run(ctx, "-t", c.table, "-X", c.chain)
	}
}

// DrainPolicyRouting снимает половину перехвата, живущую в policy-routing:
// правило `ip rule` по нашей метке и всю нашу таблицу маршрутов. От бэкенда
// применения правил она не зависит — одна на оба режима, — поэтому вынесена из
// снятия правил канала и зовётся отдельно там, где перехвата больше нет.
//
// Дренаж ВСЕХ правил метки, а не одного: Install исторически копил дубли на
// автоприоритетах 0-N, и одиночный `del` оставил бы остальные. Цикл до ENOENT,
// с защитным ограничением по числу проходов.
func (it *IPTables) DrainPolicyRouting(ctx context.Context) {
	it.drainFwmarkRules(ctx)
	_ = it.runIP(ctx, "route", "flush", "table", fmt.Sprintf("%d", RoutingTable))
}

// removeSourceHooks снимает наши джампы из PREROUTING и наши comment-правила.
// Джампы и правила блоба — только в таблицах СВОЕЙ раскладки: стек один, и
// скраб mangle/nat awgm-каналом снёс бы живой legacy-перехват (см.
// dropInterceptionChains). withDNSRescue гейтит ОДИН тег — DNS-RESCUE: оно
// лежит в nat-таблице роутера, оставаясь при этом нашим текущим, и снятие
// правил чужого канала обязано пройти мимо него (см. UninstallForeignRules).
//
// activeRun/activeRunOut/layout — ОДИН снимок (activeChannelLayout), не три
// раздельных: конкурентный UseAwgm/UseLegacy между отдельными захватами
// runnerMu мог бы спарить канал одного бэкенда с раскладкой другого — скраб
// ушёл бы не тем бинарём или не в ту таблицу, и уцелевший джамп остался бы
// невидим активному каналу (см. обязательство Task 7 по гонке Enable/Reconcile).
func (it *IPTables) removeSourceHooks(ctx context.Context, withDNSRescue bool) {
	activeRun, activeRunOut, layout := it.activeChannelLayout()
	it.removeSourceHooksFromTable(ctx, activeRun, activeRunOut, udpChainTable(layout), ChainName)
	it.removeSourceHooksFromTable(ctx, activeRun, activeRunOut, tcpChainTable(layout), RedirectChain)

	// Канал скраба выбирается по принципу «чья таблица»: правило снимает тот
	// бинарь, который его поставил, иначе снять его нечем. Наши правила в наших
	// таблицах — активный канал; правила в таблицах firewall'а роутера —
	// ВСЕГДА legacy, потому что и ставятся они туда legacy-каналом.
	legacyRun, legacyRunOut := it.legacyChannel()

	// DNS-RESCUE: direct PREROUTING REDIRECT rules in nat, tagged with
	// `-m comment --comment AWGM-DNS-RESCUE`. Scrub before re-install
	// so we don't accumulate duplicates and so port changes (e.g. NDMS
	// reassigned ndnproxy:41100→41200) propagate cleanly. Лежат в nat-таблице
	// роутера, куда их кладёт legacy-канал даже в awgm-режиме, — им же и
	// снимаем.
	if withDNSRescue {
		removeCommentTaggedRules(ctx, legacyRun, legacyRunOut, "nat", "PREROUTING", DNSRescueTag)
	}
	// Legacy: DNS-NOPOLICY MARK rules in mangle from 2.10.x and
	// earlier. Always scrub on Install for upgrade migration — the
	// rules are dead code now, but if left in place they'd accumulate
	// across upgrades. Ставились они версиями, у которых awgm-режима не было
	// вовсе, то есть лежат в legacy-mangle.
	removeCommentTaggedRules(ctx, legacyRun, legacyRunOut, "mangle", "PREROUTING", DNSNoPolicyTag)
	// Ingress и PPE эмитятся в блобе перехвата — таблица активной раскладки.
	// Ingress-scope: direct MARK/CONNMARK rules in PREROUTING, tagged
	// AWGM-INGRESS. Scrub before re-install so the list stays idempotent and
	// removed interfaces don't leave dangling marks.
	removeCommentTaggedRules(ctx, activeRun, activeRunOut, udpChainTable(layout), "PREROUTING", IngressTag)
	// PPE (awgm-режим): не джамп в AWGM-цепочку, поэтому под шаблон
	// removeSourceHooksFromTable не попадает — снимаем по comment-тегу, иначе
	// restore --noflush добавлял бы ещё одно такое правило на каждый Install.
	removeCommentTaggedRules(ctx, activeRun, activeRunOut, udpChainTable(layout), "PREROUTING", PPETag)
}

// removeCommentTaggedRules scrubs every rule in `chain` whose
// iptables-save output contains the given comment tag. Used for
// DNS-NOPOLICY where rules are direct PREROUTING entries (not jumps
// to a custom chain). The grep+sed pattern is the same approach as
// XKeen's removal logic — robust to rule ordering and matcher changes
// as long as the `-m comment --comment <tag>` survives serialisation.
//
// Канал передаётся вызывающим, а не берётся у активного бэкенда: часть наших
// comment-правил живёт в таблицах firewall'а роутера, и спросить о них надо
// legacy-бинарь независимо от того, чем сейчас применяются правила перехвата.
func removeCommentTaggedRules(ctx context.Context, run runFn, runOut runOutFn, table, chain, tag string) {
	if runOut == nil || run == nil {
		return
	}
	out, err := runOut(ctx, "-t", table, "-S", chain)
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
		_ = run(ctx, args...)
	}
}

// run/runOut приходят от вызывающего, а не берутся у активного бэкенда сами:
// вызывающий обязан снять их ОДНИМ снимком вместе с раскладкой (см.
// activeChannelLayout) — раздельный повторный запрос здесь развязал бы пару и
// вернул тот же разъезд, ради которого снимок существует.
func (it *IPTables) removeSourceHooksFromTable(ctx context.Context, run runFn, runOut runOutFn, table, chain string) {
	if runOut == nil || run == nil {
		return
	}
	out, err := runOut(ctx, "-t", table, "-S", "PREROUTING")
	if err != nil {
		return
	}
	// Match both `-j chain` (old jump syntax pre-fastnat-fix) and
	// `-g chain` (current goto syntax) so upgrading installs scrub
	// stale jumps from previous versions before we re-append the new one.
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
		_ = run(ctx, args...)
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
//
// Опрос идёт в раскладке АКТИВНОГО бэкенда: цепочки лежат в разных таблицах, и
// вопрос про mangle/nat в awgm-режиме не нашёл бы ничего никогда.
func (it *IPTables) HasAnyInstalled(ctx context.Context) bool {
	run, awgmLayout := it.activeRunLayout()
	for _, c := range interceptionChains(awgmLayout) {
		if run(ctx, "-t", c.table, "-nL", c.chain) == nil {
			return true
		}
	}
	return false
}

// IsInstalled returns true only when both AWGM chains exist. Used for the
// enabled-reconcile path: if either chain is missing a full re-install is
// needed to reach a known-good state. Раскладка — как у HasAnyInstalled.
func (it *IPTables) IsInstalled(ctx context.Context) bool {
	run, awgmLayout := it.activeRunLayout()
	for _, c := range interceptionChains(awgmLayout) {
		if run(ctx, "-t", c.table, "-nL", c.chain) != nil {
			return false
		}
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
	// Один снимок раннера и раскладки на весь Probe: смена бэкенда между двумя
	// таблицами дала бы вердикт, собранный из двух разных раскладок.
	runOut, awgmLayout := it.activeRunOutLayout()
	udpTable := udpChainTable(awgmLayout)
	udpDump, err := runOut(ctx, "-t", udpTable, "-S")
	if err != nil {
		return false, false, err
	}
	// В awgm-раскладке обе цепочки живут в одной таблице — тот же дамп
	// отвечает и про TCP, второй exec не нужен.
	tcpDump := udpDump
	if tcpTable := tcpChainTable(awgmLayout); tcpTable != udpTable {
		if tcpDump, err = runOut(ctx, "-t", tcpTable, "-S"); err != nil {
			return false, false, err
		}
	}
	mChain, mJump := scanChainDump(udpDump, ChainName)
	nChain, nJump := scanChainDump(tcpDump, RedirectChain)
	installed = mChain && nChain
	jumps = installed && mJump && nJump
	return installed, jumps, nil
}

func scanChainDump(out, chain string) (chainExists, jumpExists bool) {
	for _, line := range strings.Split(out, "\n") {
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
