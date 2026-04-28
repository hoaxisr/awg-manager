// internal/accesspolicy/marks.go
package accesspolicy

import (
	"context"
	"errors"
	"fmt"
)

// PolicyMarkSource is the narrow contract ServiceImpl needs to fetch
// runtime mark assignments. Implemented by *query.PolicyMarkStore.
type PolicyMarkSource interface {
	Get(ctx context.Context, policyName string) (string, error)
}

// ErrNoMarkSource is returned by GetPolicyMark when no PolicyMarkSource
// is wired (defensive for tests / partial DI).
var ErrNoMarkSource = errors.New("policyMarks not configured")

// GetPolicyMark returns the hex-formatted NDMS-assigned fwmark for the
// named policy (e.g. "0xffffaaa"). Returns query.ErrPolicyMarkNotFound
// if the policy is absent or has no mark; ErrNoMarkSource if not wired.
func (s *ServiceImpl) GetPolicyMark(ctx context.Context, policyName string) (string, error) {
	if s.policyMarks == nil {
		return "", ErrNoMarkSource
	}
	mark, err := s.policyMarks.Get(ctx, policyName)
	if err != nil {
		return "", fmt.Errorf("policy %q: %w", policyName, err)
	}
	return mark, nil
}
