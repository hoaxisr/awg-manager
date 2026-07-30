package wdtt

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AccessManager applies router access settings for the wdtt0 interface.
// NDMS calls are best-effort (wdtt0 is invisible to NDMS); entware iptables
// NAT/LAN rules are always applied separately.
type AccessManager interface {
	ApplyNATModeToInterface(ctx context.Context, ifaceName, mode, prevWAN string) (string, error)
	ApplyPolicyToInterface(ctx context.Context, ifaceName, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
	// EnsureInterfaceFirewallPermit opens Keenetic firewall for Entware-created
	// interfaces (wdtt0), same pattern as FakeIP OpkgTun.
	EnsureInterfaceFirewallPermit(ctx context.Context, ifaceName string) error
	// KernelIfaceName resolves NDMS iface id to kernel dev for iptables -o.
	KernelIfaceName(ctx context.Context, ndmsName string) string
	// ResolveLANSegmentCIDRs maps NDMS bridge names to network CIDRs.
	ResolveLANSegmentCIDRs(ctx context.Context, segmentNames []string) ([]string, error)
	// DefaultGatewayNDMS returns the NDMS id of the current default-route WAN.
	DefaultGatewayNDMS(ctx context.Context) (string, error)
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
	iface := DefaultWdttIface
	mode := normalizeNatMode(cfg.NatMode)
	prevWAN := strings.TrimSpace(cfg.NatStaticWAN)
	newStaticWAN := prevWAN

	if s.accessMgr != nil {
		wan, err := s.accessMgr.ApplyNATModeToInterface(ctx, iface, mode, prevWAN)
		if err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS NAT "+mode+" пропущен: "+err.Error())
			}
			if mode == "internet-only" && wan == "" {
				if gw, gwErr := s.accessMgr.DefaultGatewayNDMS(ctx); gwErr == nil && gw != "" {
					wan = gw
				}
			}
		}
		if mode == "internet-only" && wan != "" {
			newStaticWAN = wan
		} else if mode != "internet-only" {
			newStaticWAN = ""
		}

		policy := normalizePolicy(cfg.Policy)
		if err := s.accessMgr.ApplyPolicyToInterface(ctx, iface, policy); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "policy "+policy+" пропущен (NDMS не знает "+iface+"): "+err.Error())
			}
		}

		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		if err := s.accessMgr.ApplyLANSegmentsToInterface(ctx, iface, DefaultWdttAddress, DefaultWdttMask, segments); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS LAN ACL пропущен: "+err.Error())
			}
		}

		if mode != "none" {
			if err := s.accessMgr.EnsureInterfaceFirewallPermit(ctx, iface); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("access", id, "firewall permit пропущен (NDMS не знает "+iface+"): "+err.Error())
				}
			}
		}
	} else if mode != "internet-only" {
		newStaticWAN = ""
	}

	if newStaticWAN != prevWAN {
		_ = s.setServerNatStaticWAN(id, newStaticWAN)
	}

	wanDev := ""
	if mode == "internet-only" && newStaticWAN != "" && s.accessMgr != nil {
		wanDev = s.accessMgr.KernelIfaceName(ctx, newStaticWAN)
	}

	if mode != "none" {
		if err := applyEntwareNAT(ctx, iface, mode, wanDev); err != nil {
			return fmt.Errorf("entware NAT %s: %w", mode, err)
		}
		if s.appLog != nil {
			s.appLog.Info("access", id, "entware NAT "+mode+" на "+iface+" (iptables MASQUERADE)")
		}
	} else {
		removeEntwareNAT(ctx, iface)
	}

	segments := cfg.LanSegments
	if segments == nil {
		segments = []string{}
	}
	if err := applyEntwareLAN(ctx, iface, segments, s.accessMgr); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("access", id, "LAN iptables: "+err.Error())
		}
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
