package connections

import (
	"net"
	"sort"
	"strings"
)

// validSortColumns is the whitelist of accepted SortBy values. Anything else
// is silently ignored by applySort (no-op, preserve conntrack order).
var validSortColumns = map[string]bool{
	"proto": true,
	"src":   true,
	"dst":   true,
	"iface": true,
	"state": true,
	"bytes": true,
}

// applySort sorts conns in place by the requested column. Empty or unknown
// sortBy is a no-op. sortDir defaults to "asc"; only "desc" reverses.
//
// Stable sort guarantees that connections with equal sort keys preserve their
// conntrack order — important for grouping use cases like "show all connections
// to one destination in their natural arrival order".
func applySort(conns []Connection, sortBy, sortDir string) {
	if !validSortColumns[sortBy] {
		return
	}
	desc := sortDir == "desc"
	less := lessFor(sortBy)
	sort.SliceStable(conns, func(i, j int) bool {
		if desc {
			return less(conns[j], conns[i])
		}
		return less(conns[i], conns[j])
	})
}

// lessFor returns the strict-less comparator for the given column.
func lessFor(col string) func(a, b Connection) bool {
	switch col {
	case "proto":
		return func(a, b Connection) bool {
			return strings.ToLower(a.Protocol) < strings.ToLower(b.Protocol)
		}
	case "state":
		return func(a, b Connection) bool { return a.State < b.State }
	case "iface":
		return func(a, b Connection) bool { return a.TunnelName < b.TunnelName }
	case "bytes":
		return func(a, b Connection) bool { return a.Bytes < b.Bytes }
	case "src":
		return func(a, b Connection) bool {
			return ipPortLess(a.Src, a.SrcPort, b.Src, b.SrcPort)
		}
	case "dst":
		return func(a, b Connection) bool {
			return ipPortLess(a.Dst, a.DstPort, b.Dst, b.DstPort)
		}
	}
	// Unreachable: applySort already validated col against validSortColumns.
	return func(a, b Connection) bool { return false }
}

// ipPortLess compares two host:port pairs. IPv4 addresses parse to uint32
// and compare numerically; non-IPv4 strings (IPv6 or malformed) fall back
// to lexical comparison on the address string. Within equal addresses,
// port is the tiebreaker.
func ipPortLess(aIP string, aPort int, bIP string, bPort int) bool {
	aNum, aOK := ipv4ToUint32(aIP)
	bNum, bOK := ipv4ToUint32(bIP)
	switch {
	case aOK && bOK:
		if aNum != bNum {
			return aNum < bNum
		}
		return aPort < bPort
	case aOK && !bOK:
		// IPv4 sorts ahead of IPv6/malformed for deterministic ordering.
		return true
	case !aOK && bOK:
		return false
	default:
		// Both non-IPv4 — lexical fallback on the address, then port.
		if aIP != bIP {
			return aIP < bIP
		}
		return aPort < bPort
	}
}

// ipv4ToUint32 parses an IPv4 dotted-quad string into a uint32. Returns
// (0, false) for IPv6 or any non-IPv4 input.
func ipv4ToUint32(s string) (uint32, bool) {
	ip := net.ParseIP(s)
	if ip == nil {
		return 0, false
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3]), true
}
