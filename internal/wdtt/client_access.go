package wdtt

import (
	"context"
)

// ClientRouteHooks applies «VPN для устройств» ip rules when raw client starts.
type ClientRouteHooks interface {
	OnTunnelStart(ctx context.Context, tunnelID, kernelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
}

func (s *Service) SetClientRouteHooks(hooks ClientRouteHooks) {
	if s == nil {
		return
	}
	s.clientRoutes = hooks
}

func (s *Service) notifyClientRouteStart(ctx context.Context, clientID, kernelIface string) {
	if s == nil || s.clientRoutes == nil {
		return
	}
	tunnelID := RawTunnelID(clientID)
	if err := s.clientRoutes.OnTunnelStart(ctx, tunnelID, kernelIface); err != nil && s.appLog != nil {
		s.appLog.Warn("client-route", clientID, "OnTunnelStart: "+err.Error())
	}
}

func (s *Service) notifyClientRouteStop(ctx context.Context, clientID string) {
	if s == nil || s.clientRoutes == nil {
		return
	}
	tunnelID := RawTunnelID(clientID)
	if err := s.clientRoutes.OnTunnelStop(ctx, tunnelID); err != nil && s.appLog != nil {
		s.appLog.Warn("client-route", clientID, "OnTunnelStop: "+err.Error())
	}
}

// applyClientRawIface — минимальная NDMS-регистрация raw-клиента: интерфейс для
// маршрутизации LAN. NAT/выход в интернет — на wdtt-server (VPS: 10.70.0.0/16).
func (s *Service) applyClientRawIface(ctx context.Context, id string, cfg ClientConfig) error {
	if cfg.UsesWireGuard() || !cfg.usesNDMSOpkgTun() || s.accessMgr == nil {
		return nil
	}
	ndmsIface := cfg.ndmsAccessIface()
	if err := s.accessMgr.EnsureInterfaceFirewallPermit(ctx, ndmsIface); err != nil {
		if s.appLog != nil {
			s.appLog.Warn("access", id, "firewall permit на "+ndmsIface+": "+err.Error())
		}
	} else if s.appLog != nil {
		s.appLog.Info("access", id, "NDMS firewall permit на "+ndmsIface+" (raw client)")
	}
	s.maybeReconcileRouter(ctx)
	return nil
}
