package uplink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// DialContext is a strict direct dial through this WAN. Hostnames use only the
// explicit provider DNS list, and DNS sockets carry the same device/source
// binding as HTTP sockets. There is no system-resolver or environment proxy
// fallback. Fresh resolvers keep provider-specific answers out of shared caches.
func (b Binding) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&bindingDialer{binding: b, raw: boundDial}).dial(ctx, network, address)
}

type rawBoundDial func(context.Context, string, string, string, string) (net.Conn, error)
type bindingDialer struct {
	binding Binding
	raw     rawBoundDial
}

func (d *bindingDialer) literal(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil, errors.New("bound socket destination must be literal")
	}
	ip = ip.Unmap()
	address = net.JoinHostPort(ip.String(), port)
	source := d.binding.IPv6
	if ip.Is4() {
		source = d.binding.IPv4
		network += "4"
	} else {
		network += "6"
	}
	if source == "" {
		return nil, errors.New("WAN has no source address for destination family")
	}
	return d.raw(ctx, network, address, source, d.binding.Device)
}

func (d *bindingDialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if err := d.binding.Validate(); err != nil {
		return nil, err
	}
	if network != "tcp" {
		return nil, errors.New("subscription transport requires TCP")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		if len(d.binding.DNSServers) == 0 {
			return nil, errors.New("selected WAN has no explicit DNS servers")
		}
		var last error
		for _, candidate := range d.binding.DNSServers {
			server, err := ParseDNSServer(candidate)
			if err != nil {
				return nil, err
			}
			attempt, cancel := context.WithTimeout(ctx, 3*time.Second)
			resolver := &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				base := "udp"
				if network == "tcp" {
					base = "tcp"
				}
				return d.literal(ctx, base, server.String())
			}}
			ips, err = resolver.LookupIP(attempt, "ip", host)
			cancel()
			if err == nil && len(ips) > 0 {
				break
			}
			last = err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("provider DNS lookup failed: %w", last)
		}
	}
	// Validate every answer before any connect to close DNS-rebinding against
	// router RCI/loopback and link-local metadata. LAN sources remain allowed,
	// matching the existing subscription downloader's address policy.
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return nil, errors.New("subscription resolves to an internal service address")
		}
	}
	var failures []error
	for _, ip := range ips {
		conn, err := d.literal(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		failures = append(failures, err)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.Join(failures...)
}

func boundDial(ctx context.Context, network, address, source, device string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: strictDeviceControl(device)}
	if network == "udp4" || network == "udp6" {
		dialer.LocalAddr = &net.UDPAddr{IP: net.ParseIP(source)}
	} else {
		dialer.LocalAddr = &net.TCPAddr{IP: net.ParseIP(source)}
	}
	return dialer.DialContext(ctx, network, address)
}
