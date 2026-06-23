package router

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Socket states in /proc/net/{tcp,udp} (hex). TCP LISTEN = 0x0A; a bound,
// unconnected UDP socket = 0x07. sing-box's redirect-in (TCP) and tproxy-in
// (UDP) inbounds bind 0.0.0.0 in these states.
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

// tunInterfaceReady reports whether the tun iface has carrier (sing-box
// attached). Reads /sys/class/net/<iface>/carrier; a read error or "0" → not
// ready. A bare tun device with no attached endpoint reports carrier 0 (or the
// read fails with EINVAL), so this distinguishes "iface exists" from "sing-box
// is actually driving it".
func tunInterfaceReady(iface string) bool {
	if iface == "" {
		return false
	}
	data, err := os.ReadFile("/sys/class/net/" + iface + "/carrier")
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
// jumps present"). /proc/net/route stores Destination and Mask as little-endian
// hex; a row matches when its iface equals iface and its (destination, mask)
// equals the pool's network address and prefix mask.
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
	wantDest := pool.Masked().Addr().As4()
	wantMask := net.CIDRMask(pool.Bits(), 32) // big-endian 4 bytes
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if i == 0 { // header
			continue
		}
		f := strings.Fields(line)
		if len(f) < 8 {
			continue
		}
		if f[0] != iface {
			continue
		}
		dest, ok := parseProcRouteHex(f[1])
		if !ok || dest != wantDest {
			continue
		}
		mask, ok := parseProcRouteHex(f[7])
		if !ok {
			continue
		}
		if mask == [4]byte(wantMask) {
			return true
		}
	}
	return false
}

// fakeIPPoolRoute6Present is the seam for "a v6 CIDR route to the tun iface
// exists". Overridable in tests. Reads /proc/net/ipv6_route. Fail-closed (read
// error / parse error → false → the drift-heal re-adds, which NDMS treats
// idempotently). The reconcile drift-heal uses it to gate the per-CIDR v6
// re-add, exactly as fakeIPPoolRoutePresent gates the v4 re-add — this closes
// the v6-only self-heal gap (a config with v6 CIDRs but no v4 had no drift
// signal) and keeps steady-state POSTs at zero.
var fakeIPPoolRoute6Present = liveFakeIPPoolRoute6Present

// liveFakeIPPoolRoute6Present parses /proc/net/ipv6_route and reports whether a
// v6 route for the given prefix out the given iface is installed. Columns:
// destination(32 hex) dest_prefix_len(2 hex) source(32) src_prefix_len(2)
// next_hop(32) metric flags refcnt use iface. The IPv6 address is 128-bit / 32
// hex chars in NATIVE order (no little-endian swap, unlike /proc/net/route v4).
// The caller passes pool already .Masked(); a row matches when its iface equals
// iface and its (destination, prefix_len) equals the pool.
func liveFakeIPPoolRoute6Present(iface string, pool netip.Prefix) bool {
	if iface == "" || !pool.IsValid() || !pool.Addr().Is6() {
		return false
	}
	data, err := os.ReadFile("/proc/net/ipv6_route")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[len(f)-1] != iface {
			continue
		}
		raw, derr := hex.DecodeString(f[0])
		if derr != nil || len(raw) != 16 {
			continue
		}
		plen, perr := strconv.ParseUint(f[1], 16, 8)
		if perr != nil {
			continue
		}
		if netip.PrefixFrom(netip.AddrFrom16([16]byte(raw)), int(plen)) == pool {
			return true
		}
	}
	return false
}

// parseProcRouteHex decodes a /proc/net/route Destination/Mask field (8 hex
// chars, little-endian u32) into a big-endian 4-byte address for comparison.
func parseProcRouteHex(s string) ([4]byte, bool) {
	var out [4]byte
	if len(s) != 8 {
		return out, false
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return out, false
	}
	v := binary.LittleEndian.Uint32(raw)
	binary.BigEndian.PutUint32(out[:], v)
	return out, true
}
