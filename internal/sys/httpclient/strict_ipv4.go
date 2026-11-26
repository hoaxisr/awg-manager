package httpclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// NewStrictIPv4Transport is for tunnel acceptance, not best-effort browsing.
// No cached/WAN DNS, proxy or IPv6 fallback may mask a broken IPv4 path.
func NewStrictIPv4Transport(iface string, servers []string) (*http.Transport, error) {
	if strings.TrimSpace(iface) == "" || len(servers) == 0 {
		return nil, errors.New("strict IPv4 check requires an interface and DNS servers")
	}
	tr, err := NewTransport(TransportConfig{Interface: iface, Proxy: ProxyDirect})
	if err != nil {
		return nil, err
	}
	d := bindDialer(iface, 5*time.Second)
	tr.DialContext = strictIPv4Dial(func(ctx context.Context, host string) (string, error) {
		var last error
		for _, server := range servers {
			qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			ip, err := LookupIPv4ForBind(qctx, host, []string{server}, iface)
			cancel()
			if err == nil {
				return ip, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return "", last
	}, d.DialContext)
	return tr, nil
}

func strictIPv4Dial(resolve func(context.Context, string) (string, error), dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, _ string, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			host, err = resolve(ctx, host)
			if err != nil {
				return nil, err
			}
			ip = net.ParseIP(host)
		}
		if ip == nil || ip.To4() == nil {
			return nil, errors.New("IPv4 address was not resolved through the tunnel")
		}
		return dial(ctx, "tcp4", net.JoinHostPort(ip.To4().String(), port))
	}
}
