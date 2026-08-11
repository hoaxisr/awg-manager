//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"strings"
)

// resolveServerEntwareNATExtIface picks kernel egress for entware MASQUERADE on wdttraw0.
// Mirrors managed AWG: internet-only → static WAN; PolicyN → policy table default;
// policy none + full → NDMS default-route WAN (same as managed full NAT).
func (s *Service) resolveServerEntwareNATExtIface(ctx context.Context, cfg ServerConfig, mode string) (string, error) {
	mode = normalizeNatMode(mode)
	if mode == "none" {
		return "", nil
	}
	if mode == "internet-only" {
		wan := strings.TrimSpace(cfg.NatStaticWAN)
		if wan != "" && s.accessMgr != nil {
			if k := strings.TrimSpace(s.accessMgr.KernelIfaceName(ctx, wan)); k != "" {
				return k, nil
			}
		}
	}
	if policy := normalizePolicy(cfg.Policy); policy != "none" && s.policyTables != nil {
		table, err := s.policyTables.PolicyTable4(ctx, policy)
		if err != nil {
			return "", fmt.Errorf("policy %s table: %w", policy, err)
		}
		if dev := policyTableDefaultDev(ctx, table); dev != "" {
			return dev, nil
		}
	}
	if s.accessMgr != nil {
		if ndmsWAN, err := s.accessMgr.DefaultGatewayNDMS(ctx); err == nil && ndmsWAN != "" {
			if k := strings.TrimSpace(s.accessMgr.KernelIfaceName(ctx, ndmsWAN)); k != "" {
				return k, nil
			}
		}
	}
	return defaultWANDev(ctx)
}
