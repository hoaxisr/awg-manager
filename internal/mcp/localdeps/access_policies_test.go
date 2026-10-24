package localdeps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakePolicies records the access policy writes; the reads come from its
// fields.
type fakePolicies struct {
	accesspolicy.Service
	policies      []accesspolicy.Policy
	devices       []accesspolicy.Device
	globals       []accesspolicy.GlobalInterface
	calls         []string
	deleteErr     error
	standaloneErr error
}

func (f *fakePolicies) List(context.Context) ([]accesspolicy.Policy, error) { return f.policies, nil }
func (f *fakePolicies) ListDevices(context.Context) ([]accesspolicy.Device, error) {
	return f.devices, nil
}
func (f *fakePolicies) ListGlobalInterfaces(context.Context) ([]accesspolicy.GlobalInterface, error) {
	return f.globals, nil
}
func (f *fakePolicies) Create(_ context.Context, d string) (*accesspolicy.Policy, error) {
	f.calls = append(f.calls, "create "+d)
	p := accesspolicy.Policy{Name: "Policy3", Description: d, IsStandard: true}
	f.policies = append(f.policies, p)
	return &p, nil
}
func (f *fakePolicies) Delete(_ context.Context, name string) error {
	f.calls = append(f.calls, "delete "+name)
	return f.deleteErr
}
func (f *fakePolicies) SetDescription(_ context.Context, name, d string) error {
	f.calls = append(f.calls, "describe "+name+" "+d)
	return nil
}
func (f *fakePolicies) SetStandalone(_ context.Context, name string, on bool) error {
	f.calls = append(f.calls, "standalone "+name)
	if f.standaloneErr != nil {
		return f.standaloneErr
	}
	f.policies[0].Standalone = on
	return nil
}
func (f *fakePolicies) PermitInterface(_ context.Context, name, iface string, order int) error {
	f.calls = append(f.calls, "permit "+name+" "+iface)
	return nil
}
func (f *fakePolicies) DenyInterface(_ context.Context, name, iface string) error {
	f.calls = append(f.calls, "deny "+name+" "+iface)
	return nil
}
func (f *fakePolicies) AssignDevice(_ context.Context, mac, policy string) error {
	f.calls = append(f.calls, "assign "+mac+" "+policy)
	return nil
}
func (f *fakePolicies) UnassignDevice(_ context.Context, mac string) error {
	f.calls = append(f.calls, "unassign "+mac)
	return nil
}

// TestLocal_ListAccessPolicies — список отдаёт то же, что инструменты
// правки: автономный режим и весь порядок интерфейсов с запрещёнными, а в
// interfaces — только разрешённые.
func TestLocal_ListAccessPolicies(t *testing.T) {
	f := policiesHarness()
	f.policies[0].Standalone = true
	got, err := New(Config{Policies: f}).ListAccessPolicies(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	p := got[0]
	if !p.Standalone || len(p.Interfaces) != 1 || p.Interfaces[0] != "Wireguard0" || len(p.Order) != 2 || !p.Order[1].Denied {
		t.Fatalf("policy = %+v", p)
	}
}

func policiesHarness() *fakePolicies {
	return &fakePolicies{
		policies: []accesspolicy.Policy{{Name: "Policy0", Description: "VPN", IsStandard: true, Interfaces: []accesspolicy.PermittedIface{
			{Name: "Wireguard0", Order: 0}, {Name: "ISP", Order: 1, Denied: true},
		}}},
		devices: []accesspolicy.Device{{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10", Name: "tv"}},
		globals: []accesspolicy.GlobalInterface{{Name: "Wireguard0", Up: true}, {Name: "ISP", Up: true}},
	}
}

// TestLocal_AccessPolicyWrites — правки доходят до сервиса, политика
// читается заново; полный список с запрещёнными — в Order, разрешённые — в
// Interfaces, как и раньше.
func TestLocal_AccessPolicyWrites(t *testing.T) {
	fp := policiesHarness()
	bus := &recBus{}
	l := New(Config{Policies: fp, Bus: bus})
	ctx := context.Background()

	fp.standaloneErr = errors.New("rci: busy")
	if _, err := l.UpdateAccessPolicy(ctx, "Policy0", ptr("Kids"), ptr(true)); err == nil || !strings.Contains(err.Error(), "description was changed") || !bus.has(events.ResourceRoutingAccessPolicies) {
		t.Fatalf("a write that half-succeeded must say so and publish: %v, %v", err, bus.pub)
	}
	fp.standaloneErr, fp.calls, bus.pub = nil, nil, nil
	got, err := l.UpdateAccessPolicy(ctx, "Policy0", nil, ptr(true))
	if err != nil || !got.Standalone || len(got.Interfaces) != 1 || len(got.Order) != 2 || !got.Order[1].Denied {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "ISP", true, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Wireguard0", false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Ghost0", true, 0); err == nil {
		t.Fatal("an interface the router does not offer must be refused")
	}
	bus.pub = nil
	if _, err := l.AssignDevicePolicy(ctx, "AA:BB:CC:DD:EE:FF", "Policy0"); err != nil || !bus.has(events.ResourceRoutingAccessPolicies) || !bus.has(events.ResourceRoutingPolicyDevices) {
		t.Fatalf("moving a device changes the policy list's counts too, as REST publishes: %v, %v", err, bus.pub)
	}
	if _, err := l.AssignDevicePolicy(ctx, "aa:bb:cc:dd:ee:ff", ""); err != nil {
		t.Fatal(err)
	}
	want := "standalone Policy0|permit Policy0 ISP|deny Policy0 Wireguard0|assign aa:bb:cc:dd:ee:ff Policy0|unassign aa:bb:cc:dd:ee:ff"
	if got := strings.Join(fp.calls, "|"); got != want {
		t.Fatalf("calls = %s", got)
	}
	if _, err := l.AssignDevicePolicy(ctx, "11:22:33:44:55:66", "Policy0"); err == nil {
		t.Fatal("an unknown device must be refused")
	}
	if _, err := l.AssignDevicePolicy(ctx, "aa:bb:cc:dd:ee:ff", "Policy9"); err == nil {
		t.Fatal("an unknown policy must be refused")
	}
}

// TestLocal_DeleteAccessPolicyGuardsTheRouterPolicy — политика, по
// которой работает маршрутизатор sing-box, не удаляется: его устройства
// отрезало бы от sing-box.
func TestLocal_DeleteAccessPolicyGuardsTheRouterPolicy(t *testing.T) {
	fp := policiesHarness()
	fr := routerHarness()
	fr.settings = storage.SingboxRouterSettings{PolicyName: "Policy0"}
	l := New(Config{Policies: fp, Router: fr})

	if _, err := l.DeleteAccessPolicy(context.Background(), "Policy0"); err == nil || !strings.Contains(err.Error(), "sing-box") || len(fp.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, fp.calls)
	}
	fr.settings.PolicyName = "Policy1"
	deleted, err := l.DeleteAccessPolicy(context.Background(), "Policy0")
	if err != nil || deleted.Name != "Policy0" || fp.calls[0] != "delete Policy0" {
		t.Fatalf("deleted = %+v, %v", deleted, err)
	}
}
