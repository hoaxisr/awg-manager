package wdtt

import (
	"context"
	"strings"
	"time"
)

const natReconcileInterval = 15 * time.Second

// StartNATReconciler periodically re-applies entware iptables NAT for running
// WDTT servers on the legacy wdtt0 path. sing-box router reconcile can flush
// POSTROUTING/FORWARD rules. NDMS OpkgTun servers use NDMS NAT instead.
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
		if srv.Config.usesNDMSOpkgTun() {
			continue
		}
		legacyIfaces[srv.Config.kernelWGIface()] = true
	}
	if len(legacyIfaces) == 0 {
		if !s.anyServerRunning(full) {
			removeEntwareNAT(ctx, DefaultWdttIface)
		}
		return
	}
	if !s.anyServerRunning(full) {
		for iface := range legacyIfaces {
			removeEntwareNAT(ctx, iface)
		}
		return
	}
	for _, srv := range full.Servers {
		if !s.serverProcs.get(srv.ID).Status().Running {
			continue
		}
		cfg := srv.Config
		if cfg.usesNDMSOpkgTun() {
			continue
		}
		iface := cfg.kernelWGIface()
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
		if !entwareNATPresent(ctx, iface, wanDev) {
			if err := applyEntwareNAT(ctx, iface, mode, wanDev); err != nil {
				if s.appLog != nil {
					s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
				}
			} else if s.appLog != nil {
				s.appLog.Info("nat-reconcile", srv.ID, "entware NAT восстановлен на "+iface)
			}
		}
		segments := cfg.LanSegments
		if segments == nil {
			segments = []string{}
		}
		if len(segments) > 0 && s.accessMgr != nil {
			cidrs, err := s.accessMgr.ResolveLANSegmentCIDRs(ctx, segments)
			if err != nil {
				if s.appLog != nil {
					s.appLog.Warn("lan-reconcile", srv.ID, err.Error())
				}
			} else if !entwareLANPresent(ctx, DefaultWdttAddress+"/24", cidrs) {
				if err := applyEntwareLAN(ctx, iface, segments, s.accessMgr); err != nil && s.appLog != nil {
					s.appLog.Warn("lan-reconcile", srv.ID, err.Error())
				}
			}
		}
	}
}
