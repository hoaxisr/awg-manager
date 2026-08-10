package wdtt

import (
	"context"
	"strings"
)

// reconcileClientRawNDMS поднимает OpkgTun, если wt-client уже работает, а NDMS-
// интерфейс после рестарта awg-manager остался down (ResumeEnabled no-op).
func (s *Service) reconcileClientRawNDMS(ctx context.Context, id string, cfg ClientConfig) (bool, error) {
	if cfg.UsesWireGuard() || !cfg.usesNDMSOpkgTun() {
		return false, nil
	}
	if strings.TrimSpace(cfg.RawClientIP) == "" {
		return false, nil
	}
	iface := cfg.kernelRawIface()
	if s.ifaceChecker != nil && s.ifaceChecker.InterfaceOperUp(iface) {
		return false, nil
	}
	if !s.clientProcs.get(id).Status().Running {
		return false, nil
	}
	if err := s.prepareClientNDMSOpkgTun(ctx, id, cfg); err != nil {
		return false, err
	}
	conf := RawConfPayload{ClientIP: cfg.RawClientIP, MTU: 1300}
	if err := s.activateClientNDMSOpkgTun(ctx, id, cfg, conf); err != nil {
		return false, err
	}
	if err := s.applyClientRawIface(ctx, id, cfg); err != nil {
		return false, err
	}
	s.notifyClientRouteStart(ctx, id, iface)
	s.restoreOpkgPolicyPermits(ctx, id, cfg)
	// Лог «восстановлен» — только если перечитка реально подтвердила operUp;
	// иначе это ложный сигнал успеха при живом-но-не-поднявшемся интерфейсе
	// (супервизор сам перечитает статус и заэскалирует в рестарт).
	if s.appLog != nil && s.ifaceChecker != nil && s.ifaceChecker.InterfaceOperUp(iface) {
		s.appLog.Info("start", id, "OpkgTun восстановлен (rawClientIp="+cfg.RawClientIP+")")
	}
	return true, nil
}

func rawClientNDMSReady(cfg ClientConfig, checker InterfaceChecker) bool {
	if cfg.UsesWireGuard() || !cfg.usesNDMSOpkgTun() || strings.TrimSpace(cfg.RawClientIP) == "" {
		return false
	}
	if checker == nil {
		return false
	}
	return checker.InterfaceOperUp(cfg.kernelRawIface())
}
