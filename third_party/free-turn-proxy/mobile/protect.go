package mobile

import (
	"syscall"

	"github.com/samosvalishe/free-turn-proxy/internal/netctl"
)

// Protector is implemented by the host to keep the client's own sockets out of
// a VPN tunnel it is feeding. On Android that is VpnService.protect(fd); on
// other tun-based hosts the equivalent. Without it the client's TURN / VK API /
// DNS traffic is routed back into the tunnel and the connection deadlocks.
type Protector interface {
	// Protect is invoked once for every outbound socket the client opens, with
	// the socket's raw file descriptor, before it connects.
	Protect(fd int) bool
}

// SetProtect installs the host socket protector. Pass nil to clear it (the
// default is a no-op, so desktop builds are unaffected). May be called before
// or after Start; it is read at dial time.
func SetProtect(p Protector) {
	if p == nil {
		netctl.SetControl(nil)
		return
	}
	netctl.SetControl(func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) { p.Protect(int(fd)) })
	})
}
