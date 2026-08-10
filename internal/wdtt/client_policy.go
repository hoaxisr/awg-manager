package wdtt

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// NDMSPolicyPermitter — узкий срез command.PolicyCommands для permit global.
type NDMSPolicyPermitter interface {
	PermitInterface(ctx context.Context, name, iface string, order int) error
}

// NDMSPolicyLister — узкий срез query.PolicyStore.
type NDMSPolicyLister interface {
	List(ctx context.Context) ([]ndms.Policy, error)
}

// NDMSPolicyTableGetter resolves policy name → Linux routing table id (table4).
type NDMSPolicyTableGetter interface {
	PolicyTable4(ctx context.Context, policyName string) (int, error)
}

func (s *Service) SetNDMSPolicyRouting(permit NDMSPolicyPermitter, list NDMSPolicyLister, tables NDMSPolicyTableGetter) {
	s.policyPermit = permit
	s.policyList = list
	s.policyTables = tables
}

// opkgPermitAppendOrder возвращает order для append permit без сдвига существующих
// uplink'ов. order=0 вставляет в начало и ломает приоритет ISP/WG.
func opkgPermitAppendOrder(policy ndms.Policy, ndmsName string) (already bool, order int) {
	order = 0
	for _, pi := range policy.Interfaces {
		if pi.Name == ndmsName && !pi.Denied {
			return true, order
		}
		if !pi.Denied {
			order++
		}
	}
	return false, order
}

// opkgPolicyNeedsDefault reports whether OpkgTun should own the policy default
// route: it's the sole permitted uplink, or the first in priority (order 0).
func opkgPolicyNeedsDefault(policy ndms.Policy, ndmsName string) bool {
	var permitted []ndms.PermittedIface
	for _, pi := range policy.Interfaces {
		if !pi.Denied {
			permitted = append(permitted, pi)
		}
	}
	if len(permitted) == 0 {
		return false
	}
	if len(permitted) == 1 && permitted[0].Name == ndmsName {
		return true
	}
	return permitted[0].Name == ndmsName
}

func opkgPolicySoleUplink(policy ndms.Policy, ndmsName string) bool {
	var permitted []ndms.PermittedIface
	for _, pi := range policy.Interfaces {
		if !pi.Denied {
			permitted = append(permitted, pi)
		}
	}
	return len(permitted) == 1 && permitted[0].Name == ndmsName
}

// ReconcileOpkgPolicyRoutes syncs default routes for policies where OpkgTun is uplink.
func (s *Service) ReconcileOpkgPolicyRoutes(ctx context.Context) {
	s.reconcileRunningClientsPolicyRoutes(ctx)
}

func (s *Service) ensureOpkgPermittedInPolicy(ctx context.Context, policyName, ndmsName string) error {
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
	already, order := opkgPermitAppendOrder(*target, ndmsName)
	if already {
		return nil
	}
	if err := s.policyPermit.PermitInterface(ctx, policyName, ndmsName, order); err != nil {
		return err
	}
	if s.appLog != nil {
		s.appLog.Info("policy-permit", ndmsName, fmt.Sprintf("permit global в %s order %d", policyName, order))
	}
	return nil
}

func kernelIfaceFromNDMS(ndmsName string) string {
	if idx, ok := parseOpkgTunIndex(ndmsName); ok {
		return opkgTunKernelName(idx)
	}
	return ""
}
