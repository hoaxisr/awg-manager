package wdtt

import "context"

func (s *Service) reconcileRunningClientsPolicyRoutes(ctx context.Context) {
	full, err := s.store.Load()
	if err != nil {
		return
	}
	for _, cl := range full.Clients {
		if cl.Config.UsesWireGuard() || !cl.Config.usesNDMSOpkgTun() {
			continue
		}
		if !s.clientProcs.get(cl.ID).Status().Running {
			continue
		}
		s.syncOpkgPolicyDefaultRoutes(ctx, cl.Config.ndmsAccessIface(), cl.Config.kernelRawIface())
	}
}
