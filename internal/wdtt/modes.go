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

// kernelServerIface — kernel TUN wdtt-server: opkgtunN/wdtt0 (WG). Raw — kernelRawIface отдельно.
func (c ServerConfig) kernelServerIface() string {
	return c.kernelWGIface()
}

// kernelRawIface — kernel TUN raw-половины сервера: opkgtunN с бинарём, знающим
// -raw-iface, иначе legacy wdttraw0. Имя обязано ходить через эту функцию
// всюду, где по нему адресуются правила: iptables, policy-mark, ingress.
func (c ServerConfig) kernelRawIface() string {
	if iface := strings.TrimSpace(c.RawIface); iface != "" {
		return iface
	}
	return DefaultRawServerIface
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

// serverEntwareNATPlans — legacy/full list (wdttraw0 + kernel WG).
func (c ServerConfig) serverEntwareNATPlans() []entwareNATPlan {
	return []entwareNATPlan{
		{Iface: c.kernelRawIface(), CIDR: rawServerPeerCIDR()},
		{Iface: c.kernelWGIface(), CIDR: wgServerPeerCIDR()},
	}
}

// serverEntwareNATPlansForMode — entware там, где NDMS не покрывает (паритет managed AWG).
// OpkgTun + NAT≠none: WG через NDMS NAT/policy на OpkgTun; entware только wdttraw0/raw.
func (c ServerConfig) serverEntwareNATPlansForMode(mode string) []entwareNATPlan {
	raw := entwareNATPlan{Iface: c.kernelRawIface(), CIDR: rawServerPeerCIDR()}
	if c.usesNDMSAccess() && normalizeNatMode(mode) != "none" {
		return []entwareNATPlan{raw}
	}
	return c.serverEntwareNATPlans()
}

func entwarePlansIfaces(plans []entwareNATPlan) []string {
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

func entwarePlansCIDRs(plans []entwareNATPlan) []string {
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

func (c ServerConfig) serverEntwareNATIfacesForMode(mode string) []string {
	return entwarePlansIfaces(c.serverEntwareNATPlansForMode(mode))
}

func (c ServerConfig) serverEntwarePeerCIDRsForMode(mode string) []string {
	return entwarePlansCIDRs(c.serverEntwareNATPlansForMode(mode))
}

func rawServerPeerCIDR() string {
	_, n, err := net.ParseCIDR(DefaultRawServerAddr + "/16")
	if err != nil {
		return "10.70.0.0/16"
	}
	return n.String()
}

func (c ServerConfig) serverEntwareNATIfaces() []string {
	return entwarePlansIfaces(c.serverEntwareNATPlans())
}

func (c ServerConfig) serverEntwarePeerCIDRs() []string {
	return entwarePlansCIDRs(c.serverEntwareNATPlans())
}
