//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

func (s *Service) syncOpkgPolicyDefaultRoutes(ctx context.Context, ndmsName, kernelIface string) {
	if s.policyList == nil || s.policyTables == nil || kernelIface == "" {
		return
	}
	policies, err := s.policyList.List(ctx)
	if err != nil {
		if s.appLog != nil {
			s.appLog.Warn("policy-route", ndmsName, "list policies: "+err.Error())
		}
		return
	}
	for _, p := range policies {
		if !accesspolicy.IsStandardPolicyName(p.Name) {
			continue
		}
		if !opkgPolicyNeedsDefault(p, ndmsName) {
			continue
		}
		table, err := s.policyTables.PolicyTable4(ctx, p.Name)
		if err != nil {
			if s.appLog != nil {
				s.appLog.Warn("policy-route", ndmsName, fmt.Sprintf("%s table: %v", p.Name, err))
			}
			continue
		}
		if err := replacePolicyDefaultRoute(ctx, kernelIface, table); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("policy-route", ndmsName, fmt.Sprintf("%s default: %v", p.Name, err))
			}
			continue
		}
		if s.appLog != nil {
			s.appLog.Info("policy-route", ndmsName, fmt.Sprintf("default via %s в %s (table %d)", kernelIface, p.Name, table))
		}
	}
}

func replacePolicyDefaultRoute(ctx context.Context, kernelIface string, table int) error {
	tableStr := strconv.Itoa(table)
	res, err := exec.Run(ctx, "/opt/sbin/ip", "route", "replace", "default", "dev", kernelIface, "table", tableStr)
	if err != nil {
		return fmt.Errorf("ip route replace default dev %s table %s: %w", kernelIface, tableStr, exec.FormatError(res, err))
	}
	return nil
}
