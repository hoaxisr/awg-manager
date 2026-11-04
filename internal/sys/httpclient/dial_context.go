package httpclient

import (
	"context"
	"net"
	"strings"
	"time"
)

// DialTCP opens a single TCP connection to addr ("host:port"), bound to
// iface via SO_BINDTODEVICE when non-empty. Hostnames resolve through the
// same iface-bound DNS path the HTTP client uses. The caller closes the conn.
func DialTCP(ctx context.Context, iface, addr string, connectTimeout time.Duration) (net.Conn, error) {
	return buildDialContext(iface, nil, connectTimeout)(ctx, "tcp", addr)
}

// boundNetwork — сеть привязанного соединения к адресу.
func boundNetwork(ip net.IP) string {
	if ip != nil && ip.To4() == nil {
		return "tcp"
	}
	return "tcp4"
}

func buildDialContext(bindIface string, dnsServers []string, connectTimeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := bindDialer(bindIface, connectTimeout)
	bindIface = strings.TrimSpace(bindIface)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if bindIface != "" {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return dialBoundHost(ctx, dialer.DialContext, host, port, dnsServers, bindIface)
			}
			network = boundNetwork(ip)
		}
		return dialer.DialContext(ctx, network, addr)
	}
}

// dialBoundHost — имя через привязанный интерфейс: сначала IPv4, при отказе
// резолва или соединения AAAA. Туннель без IPv4 иначе не проходит проверку (F132).
func dialBoundHost(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error),
	host, port string, dnsServers []string, bindIface string) (net.Conn, error) {
	ip4, err := ResolveIPv4Resilient(ctx, host, dnsServers, bindIface)
	if err == nil {
		var conn net.Conn
		if conn, err = dial(ctx, "tcp4", net.JoinHostPort(ip4, port)); err == nil {
			return conn, nil
		}
	}
	if ctx.Err() != nil {
		return nil, err
	}
	ip6, lerr := lookupIPv6(ctx, host, dnsServers, bindIface)
	if lerr != nil {
		return nil, err // причина — отказ IPv4: AAAA лишь запасной путь
	}
	return dial(ctx, "tcp", net.JoinHostPort(ip6, port))
}
