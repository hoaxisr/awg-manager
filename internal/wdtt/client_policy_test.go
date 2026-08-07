package wdtt

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

type fakePolicyPermitter struct {
	calls []string
}

func (f *fakePolicyPermitter) PermitInterface(_ context.Context, policy, iface string, order int) error {
	f.calls = append(f.calls, policy+":"+iface+":"+itoa(order))
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

type fakePolicyLister struct {
	policies []ndms.Policy
}

func (f *fakePolicyLister) List(context.Context) ([]ndms.Policy, error) {
	return f.policies, nil
}

func TestOpkgPermitAppendOrder(t *testing.T) {
	policy := ndms.Policy{
		Name: "Policy0",
		Interfaces: []ndms.PermittedIface{
			{Name: "ISP", Denied: false},
			{Name: "Wireguard2", Denied: false},
		},
	}
	if already, order := opkgPermitAppendOrder(policy, "OpkgTun18"); already || order != 2 {
		t.Fatalf("append OpkgTun18: already=%v order=%d want order 2", already, order)
	}
	if already, order := opkgPermitAppendOrder(policy, "ISP"); !already || order != 0 {
		t.Fatalf("existing ISP: already=%v order=%d", already, order)
	}
}

func TestEnsureOpkgPermittedInPolicy_AppendsAtEnd(t *testing.T) {
	permit := &fakePolicyPermitter{}
	list := &fakePolicyLister{policies: []ndms.Policy{{
		Name: "Policy3",
		Interfaces: []ndms.PermittedIface{
			{Name: "ISP", Denied: false},
		},
	}}}
	s := NewService(t.TempDir(), t.TempDir(), "", "")
	s.policyPermit = permit
	s.policyList = list

	if err := s.ensureOpkgPermittedInPolicy(context.Background(), "Policy3", "OpkgTun18"); err != nil {
		t.Fatal(err)
	}
	if len(permit.calls) != 1 || permit.calls[0] != "Policy3:OpkgTun18:1" {
		t.Fatalf("calls=%v", permit.calls)
	}
	list.policies[0].Interfaces = append(list.policies[0].Interfaces, ndms.PermittedIface{Name: "OpkgTun18"})
	if err := s.ensureOpkgPermittedInPolicy(context.Background(), "Policy3", "OpkgTun18"); err != nil {
		t.Fatal(err)
	}
	if len(permit.calls) != 1 {
		t.Fatalf("idempotent: calls=%v", permit.calls)
	}
}
