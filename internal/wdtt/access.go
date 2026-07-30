package wdtt

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AccessManager applies router access settings for the WDTT WireGuard interface.
// With NDMS OpkgTun (OpkgTun90..99) NAT/LAN/policy use the same NDMS path as
// managed WireGuard servers. Legacy wdtt0 falls back to entware iptables.
type AccessManager interface {
	ApplyNATModeToInterface(ctx context.Context, ifaceName, mode, prevWAN string) (string, error)
	ApplyPolicyToInterface(ctx context.Context, ifaceName, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
	EnsureInterfaceFirewallPermit(ctx context.Context, ifaceName string) error
	KernelIfaceName(ctx context.Context, ndmsName string) string
	ResolveLANSegmentCIDRs(ctx context.Context, segmentNames []string) ([]string, error)
	DefaultGatewayNDMS(ctx context.Context) (string, error)
}

// InterfaceChecker waits until the WG interface appears after wdtt-server start.
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
	ndmsIface := cfg.ndmsAccessIface()
	kernelIface := cfg.kernelWGIface()
	useNDMS := cfg.usesNDMSOpkgTun()

	mode := normalizeNatMode(cfg.NatMode)
	prevWAN := strings.TrimSpace(cfg.NatStaticWAN)
	newStaticWAN := prevWAN

	if s.accessMgr != nil {
		accessIface := ndmsIface
		if !useNDMS {
			accessIface = DefaultWdttIface
		}
		wan, err := s.accessMgr.ApplyNATModeToInterface(ctx, accessIface, mode, prevWAN)
		if err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS NAT "+mode+" пропущен: "+err.Error())
			}
			if mode == "internet-only" && wan == "" {
				if gw, gwErr := s.accessMgr.DefaultGatewayNDMS(ctx); gwErr == nil && gw != "" {
					wan = gw
				}
			}
		} else if s.appLog != nil && useNDMS {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS NAT %s на %s", mode, accessIface))
		}
		if mode == "internet-only" && wan != "" {
			newStaticWAN = wan
		} else if mode != "internet-only" {
			newStaticWAN = ""
		}

		policy := normalizePolicy(cfg.Policy)
		if err := s.accessMgr.ApplyPolicyToInterface(ctx, accessIface, policy); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "policy "+policy+" пропущен: "+err.Error())
			}
		} else if s.appLog != nil && useNDMS && policy != "none" {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS policy %s на %s", policy, accessIface))
		}

		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		if err := s.accessMgr.ApplyLANSegmentsToInterface(ctx, accessIface, DefaultWdttAddress, DefaultWdttMask, segments); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS LAN ACL пропущен: "+err.Error())
			}
		} else if s.appLog != nil && useNDMS && len(segments) > 0 {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS LAN на %s: %v", accessIface, segments))
		}

		if mode != "none" && !useNDMS {
			if err := s.accessMgr.EnsureInterfaceFirewallPermit(ctx, accessIface); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("access", id, "firewall permit пропущен (NDMS не знает "+accessIface+"): "+err.Error())
				}
			}
		}
	} else if mode != "internet-only" {
		newStaticWAN = ""
	}

	if newStaticWAN != prevWAN {
		_ = s.setServerNatStaticWAN(id, newStaticWAN)
	}

	if useNDMS {
		s.maybeReconcileRouter(ctx)
		return nil
	}

	wanDev := ""
	if mode == "internet-only" && newStaticWAN != "" && s.accessMgr != nil {
		wanDev = s.accessMgr.KernelIfaceName(ctx, newStaticWAN)
	}

	if mode != "none" {
		if err := applyEntwareNAT(ctx, kernelIface, mode, wanDev); err != nil {
			return fmt.Errorf("entware NAT %s: %w", mode, err)
		}
		if s.appLog != nil {
			s.appLog.Info("access", id, "entware NAT "+mode+" на "+kernelIface+" (iptables MASQUERADE)")
		}
	} else {
		removeEntwareNAT(ctx, kernelIface)
	}

	segments := cfg.LanSegments
	if segments == nil {
		segments = []string{}
	}
	if err := applyEntwareLAN(ctx, kernelIface, segments, s.accessMgr); err != nil {
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
