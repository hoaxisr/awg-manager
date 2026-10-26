package localdeps

import (
	"context"
	"errors"
	"fmt"
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
	// readBackErr fails every List after a write: the router took the
	// change, the read that follows did not go through.
	readBackErr error
	written     bool
}

func (f *fakePolicies) List(context.Context) ([]accesspolicy.Policy, error) {
	if f.written && f.readBackErr != nil {
		return nil, f.readBackErr
	}
	return f.policies, nil
}
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
	f.written = true
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
	f.calls = append(f.calls, fmt.Sprintf("permit %s %s at %d", name, iface, order))
	f.written = true
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
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "ISP", true, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Wireguard0", false, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Ghost0", true, 0, nil); err == nil {
		t.Fatal("an interface the router does not offer must be refused")
	}
	bus.pub = nil
	if _, err := l.AssignDevicePolicy(ctx, "AA:BB:CC:DD:EE:FF", "Policy0"); err != nil || !bus.has(events.ResourceRoutingAccessPolicies) || !bus.has(events.ResourceRoutingPolicyDevices) {
		t.Fatalf("moving a device changes the policy list's counts too, as REST publishes: %v, %v", err, bus.pub)
	}
	if _, err := l.AssignDevicePolicy(ctx, "aa:bb:cc:dd:ee:ff", ""); err != nil {
		t.Fatal(err)
	}
	want := "standalone Policy0|permit Policy0 ISP at 1|deny Policy0 Wireguard0|assign aa:bb:cc:dd:ee:ff Policy0|unassign aa:bb:cc:dd:ee:ff"
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

// TestLocal_SetAccessPolicyInterfaceChecksTheOrder — позиция считается в
// списке, который агент прочитал; список, изменившийся с тех пор, — отказ
// без записи. Снять можно и интерфейс, которого роутер больше не
// предлагает (туннель удалён), но не тот, которого в политике нет.
func TestLocal_SetAccessPolicyInterfaceChecksTheOrder(t *testing.T) {
	fp := policiesHarness()
	l := New(Config{Policies: fp})
	ctx := context.Background()

	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "ISP", true, 0, []string{"ISP", "Wireguard0"}); err == nil || !strings.Contains(err.Error(), "list_access_policies") || len(fp.calls) != 0 {
		t.Fatalf("a stale order must be refused before the write: %v, %v", err, fp.calls)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "ISP", true, 0, []string{"Wireguard0", "ISP"}); err != nil || fp.calls[0] != "permit Policy0 ISP at 0" {
		t.Fatalf("the order read is the order now: %v, %v", err, fp.calls)
	}

	fp.calls = nil
	fp.policies[0].Interfaces = append(fp.policies[0].Interfaces, accesspolicy.PermittedIface{Name: "Wireguard7", Order: 2})
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Wireguard7", false, 0, nil); err != nil || len(fp.calls) != 1 || fp.calls[0] != "deny Policy0 Wireguard7" {
		t.Fatalf("an interface the router no longer offers must still be removable: %v, %v", err, fp.calls)
	}
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Wireguard1", false, 0, nil); err == nil || len(fp.calls) != 1 {
		t.Fatalf("an interface not in the policy has nothing to remove: %v, %v", err, fp.calls)
	}
}

// TestLocal_DeleteAccessPolicyFailsClosed — проверка «по этой политике
// работает маршрутизатор sing-box», которая не смогла прочитать настройки,
// не пропускает удаление.
func TestLocal_DeleteAccessPolicyFailsClosed(t *testing.T) {
	fp := policiesHarness()
	fr := &settingsErrRouter{fakeRouter: routerHarness()}
	l := New(Config{Policies: fp, Router: fr})
	if _, err := l.DeleteAccessPolicy(context.Background(), "Policy0"); err == nil || !strings.Contains(err.Error(), "not deleted") || len(fp.calls) != 0 {
		t.Fatalf("err = %v, calls = %v", err, fp.calls)
	}
}

type settingsErrRouter struct{ *fakeRouter }

func (settingsErrRouter) GetSettings(context.Context) (storage.SingboxRouterSettings, error) {
	return storage.SingboxRouterSettings{}, errors.New("settings unreadable")
}

// TestLocal_SetAccessPolicyInterfaceAppendsByDefault — без позиции
// интерфейс встаёт последним: трафик идёт через первый работающий, и
// позиция 0 по умолчанию сразу увела бы на него всю политику.
func TestLocal_SetAccessPolicyInterfaceAppendsByDefault(t *testing.T) {
	fp := policiesHarness()
	fp.globals = append(fp.globals, accesspolicy.GlobalInterface{Name: "Wireguard1", Up: true})
	l := New(Config{Policies: fp})
	ctx := context.Background()

	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Wireguard1", true, -1, nil); err != nil || fp.calls[0] != "permit Policy0 Wireguard1 at 2" {
		t.Fatalf("a new interface goes after the two the policy has: %v, %v", err, fp.calls)
	}
	// An interface already in the list moves to the end of the others.
	if _, err := l.SetAccessPolicyInterface(ctx, "Policy0", "Wireguard0", true, -1, nil); err != nil || fp.calls[1] != "permit Policy0 Wireguard0 at 1" {
		t.Fatalf("calls = %v, %v", fp.calls, err)
	}
}

// TestLocal_AccessPolicyEditsSurviveAFailedReadBack — запись прошла, а
// перечитать политику не удалось: это не ошибка — изменение уже действует,
// — а политика как была, с пометкой.
func TestLocal_AccessPolicyEditsSurviveAFailedReadBack(t *testing.T) {
	fp := policiesHarness()
	fp.readBackErr = errors.New("rci: timeout")
	l := New(Config{Policies: fp})
	ctx := context.Background()

	got, err := l.SetAccessPolicyInterface(ctx, "Policy0", "ISP", true, -1, nil)
	if err != nil || got.Unconfirmed == "" || !strings.Contains(got.Unconfirmed, "applied") || got.Name != "Policy0" || len(fp.calls) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	fp.written = false
	got, err = l.UpdateAccessPolicy(ctx, "Policy0", ptr("Kids"), nil)
	if err != nil || got.Unconfirmed == "" || got.Description != "VPN" {
		t.Fatalf("the policy comes back as it was, marked: %+v, %v", got, err)
	}
	fp.readBackErr, fp.written = nil, false
	if got, err = l.UpdateAccessPolicy(ctx, "Policy0", ptr("Kids"), nil); err != nil || got.Unconfirmed != "" {
		t.Fatalf("a read that works is not marked: %+v, %v", got, err)
	}
}
