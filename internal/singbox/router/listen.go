package router

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Socket states in /proc/net/{tcp,udp} (hex). TCP LISTEN = 0x0A; a bound,
// unconnected UDP socket = 0x07. sing-box's redirect-in (TCP, 0.0.0.0) and
// tproxy-in (UDP, 127.0.0.1) inbounds sit in these states; the check below
// matches port+state only, so it is bind-address-agnostic.
//
// Why state matters: sing-box's accepted TCP connections REUSE the listener's
// local port (RedirectPort), so /proc/net/tcp carries the LISTEN row plus one
// row per live flow — all sharing local port RedirectPort but in state 01
// (ESTABLISHED). Matching on the local port alone would false-positive on
// dozens of connections; we must also match the state.
const (
	tcpStateListen = "0A"
	udpStateBound  = "07"
)

// localPortInState reports whether procData (contents of /proc/net/tcp or
// /proc/net/udp) has a socket whose LOCAL-address port == port and whose
// state == state. It parses the fixed columns instead of substring-matching:
// column 1 (0-based) is local_address "HEXIP:HEXPORT", column 3 is the state.
func localPortInState(procData string, port int, state string) bool {
	want := fmt.Sprintf("%04X", port)
	for _, line := range strings.Split(procData, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		colon := strings.LastIndexByte(f[1], ':')
		if colon < 0 {
			continue
		}
		if f[1][colon+1:] == want && f[3] == state {
			return true
		}
	}
	return false
}

// singboxListeningProbe is the seam GetStatus uses to check sing-box socket
// binding. Overridable in tests so status checks don't touch real procfs.
var singboxListeningProbe = singboxIntercepting

// singboxIntercepting reports whether sing-box is actually listening on both
// router inbound sockets — the TCP REDIRECT port (LISTEN) and the UDP TPROXY
// port (bound). Process-alive (pidof) is not enough: an inbound that failed to
// bind would leave iptables handing packets to a dead socket. Reads procfs
// directly — ss/netstat are not in stock Entware. A read error reports false.
func singboxIntercepting() bool {
	tcp, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return false
	}
	if !localPortInState(string(tcp), RedirectPort, tcpStateListen) {
		return false
	}
	udp, err := os.ReadFile("/proc/net/udp")
	if err != nil {
		return false
	}
	return localPortInState(string(udp), TPROXYPort, udpStateBound)
}

// ---------------------------------------------------------------------------
// fakeip-tun readiness probes
//
// These three seams replace the tproxy socket/jump checks for fakeip-tun mode:
// the tun path has no inbound sockets and no iptables jumps. The real
// implementations read live kernel state (sysfs carrier, a live DNS query,
// /proc/net/route) and are stand-verified later (Task 1F.1); the package-var
// seam shape keeps the GetStatus/waitForSingbox branching unit-testable now.
// All three are fail-closed: any read/parse/timeout error reports "not ready".
// ---------------------------------------------------------------------------

// fakeIPDNSProbeDomain is the domain queried by liveFakeIPDNSProbe. It only
// reports ready if this name resolves INTO the fakeip pool, which holds for the
// v1 default config (empty DomainRuleSets ⇒ every A/AAAA is faked).
//
// CONSTRAINT (roadmap landmine): once domain-scoping (DomainRuleSets) becomes
// user-configurable, a ruleset that excludes this domain would make the live
// answer a real public IP — never in-pool — so waitForSingbox would never see
// readiness and Enable would hang to timeout. When that feature ships, the
// readiness probe MUST query a domain known to be in fakeip scope (e.g. one
// representative of the active ruleset, or a synthetic always-faked probe name)
// instead of this fixed public name.
const fakeIPDNSProbeDomain = "example.com"

// fakeIPDNSProbeTimeout bounds a SINGLE live DNS query attempt inside
// liveFakeIPDNSProbe so one hung lookup cannot stall the whole probe.
const fakeIPDNSProbeTimeout = 1500 * time.Millisecond

// fakeIPDNSConfirmTimeout is the generous overall budget the best-effort
// post-readiness DNS confirm (enableFakeIPTun) gives the probe. It is longer
// than a single attempt so the probe's internal retry loop can ride out the
// brief first-seconds slowness that resolv.conf's attempts:1 would otherwise
// turn into a hard failure (stand-verified 2026-06-15).
const fakeIPDNSConfirmTimeout = 3 * time.Second

// tunReadyProbe is the seam GetStatus/waitForSingbox use for tun liveness.
// Overridable in tests.
var tunReadyProbe = tunInterfaceReady

// sysClassNetDir — корень sysfs сетевых устройств; шов для тестов tunInterfaceReady.
var sysClassNetDir = "/sys/class/net"

// tunInterfaceReady reports whether the tun iface has carrier (sing-box
// attached). Reads /sys/class/net/<iface>/carrier; a read error or "0" → not
// ready. A bare tun device with no attached endpoint reports carrier 0 (or the
// read fails with EINVAL), so this distinguishes "iface exists" from "sing-box
// is actually driving it".
func tunInterfaceReady(iface string) bool {
	if iface == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(sysClassNetDir, iface, "carrier"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

// fakeIPDNSProbe is the seam for the live "is sing-box answering with a fakeip
// address" check. Overridable in tests. Returns true iff a DNS query to
// dnsAddr:53 returns an A answer inside fakeipNet. Fail-closed (timeout/error →
// false).
var fakeIPDNSProbe = liveFakeIPDNSProbe

// liveFakeIPDNSProbe queries dnsAddr:53 (the tun-side /30 host where sing-box
// listens) for a probe domain and reports whether any returned A address falls
// inside the fakeip pool. A positive result proves the whole path is live: the
// tun /30 connected route makes dnsAddr reachable, sing-box's DNS server is up,
// and it is minting fakeip addresses. Uses the Go resolver with a custom Dial
// pinned to dnsAddr:53 so it never consults the host resolver.
//
// The Go resolver still inherits resolv.conf's `options attempts:1` (it controls
// retries, not the per-query timeout we set below), so a single LookupNetIP makes
// exactly ONE attempt. To not let that one-shot fragility decide the verdict, we
// loop a few attempts INSIDE the caller's context budget: each attempt is bounded
// by fakeIPDNSProbeTimeout, and we stop as soon as one succeeds or the caller's
// ctx is done. This stays dependency-free (no raw DNS client) — the main fix for
// the false Enable-timeout is that this probe is no longer a readiness gate.
func liveFakeIPDNSProbe(ctx context.Context, dnsAddr string, fakeipNet netip.Prefix) bool {
	if dnsAddr == "" || !fakeipNet.IsValid() {
		return false
	}
	target := net.JoinHostPort(dnsAddr, "53")
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", target)
		},
	}
	for {
		if ctx.Err() != nil {
			return false
		}
		qctx, cancel := context.WithTimeout(ctx, fakeIPDNSProbeTimeout)
		addrs, err := r.LookupNetIP(qctx, "ip4", fakeIPDNSProbeDomain)
		cancel()
		if err == nil {
			for _, a := range addrs {
				if fakeipNet.Contains(a.Unmap()) {
					return true
				}
			}
			return false // answered, but not in pool — a real verdict, don't retry
		}
		// err != nil: timeout/refused/no-route — retry until the caller's ctx is
		// exhausted (the per-attempt qctx may have expired; the outer ctx gates us).
	}
}

// fakeIPPoolRoutePresent is the seam for "the fakeip pool auto-route to the tun
// iface exists". Overridable in tests. Reads /proc/net/route (v4). Fail-closed.
var fakeIPPoolRoutePresent = liveFakeIPPoolRoutePresent

// liveFakeIPPoolRoutePresent parses /proc/net/route and reports whether a v4
// route for the fakeip pool out the tun iface is installed — the honest
// structural check for steady-state Active (the fakeip equivalent of "TPROXY
// jumps present"). /proc/net/route stores Destination and Mask as host-order
// hex (see parseProcRouteHex); a row matches when its iface equals iface and
// its (destination, mask) equals the pool's network address and prefix mask.
//
// v1 SCOPE: v4 pool route only. The v6 pool auto-route (fc00::/18 → opkgtun) is
// NOT checked here, so GetStatus.Active can report true while the v6 path is
// structurally absent. Accepted for v1 because the v4 resolver answers both A
// and AAAA (spec §3.8 — v6-only clients are out of v1 scope); when v6 delivery
// becomes first-class, mirror this check against /proc/net/ipv6_route.
func liveFakeIPPoolRoutePresent(iface string, pool netip.Prefix) bool {
	if iface == "" || !pool.IsValid() || !pool.Addr().Is4() {
		return false
	}
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	return slices.Contains(parseRoutes4(iface, string(data)), pool.Masked())
}

// fakeIPPoolRoute6Present is the seam for "a v6 CIDR route to the tun iface
// exists". Overridable in tests. Reads /proc/net/ipv6_route. Fail-closed (read
// error / parse error → false → the drift-heal re-adds, which NDMS treats
// idempotently). The reconcile drift-heal uses it to gate the per-CIDR v6
// re-add, exactly as fakeIPPoolRoutePresent gates the v4 re-add — this closes
// the v6-only self-heal gap (a config with v6 CIDRs but no v4 had no drift
// signal) and keeps steady-state POSTs at zero.
var fakeIPPoolRoute6Present = liveFakeIPPoolRoute6Present

// liveFakeIPPoolRoute6Present reports whether a v6 route for the given prefix
// out the given iface is installed in /proc/net/ipv6_route (format —
// parseRoutes6).
func liveFakeIPPoolRoute6Present(iface string, pool netip.Prefix) bool {
	if iface == "" || !pool.IsValid() || !pool.Addr().Is6() {
		return false
	}
	data, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return false
	}
	return slices.Contains(parseRoutes6(iface, string(data)), pool.Masked())
}

// fakeIPTunRoutes — маршруты ядра через iface, оба семейства, без повторов
// (/proc/net/ipv6_route показывает и таблицы политик). Шов для тестов.
// Ошибка чтения — ошибка: по неполному списку снимать нельзя (F756). Нет
// /proc/net/ipv6_route (ядро без IPv6) — не ошибка: v6-маршрутов нет.
var fakeIPTunRoutes = liveFakeIPTunRoutes

// rtfCache — RTF_CACHE из include/uapi/linux/ipv6_route.h.
const rtfCache = 0x01000000

// procNetRoute / procNetIPv6Route — пути таблиц маршрутов (подмена в тестах).
var procNetRoute, procNetIPv6Route = "/proc/net/route", "/proc/net/ipv6_route"

func liveFakeIPTunRoutes(iface string) ([]netip.Prefix, error) {
	v4, err := os.ReadFile(procNetRoute)
	if err != nil {
		return nil, err
	}
	v6, err := os.ReadFile(procNetIPv6Route)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return parseTunRoutes(iface, string(v4), string(v6)), nil
}

// parseTunRoutes — маршруты iface из обеих таблиц, без повторов.
func parseTunRoutes(iface, v4, v6 string) []netip.Prefix {
	var out []netip.Prefix
	seen := map[netip.Prefix]bool{}
	for _, p := range append(parseRoutes4(iface, v4), parseRoutes6(iface, v6)...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// parseRoutes4 — маршруты iface из /proc/net/route: Destination и Mask —
// hex в порядке хоста (parseProcRouteHex); первая строка — заголовок.
func parseRoutes4(iface, data string) []netip.Prefix {
	var out []netip.Prefix
	for i, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 8 || f[0] != iface {
			continue
		}
		dest, ok1 := parseProcRouteHex(f[1])
		mask, ok2 := parseProcRouteHex(f[7])
		if !ok1 || !ok2 {
			continue
		}
		if bits, size := net.IPMask(mask[:]).Size(); size == 32 {
			out = append(out, netip.PrefixFrom(netip.AddrFrom4(dest), bits))
		}
	}
	return out
}

// parseRoutes6 — маршруты iface из /proc/net/ipv6_route. Колонки:
// destination(32 hex) dest_prefix_len(2 hex) source(32) src_prefix_len(2)
// next_hop(32) metric refcnt use flags iface; адрес — в сетевом порядке, без
// перестановки байтов (в отличие от v4).
func parseRoutes6(iface, data string) []netip.Prefix {
	var out []netip.Prefix
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[len(f)-1] != iface {
			continue
		}
		raw, derr := hex.DecodeString(f[0])
		plen, perr := strconv.ParseUint(f[1], 16, 8)
		flags, ferr := strconv.ParseUint(f[8], 16, 32)
		if derr != nil || perr != nil || ferr != nil || len(raw) != 16 {
			continue
		}
		// Клоны кэша (до 4.15 живут в дереве и видны здесь) — не маршруты:
		// в NDMS их нет, снятие было бы ошибкой с E на каждом тике.
		if flags&rtfCache != 0 {
			continue
		}
		out = append(out, netip.PrefixFrom(netip.AddrFrom16([16]byte(raw)), int(plen)))
	}
	return out
}

// parseProcRouteHex decodes a /proc/net/route Destination/Mask field (8 hex
// chars) into a big-endian 4-byte address for comparison. The kernel prints
// these fields with %08X of a __be32, i.e. in HOST byte order — so the value
// must be decoded with native endianness: on mipsle 10.128.0.0 prints as
// "0000800A" (swap needed), on big-endian mips as "0A800000" (already in
// wire order). Hardcoding LittleEndian here broke the check on mips.
func parseProcRouteHex(s string) ([4]byte, bool) {
	var out [4]byte
	if len(s) != 8 {
		return out, false
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, false
	}
	v := binary.NativeEndian.Uint32(raw)
	binary.BigEndian.PutUint32(out[:], v)
	return out, true
}
