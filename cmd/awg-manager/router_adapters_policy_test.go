package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// recPolicySvc is a minimal accesspolicy.Service fake for CreatePolicy tests:
// it records mutating calls and lets Create/PermitInterface fail on demand.
type recPolicySvc struct {
	calls     []string
	createErr error
	permitErr error
	mark      string
}

func (s *recPolicySvc) List(context.Context) ([]accesspolicy.Policy, error) { return nil, nil }
func (s *recPolicySvc) Create(_ context.Context, d string) (*accesspolicy.Policy, error) {
	s.calls = append(s.calls, "Create:"+d)
	return &accesspolicy.Policy{Name: "Policy4", Description: d}, s.createErr
}
func (s *recPolicySvc) Delete(context.Context, string) error { return nil }
func (s *recPolicySvc) SetDescription(context.Context, string, string) error {
	return nil
}
func (s *recPolicySvc) SetStandalone(context.Context, string, bool) error { return nil }
func (s *recPolicySvc) PermitInterface(_ context.Context, n, i string, o int) error {
	s.calls = append(s.calls, fmt.Sprintf("Permit:%s:%s:%d", n, i, o))
	return s.permitErr
}
func (s *recPolicySvc) DenyInterface(context.Context, string, string) error { return nil }
func (s *recPolicySvc) AssignDevice(context.Context, string, string) error  { return nil }
func (s *recPolicySvc) UnassignDevice(context.Context, string) error        { return nil }
func (s *recPolicySvc) ListDevices(context.Context) ([]accesspolicy.Device, error) {
	return nil, nil
}
func (s *recPolicySvc) ListGlobalInterfaces(context.Context) ([]accesspolicy.GlobalInterface, error) {
	return nil, nil
}
func (s *recPolicySvc) SetInterfaceUp(context.Context, string, bool) error { return nil }
func (s *recPolicySvc) GetPolicyMark(context.Context, string) (string, error) {
	return s.mark, nil
}
func (s *recPolicySvc) ListPolicyExits(context.Context, string) ([]ndmsquery.PolicyDefaultExit, error) {
	return nil, nil
}

// Создание политики — ровно один вызов Create: выходы зависят от режима и
// ставятся при его включении (router/policy_wan.go). Прежний permit WAN с
// order 100 отвергался на 5.01 и оставлял сироту (F440).
func TestCreatePolicy_CreatesWithoutPermit(t *testing.T) {
	svc := &recPolicySvc{mark: "0xffffaab"}
	a := &routerAccessPolicyAdapter{svc: svc}
	got, err := a.CreatePolicy(context.Background(), "awgm-router")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Create:awgm-router"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("calls = %v, want %v", svc.calls, want)
	}
	if got.Name != "Policy4" || got.Mark != "0xffffaab" || !got.IsOurDefault {
		t.Fatalf("= %+v", got)
	}
	other, _ := (&routerAccessPolicyAdapter{svc: &recPolicySvc{}}).CreatePolicy(context.Background(), "Kids")
	if other.IsOurDefault {
		t.Fatal("IsOurDefault только для awgm-router")
	}
}

func TestCreatePolicy_CreateFailureIsReported(t *testing.T) {
	svc := &recPolicySvc{createErr: errors.New("rci")}
	a := &routerAccessPolicyAdapter{svc: svc}
	if _, err := a.CreatePolicy(context.Background(), "Kids"); err == nil {
		t.Fatal("отказ Create обязан дойти до вызывающего")
	}
}
