package warpscout

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"
)

// DialContextFunc — функция установления сетевого соединения с контекстом.
type DialContextFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// LookupIPAddrFunc — функция разрешения DNS-имени в список IP-адресов.
type LookupIPAddrFunc func(ctx context.Context, host string) ([]net.IPAddr, error)

// NewGuardedDialContext оборачивает baseDial проверкой IP до установления
// соединения (включая адреса, полученные после DNS-резолва) и проверкой
// фактического RemoteAddr после подключения.
func NewGuardedDialContext(baseDial DialContextFunc) DialContextFunc {
	return NewGuardedDialContextWithLookup(baseDial, nil)
}

// NewGuardedDialContextWithLookup действует как NewGuardedDialContext,
// позволяя передать собственный резолвер (например, в модульных тестах).
func NewGuardedDialContextWithLookup(baseDial DialContextFunc, lookup LookupIPAddrFunc) DialContextFunc {
	if baseDial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		baseDial = d.DialContext
	}
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		trimmedHost := strings.TrimSpace(strings.Trim(host, "[]"))
		if trimmedHost == "" {
			return nil, fmt.Errorf("%w: пустой хост для подключения", ErrInvalidInput)
		}

		if parsedIP, parseErr := netip.ParseAddr(trimmedHost); parseErr == nil {
			if IsDisallowedIP(parsedIP) {
				return nil, fmt.Errorf("%w: %s", ErrDisallowedAddress, parsedIP.WithZone("").Unmap().String())
			}
			conn, dialErr := baseDial(ctx, network, addr)
			if dialErr != nil {
				return nil, dialErr
			}
			return verifyRemoteConn(conn)
		}

		ips, err := lookup(ctx, trimmedHost)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("не удалось разрешить адрес %s", trimmedHost)
		}

		var v4, v6 []netip.Addr
		seen := make(map[netip.Addr]struct{}, len(ips))
		for _, ipAddr := range ips {
			parsed, ok := netip.AddrFromSlice(ipAddr.IP)
			if !ok {
				continue
			}
			unmapped := parsed.Unmap()
			if IsDisallowedIP(unmapped) {
				return nil, fmt.Errorf("%w: %s -> %s", ErrDisallowedAddress, trimmedHost, unmapped.WithZone("").String())
			}
			if ipAddr.Zone != "" && unmapped.Is6() {
				unmapped = unmapped.WithZone(ipAddr.Zone)
			}
			if _, dup := seen[unmapped]; dup {
				continue
			}
			seen[unmapped] = struct{}{}
			if unmapped.Is4() {
				v4 = append(v4, unmapped)
			} else {
				v6 = append(v6, unmapped)
			}
		}

		var candidates []netip.Addr
		switch network {
		case "tcp4":
			candidates = v4
		case "tcp6":
			candidates = v6
		default:
			candidates = make([]netip.Addr, 0, len(v4)+len(v6))
			candidates = append(candidates, v4...)
			candidates = append(candidates, v6...)
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("нет подходящего IP-адреса для %s (%s)", trimmedHost, network)
		}

		var lastErr error
		for _, ip := range candidates {
			if ctx != nil && ctx.Err() != nil {
				if lastErr != nil {
					return nil, lastErr
				}
				return nil, ctx.Err()
			}
			dialNet := network
			if network == "tcp" {
				if ip.Is4() {
					dialNet = "tcp4"
				} else {
					dialNet = "tcp6"
				}
			}
			conn, dialErr := baseDial(ctx, dialNet, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return verifyRemoteConn(conn)
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

func verifyRemoteConn(conn net.Conn) (net.Conn, error) {
	if conn == nil {
		return nil, nil
	}
	ra := conn.RemoteAddr()
	if ra == nil {
		return conn, nil
	}
	var ip netip.Addr
	switch a := ra.(type) {
	case *net.TCPAddr:
		if parsed, ok := netip.AddrFromSlice(a.IP); ok {
			ip = parsed
		}
	case *net.UDPAddr:
		if parsed, ok := netip.AddrFromSlice(a.IP); ok {
			ip = parsed
		}
	default:
		if host, _, err := net.SplitHostPort(ra.String()); err == nil {
			if parsed, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
				ip = parsed
			}
		} else if parsed, err := netip.ParseAddr(strings.Trim(ra.String(), "[]")); err == nil {
			ip = parsed
		}
	}
	if ip.IsValid() && IsDisallowedIP(ip) {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %s", ErrDisallowedAddress, ip.WithZone("").Unmap().String())
	}
	return conn, nil
}
