package router

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type applyPolicyProvider struct {
	fakeAccessPolicyProvider
	restored bool
}

func (p *applyPolicyProvider) ListPolicyExits(context.Context, string) ([]query.PolicyDefaultExit, error) {
	if p.restored {
		return []query.PolicyDefaultExit{{Name: "Policy0", Mark: "0xffffaaa"}}, nil
	}
	return nil, nil
}

type applyRouteProvider struct {
	*recDefaultRoute
	policy *applyPolicyProvider
}

func (r *applyRouteProvider) SetDefaultRoute(ctx context.Context, name string) error {
	r.policy.restored = true
	return r.recDefaultRoute.SetDefaultRoute(ctx, name)
}

func TestPersistConfigHotUpdateRestoresPolicyRoute(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	provisionPolicyTunForReconcile(t, h)
	h.withPolicy(t, "Policy0")
	policy := &applyPolicyProvider{}
	h.svc.deps.Policies = policy
	h.svc.deps.DefaultRoute = &applyRouteProvider{
		recDefaultRoute: &recDefaultRoute{log: h.log},
		policy:          policy,
	}
	// Даже исчерпанная периодическая серия не должна подавлять событие apply.
	h.svc.policyTunRouteStrikes.Store(8)

	cfg, err := h.svc.loadAppliedRouterConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.persistConfig(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfig: %v", err)
	}
	if got := h.log.count("SetDefaultRoute:OpkgTun0"); got != 1 {
		t.Fatalf("hot update route writes = %d, want exactly 1; calls=%v", got, h.log.calls)
	}
	if got := h.svc.policyTunRouteStrikes.Load(); got != 0 {
		t.Fatalf("restored runtime was not read back: strikes=%d", got)
	}
	if len(h.log.calls) != 1 {
		t.Fatalf("hot update performed unrelated policy/firewall operations: %v", h.log.calls)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Fatalf("content-only hot update created a phantom draft: %v", err)
	}
}
