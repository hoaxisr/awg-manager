package router

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

// LANBridgeDNSRedir pairs a Linux bridge with the ndnproxy port we
// short-circuit its mark=0 DNS into, bypassing NDMS's _NDM_DNS_FLT_REDIR
// catch-all REDIRECT that would otherwise hand the packet to whatever is
// bound to port 53 (which, with sing-box's hijack-dns side-effect, ends
// up being a transparent listener that silently drops non-TPROXY'd DNS).
type LANBridgeDNSRedir struct {
	Bridge string // kernel bridge name, e.g. "br0"
	Port   int    // ndnproxy port, e.g. 41100
}

// DiscoverLANBridges returns (bridge, ndnproxy-port) pairs for every
// Linux LAN bridge that NDMS has at least one _NDM_HOTSPOT_DNSREDIR
// REDIRECT rule for on UDP/TCP --dport 53.
//
// Why we read _NDM_HOTSPOT_DNSREDIR specifically: it's the chain that
// already maps (bridge, mark) → ndnproxy port for every NDMS access
// policy. Even when no segment-level policy is bound to a bridge in
// the NDMS web UI (so _NDM_HOTSPOT_PREROUTING_MANGL doesn't catch-all
// mark that bridge), NDMS still provisions DNSREDIR rules for it.
// That makes this chain the source of truth for "which bridges does
// NDMS know how to REDIRECT DNS for, and what port should we use".
//
// Why not let NDMS REDIRECT naturally: in nat PREROUTING NDMS runs
// _NDM_DNS_REDIRECT, which runs _NDM_DNS_FLT_REDIR FIRST, which
// unconditionally REDIRECTs DNS to :53 — terminating the chain before
// _NDM_HOTSPOT_DNSREDIR (with its per-policy ports) ever runs. With
// sing-box's hijack-dns transparent listener occupying :53, that
// REDIRECT lands on a void. Inserting our own REDIRECT to the
// _NDM_HOTSPOT_DNSREDIR port at PREROUTING position 1 sidesteps the
// FLT_REDIR catch-all and lands the packet on ndnproxy directly.
// Issue #132.
//
// When a bridge has multiple eligible (mark, port) pairs, we prefer
// the port belonging to the mark that is NOT singboxPolicyMark. The
// sing-box policy is intentionally permit-less (it exists only to feed
// TPROXY), so its ndnproxy can't resolve upstream — routing default
// DNS there would just relocate the timeout. If singboxPolicyMark is
// the only choice, we fall back to it (still better than nothing).
//
// Returns empty slice (not nil, not error) when no bridges qualify;
// callers should skip the DNS-RESCUE install logic in that case.
func DiscoverLANBridges(ctx context.Context, singboxPolicyMark string) ([]LANBridgeDNSRedir, error) {
	result, err := sysexec.Run(ctx, sysiptables.Binary, "-w", "-t", "nat",
		"-S", "_NDM_HOTSPOT_DNSREDIR")
	if err != nil || result == nil {
		// Chain doesn't exist: router has no hotspot config (fresh
		// install, no LAN policies created yet). Nothing to redirect
		// to — return empty, caller skips.
		return []LANBridgeDNSRedir{}, nil
	}

	candidates := map[string]map[string]int{}
	for _, line := range splitLines(result.Stdout) {
		iface, mark, port, ok := parseDNSRedirRule(line)
		if !ok {
			continue
		}
		if !isLinuxBridge(iface) {
			continue
		}
		if candidates[iface] == nil {
			candidates[iface] = map[string]int{}
		}
		candidates[iface][mark] = port
	}

	bridges := make([]string, 0, len(candidates))
	for b := range candidates {
		bridges = append(bridges, b)
	}
	sort.Strings(bridges)

	out := make([]LANBridgeDNSRedir, 0, len(bridges))
	for _, b := range bridges {
		port := pickPort(candidates[b], singboxPolicyMark)
		if port == 0 {
			continue
		}
		out = append(out, LANBridgeDNSRedir{Bridge: b, Port: port})
	}
	return out, nil
}

// pickPort chooses one ndnproxy port from the (mark→port) candidates
// for a bridge. It returns the port belonging to any mark other than
// singboxPolicyMark when one exists, falling back to singboxPolicyMark's
// port only when it's the sole option. Marks are sorted before selection
// so the choice is deterministic across runs — that keeps
// reconcileInstalled stable when nothing actually changed.
func pickPort(markPorts map[string]int, singboxPolicyMark string) int {
	if len(markPorts) == 0 {
		return 0
	}
	marks := make([]string, 0, len(markPorts))
	for m := range markPorts {
		marks = append(marks, m)
	}
	sort.Strings(marks)

	if singboxPolicyMark != "" {
		for _, m := range marks {
			if !strings.EqualFold(m, singboxPolicyMark) {
				return markPorts[m]
			}
		}
	}
	return markPorts[marks[0]]
}

// parseDNSRedirRule extracts (interface, mark, port) from one
// _NDM_HOTSPOT_DNSREDIR rule line. Returns ok=false unless the rule
// targets DNS port 53 with a REDIRECT — sibling rules for ports 1900
// (SSDP) and 5351 (NAT-PMP) are filtered out.
//
// Example accepted line:
//
//	-A _NDM_HOTSPOT_DNSREDIR -d 192.168.0.1/32 -i br0 -p udp -m mark --mark 0xffffaae -m pkttype --pkt-type unicast -m udp --dport 53 -j REDIRECT --to-ports 41104
func parseDNSRedirRule(line string) (iface, mark string, port int, ok bool) {
	if !strings.HasPrefix(line, "-A _NDM_HOTSPOT_DNSREDIR ") {
		return "", "", 0, false
	}
	tokens := strings.Fields(line)
	var (
		hasDNS      bool
		hasRedirect bool
	)
	for i := 0; i < len(tokens)-1; i++ {
		switch tokens[i] {
		case "-i":
			iface = tokens[i+1]
		case "--mark":
			mark = tokens[i+1]
		case "--dport":
			if tokens[i+1] == "53" {
				hasDNS = true
			}
		case "-j":
			if tokens[i+1] == "REDIRECT" {
				hasRedirect = true
			}
		case "--to-ports":
			if p, err := strconv.Atoi(tokens[i+1]); err == nil {
				port = p
			}
		}
	}
	if !hasDNS || !hasRedirect || iface == "" || mark == "" || port == 0 {
		return "", "", 0, false
	}
	return iface, mark, port, true
}

// discoverLANBridges — шов под DiscoverLANBridges (та же роль, что у
// singboxListeningProbe и fakeIPLinkPresent). Без него путь «NDMS-хотспот
// обнаружен → DNS-RESCUE доехал до правил» не проверяем на уровне сервиса:
// сама функция читает дамп iptables и /sys через пакетный sysexec.
var discoverLANBridges = DiscoverLANBridges

// isLinuxBridge reports whether the named interface is a real Linux
// bridge (has a /sys/class/net/<name>/bridge directory). WireGuard
// tunnels, physical NICs, PPP, and SSTP "bridges" that NDMS marks but
// that aren't true L2 bridges return false.
func isLinuxBridge(iface string) bool {
	info, err := os.Stat(fmt.Sprintf("/sys/class/net/%s/bridge", iface))
	return err == nil && info.IsDir()
}

func splitLines(s string) []string {
	out := make([]string, 0, 16)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
