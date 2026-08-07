package wdtt

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AccessManager applies router access settings for the WDTT WireGuard interface.
// With NDMS OpkgTun (OpkgTun17..49) NAT/LAN/policy use the same NDMS path as
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
	kernelIface := cfg.kernelServerIface()
	useNDMS := cfg.usesNDMSAccess()
	peerCIDR := cfg.serverPeerCIDR()
	accessAddr := cfg.serverAccessAddress()
	accessMask := cfg.serverAccessMask()

	mode := normalizeNatMode(cfg.NatMode)
	prevWAN := strings.TrimSpace(cfg.NatStaticWAN)
	newStaticWAN := prevWAN

	if s.accessMgr != nil && useNDMS {
		wan, err := s.accessMgr.ApplyNATModeToInterface(ctx, ndmsIface, mode, prevWAN)
		if err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS NAT "+mode+" пропущен: "+err.Error())
			}
			if mode == "internet-only" && wan == "" {
				if gw, gwErr := s.accessMgr.DefaultGatewayNDMS(ctx); gwErr == nil && gw != "" {
					wan = gw
				}
			}
		} else if s.appLog != nil {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS NAT %s на %s", mode, ndmsIface))
		}
		if mode == "internet-only" && wan != "" {
			newStaticWAN = wan
		} else if mode != "internet-only" {
			newStaticWAN = ""
		}

		policy := normalizePolicy(cfg.Policy)
		if err := s.accessMgr.ApplyPolicyToInterface(ctx, ndmsIface, policy); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "policy "+policy+" пропущен: "+err.Error())
			}
		} else if s.appLog != nil && policy != "none" {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS policy %s на %s", policy, ndmsIface))
		}

		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		if err := s.accessMgr.ApplyLANSegmentsToInterface(ctx, ndmsIface, accessAddr, accessMask, segments); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS LAN ACL пропущен: "+err.Error())
			}
		} else if s.appLog != nil && len(segments) > 0 {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS LAN на %s: %v", ndmsIface, segments))
		}

		if mode != "none" {
			if err := s.accessMgr.EnsureInterfaceFirewallPermit(ctx, ndmsIface); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("access", id, "firewall permit пропущен на "+ndmsIface+": "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("access", id, "NDMS firewall permit на "+ndmsIface)
			}
		}
	} else if s.accessMgr != nil {
		if mode == "internet-only" {
			if gw, gwErr := s.accessMgr.DefaultGatewayNDMS(ctx); gwErr == nil && gw != "" {
				newStaticWAN = gw
			}
		} else {
			newStaticWAN = ""
		}
	} else if mode != "internet-only" {
		newStaticWAN = ""
	}

	if newStaticWAN != prevWAN {
		_ = s.setServerNatStaticWAN(id, newStaticWAN)
	}

	if useNDMS {
		s.maybeReconcileRouter(ctx)
	}

	if err := cfg.ensureServerWgClientRoute(ctx); err != nil {
		return fmt.Errorf("wg client route: %w", err)
	}

	wanDev := ""
	if mode == "internet-only" && newStaticWAN != "" && s.accessMgr != nil {
		wanDev = s.accessMgr.KernelIfaceName(ctx, newStaticWAN)
	}

	if cfg.needsEntwareNAT() {
		if mode != "none" {
			if err := applyEntwareNATForServer(ctx, cfg, mode, wanDev); err != nil {
				return fmt.Errorf("entware NAT %s: %w", mode, err)
			}
			if s.appLog != nil {
				s.appLog.Info("access", id, fmt.Sprintf("entware NAT %s (%v → %v)", mode, cfg.serverEntwareNATIfaces(), cfg.serverEntwarePeerCIDRs()))
			}
		} else {
			removeEntwareNATForServer(ctx, cfg)
		}

		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		lanIface := kernelIface
		if useNDMS {
			lanIface = DefaultRawServerIface
		}
		if err := applyEntwareLAN(ctx, lanIface, segments, s.accessMgr, peerCIDR); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "LAN iptables: "+err.Error())
			}
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
