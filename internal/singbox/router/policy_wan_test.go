package router

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeWANErr struct{}

func (fakeWANErr) ListWAN(context.Context) ([]WANInterfaceInfo, error) {
	return nil, errors.New("rci")
}

// running-config: у целевой политики разрешены туннель, WAN и чужой VPN; есть
// `no permit` (позиций не занимает) и соседняя политика со своим WAN.
var policyWANLines = []string{
	"ip policy Policy1",
	"    description \"permit global PPPoE0 in text\"",
	"    permit global OpkgTun0",
	"    permit global PPPoE0",
	"    no permit global ISP",
	"    permit global Wireguard0",
	"!",
	"ip policy Policy2",
	"    permit global ISP",
	"!",
}

var policyWANs = []WANInterfaceInfo{
	{ID: "ISP", Up: true, Priority: 600},
	{ID: "PPPoE0", Up: true, Priority: 700},
	{ID: "UsbLte0", Up: false, Priority: 900},
}

func TestPolicyPermits_OnlyTargetPolicyPermitLines(t *testing.T) {
	got := policyPermits(policyWANLines, "Policy1")
	if want := []string{"OpkgTun0", "PPPoE0", "Wireguard0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("= %v, want %v", got, want)
	}
	if got := policyPermits(policyWANLines, "Policy3"); got != nil {
		t.Fatalf("нет политики — нет выходов, got %v", got)
	}
}

// policy-tun снимает из политики только WAN; туннель и чужой VPN остаются,
// соседняя политика не трогается.
func TestDenyPolicyWAN_RemovesOnlyWANFromTargetPolicy(t *testing.T) {
	pol := &fakeAccessPolicyProvider{}
	s := &ServiceImpl{deps: Deps{Policies: pol, WANInterfaces: fakeWAN{list: policyWANs}}}
	s.denyPolicyWAN(context.Background(), storage.SingboxRouterSettings{PolicyName: "Policy1"}, policyWANLines)
	if want := []string{"Policy1:PPPoE0"}; !reflect.DeepEqual(pol.denies, want) {
		t.Fatalf("denies = %v, want %v", pol.denies, want)
	}
}

// Без WAN в политике и при неизвестном списке WAN — ни одной записи в NDMS.
func TestDenyPolicyWAN_NoWriteWithoutWANOrUnknownList(t *testing.T) {
	for name, tc := range map[string]struct {
		wan   WANInterfaceLister
		lines []string
	}{
		"WAN не разрешён":    {fakeWAN{list: policyWANs}, []string{"ip policy Policy1", "    permit global OpkgTun0", "!"}},
		"список WAN упал":    {fakeWANErr{}, policyWANLines},
		"нет running-config": {fakeWAN{list: policyWANs}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			pol := &fakeAccessPolicyProvider{}
			s := &ServiceImpl{deps: Deps{Policies: pol, WANInterfaces: tc.wan}}
			s.denyPolicyWAN(context.Background(), storage.SingboxRouterSettings{PolicyName: "Policy1"}, tc.lines)
			if len(pol.denies) != 0 {
				t.Fatalf("denies = %v, want none", pol.denies)
			}
		})
	}
}

// tproxy без WAN в политике разрешает поднятый WAN с наибольшим приоритетом в
// конец списка: order = число текущих permit (no permit не в счёт).
func TestEnsurePolicyWAN_PermitsPreferredUpWANAtEnd(t *testing.T) {
	lines := []string{"ip policy Policy1", "    permit global Wireguard0", "    no permit global ISP", "!"}
	pol := &fakeAccessPolicyProvider{}
	s := &ServiceImpl{deps: Deps{Policies: pol, WANInterfaces: fakeWAN{list: policyWANs}}}
	s.ensurePolicyWAN(context.Background(), storage.SingboxRouterSettings{PolicyName: "Policy1"}, lines)
	if want := []string{"Policy1:PPPoE0:1"}; !reflect.DeepEqual(pol.permits, want) {
		t.Fatalf("permits = %v, want %v", pol.permits, want)
	}
}

// Пустая политика (свежесозданная) — order 0: другого NDMS не примет.
func TestEnsurePolicyWAN_EmptyPolicyGetsOrder0(t *testing.T) {
	pol := &fakeAccessPolicyProvider{}
	s := &ServiceImpl{deps: Deps{Policies: pol, WANInterfaces: fakeWAN{list: policyWANs}}}
	s.ensurePolicyWAN(context.Background(), storage.SingboxRouterSettings{PolicyName: "Policy1"}, []string{"ip policy Policy1", "!"})
	if want := []string{"Policy1:PPPoE0:0"}; !reflect.DeepEqual(pol.permits, want) {
		t.Fatalf("permits = %v, want %v", pol.permits, want)
	}
}

// Любой WAN уже разрешён, running-config не прочитан, поднятого WAN нет или
// список WAN неизвестен — permit не шлётся.
func TestEnsurePolicyWAN_NoWriteWhenPresentOrUnknown(t *testing.T) {
	down := []WANInterfaceInfo{{ID: "PPPoE0", Up: false, Priority: 700}}
	for name, tc := range map[string]struct {
		wan   WANInterfaceLister
		lines []string
	}{
		"WAN уже есть":       {fakeWAN{list: policyWANs}, []string{"ip policy Policy1", "    permit global ISP", "!"}},
		"нет running-config": {fakeWAN{list: policyWANs}, nil},
		"нет поднятого WAN":  {fakeWAN{list: down}, []string{"ip policy Policy1", "!"}},
		"список WAN упал":    {fakeWANErr{}, []string{"ip policy Policy1", "!"}},
	} {
		t.Run(name, func(t *testing.T) {
			pol := &fakeAccessPolicyProvider{}
			s := &ServiceImpl{deps: Deps{Policies: pol, WANInterfaces: tc.wan}}
			s.ensurePolicyWAN(context.Background(), storage.SingboxRouterSettings{PolicyName: "Policy1"}, tc.lines)
			if len(pol.permits) != 0 {
				t.Fatalf("permits = %v, want none", pol.permits)
			}
		})
	}
}

// Включение policy-tun снимает WAN, разрешённый в целевой политике (F440).
func TestEnablePolicyTun_DeniesWANInPolicy(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	h.svc.deps.WANInterfaces = fakeWAN{list: []WANInterfaceInfo{{ID: "PPPoE0", Up: true}}}
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{
		"ip policy Policy0", "    permit global PPPoE0", "!",
	}}
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if want := []string{"Policy0:PPPoE0"}; !reflect.DeepEqual(pol.denies, want) {
		t.Fatalf("denies = %v, want %v", pol.denies, want)
	}
}

// WAN, разрешённый мимо нас на работающем режиме, снимается на тике reconcile.
func TestReconcilePolicyTun_DeniesWANInPolicy(t *testing.T) {
	h := newPolicyTunEnableHarness(t, "")
	pol := h.withPolicy(t, "Policy0")
	sr := provisionPolicyTunForReconcile(t, h)
	pol.denies = nil
	h.svc.deps.WANInterfaces = fakeWAN{list: []WANInterfaceInfo{{ID: "PPPoE0", Up: true}}}
	h.svc.deps.RunningConfig = &fakeRunningConfig{lines: append(healthyPolicyTunRC("OpkgTun0"),
		"ip policy Policy0", "    permit global PPPoE0", "!")}
	if err := h.svc.reconcilePolicyTun(context.Background(), sr); err != nil {
		t.Fatalf("reconcilePolicyTun: %v", err)
	}
	if want := []string{"Policy0:PPPoE0"}; !reflect.DeepEqual(pol.denies, want) {
		t.Fatalf("denies = %v, want %v", pol.denies, want)
	}
}

// Тик tproxy выдаёт WAN политике, у которой его нет (F440).
func TestReconcileInstalled_PermitsWANInPolicy(t *testing.T) {
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newReconcileInstalledService(t, sb)
	pol := svc.deps.Policies.(*fakeAccessPolicyProvider)
	svc.deps.WANInterfaces = fakeWAN{list: []WANInterfaceInfo{{ID: "PPPoE0", Up: true}}}
	svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{"ip policy Policy0", "!"}}
	if err := svc.reconcileInstalled(context.Background(), reconcileInstalledSettings); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if want := []string{"Policy0:PPPoE0:0"}; !reflect.DeepEqual(pol.permits, want) {
		t.Fatalf("permits = %v, want %v", pol.permits, want)
	}
}

// Включение tproxy с политикой выдаёт ей WAN до установки правил: выход нужен
// трафику мимо sing-box независимо от исхода установки (F440).
func TestEnableTProxy_PermitsWANInPolicy(t *testing.T) {
	svc, dir := newQoSSlotTestService(t, "vpn")
	ensureDisabledDir(t, dir)
	if err := svc.deps.Orch.SetEnabledSilent(orchestrator.SlotRouter, false); err != nil {
		t.Fatalf("park router slot: %v", err)
	}
	svc.deps.Settings = newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode: "tproxy", DeviceMode: "policy", PolicyName: "Policy0", WANAutoDetect: true,
	})
	svc.deps.Singbox = &fakeSingbox{dir: dir, isRunningFn: func() (bool, int) { return true, 1234 }}
	stubListeningProbe(t, func() bool { return true })
	pol := &fakeAccessPolicyProvider{mark: "0xffffaaa"}
	svc.deps.Policies = pol
	svc.deps.WANInterfaces = fakeWAN{list: []WANInterfaceInfo{{ID: "PPPoE0", Up: true}}}
	svc.deps.RunningConfig = &fakeRunningConfig{lines: []string{"ip policy Policy0", "!"}}
	svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return errors.New("stop here") })
	svc.deps.WANIPCollector = &fakeWANIPCollector{}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }
	svc.deps.XtDscpProbe = func(context.Context) bool { return true }

	_ = svc.Enable(context.Background())
	if want := []string{"Policy0:PPPoE0:0"}; !reflect.DeepEqual(pol.permits, want) {
		t.Fatalf("permits = %v, want %v", pol.permits, want)
	}
}
