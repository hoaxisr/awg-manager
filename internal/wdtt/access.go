package wdtt

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// AccessManager applies router access settings for the WDTT WireGuard interface.
// With NDMS OpkgTun (OpkgTun17..49) NAT/LAN/policy use the same NDMS path as
// managed WireGuard servers. Legacy wdtt0 falls back to entware iptables.
type AccessManager interface {
	ApplyNATModeToInterface(ctx context.Context, ifaceName, mode string, prevWANs []string) ([]string, error)
	ApplyPolicyToInterface(ctx context.Context, ifaceName, policy string) error
	ApplyLANSegmentsToInterface(ctx context.Context, iface, addr, mask string, segments []string) error
	EnsureInterfaceFirewallPermit(ctx context.Context, ifaceName string) error
	KernelIfaceName(ctx context.Context, ndmsName string) string
	ResolveLANSegmentCIDRs(ctx context.Context, segmentNames []string) ([]string, error)
	DefaultGatewayNDMS(ctx context.Context) (string, error)
}

// InterfaceChecker waits until the WG interface appears after wdtt-server start
// and reports whether a kernel iface is operationally up (operstate up/unknown).
type InterfaceChecker interface {
	InterfaceExists(name string) bool
	InterfaceOperUp(name string) bool
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
	accessAddr := cfg.serverAccessAddress()
	accessMask := cfg.serverAccessMask()

	mode := normalizeNatMode(cfg.NatMode)
	prevWANs := cfg.staticNATList()
	newStaticWANs := prevWANs

	if s.accessMgr != nil && useNDMS {
		wans, err := s.accessMgr.ApplyNATModeToInterface(ctx, ndmsIface, mode, prevWANs)
		if err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NDMS NAT "+mode+" пропущен: "+err.Error())
			}
			if mode == "internet-only" && len(wans) == 0 {
				if gw, gwErr := s.accessMgr.DefaultGatewayNDMS(ctx); gwErr == nil && gw != "" {
					wans = []string{gw}
				}
			}
		} else if s.appLog != nil {
			s.appLog.Info("access", id, fmt.Sprintf("NDMS NAT %s на %s", mode, ndmsIface))
		}
		if mode == "internet-only" && len(wans) > 0 {
			newStaticWANs = wans
		} else if mode != "internet-only" {
			newStaticWANs = nil
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
				newStaticWANs = []string{gw}
			}
		} else {
			newStaticWANs = nil
		}
	} else if mode != "internet-only" {
		newStaticWANs = nil
	}

	if !reflect.DeepEqual(newStaticWANs, prevWANs) {
		_ = s.setServerNatStaticWANs(id, newStaticWANs)
	}

	if useNDMS {
		s.maybeReconcileRouter(ctx)
	}

	rawMark, err := s.applyRawServerPolicy(ctx, id, cfg)
	if err != nil {
		return err
	}
	s.ensureWdttIngressRefs(ctx, cfg)

	wanDev := ""
	if mode == "internet-only" && len(newStaticWANs) > 0 && s.accessMgr != nil {
		if gw, gwErr := s.accessMgr.DefaultGatewayNDMS(ctx); gwErr == nil && gw != "" {
			wanDev = s.accessMgr.KernelIfaceName(ctx, gw)
		}
	} else if cfg.needsEntwareNAT() && mode != "none" {
		if dev, err := s.resolveServerEntwareNATExtIface(ctx, cfg, mode); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "NAT egress: "+err.Error())
			}
		} else {
			wanDev = dev
		}
	}

	// Entware NAT/FORWARD до wg-маршрута: raw/wdttraw0 должен форвардиться даже
	// если ip route add на opkgtun гоняется с NDMS.
	if cfg.needsEntwareNAT() {
		if mode != "none" {
			if err := applyEntwareNATForServer(ctx, cfg, mode, wanDev, rawMark); err != nil {
				return fmt.Errorf("entware NAT %s: %w", mode, err)
			}
			if s.appLog != nil {
				s.appLog.Info("access", id, fmt.Sprintf("entware NAT %s (%v → %v via %s)", mode,
					cfg.serverEntwareNATIfacesForMode(mode), cfg.serverEntwarePeerCIDRsForMode(mode), wanDev))
			}
		} else {
			removeEntwareNATForServer(ctx, cfg)
			removeWdttForwardNetfilterHook()
		}

		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		peerCIDRs := cfg.serverEntwarePeerCIDRsForMode(mode)
		if err := applyEntwareLAN(ctx, kernelIface, segments, s.accessMgr, peerCIDRs...); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("access", id, "LAN iptables: "+err.Error())
			}
		}
	}

	// На NDMS-пути адрес OpkgTun = 10.66.0.1/16 (PR #697, F2) — connected-маршрут
	// сети интерфейса уже покрывает весь пул клиентов, ручной ip route add избыточен.
	if !useNDMS {
		if err := cfg.ensureServerWgClientRoute(ctx); err != nil {
			return fmt.Errorf("wg client route: %w", err)
		}
	}

	return nil
}

func (s *Service) setServerNatStaticWANs(id string, wans []string) error {
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
	cfg := &full.Servers[idx].Config
	if cfg.NatStaticWAN == "" && reflect.DeepEqual(cfg.NatStaticWANs, wans) {
		return nil
	}
	cfg.NatStaticWANs = wans
	cfg.NatStaticWAN = "" // источник правды теперь список
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
