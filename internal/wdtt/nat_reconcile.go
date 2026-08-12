package wdtt

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const natReconcileInterval = 15 * time.Second

// StartNATReconciler periodically re-applies entware iptables NAT for running
// WDTT servers. sing-box router reconcile can flush POSTROUTING/FORWARD rules.
// OpkgTun: entware только wdttraw0; WG через NDMS как у managed AWG.
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
	// Ни один сервер не работает — правила надо снять, но ровно один раз на
	// переход в это состояние: сами по себе они не воскресают, а слепой снос
	// стоит ~18 форков iptables и при выключенном сервере повторялся бы каждые
	// 15 с вечно. Защёлка снимается ниже, как только сервер снова работает.
	if !s.anyServerRunning(full) {
		if s.natIdleSwept {
			return
		}
		if len(legacyIfaces) == 0 {
			removeEntwareNAT(ctx, DefaultWdttIface)
		} else {
			for iface := range legacyIfaces {
				removeEntwareNAT(ctx, iface)
			}
		}
		removeWdttForwardNetfilterHook()
		s.natIdleSwept = true
		return
	}
	s.natIdleSwept = false
	if len(legacyIfaces) == 0 {
		return
	}
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
			removeWdttForwardNetfilterHook()
			continue
		}
		wanDev := ""
		if dev, err := s.resolveServerEntwareNATExtIface(ctx, cfg, mode); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("nat-reconcile", srv.ID, "NAT egress: "+err.Error())
			}
		} else {
			wanDev = dev
		}
		policy := normalizePolicy(cfg.Policy)
		wantMark := ""
		if policy != "none" && s.policyMarks != nil {
			if mark, err := s.policyMarks.GetPolicyMark(ctx, policy); err == nil {
				wantMark = mark
			}
		}
		// Безусловно, каждый тик, до if/else-цепочки ниже: файл хука
		// должен всегда отражать актуальный spec (DNS/MASQUERADE/mark), а не
		// только FORWARD-ifaces — иначе после NDM table-flap DNAT :53 и
		// MASQUERADE не восстановятся до следующего цикла (C1, PR #697).
		// Второй сервер невозможен (CreateServer запрещает) — агрегация
		// spec по нескольким серверам не нужна.
		hookWanDev, err := resolveExtIfaceOrDefault(ctx, wanDev)
		if err != nil && s.appLog != nil {
			s.appLog.Warn("nat-reconcile", srv.ID, "NAT egress (hook): "+err.Error())
		}
		if err := ensureWdttNetfilterHook(ctx, wdttNetfilterSpecForServer(cfg, mode, hookWanDev, wantMark)); err != nil && s.appLog != nil {
			s.appLog.Warn("nat-reconcile", srv.ID, "netfilter.d hook: "+err.Error())
		}
		if !entwareNATPresentForServer(ctx, cfg, mode, wanDev) {
			if err := applyEntwareNATForServer(ctx, cfg, mode, wanDev, wantMark); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "entware NAT восстановлен "+strings.Join(cfg.serverEntwareNATIfacesForMode(mode), ","))
			}
		} else if fwdOut, err := iptables.RunOutput(ctx, "-S", "FORWARD"); err != nil || !entwareForwardIfacesPresent(fwdOut, cfg.serverEntwareNATIfacesForMode(mode)) {
			if err := setupEntwareForward(ctx, cfg.serverEntwareNATIfacesForMode(mode)...); err != nil && s.appLog != nil {
				s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
			} else if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "FORWARD восстановлен "+strings.Join(cfg.serverEntwareNATIfacesForMode(mode), ","))
			}
		} else if !entwareMSSPresentAll(ctx, cfg.serverEntwarePeerCIDRsForMode(mode)) {
			setupEntwareMSSClamp(ctx, cfg.serverEntwarePeerCIDRsForMode(mode)...)
			if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "TCPMSS clamp восстановлен ("+strings.Join(cfg.serverEntwarePeerCIDRsForMode(mode), ", ")+")")
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
		peerCIDRs := cfg.serverEntwarePeerCIDRsForMode(mode)
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
		if !rawServerPolicyMarkPresent(ctx, wantMark) {
			if _, err := s.applyRawServerPolicy(ctx, srv.ID, cfg); err != nil && s.appLog != nil {
				s.appLog.Warn("policy-reconcile", srv.ID, err.Error())
			}
		}
		s.ensureWdttIngressRefs(ctx, cfg)
	}
}
