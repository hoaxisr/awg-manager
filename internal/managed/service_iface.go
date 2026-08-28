package managed

import (
	"context"
	"fmt"
)

// ApplyNATModeToInterface applies a NAT mode to any NDMS WireGuard interface.
// prevWANs — сохранённые выходы для teardown static-NAT режима internet-only.
func (s *Service) ApplyNATModeToInterface(ctx context.Context, ifaceName, mode string, prevWANs []string) ([]string, error) {
	switch mode {
	case "full", "internet-only", "none":
	default:
		return nil, fmt.Errorf("неизвестный NAT-режим: %q", mode)
	}
	return s.applyNATModeRaw(ctx, ifaceName, mode, prevWANs)
}

// ApplyLANSegmentsToInterface sets LAN segment ACL for any WireGuard-like interface.
func (s *Service) ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error {
	return s.applyLANSegmentsRaw(ctx, iface, addr, mask, segments)
}

// ApplyPolicyToInterface sets or clears the ip hotspot policy on an interface.
func (s *Service) ApplyPolicyToInterface(ctx context.Context, ifaceName, policy string) error {
	if policy == "" {
		return fmt.Errorf("policy must not be empty")
	}
	if policy != "none" {
		opts, err := s.ListPolicies(ctx)
		if err != nil {
			return fmt.Errorf("list policies: %w", err)
		}
		known := false
		for _, o := range opts {
			if o.ID == policy {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown policy: %s", policy)
		}
	}
	if policy == "none" {
		if err := s.rciClearHotspotPolicy(ctx, ifaceName); err != nil {
			return fmt.Errorf("clear policy: %w", err)
		}
	} else {
		if err := s.rciSetHotspotPolicy(ctx, ifaceName, policy); err != nil {
			return fmt.Errorf("set policy: %w", err)
		}
	}
	s.log.Info("interface policy changed", "interface", ifaceName, "policy", policy)
	s.appLog.Info("policy", ifaceName, fmt.Sprintf("Policy set to %s", policy))
	return nil
}
