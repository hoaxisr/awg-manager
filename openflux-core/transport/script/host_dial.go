package script

import (
	"context"
	"fmt"
	"net"
	"sort"
)

// lookupIPAddr is the resolver dialContext uses; tests replace it.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

// dialContext is what every TCP connection of a script (http, ws) dials
// through: the IPv4 addresses of a name first, then the IPv6 ones. A network
// with a broken IPv6 path (an address that resolves but goes nowhere) made a
// carrier wait out the dial timeout on every connect; the native carriers
// learned this first (transport/yandex/dial.go). A literal address or a
// non-TCP network is dialed as given.
func (t *ScriptTransport) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := t.dialer()
	if network != "tcp" {
		return d.DialContext(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) != nil {
		return d.DialContext(ctx, network, address)
	}
	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("lookup %s: no addresses", host)
	}
	sort.SliceStable(addrs, func(i, j int) bool { return addrs[i].IP.To4() != nil && addrs[j].IP.To4() == nil })
	var firstErr error
	for _, a := range addrs {
		netw := "tcp6"
		if a.IP.To4() != nil {
			netw = "tcp4"
		}
		conn, err := d.DialContext(ctx, netw, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, firstErr
}
