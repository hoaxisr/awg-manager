// Package netbind keeps the core's own connections off its own tunnel.
//
// With the Windows full tunnel (--inbound=tun) the default route points at
// the OpenFlux adapter, but the carriers (Yandex, the node's direct port)
// must still leave through the real network, or they would loop into the
// tunnel they carry. Android gets this by excluding the app from its VPN;
// here every outbound socket of the core is bound to the physical
// interface instead (IP_UNICAST_IF), which holds whatever the routes say.
// Dial sites use Dialer or DialContext; Bind also covers the defaults
// (http.DefaultTransport, websocket.DefaultDialer, net.DefaultResolver).
package netbind

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

type controlFunc = func(network, address string, c syscall.RawConn) error

var current atomic.Pointer[controlFunc]

// control applies the current binding, if any.
func control(network, address string, c syscall.RawConn) error {
	if f := current.Load(); f != nil {
		return (*f)(network, address, c)
	}
	return nil
}

// Dialer is a net.Dialer that follows the binding.
func Dialer(timeout time.Duration) *net.Dialer {
	return Wrap(&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second})
}

// Wrap makes d follow the binding (keeping its own Control, if any).
func Wrap(d *net.Dialer) *net.Dialer {
	own := d.Control
	d.Control = func(network, address string, c syscall.RawConn) error {
		if own != nil {
			if err := own(network, address, c); err != nil {
				return err
			}
		}
		return control(network, address, c)
	}
	return d
}

// DialContext dials with a default Dialer (30 s timeout).
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return Dialer(30*time.Second).DialContext(ctx, network, address)
}

// Bound reports whether sockets are bound to an interface now.
func Bound() bool { return current.Load() != nil }

// install sets the binding and points the standard library's defaults at
// bound dialers, so code that never names a dialer is covered too.
func install(f controlFunc) {
	current.Store(&f)
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.DialContext = DialContext
	}
	websocket.DefaultDialer.NetDialContext = DialContext
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return Dialer(10*time.Second).DialContext(ctx, network, address)
		},
	}
}
