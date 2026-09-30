package managed

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// confirmInterface — интерфейс для Apply*ToInterface по свежему списку.
// Интерфейса нет: снятие (teardown) — снимать нечего, ok=false без ошибки;
// постановка — ошибка. Ни в том, ни в другом случае команд нет (F546).
func (s *Service) confirmInterface(ctx context.Context, ifaceName string, teardown bool) (query.Confirmed, bool, error) {
	iface, ok, err := s.confirmServer(ctx, ifaceName)
	if err != nil {
		return query.Confirmed{}, false, err
	}
	if !ok && !teardown {
		return query.Confirmed{}, false, fmt.Errorf("интерфейс %s не найден в NDMS", ifaceName)
	}
	return iface, ok, nil
}

// ApplyNATModeToInterface applies a NAT mode to any NDMS WireGuard interface.
// prevWANs — сохранённые выходы для teardown static-NAT режима internet-only.
func (s *Service) ApplyNATModeToInterface(ctx context.Context, ifaceName, mode string, prevWANs []string) ([]string, error) {
	switch mode {
	case "full", "internet-only", "none":
	default:
		return nil, fmt.Errorf("неизвестный NAT-режим: %q", mode)
	}
	iface, ok, err := s.confirmInterface(ctx, ifaceName, mode == "none")
	if err != nil || !ok {
		return nil, err
	}
	return s.applyNATModeRaw(ctx, iface, mode, prevWANs)
}

// ApplyLANSegmentsToInterface sets LAN segment ACL for any WireGuard-like interface.
func (s *Service) ApplyLANSegmentsToInterface(ctx context.Context, ifaceName, addr, mask string, segments []string) error {
	iface, ok, err := s.confirmInterface(ctx, ifaceName, len(segments) == 0)
	if err != nil || !ok {
		return err
	}
	return s.applyLANSegmentsRaw(ctx, iface, addr, mask, segments, nil)
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
	iface, ok, err := s.confirmInterface(ctx, ifaceName, policy == "none")
	if err != nil || !ok {
		return err
	}
	if policy == "none" {
		if err := s.rciClearHotspotPolicy(ctx, iface); err != nil {
			return fmt.Errorf("clear policy: %w", err)
		}
	} else {
		if err := s.rciSetHotspotPolicy(ctx, iface, policy); err != nil {
			return fmt.Errorf("set policy: %w", err)
		}
	}
	s.log.Info("interface policy changed", "interface", ifaceName, "policy", policy)
	s.appLog.Info("policy", ifaceName, fmt.Sprintf("Policy set to %s", policy))
	return nil
}
