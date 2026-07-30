package wdtt

import (
	"context"
	"strings"
	"time"
)

const natReconcileInterval = 15 * time.Second

// StartNATReconciler periodically re-applies entware iptables NAT for running
// WDTT servers. sing-box router reconcile can flush POSTROUTING/FORWARD rules.
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
	// Ни одного живого сервера — снимаем свои правила. Иначе внешний kill,
	// падение процесса или удаление инстанса оставляли бы MASQUERADE и
	// FORWARD на несуществующем wdtt0 навсегда: снятие есть только на
	// штатном пути остановки.
	if !s.anyServerRunning(full) {
		removeEntwareNAT(ctx, DefaultWdttIface)
		return
	}
	for _, srv := range full.Servers {
		if !s.serverProcs.get(srv.ID).Status().Running {
			continue
		}
		iface := DefaultWdttIface
		mode := normalizeNatMode(srv.Config.NatMode)
		if mode == "none" {
			continue
		}
		wanDev := ""
		if mode == "internet-only" {
			if wan := strings.TrimSpace(srv.Config.NatStaticWAN); wan != "" && s.accessMgr != nil {
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
		segments := srv.Config.LanSegments
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
