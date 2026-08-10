package wdtt

import (
	"context"
	"fmt"
	"slices"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func scanPoliciesPermittingOpkg(policies []ndms.Policy, ndmsName string) []OpkgPolicyPermit {
	var out []OpkgPolicyPermit
	for _, p := range policies {
		if !accesspolicy.IsStandardPolicyName(p.Name) {
			continue
		}
		for _, pi := range p.Interfaces {
			if pi.Name == ndmsName && !pi.Denied {
				out = append(out, OpkgPolicyPermit{Name: p.Name, Order: pi.Order})
				break
			}
		}
	}
	return out
}

func policyPermitsEqual(a, b []OpkgPolicyPermit) bool {
	if len(a) != len(b) {
		return false
	}
	aa := slices.Clone(a)
	bb := slices.Clone(b)
	slices.SortFunc(aa, func(x, y OpkgPolicyPermit) int {
		if x.Name != y.Name {
			if x.Name < y.Name {
				return -1
			}
			return 1
		}
		return x.Order - y.Order
	})
	slices.SortFunc(bb, func(x, y OpkgPolicyPermit) int {
		if x.Name != y.Name {
			if x.Name < y.Name {
				return -1
			}
			return 1
		}
		return x.Order - y.Order
	})
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func (s *Service) scanLiveOpkgPolicyPermits(ctx context.Context, ndmsName string) []OpkgPolicyPermit {
	if s.policyList == nil || ndmsName == "" {
		return nil
	}
	policies, err := s.policyList.List(ctx)
	if err != nil {
		return nil
	}
	return scanPoliciesPermittingOpkg(policies, ndmsName)
}

func (s *Service) findRawClientByNdmsIface(ndmsName string) (id string, cfg ClientConfig, ok bool) {
	full, err := s.store.Load()
	if err != nil {
		return "", ClientConfig{}, false
	}
	for _, cl := range full.Clients {
		if cl.Config.usesNDMSOpkgTun() && cl.Config.ndmsAccessIface() == ndmsName {
			return cl.ID, cl.Config, true
		}
	}
	return "", ClientConfig{}, false
}

func (s *Service) persistClientPolicyPermits(clientID string, permits []OpkgPolicyPermit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	full, err := s.store.Load()
	if err != nil {
		return err
	}
	idx := findClientIndex(full.Clients, clientID)
	if idx < 0 {
		return fmt.Errorf("клиент %q не найден", clientID)
	}
	dup := slices.Clone(permits)
	full.Clients[idx].Config.PolicyPermits = dup
	return s.store.Save(full)
}

// captureOpkgPolicyPermitsForConfig сохраняет в конфиг политики, где OpkgTun сейчас
// разрешён. Вызывается перед DeleteOpkgTun, пока NDMS ещё видит интерфейс в политиках.
func (s *Service) captureOpkgPolicyPermitsForConfig(ctx context.Context, cfg ClientConfig) {
	ndmsName := cfg.ndmsAccessIface()
	if ndmsName == "" {
		return
	}
	id, _, ok := s.findRawClientByNdmsIface(ndmsName)
	if !ok {
		return
	}
	live := s.scanLiveOpkgPolicyPermits(ctx, ndmsName)
	if len(live) == 0 && len(cfg.PolicyPermits) > 0 {
		// Интерфейс уже снят или permit снят вручную — не затираем сохранённое.
		return
	}
	if policyPermitsEqual(live, cfg.PolicyPermits) {
		return
	}
	if err := s.persistClientPolicyPermits(id, live); err != nil && s.appLog != nil {
		s.appLog.Warn("policy-permit", ndmsName, "capture: "+err.Error())
	} else if s.appLog != nil && len(live) > 0 {
		s.appLog.Info("policy-permit", ndmsName, fmt.Sprintf("сохранены политики для восстановления: %d", len(live)))
	}
}

func (s *Service) syncOpkgPolicyPermitsFromLive(ctx context.Context, clientID string, cfg ClientConfig) {
	ndmsName := cfg.ndmsAccessIface()
	if ndmsName == "" || !s.opkgTunExists(ctx, ndmsName) {
		return
	}
	live := s.scanLiveOpkgPolicyPermits(ctx, ndmsName)
	if policyPermitsEqual(live, cfg.PolicyPermits) {
		return
	}
	if err := s.persistClientPolicyPermits(clientID, live); err != nil && s.appLog != nil {
		s.appLog.Warn("policy-permit", ndmsName, "sync live: "+err.Error())
	}
}

func (s *Service) ensureOpkgPermittedAtOrder(ctx context.Context, policyName, ndmsName string, order int) error {
	if s.policyPermit == nil || s.policyList == nil {
		return nil
	}
	policies, err := s.policyList.List(ctx)
	if err != nil {
		return fmt.Errorf("list policies: %w", err)
	}
	var target *ndms.Policy
	for i := range policies {
		if policies[i].Name == policyName {
			target = &policies[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("policy %q not found", policyName)
	}
	if already, _ := opkgPermitAppendOrder(*target, ndmsName); already {
		return nil
	}
	if err := s.policyPermit.PermitInterface(ctx, policyName, ndmsName, order); err != nil {
		return err
	}
	if s.appLog != nil {
		s.appLog.Info("policy-permit", ndmsName, fmt.Sprintf("восстановлен permit в %s order %d", policyName, order))
	}
	return nil
}

func (s *Service) restoreOpkgPolicyPermits(ctx context.Context, id string, cfg ClientConfig) {
	ndmsName := cfg.ndmsAccessIface()
	kernelIface := cfg.kernelRawIface()
	permits := cfg.PolicyPermits
	if len(permits) == 0 {
		if live := s.scanLiveOpkgPolicyPermits(ctx, ndmsName); len(live) > 0 {
			permits = live
			_ = s.persistClientPolicyPermits(id, live)
		}
	}
	for _, ref := range permits {
		if err := s.ensureOpkgPermittedAtOrder(ctx, ref.Name, ndmsName, ref.Order); err != nil && s.appLog != nil {
			s.appLog.Warn("policy-permit", ndmsName, fmt.Sprintf("%s: %v", ref.Name, err))
		}
	}
	s.syncOpkgPolicyDefaultRoutes(ctx, ndmsName, kernelIface)
}

// RecordOpkgPolicyPermit вызывается accesspolicy после ручного permit OpkgTun.
func (s *Service) RecordOpkgPolicyPermit(ctx context.Context, ndmsName, policyName string, order int) {
	id, cfg, ok := s.findRawClientByNdmsIface(ndmsName)
	if !ok {
		return
	}
	ref := OpkgPolicyPermit{Name: policyName, Order: order}
	for _, p := range cfg.PolicyPermits {
		if p.Name == policyName {
			return
		}
	}
	next := append(slices.Clone(cfg.PolicyPermits), ref)
	if err := s.persistClientPolicyPermits(id, next); err != nil && s.appLog != nil {
		s.appLog.Warn("policy-permit", ndmsName, "record: "+err.Error())
	}
}

// RemoveOpkgPolicyPermit вызывается accesspolicy после deny OpkgTun в политике.
func (s *Service) RemoveOpkgPolicyPermit(_ context.Context, ndmsName, policyName string) {
	id, cfg, ok := s.findRawClientByNdmsIface(ndmsName)
	if !ok {
		return
	}
	var next []OpkgPolicyPermit
	for _, p := range cfg.PolicyPermits {
		if p.Name != policyName {
			next = append(next, p)
		}
	}
	if len(next) == len(cfg.PolicyPermits) {
		return
	}
	if err := s.persistClientPolicyPermits(id, next); err != nil && s.appLog != nil {
		s.appLog.Warn("policy-permit", ndmsName, "remove: "+err.Error())
	}
}
