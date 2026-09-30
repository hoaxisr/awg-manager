package router

import (
	"context"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// cloudSyncQueryTimeout limits DNS resolution and RCI queries during cloud relay sync.
const cloudSyncQueryTimeout = 3 * time.Second

// cloudSyncInterval controls how often cloud relays are actively queried from RCI/DNS.
const cloudSyncInterval = 60 * time.Second

// ensureCloudSetExists ensures the AWGM-CLOUD ipset exists before iptables rules are applied.
func (s *ServiceImpl) ensureCloudSetExists(ctx context.Context) {
	if err := bypassset.EnsureXtSetModule(ctx); err != nil {
		s.bypassLog.Warn("cloud-set", "", "загрузка xt_set: "+err.Error())
	}
	if bypassset.IPSetBinary() == "" {
		s.bypassLog.Warn("cloud-set", "", bypassset.ErrIPSetNotAvailable.Error())
		return
	}
	if err := bypassset.CreateCloudSet(ctx); err != nil {
		s.bypassLog.Warn("cloud-set", "", "создание набора: "+err.Error())
	}
}

// teardownCloudSet destroys the AWGM-CLOUD ipset.
func (s *ServiceImpl) teardownCloudSet(ctx context.Context) {
	if bypassset.IPSetBinary() != "" {
		if err := bypassset.DestroyCloudSet(ctx); err != nil {
			s.bypassLog.Warn("cloud-set", "", "снос набора AWGM-CLOUD: "+err.Error())
		}
	}
}

// isCoveredByCIDRs checks if an IP is already covered by a list of subnets.
func isCoveredByCIDRs(ipStr string, cidrs []string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		_, ipNet, err := net.ParseCIDR(c)
		if err == nil && ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// syncKeeneticCloudRelays discovers active Keenetic Cloud and KeenDNS relay IPs
// from RCI (/show/ndns) and DNS, populating the AWGM-CLOUD ipset in the kernel
// and tracking dynamic IPs for engine routing rules.
func (s *ServiceImpl) syncKeeneticCloudRelays(ctx context.Context, sr storage.SingboxRouterSettings) {
	if !sr.KeeneticCloudTunnel {
		return
	}

	// 1. Ensure kernel ipset exists and has base CIDRs
	s.ensureCloudSetExists(ctx)
	s.cloudSyncMu.Lock()
	needBasePopulate := !s.cloudBasePopulated
	s.cloudSyncMu.Unlock()

	if needBasePopulate {
		_ = bypassset.PopulateCloudSet(ctx, KeeneticCloudCIDRs)
		s.cloudSyncMu.Lock()
		s.cloudBasePopulated = true
		s.cloudSyncMu.Unlock()
	}

	// Rate limit active discovery queries (every cloudSyncInterval)
	s.cloudSyncMu.Lock()
	fresh := !s.cloudSyncLast.IsZero() && time.Since(s.cloudSyncLast) < cloudSyncInterval
	s.cloudSyncMu.Unlock()
	if fresh {
		return
	}

	qctx, cancel := context.WithTimeout(ctx, cloudSyncQueryTimeout)
	defer cancel()

	var discoveredIPs []string

	// 2. Query RCI via KeenCloudRelayProvider (if available)
	infoProv, _ := s.keenDNSPreset()
	var relayDomains []string
	if cp, ok := infoProv.(KeenCloudRelayProvider); ok && cp != nil {
		rIPs, rDomains, err := cp.KeenCloudRelays(qctx)
		if err == nil {
			for _, ip := range rIPs {
				ip = strings.TrimSpace(ip)
				if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil {
					if !slices.Contains(discoveredIPs, ip) {
						discoveredIPs = append(discoveredIPs, ip)
					}
				}
			}
			relayDomains = rDomains
		}
	}

	// 3. Resolve core Keenetic broker domains and active relay domains via DNS
	domainsToResolve := []string{
		"broker.keenetic.cloud",
		"broker.netcraze.cloud",
		"cloud.keenetic.net",
		"ea.master.netcraze.cloud",
		"master.netcraze.cloud",
		"ea.master.keenetic.cloud",
		"master.keenetic.cloud",
	}
	for _, rd := range relayDomains {
		rd = strings.TrimSpace(rd)
		if rd != "" && !slices.Contains(domainsToResolve, rd) {
			domainsToResolve = append(domainsToResolve, rd)
		}
	}

	for _, domain := range domainsToResolve {
		addrs, err := net.DefaultResolver.LookupHost(qctx, domain)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			addr = strings.TrimSpace(addr)
			if parsed := net.ParseIP(addr); parsed != nil && parsed.To4() != nil {
				if !slices.Contains(discoveredIPs, addr) {
					discoveredIPs = append(discoveredIPs, addr)
				}
			}
		}
	}

	// 4. Update ipset and dynamic list
	s.cloudSyncMu.Lock()
	defer s.cloudSyncMu.Unlock()
	s.cloudSyncLast = time.Now()

	for _, ip := range discoveredIPs {
		cidr := ip + "/32"
		_ = bypassset.AddCloudEntry(ctx, cidr)

		if !slices.Contains(s.dynamicCloudIPs, cidr) {
			s.dynamicCloudIPs = append(s.dynamicCloudIPs, cidr)
			if !isCoveredByCIDRs(ip, KeeneticCloudCIDRs) {
				s.appLog.Info("cloud-sync", ip, "new unlisted Keenetic Cloud relay discovered and dynamically added to AWGM-CLOUD ipset")
			}
		}
	}
}

// dynamicCloudCIDRs returns a copy of discovered dynamic cloud IP CIDRs.
func (s *ServiceImpl) dynamicCloudCIDRs() []string {
	s.cloudSyncMu.Lock()
	defer s.cloudSyncMu.Unlock()
	return slices.Clone(s.dynamicCloudIPs)
}

// setDynamicCloudCIDRs manually updates the dynamic cloud CIDRs list (used by tests).
func (s *ServiceImpl) setDynamicCloudCIDRs(cidrs []string) {
	s.cloudSyncMu.Lock()
	defer s.cloudSyncMu.Unlock()
	s.dynamicCloudIPs = slices.Clone(cidrs)
}
