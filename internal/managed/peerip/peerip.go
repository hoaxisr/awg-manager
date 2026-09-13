// Package peerip allocates client addresses inside a managed WireGuard
// server's subnet. It is the one implementation of that rule on the Go
// side: managed.AddPeer applies it when a request names no address, and
// the MCP fake mirrors the service through the same function.
//
// It is a leaf package rather than part of internal/managed because CI
// cross-builds cmd/mcp-dev for darwin (job "mcp-portable"), and
// internal/managed pulls in Linux syscalls through osdetect.
package peerip

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrNoFree is returned when no host address is left to allocate, or the
// server address is not an IPv4 address the allocator understands.
var ErrNoFree = errors.New("no free address left in the server subnet")

// NextFree returns the first unused host address in the server's /24, as
// "x.y.z.n/32". It mirrors suggestNextPeerIP in the web UI
// (frontend/src/lib/utils/serverPeerOptions.ts): hosts start at .2, the
// server's own address is never handed out, and used addresses may carry
// a prefix, which is ignored when comparing.
//
// An empty result means the subnet is full or the server address is
// unusable; the caller must then ask the user rather than invent one.
func NextFree(serverAddress string, used []string) string {
	host := strings.TrimSpace(serverAddress)
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil {
		return ""
	}
	base := ip.To4()
	taken := map[string]bool{base.String(): true}
	for _, u := range used {
		u = strings.TrimSpace(u)
		if i := strings.IndexByte(u, '/'); i >= 0 {
			u = u[:i]
		}
		if parsed := net.ParseIP(u); parsed != nil && parsed.To4() != nil {
			taken[parsed.To4().String()] = true
		}
	}
	for n := 2; n < 255; n++ {
		candidate := fmt.Sprintf("%d.%d.%d.%d", base[0], base[1], base[2], n)
		if !taken[candidate] {
			return candidate + "/32"
		}
	}
	return ""
}
