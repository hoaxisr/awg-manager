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

func (s *Service) reconcileRunningServersNAT(ctx context.Context) {
	if s.accessMgr == nil {
		return
	}
	full, err := s.store.Load()
	if err != nil {
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
			if wan := strings.TrimSpace(srv.Config.NatStaticWAN); wan != "" {
				wanDev = s.accessMgr.KernelIfaceName(ctx, wan)
			}
		}
		if entwareNATPresent(ctx, iface, wanDev) {
			continue
		}
		if err := applyEntwareNAT(ctx, iface, mode, wanDev); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("nat-reconcile", srv.ID, err.Error())
			}
			continue
		}
		if s.appLog != nil {
			s.appLog.Info("nat-reconcile", srv.ID, "entware NAT восстановлен на "+iface)
		}
	}
}
