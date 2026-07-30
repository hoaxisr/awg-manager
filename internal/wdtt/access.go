package wdtt

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AccessManager applies NDMS access settings to the wdtt0 interface.
type AccessManager interface {
	ApplyNATModeToInterface(ctx context.Context, ifaceName, mode, prevWAN string) (string, error)
	ApplyPolicyToInterface(ctx context.Context, ifaceName, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
	// EnsureInterfaceFirewallPermit opens Keenetic firewall for Entware-created
	// interfaces (wdtt0), same pattern as FakeIP OpkgTun.
	EnsureInterfaceFirewallPermit(ctx context.Context, ifaceName string) error
	// KernelIfaceName resolves NDMS iface id to kernel dev for iptables -o.
	KernelIfaceName(ctx context.Context, ndmsName string) string
}

// InterfaceChecker waits until wdtt0 appears after wdtt-server start.
type InterfaceChecker interface {
	InterfaceExists(name string) bool
}

func normalizeNatMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "full", "internet-only", "none":
		return strings.TrimSpace(mode)
	default:
		return "full"
	}
}

func normalizePolicy(policy string) string {
	p := strings.TrimSpace(policy)
	if p == "" {
		return "none"
	}
	return p
}

func (s *Service) applyServerAccess(ctx context.Context, id string, cfg ServerConfig) error {
	if s.accessMgr == nil {
		return nil
	}
	iface := DefaultWdttIface
	mode := normalizeNatMode(cfg.NatMode)
	prevWAN := strings.TrimSpace(cfg.NatStaticWAN)

	wan, err := s.accessMgr.ApplyNATModeToInterface(ctx, iface, mode, prevWAN)
	if err != nil {
		// wdtt0 is invisible to NDMS; RCI NAT/ACL often fail while the server is fine.
		if s.appLog != nil {
			s.appLog.Warn("access", id, "NDMS NAT "+mode+" пропущен: "+err.Error())
		}
	}
	newStaticWAN := prevWAN
	if mode == "internet-only" && wan != "" {
		newStaticWAN = wan
	} else if mode != "internet-only" {
		newStaticWAN = ""
	}
	if newStaticWAN != prevWAN {
		_ = s.setServerNatStaticWAN(id, newStaticWAN)
	}

	policy := normalizePolicy(cfg.Policy)
	if err := s.accessMgr.ApplyPolicyToInterface(ctx, iface, policy); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("access", id, "policy "+policy+" пропущен: "+err.Error())
		}
	}

	segments := cfg.LanSegments
	if segments == nil {
		segments = []string{}
	}
	if err := s.accessMgr.ApplyLANSegmentsToInterface(ctx, iface, DefaultWdttAddress, DefaultWdttMask, segments); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("access", id, "LAN segments пропущены: "+err.Error())
		}
	}
	if mode != "none" {
		// wdtt0 is a Linux netdev (wireguard-go), not an NDMS OpkgTun — ACL bind
		// often fails with "no wdtt0 IP interface found". Best-effort only.
		if err := s.accessMgr.EnsureInterfaceFirewallPermit(ctx, iface); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "firewall permit пропущен (NDMS не знает "+iface+"): "+err.Error())
			}
		}
		wanDev := ""
		if mode == "internet-only" && newStaticWAN != "" {
			wanDev = s.accessMgr.KernelIfaceName(ctx, newStaticWAN)
		}
		if err := applyEntwareNAT(ctx, iface, mode, wanDev); err != nil {
			return fmt.Errorf("entware NAT %s: %w", mode, err)
		}
		if s.appLog != nil {
			s.appLog.Info("access", id, "entware NAT "+mode+" на "+iface+" (iptables MASQUERADE)")
		}
	} else {
		removeEntwareNAT(ctx, iface)
	}
	return nil
}

func (s *Service) setServerNatStaticWAN(id, wan string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findServerIndex(full.Servers, id)
	if idx < 0 {
		return fmt.Errorf("сервер %q не найден", id)
	}
	if full.Servers[idx].Config.NatStaticWAN == wan {
		return nil
	}
	full.Servers[idx].Config.NatStaticWAN = wan
	return s.store.Save(full)
}

func waitForInterface(checker InterfaceChecker, name string, timeout time.Duration) bool {
	if checker == nil {
		time.Sleep(500 * time.Millisecond)
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if checker.InterfaceExists(name) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
