package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
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

func newWAN(up ...wan.Interface) *wan.Model {
	m := wan.NewModel()
	m.Populate(up)
	return m
}

func TestCreatePolicy_RefusesBeforeCreateWithoutWAN(t *testing.T) {
	for name, a := range map[string]*routerAccessPolicyAdapter{
		"nil model":  {svc: &recPolicySvc{}, wan: nil},
		"no WAN up":  {svc: &recPolicySvc{}, wan: newWAN(wan.Interface{Name: "ppp0", ID: "PPPoE0", Up: false, Priority: 700})},
		"no NDMS id": {svc: &recPolicySvc{}, wan: newWAN(wan.Interface{Name: "ppp0", ID: "", Up: true, Priority: 700})},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := a.CreatePolicy(context.Background(), "Kids"); err == nil {
				t.Fatal("ожидался отказ")
			}
			if calls := a.svc.(*recPolicySvc).calls; len(calls) != 0 {
				t.Fatalf("служба не должна вызываться до проверки WAN: %v", calls)
			}
		})
	}
}

// Permit уходит на WAN с наибольшим приоритетом среди Up, с литеральным приоритетом 100;
// IsOurDefault — только для описания awgm-router.
func TestCreatePolicy_PermitsPreferredWANWithPriority100(t *testing.T) {
	svc := &recPolicySvc{mark: "0xffffaab"}
	a := &routerAccessPolicyAdapter{svc: svc, wan: newWAN(
		wan.Interface{Name: "eth3", ID: "ISP", Up: true, Priority: 600},
		wan.Interface{Name: "ppp0", ID: "PPPoE0", Up: true, Priority: 700},
		wan.Interface{Name: "usb0", ID: "UsbLte0", Up: false, Priority: 900},
	)}
	got, err := a.CreatePolicy(context.Background(), "awgm-router")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Create:awgm-router", "Permit:Policy4:PPPoE0:100"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("calls = %v, want %v", svc.calls, want)
	}
	if got.Name != "Policy4" || got.Mark != "0xffffaab" || !got.IsOurDefault {
		t.Fatalf("= %+v", got)
	}
	other, _ := (&routerAccessPolicyAdapter{svc: &recPolicySvc{}, wan: a.wan}).CreatePolicy(context.Background(), "Kids")
	if other.IsOurDefault {
		t.Fatal("IsOurDefault только для awgm-router")
	}
}

// Отказ создания политики останавливает конвейер: permit на несозданной
// политике ушёл бы на роутер с именем-заглушкой из фейка (в проде — с нулевым
// p.Name) и в лучшем случае был бы отвергнут NDMS.
func TestCreatePolicy_CreateFailureStopsBeforePermit(t *testing.T) {
	svc := &recPolicySvc{createErr: errors.New("rci")}
	a := &routerAccessPolicyAdapter{svc: svc, wan: newWAN(wan.Interface{Name: "ppp0", ID: "PPPoE0", Up: true, Priority: 1})}
	if _, err := a.CreatePolicy(context.Background(), "Kids"); err == nil {
		t.Fatal("отказ Create обязан дойти до вызывающего")
	}
	if want := []string{"Create:Kids"}; !reflect.DeepEqual(svc.calls, want) {
		t.Fatalf("calls = %v, want %v (permit после отказа Create запрещён)", svc.calls, want)
	}
}

func TestCreatePolicy_PermitFailureIsReported(t *testing.T) {
	svc := &recPolicySvc{permitErr: errors.New("rci")}
	a := &routerAccessPolicyAdapter{svc: svc, wan: newWAN(wan.Interface{Name: "ppp0", ID: "PPPoE0", Up: true, Priority: 1})}
	if _, err := a.CreatePolicy(context.Background(), "Kids"); err == nil || !strings.Contains(err.Error(), "permit WAN PPPoE0 on policy Policy4") {
		t.Fatalf("err = %v", err)
	}
}
