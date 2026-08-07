package wdtt

import (
	"net"
	"strings"
)

const (
	ConnModeWG  = "wg"
	ConnModeRaw = "raw"
)

// normalizeConnMode returns wg or raw (default wg).
func normalizeConnMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ConnModeRaw:
		return ConnModeRaw
	default:
		return ConnModeWG
	}
}

func (c ClientConfig) UsesWireGuard() bool {
	return normalizeConnMode(c.ConnMode) == ConnModeWG
}

func (c ClientConfig) kernelRawIface() string {
	if iface := strings.TrimSpace(c.RawIface); iface != "" {
		return iface
	}
	return DefaultRawClientTun
}

func (c ClientConfig) ndmsAccessIface() string {
	return strings.TrimSpace(c.NdmsIface)
}

func (c ClientConfig) usesNDMSOpkgTun() bool {
	_, ok := parseOpkgTunIndex(c.NdmsIface)
	return ok && strings.TrimSpace(c.RawIface) != ""
}

func (c ServerConfig) UsesWireGuardRelay() bool {
	return normalizeConnMode(c.RelayMode) == ConnModeWG
}

// needsEntwareNAT — iptables NAT на wdttraw0 (NDMS его не покрывает) и на wdtt0
// без OpkgTun. При OpkgTun WG-клиенты идут через NDMS, raw — через entware.
func (c ServerConfig) needsEntwareNAT() bool {
	return len(c.serverEntwareNATPlans()) > 0
}

func (c ServerConfig) usesNDMSAccess() bool {
	return c.usesNDMSOpkgTun()
}

// kernelServerIface — kernel TUN wdtt-server: opkgtunN/wdtt0 (WG). Raw — wdttraw0 отдельно.
func (c ServerConfig) kernelServerIface() string {
	return c.kernelWGIface()
}

func (c ServerConfig) serverAccessAddress() string {
	if c.usesNDMSOpkgTun() {
		return DefaultWdttServerGatewayAddr
	}
	return DefaultWdttAddress
}

func (c ServerConfig) serverAccessMask() string {
	if c.usesNDMSOpkgTun() {
		return DefaultWdttServerGatewayMask
	}
	return DefaultWdttMask
}

// serverPeerCIDR — подсеть WG-клиентов для entware LAN (raw /16 — в serverEntwareNATPlans).
func (c ServerConfig) serverPeerCIDR() string {
	return wdttPeerCIDR()
}

// entwareNATPlan — kernel-iface + CIDR клиентов для iptables NAT/MSS.
type entwareNATPlan struct {
	Iface string
	CIDR  string
}

// serverEntwareNATPlans — пары iface/CIDR для iptables NAT/MSS.
// wdttraw0 всегда; kernel WG (opkgtunN/wdtt0) — entware даже с OpkgTun
// (NDMS NAT на OpkgTun не всегда покрывает userspace WG на opkgtun).
func (c ServerConfig) serverEntwareNATPlans() []entwareNATPlan {
	return []entwareNATPlan{
		{Iface: DefaultRawServerIface, CIDR: rawServerPeerCIDR()},
		{Iface: c.kernelWGIface(), CIDR: wgServerPeerCIDR()},
	}
}

func rawServerPeerCIDR() string {
	_, n, err := net.ParseCIDR(DefaultRawServerAddr + "/16")
	if err != nil {
		return "10.70.0.0/16"
	}
	return n.String()
}

func (c ServerConfig) serverEntwareNATIfaces() []string {
	plans := c.serverEntwareNATPlans()
	seen := make(map[string]bool, len(plans))
	out := make([]string, 0, len(plans))
	for _, p := range plans {
		iface := strings.TrimSpace(p.Iface)
		if iface == "" || seen[iface] {
			continue
		}
		seen[iface] = true
		out = append(out, iface)
	}
	return out
}

func (c ServerConfig) serverEntwarePeerCIDRs() []string {
	plans := c.serverEntwareNATPlans()
	seen := make(map[string]bool, len(plans))
	out := make([]string, 0, len(plans))
	for _, p := range plans {
		cidr := strings.TrimSpace(p.CIDR)
		if cidr == "" || seen[cidr] {
			continue
		}
		seen[cidr] = true
		out = append(out, cidr)
	}
	return out
}
