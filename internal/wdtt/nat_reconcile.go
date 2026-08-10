package wdtt

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const natReconcileInterval = 15 * time.Second

// StartNATReconciler periodically re-applies entware iptables NAT for running
// WDTT servers on the legacy wdtt0 path. sing-box router reconcile can flush
// POSTROUTING/FORWARD rules. NDMS OpkgTun servers also use entware on opkgtun + wdttraw0.
func (s *Service) StartNATReconciler(ctx context.Context) {
	if s == nil {
		return
	}
	go s.natReconcileLoop(ctx)
}

func (s *Service) natReconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(natReconcileInterval)
	defer ticker.Stop()
	s.reconcileRunningServersNAT(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileRunningServersNAT(ctx)
			s.reconcileRunningClientsPolicyRoutes(ctx)
			// Не на первом (синхронном) проходе: автостарт серверов
			// отложен на 8 с, до него «процесс не запущен» ещё не
			// означает сироту.
			s.reapOrphanOpkgTuns(ctx)
		}
	}
}

func (s *Service) anyServerRunning(full Config) bool {
	for _, srv := range full.Servers {
		if s.serverProcs.get(srv.ID).Status().Running {
			return true
		}
	}
	return false
}

func (s *Service) reconcileRunningServersNAT(ctx context.Context) {
	full, err := s.store.Load()
	if err != nil {
		return
	}
	legacyIfaces := map[string]bool{}
	for _, srv := range full.Servers {
		if !srv.Config.needsEntwareNAT() {
			continue
		}
		for _, iface := range srv.Config.serverEntwareNATIfaces() {
			legacyIfaces[iface] = true
		}
	}
	if len(legacyIfaces) == 0 {
		if !s.anyServerRunning(full) {
			removeEntwareNAT(ctx, DefaultWdttIface)
			removeWdttForwardNetfilterHook()
		}
		return
	}
	if !s.anyServerRunning(full) {
		for iface := range legacyIfaces {
			removeEntwareNAT(ctx, iface)
		}
		removeWdttForwardNetfilterHook()
		return
	}
	allEntwareIfaces := make([]string, 0, len(legacyIfaces))
	for iface := range legacyIfaces {
		allEntwareIfaces = append(allEntwareIfaces, iface)
	}
	_ = ensureWdttForwardNetfilterHook(ctx, allEntwareIfaces)
	for _, srv := range full.Servers {
		if !s.serverProcs.get(srv.ID).Status().Running {
			continue
		}
		cfg := srv.Config
		if !cfg.needsEntwareNAT() {
			continue
		}
		iface := cfg.kernelServerIface()
		mode := normalizeNatMode(cfg.NatMode)
		if mode == "none" {
			continue
		}
		wanDev := ""
		if mode == "internet-only" {
			if wan := strings.TrimSpace(cfg.NatStaticWAN); wan != "" && s.accessMgr != nil {
				wanDev = s.accessMgr.KernelIfaceName(ctx, wan)
			}
		}
		if !entwareNATPresentForServer(ctx, cfg, wanDev) {
			if err := applyEntwareNATForServer(ctx, cfg, mode, wanDev); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "entware NAT восстановлен "+strings.Join(cfg.serverEntwareNATIfaces(), ","))
			}
		} else if fwdOut, err := iptables.RunOutput(ctx, "-S", "FORWARD"); err != nil || !entwareForwardIfacesPresent(fwdOut, cfg.serverEntwareNATIfaces()) {
			if err := setupEntwareForward(ctx, cfg.serverEntwareNATIfaces()...); err != nil && s.appLog != nil {
				s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
			} else if err := ensureWdttForwardNetfilterHook(ctx, cfg.serverEntwareNATIfaces()); err != nil && s.appLog != nil {
				s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
			} else if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "FORWARD восстановлен "+strings.Join(cfg.serverEntwareNATIfaces(), ","))
			}
		} else if !entwareMSSPresentAll(ctx, cfg.serverEntwarePeerCIDRs()) {
			setupEntwareMSSClamp(ctx, cfg.serverEntwarePeerCIDRs()...)
			if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "TCPMSS clamp восстановлен ("+strings.Join(cfg.serverEntwarePeerCIDRs(), ", ")+")")
			}
		}
		if !wgClientRoutePresent(ctx, cfg.kernelWGIface(), wgServerPeerCIDR()) {
			if err := cfg.ensureServerWgClientRoute(ctx); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("nat-reconcile", srv.ID, "wg client route: "+err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "маршрут WG-клиентов восстановлен "+cfg.kernelWGIface())
			}
		}
		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		peerCIDRs := cfg.serverEntwarePeerCIDRs()
		if len(segments) > 0 && s.accessMgr != nil {
			cidrs, err := s.accessMgr.ResolveLANSegmentCIDRs(ctx, segments)
			if err != nil {
				if s.appLog != nil {
					s.appLog.Warn("lan-reconcile", srv.ID, err.Error())
				}
			} else if !entwareLANPresent(ctx, peerCIDRs, cidrs) {
				if err := applyEntwareLAN(ctx, iface, segments, s.accessMgr, peerCIDRs...); err != nil && s.appLog != nil {
					s.appLog.Warn("lan-reconcile", srv.ID, err.Error())
				}
			}
		}
		policy := normalizePolicy(cfg.Policy)
		wantMark := ""
		if policy != "none" && s.policyMarks != nil {
			if mark, err := s.policyMarks.GetPolicyMark(ctx, policy); err == nil {
				wantMark = mark
			}
		}
		if !rawServerPolicyMarkPresent(ctx, wantMark) {
			if err := s.applyRawServerPolicy(ctx, srv.ID, cfg); err != nil && s.appLog != nil {
				s.appLog.Warn("policy-reconcile", srv.ID, err.Error())
			}
		}
		s.ensureWdttIngressRefs(ctx, cfg)
	}
}
