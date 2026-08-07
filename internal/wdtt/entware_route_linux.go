//go:build linux

package wdtt

import (
	"context"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// ensureWgClientRoute adds a connected route for qWDTT monolith client pool
// (10.66.0.0/16) via the kernel WG TUN. NDMS OpkgTun often configures only
// 10.66.66.1/24, so replies to 10.66.0.x would otherwise leave via WAN.
func ensureWgClientRoute(ctx context.Context, iface, cidr string) error {
	iface = strings.TrimSpace(iface)
	cidr = strings.TrimSpace(cidr)
	if iface == "" || cidr == "" {
		return nil
	}
	if wgClientRoutePresent(ctx, iface, cidr) {
		return nil
	}
	res, err := exec.Run(ctx, "/opt/sbin/ip", "route", "add", cidr, "dev", iface)
	if err != nil {
		msg := res.Stderr + res.Stdout
		if strings.Contains(msg, "File exists") {
			return nil
		}
		return exec.FormatError(res, err)
	}
	return nil
}

func wgClientRoutePresent(ctx context.Context, iface, cidr string) bool {
	res, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show", "dev", iface)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 && fields[0] == cidr {
			return true
		}
	}
	return false
}

func (c ServerConfig) ensureServerWgClientRoute(ctx context.Context) error {
	iface := strings.TrimSpace(c.kernelWGIface())
	if iface == "" {
		return nil
	}
	return ensureWgClientRoute(ctx, iface, wgServerPeerCIDR())
}
