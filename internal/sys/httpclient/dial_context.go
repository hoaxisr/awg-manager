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

// boundNetwork — сеть привязанного соединения: адрес v6 идёт как есть, имя и v4
// по tcp4.
// ponytail: имя резолвится только в A; на туннеле без IPv4 нужен AAAA-резолв
// через интерфейс (F132).
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
			network = boundNetwork(ip)
			if ip == nil {
				ip, err := ResolveIPv4Resilient(ctx, host, dnsServers, bindIface)
				if err != nil {
					return nil, err
				}
				addr = net.JoinHostPort(ip, port)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
}
