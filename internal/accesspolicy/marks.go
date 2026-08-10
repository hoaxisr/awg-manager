// internal/accesspolicy/marks.go
package accesspolicy

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// PolicyMarkSource is the narrow contract ServiceImpl needs to fetch
// runtime mark assignments. Implemented by *query.PolicyMarkStore.
type PolicyMarkSource interface {
	Get(ctx context.Context, policyName string) (string, error)
	// ListByDefaultInterface — политики, чей дефолт ведёт в iface.
	ListByDefaultInterface(ctx context.Context, iface string) ([]query.PolicyDefaultExit, error)
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

// ListPolicyExits возвращает политики, чей дефолтный маршрут ведёт в iface,
// вместе с их метками. Нужен режиму policy-tun: политика там выбирается в
// NDMS, а не в настройках менеджера, и это единственный источник её метки.
func (s *ServiceImpl) ListPolicyExits(ctx context.Context, iface string) ([]query.PolicyDefaultExit, error) {
	if s.policyMarks == nil {
		return nil, ErrNoMarkSource
	}
	return s.policyMarks.ListByDefaultInterface(ctx, iface)
}
