package wdtt

import (
	"context"
	"fmt"
)

// NDMSPolicyMarkGetter resolves NDMS fwmark for a policy name.
type NDMSPolicyMarkGetter interface {
	GetPolicyMark(ctx context.Context, policyName string) (string, error)
}

func (s *Service) SetPolicyMarkGetter(g NDMSPolicyMarkGetter) {
	s.policyMarks = g
}

// IngressRefEnsurer patches sing-box router ingress refs so wdttraw0 is paired
// with the WG kernel iface when ingress is enabled for a WDTT server.
type IngressRefEnsurer interface {
	EnsureWdttServerIngressRefs(ctx context.Context, wgKernelIface string) error
}

func (s *Service) SetIngressRefEnsurer(e IngressRefEnsurer) {
	s.ingressEnsurer = e
}

func (s *Service) applyRawServerPolicy(ctx context.Context, id string, cfg ServerConfig) error {
	policy := normalizePolicy(cfg.Policy)
	if policy == "none" {
		removeRawServerPolicyMark(ctx)
		return nil
	}
	if s.policyMarks == nil {
		return nil
	}
	mark, err := s.policyMarks.GetPolicyMark(ctx, policy)
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("access", id, fmt.Sprintf("policy mark %s: %v", policy, err))
		}
		return nil
	}
	if err := applyRawServerPolicyMark(ctx, mark); err != nil {
		return fmt.Errorf("raw policy mark: %w", err)
	}
	if s.appLog != nil {
		s.appLog.Info("access", id, fmt.Sprintf("policy %s mark %s на %s", policy, mark, DefaultRawServerIface))
	}
	return nil
}

func (s *Service) ensureWdttIngressRefs(ctx context.Context, cfg ServerConfig) {
	if s.ingressEnsurer == nil {
		return
	}
	if err := s.ingressEnsurer.EnsureWdttServerIngressRefs(ctx, cfg.kernelWGIface()); err != nil && s.appLog != nil {
		s.appLog.Warn("ingress", cfg.kernelWGIface(), "ensure refs: "+err.Error())
	}
}