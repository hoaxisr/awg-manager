package managed

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// rfc1918Networks lists the private IPv4 ranges that are valid for a
// managed WireGuard server. Anything outside these blocks is rejected
// to prevent the user from picking a public-routed range that would
// silently steal traffic destined for the real Internet host.
var rfc1918Networks = []*net.IPNet{
	mustParseCIDR("10.0.0.0/8"),
	mustParseCIDR("172.16.0.0/12"),
	mustParseCIDR("192.168.0.0/16"),
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// usedSubnet is one occupied address space already configured on the
// router, paired with a label suitable for surfacing in error messages.
type usedSubnet struct {
	label string
	cidr  *net.IPNet
}

// parseManagedSubnet converts (address, mask) — where mask is either
// a CIDR prefix length or dotted notation — into the CIDR network the
// host belongs to. The returned IPNet has the host bits cleared.
func parseManagedSubnet(address, mask string) (*net.IPNet, error) {
	ip := net.ParseIP(address).To4()
	if ip == nil {
		return nil, fmt.Errorf("invalid IPv4 address: %s", address)
	}
	prefix, err := maskToPrefix(mask)
	if err != nil {
		return nil, err
	}
	cidr := &net.IPNet{
		IP:   ip.Mask(net.CIDRMask(prefix, 32)),
		Mask: net.CIDRMask(prefix, 32),
	}
	return cidr, nil
}

// maskToPrefix accepts "24" or "255.255.255.0" and returns 24.
func maskToPrefix(mask string) (int, error) {
	if n, err := strconv.Atoi(mask); err == nil {
		if n < 0 || n > 32 {
			return 0, fmt.Errorf("invalid mask: /%d", n)
		}
		return n, nil
	}
	dotted := net.ParseIP(mask).To4()
	if dotted == nil {
		return 0, fmt.Errorf("invalid mask: %s", mask)
	}
	ones, bits := net.IPMask(dotted).Size()
	if bits != 32 {
		return 0, fmt.Errorf("invalid mask: %s", mask)
	}
	return ones, nil
}

// validateRFC1918 rejects addresses outside 10/8, 172.16/12, 192.168/16.
func validateRFC1918(address string) error {
	ip := net.ParseIP(address).To4()
	if ip == nil {
		return fmt.Errorf("invalid IPv4 address: %s", address)
	}
	for _, n := range rfc1918Networks {
		if n.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("адрес %s должен быть из приватных диапазонов RFC1918 (10/8, 172.16/12, 192.168/16)", address)
}

// validateHostAddress rejects the network and broadcast addresses of
// the chosen subnet — neither can be assigned to the WireGuard
// interface as a host address.
func validateHostAddress(address string, cidr *net.IPNet) error {
	ip := net.ParseIP(address).To4()
	if ip == nil {
		return fmt.Errorf("invalid IPv4 address: %s", address)
	}
	prefix, _ := cidr.Mask.Size()
	// /31 and /32 don't have meaningful network/broadcast — accept.
	if prefix >= 31 {
		return nil
	}
	if ip.Equal(cidr.IP) {
		return fmt.Errorf("адрес %s — это адрес сети %s, а не хоста", address, cidr.String())
	}
	broadcast := lastIP(cidr)
	if ip.Equal(broadcast) {
		return fmt.Errorf("адрес %s — это broadcast подсети %s, а не хоста", address, cidr.String())
	}
	return nil
}

// lastIP returns the broadcast (last) address of a /n IPv4 network.
func lastIP(cidr *net.IPNet) net.IP {
	ip := cidr.IP.To4()
	mask := cidr.Mask
	out := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		out[i] = ip[i] | ^mask[i]
	}
	return out
}

// subnetsOverlap is true when either network contains the other's
// network address.
func subnetsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// listUsedSubnets queries every router interface and returns the
// occupied (address, mask) pairs as CIDRs. The interface whose NDMS
// id matches excludeIface is skipped — this lets Update accept the
// server's own current subnet without flagging it as a conflict.
// On RCI failure returns (nil, error); the caller decides whether
// to degrade gracefully.
func (s *Service) listUsedSubnets(ctx context.Context, excludeIface string) ([]usedSubnet, error) {
	all, err := s.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]usedSubnet, 0, len(all))
	for _, iface := range all {
		if iface.Address == "" || iface.Mask == "" {
			continue
		}
		if excludeIface != "" && iface.ID == excludeIface {
			continue
		}
		cidr, perr := parseManagedSubnet(iface.Address, iface.Mask)
		if perr != nil {
			continue
		}
		label := iface.Description
		if label == "" {
			label = iface.SystemName
		}
		if label == "" {
			label = iface.ID
		}
		out = append(out, usedSubnet{label: label, cidr: cidr})
	}
	return out, nil
}

// findConflict returns the first occupied subnet that overlaps with
// the candidate, or nil if there's no conflict.
func findConflict(candidate *net.IPNet, used []usedSubnet) *usedSubnet {
	for i := range used {
		if subnetsOverlap(candidate, used[i].cidr) {
			return &used[i]
		}
	}
	return nil
}

// SuggestAddress is the exported entry point for the API handler.
func (s *Service) SuggestAddress(ctx context.Context) (string, string, error) {
	return s.suggestAddress(ctx)
}

// suggestAddress walks a deterministic list of /24 candidates from the
// 10/8, 172.16/12 and 192.168/16 private blocks and returns the first
// network whose .1 host is free against the live interface list. The
// preferred order (10.10/16 first, then 10.20…10.250) avoids the
// commonly-occupied 10.0.0.0/24 — many ISPs and Keenetic LAN defaults
// claim it.
//
// Returns address (".1" host) and CIDR prefix as strings. Falls back
// to "10.10.0.1"/"24" when every candidate is taken (effectively never).
func (s *Service) suggestAddress(ctx context.Context) (string, string, error) {
	used, err := s.listUsedSubnets(ctx, "")
	if err != nil {
		s.log.Warn("suggest-address: cannot read interface list, returning fallback", "error", err)
		return "10.10.0.1", "24", nil
	}
	for _, candidate := range candidateNetworks() {
		if findConflict(candidate, used) == nil {
			host := firstHost(candidate)
			return host.String(), "24", nil
		}
	}
	return "10.10.0.1", "24", nil
}

// candidateNetworks builds the deterministic search order for
// suggestAddress. Step-of-10 in 10/8 first (round numbers, rarely
// occupied), then dense scan of 10/8, then 172.16/12, then 192.168/16.
func candidateNetworks() []*net.IPNet {
	out := make([]*net.IPNet, 0, 256+16+256)

	// 10.10.0.0/24, 10.20.0.0/24, ..., 10.250.0.0/24
	for i := 10; i <= 250; i += 10 {
		out = append(out, mustParseCIDR(fmt.Sprintf("10.%d.0.0/24", i)))
	}
	// 10.0.0.0/24 ... 10.255.0.0/24 (skip already enumerated multiples-of-10)
	for i := 0; i <= 255; i++ {
		if i%10 == 0 && i >= 10 && i <= 250 {
			continue
		}
		out = append(out, mustParseCIDR(fmt.Sprintf("10.%d.0.0/24", i)))
	}
	// 172.16.0.0/24 ... 172.31.0.0/24
	for i := 16; i <= 31; i++ {
		out = append(out, mustParseCIDR(fmt.Sprintf("172.%d.0.0/24", i)))
	}
	// 192.168.0.0/24 ... 192.168.255.0/24
	for i := 0; i <= 255; i++ {
		out = append(out, mustParseCIDR(fmt.Sprintf("192.168.%d.0/24", i)))
	}
	return out
}

// firstHost returns the .1 host of an IPv4 network.
func firstHost(cidr *net.IPNet) net.IP {
	ip := cidr.IP.To4()
	out := make(net.IP, 4)
	copy(out, ip)
	out[3]++
	return out
}
