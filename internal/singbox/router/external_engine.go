package router

import (
	"context"
	"errors"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ErrExternalRoutingEngineActive is returned when a sing-box router operation
// attempts to activate or switch sing-box routing while an external engine
// (such as firc) owns system routing.
var ErrExternalRoutingEngineActive = errors.New("external routing engine (firc) is active")

func isFircRoutingEngine(sr storage.SingboxRouterSettings) bool {
	return strings.EqualFold(strings.TrimSpace(sr.RoutingEngine), "firc")
}

// ParkForExternalEngine tears down sing-box routing interception (iptables/ipset,
// fakeip-tun/policy-tun OpkgTun & routes, router/fakeip/qos slots, KeenDNS preset)
// without clearing SingboxRouter.Enabled or user routing rules/outbounds so
// switching back to sing-box later can resume seamlessly.
func (s *ServiceImpl) ParkForExternalEngine(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	if s.deps.Orch != nil {
		defer s.deps.Orch.HoldReloads()()
	}

	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	return s.parkForExternalEngineLocked(ctx, settings, true)
}

func (s *ServiceImpl) parkForExternalEngineLocked(ctx context.Context, settings *storage.Settings, force bool) error {
	if settings == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.deps.Orch != nil {
		defer s.deps.Orch.HoldReloads()()
	}

	if !force && !s.hasActiveRouterResourcesLocked(ctx, settings) {
		return nil
	}

	s.appLog.Info("park-external", "", "парковка маршрутизации sing-box под внешний движок")

	mode := settings.SingboxRouter.RoutingMode
	st := settings.OpkgTun

	var firstErr error
	switch {
	case st != nil && st.Provisioned && st.Mode == storage.OpkgTunModeFakeIP:
		if err := s.disableFakeIPTunLocked(ctx, settings, false); err != nil {
			firstErr = err
		}
	case st != nil && st.Provisioned && st.Mode == storage.OpkgTunModePolicyTun:
		if err := s.disablePolicyTunLocked(ctx, settings, false); err != nil {
			firstErr = err
		}
	case mode == stateFakeIPTun:
		if err := s.disableFakeIPTunLocked(ctx, settings, false); err != nil {
			firstErr = err
		}
	case mode == statePolicyTun:
		if err := s.disablePolicyTunLocked(ctx, settings, false); err != nil {
			firstErr = err
		}
	}
	if mode != statePolicyTun && st != nil && st.Mode == storage.OpkgTunModePolicyTun {
		s.restorePolicyWANOnDisable(ctx, settings.SingboxRouter)
	}

	if mode != statePolicyTun && mode != stateFakeIPTun {
		if err := s.disableTProxyLocked(ctx, false); err != nil && firstErr == nil {
			firstErr = err
		}
	} else {
		if s.deps.IPTables != nil {
			s.deps.IPTables.Uninstall(ctx)
			s.sweepV6(ctx)
			s.teardownBypassSet(ctx)
			s.currentBypassGeoIPTags = nil
			s.appliedSpec = nil
			s.netfilterStateKnown = false
			s.appliedBlackhole = nil
		}
	}

	// Ensure all router-owned orchestrator slots are parked even in partial or cross-mode states.
	slotsParked := false
	if s.deps.Orch != nil {
		for _, slot := range []orchestrator.Slot{orchestrator.SlotRouter, orchestrator.SlotFakeIP} {
			if s.slotEnabled(slot) {
				if err := s.deps.Orch.SetEnabled(slot, false); err != nil {
					s.appLog.Warn("park-external", string(slot), "disable slot: "+err.Error())
				} else {
					slotsParked = true
				}
			}
		}
		if s.slotEnabled(orchestrator.SlotQoSRoutes) {
			if err := s.disableQoSRoutesSlot(); err != nil {
				s.appLog.Warn("park-external", "qos-routes", err.Error())
			} else {
				slotsParked = true
			}
		}
		if slotsParked {
			s.notifyRoutingSlotsChanged()
		}
	}

	s.parkKeenDNSPresetLocked(ctx)
	s.tunDownStrikes = 0
	s.emitStatus(ctx)
	return firstErr
}

// ResumeFromExternalEngine restores sing-box router interception after control
// returns from an external routing engine (e.g. firc -> sing-box). It honours
// SingboxRouter.Enabled and SingboxManuallyStopped without clearing manual stop.
func (s *ServiceImpl) ResumeFromExternalEngine(ctx context.Context) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	if s.deps.Orch != nil {
		defer s.deps.Orch.HoldReloads()()
	}

	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return err
	}
	if isFircRoutingEngine(sr) {
		return ErrExternalRoutingEngineActive
	}
	if !sr.Enabled || settings.SingboxManuallyStopped {
		s.emitStatus(ctx)
		return nil
	}

	s.syncKeenDNSPreset(ctx, sr)
	return s.enableLocked(ctx, false)
}

func (s *ServiceImpl) hasActiveRouterResourcesLocked(ctx context.Context, settings *storage.Settings) bool {
	if settings != nil && settings.OpkgTun != nil && settings.OpkgTun.Provisioned {
		return true
	}
	if s.deps.Orch != nil {
		if s.slotEnabled(orchestrator.SlotRouter) || s.slotEnabled(orchestrator.SlotFakeIP) || s.slotEnabled(orchestrator.SlotQoSRoutes) {
			return true
		}
	}
	if s.netfilterStateKnown || s.appliedSpec != nil || s.appliedBlackhole != nil || len(s.keenDNSBypass()) > 0 {
		return true
	}
	if s.deps.IPTables != nil {
		if live, err := s.deps.IPTables.probeAll(ctx); err == nil && (live.anyChain || live.installed) {
			return true
		}
	}
	return false
}

func (s *ServiceImpl) slotEnabled(slot orchestrator.Slot) bool {
	if s.deps.Orch == nil {
		return false
	}
	st, ok := s.slotSnapshot(slot)
	return ok && st.Enabled && st.Present
}

func (s *ServiceImpl) parkKeenDNSPresetLocked(ctx context.Context) {
	s.setKeenDNSBypass(nil)
	if _, sync := s.keenDNSPreset(); sync != nil {
		if err := sync.SetKeenDNSEnabled(false, ""); err != nil {
			s.appLog.Warn("keendns-preset", "", err.Error())
		}
	}
}
