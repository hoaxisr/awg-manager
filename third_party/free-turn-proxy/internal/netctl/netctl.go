// Package netctl carries a process-global socket Control hook applied to every
// OUTBOUND socket the vendored free-turn client opens. The host installs it so
// those sockets are excluded from the VPN tunnel (Android VpnService.protect on
// the raw fd) — without it the client's own TURN/VK/DNS traffic loops back into
// the tunnel and hangs. This patch is local to our vendored copy: upstream has
// no such hook, and the dialers it creates (netconn.DirectNet, dnsdial,
// turndial) are routed through Apply below.
package netctl

import "syscall"

var control func(network, address string, c syscall.RawConn) error

// SetControl installs the per-socket protector. Call before the client starts.
// nil (the default) makes Apply a no-op, so desktop builds are unaffected.
func SetControl(fn func(network, address string, c syscall.RawConn) error) {
	control = fn
}

// Apply is assigned as the Control func of every outbound net.Dialer /
// net.ListenConfig in the vendored client. It reads `control` at dial time so
// the host may register the protector before or after the client starts.
func Apply(network, address string, c syscall.RawConn) error {
	if control != nil {
		return control(network, address, c)
	}
	return nil
}
