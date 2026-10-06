package dnsroute

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// newHRTestSvc returns a dnsroute ServiceImpl wired to a hydraroute.Service
// whose HR files live under a tmp dir. Policy-list calls return no policies
// (override via the NDMS mock if needed).
func newHRTestSvc(t *testing.T, resolver InterfaceResolver) (*ServiceImpl, *hydraroute.Service) {
	t.Helper()
	dir := t.TempDir()
	restore := hydraroute.SetPaths(
		filepath.Join(dir, "domain.conf"),
		filepath.Join(dir, "ip.list"),
	)
	t.Cleanup(restore)

	hydra := hydraroute.NewService(&kernelResolverAdapter{resolver: resolver}, nil)
	hydra.SetStatusForTest(true)

	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	q, c, _, _ := newTestNDMS()
	svc := &ServiceImpl{
		store:    store,
		queries:  q,
		commands: c,
		resolver: resolver,
		hydra:    hydra,
	}
	return svc, hydra
}

// kernelResolverAdapter bridges dnsroute.InterfaceResolver to hydraroute.KernelIfaceResolver.
type kernelResolverAdapter struct {
	resolver InterfaceResolver
}

func (a *kernelResolverAdapter) GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error) {
	return a.resolver.GetKernelIfaceName(ctx, tunnelID)
}

// known-tunnel resolver stub
type stubResolver struct {
	kernelByTunnel map[string]string
	systemByIface  map[string]string
}

func (s *stubResolver) SystemTunnelsByIface(context.Context) map[string]string {
	return s.systemByIface
}

func (s *stubResolver) ResolveInterface(ctx context.Context, tunnelID string) (string, error) {
	if v, ok := s.kernelByTunnel[tunnelID]; ok {
		return v, nil
	}
	return tunnelID, nil
}

func (s *stubResolver) GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error) {
	if v, ok := s.kernelByTunnel[tunnelID]; ok {
		return v, nil
	}
	return "", errTunnelUnknown{tunnelID}
}

type errTunnelUnknown struct{ id string }

func (e errTunnelUnknown) Error() string { return "tunnel " + e.id + " unknown" }

func TestList_HRRulesComeFromFiles(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)

	// Seed HR files via hydraroute CRUD directly.
	_, _ = hydra.CreateRule(hydraroute.HRRule{
		Name: "Youtube", Domains: []string{"youtube.com"}, Target: "nwg0",
	})

	lists, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(lists) != 1 {
		t.Fatalf("expected 1, got %d: %+v", len(lists), lists)
	}
	if lists[0].Name != "Youtube" || lists[0].Backend != "hydraroute" {
		t.Errorf("unexpected: %+v", lists[0])
	}
}

func TestList_UnionsHRAndNDMS(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)

	// HR rule via hydra directly (simulates pre-existing HR file).
	_, _ = hydra.CreateRule(hydraroute.HRRule{
		Name: "HRRule", Domains: []string{"hr.com"}, Target: "nwg0",
	})

	// NDMS rule via dnsroute.Create with backend=ndms.
	_, err := svc.Create(context.Background(), DomainList{
		Name: "NDMSRule", Backend: "ndms",
		ManualDomains: []string{"ndms.com"},
		Routes:        []RouteTarget{{TunnelID: "awg10"}},
	})
	if err != nil {
		t.Fatalf("Create NDMS: %v", err)
	}

	lists, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var hrFound, ndmsFound bool
	for _, l := range lists {
		if l.Name == "HRRule" && l.Backend == "hydraroute" {
			hrFound = true
		}
		if l.Name == "NDMSRule" && l.Backend == "ndms" {
			ndmsFound = true
		}
	}
	if !hrFound || !ndmsFound {
		t.Errorf("union missing entries: %+v", lists)
	}
}

func TestCreate_HRBackend_WritesToHRFiles(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)

	created, err := svc.Create(context.Background(), DomainList{
		Name:          "Test",
		Backend:       "hydraroute",
		ManualDomains: []string{"test.com", "10.0.0.0/8"},
		HRRouteMode:   "interface",
		Routes:        []RouteTarget{{TunnelID: "awg10"}},
	})
	if err != nil {
		t.Fatalf("Create HR: %v", err)
	}
	if created.ID == "" || len(created.ID) < 6 {
		t.Errorf("expected 8-hex ID, got %q", created.ID)
	}

	// Must be in HR files, not in JSON store.
	hrRules, _, _ := hydra.ListRules()
	if len(hrRules) != 1 || hrRules[0].Name != "Test" {
		t.Errorf("HR files don't contain the rule: %+v", hrRules)
	}

	// Should NOT be in dnsroute JSON store.
	data := svc.store.GetCached()
	for _, l := range data.Lists {
		if l.Name == "Test" {
			t.Errorf("HR rule must not live in JSON store: %+v", l)
		}
	}
}

func TestCreate_HRBackend_BrokenTunnelRejected(t *testing.T) {
	// Broken-tunnel contract: if tunnelID doesn't resolve, we don't write a
	// dead entry to HR files — we reject the create.
	resolver := &stubResolver{kernelByTunnel: map[string]string{}}
	svc, hydra := newHRTestSvc(t, resolver)

	_, err := svc.Create(context.Background(), DomainList{
		Name:          "Test",
		Backend:       "hydraroute",
		ManualDomains: []string{"test.com"},
		HRRouteMode:   "interface",
		Routes:        []RouteTarget{{TunnelID: "deleted-tunnel"}},
	})
	if err == nil {
		t.Fatal("expected error for unresolvable tunnel, got nil")
	}

	hrRules, _, _ := hydra.ListRules()
	if len(hrRules) != 0 {
		t.Errorf("nothing should have been written: %+v", hrRules)
	}
}

func TestSetEnabled_HRRule_TogglesFileComment(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)

	_, _ = hydra.CreateRule(hydraroute.HRRule{
		Name: "Youtube", Domains: []string{"youtube.com"}, Target: "nwg0",
	})

	if err := svc.SetEnabled(context.Background(), "hr:Youtube", false); err != nil {
		t.Fatalf("SetEnabled disable: %v", err)
	}
	rules, _, _ := hydra.ListRules()
	if len(rules) != 1 || !rules[0].Disabled {
		t.Fatalf("expected disabled rule: %+v", rules)
	}
	lists, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *DomainList
	for i := range lists {
		if lists[i].ID == "hr:Youtube" {
			found = &lists[i]
			break
		}
	}
	if found == nil || found.Enabled {
		t.Fatalf("List() enabled flag: %+v", found)
	}

	if err := svc.SetEnabled(context.Background(), "hr:Youtube", true); err != nil {
		t.Fatalf("SetEnabled enable: %v", err)
	}
	rules, _, _ = hydra.ListRules()
	if len(rules) != 1 || rules[0].Disabled {
		t.Fatalf("expected enabled rule: %+v", rules)
	}
}

func TestDelete_HRRule_RemovesFromHRFile(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, hydra := newHRTestSvc(t, resolver)

	created, _ := hydra.CreateRule(hydraroute.HRRule{
		Name: "Temp", Domains: []string{"temp.com"}, Target: "nwg0",
	})

	if err := svc.Delete(context.Background(), "hr:"+created.Name); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	hrRules, _, _ := hydra.ListRules()
	if len(hrRules) != 0 {
		t.Errorf("expected empty after delete: %+v", hrRules)
	}
}

func TestCreate_HRPolicyModeNew_WaitsForPolicyBeforePermit(t *testing.T) {
	// Orchestration order must be:
	//   1. CreateRule (writes HR files; hydraroute schedules daemon restart)
	//   2. Wait for the policy to appear in `show rc ip policy` (HR Neo
	//      creates it after restart via RCI)
	//   3. Only then EnsurePolicyInterfaces
	// This test records the call sequence and asserts the wait happened
	// before the permit, and that the interfaces went in in the exact
	// order the caller specified.
	resolver := &stubResolver{kernelByTunnel: map[string]string{}}
	svc, hydra := newHRTestSvc(t, resolver)

	orchestrator := &recordingOrchestrator{}
	svc.policyOrchestrator = orchestrator

	_, err := svc.Create(context.Background(), DomainList{
		Name:               "Streaming",
		Backend:            "hydraroute",
		ManualDomains:      []string{"netflix.com"},
		HRRouteMode:        "policy",
		HRPolicyName:       "MediaPolicy",
		HRPolicyInterfaces: []string{"PPPoE0", "Wireguard0"}, // NDMS names
	})
	if err != nil {
		t.Fatalf("Create HR: %v", err)
	}

	// Rule must be written to HR files before orchestrator steps run.
	rules, _, _ := hydra.ListRules()
	if len(rules) != 1 || rules[0].Name != "Streaming" {
		t.Fatalf("rule not written to HR files before orchestration: %+v", rules)
	}

	wantSequence := []string{"WaitForPolicy:MediaPolicy", "EnsurePolicyInterfaces:MediaPolicy:PPPoE0,Wireguard0"}
	if !reflect.DeepEqual(orchestrator.sequence, wantSequence) {
		t.Errorf("call sequence = %v\nwant             %v", orchestrator.sequence, wantSequence)
	}
}

func TestCreate_HRPolicyModeNew_FailsIfPolicyNeverAppears(t *testing.T) {
	resolver := &stubResolver{kernelByTunnel: map[string]string{"awg10": "nwg0"}}
	svc, _ := newHRTestSvc(t, resolver)
	svc.policyOrchestrator = &failingOrchestrator{waitErr: errors.New("policy timeout")}

	_, err := svc.Create(context.Background(), DomainList{
		Name:               "Streaming",
		Backend:            "hydraroute",
		ManualDomains:      []string{"netflix.com"},
		HRRouteMode:        "policy",
		HRPolicyName:       "NeverAppears",
		HRPolicyInterfaces: []string{"Wireguard0"},
	})
	if err == nil {
		t.Fatal("expected error when policy never appears, got nil")
	}
}

type recordingOrchestrator struct {
	sequence []string
}

func (r *recordingOrchestrator) WaitForPolicy(_ context.Context, name string, _ time.Duration) error {
	r.sequence = append(r.sequence, "WaitForPolicy:"+name)
	return nil
}

func (r *recordingOrchestrator) EnsurePolicyInterfaces(_ context.Context, name string, ifaces []string) error {
	r.sequence = append(r.sequence, "EnsurePolicyInterfaces:"+name+":"+strings.Join(ifaces, ","))
	return nil
}

type failingOrchestrator struct {
	waitErr error
}

func (f *failingOrchestrator) WaitForPolicy(_ context.Context, _ string, _ time.Duration) error {
	return f.waitErr
}

func (f *failingOrchestrator) EnsurePolicyInterfaces(_ context.Context, _ string, _ []string) error {
	return errors.New("should not be called if wait failed")
}

// F498: цель system: в domain.conf — имя ядра, а читается обратно она как
// system:-id. Старый файл с NDMS-id показывается тем же туннелем; цель
// managed-туннеля читается как раньше.
func TestHRRoundTrip_SystemTargetIsKernelName(t *testing.T) {
	resolver := &stubResolver{
		kernelByTunnel: map[string]string{"system:Wireguard0": "nwg1", "awg10": "nwg0"},
		systemByIface:  map[string]string{"nwg1": "system:Wireguard0", "Wireguard0": "system:Wireguard0"},
	}
	svc, hydra := newHRTestSvc(t, resolver)
	ctx := context.Background()

	created, err := svc.Create(ctx, DomainList{
		Name: "Sys", Backend: "hydraroute", ManualDomains: []string{"sys.com"},
		HRRouteMode: "interface", Routes: []RouteTarget{{TunnelID: "system:Wireguard0"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := created.Routes; len(got) != 1 || got[0].TunnelID != "system:Wireguard0" || got[0].Interface != "nwg1" {
		t.Errorf("ответ Create: routes = %+v", got)
	}
	_, _ = hydra.CreateRule(hydraroute.HRRule{Name: "Legacy", Domains: []string{"old.com"}, Target: "Wireguard0"})
	_, _ = hydra.CreateRule(hydraroute.HRRule{Name: "Managed", Domains: []string{"m.com"}, Target: "nwg0"})

	rules, _, _ := hydra.ListRules()
	for _, r := range rules {
		if r.Name == "Sys" && r.Target != "nwg1" {
			t.Errorf("в domain.conf ушло %q, ждали имя ядра nwg1", r.Target)
		}
	}

	lists, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Sys": "system:Wireguard0", "Legacy": "system:Wireguard0", "Managed": "nwg0"}
	for _, l := range lists {
		w, ok := want[l.Name]
		if !ok {
			continue
		}
		if len(l.Routes) != 1 || l.Routes[0].TunnelID != w {
			t.Errorf("%s: routes = %+v, ждали TunnelID %q", l.Name, l.Routes, w)
		}
		delete(want, l.Name)
	}
	if len(want) != 0 {
		t.Errorf("не прочитаны: %v", want)
	}
}

func newHRTestSvcWithNDMS(t *testing.T, resolver InterfaceResolver) (*ServiceImpl, *hydraroute.Service, *query.FakeGetter) {
	t.Helper()
	dir := t.TempDir()
	restore := hydraroute.SetPaths(
		filepath.Join(dir, "domain.conf"),
		filepath.Join(dir, "ip.list"),
	)
	t.Cleanup(restore)

	hydra := hydraroute.NewService(&kernelResolverAdapter{resolver: resolver}, nil)
	hydra.SetStatusForTest(true)

	store := NewStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}

	q, c, _, fg := newTestNDMS()
	hydra.SetQueries(q)
	svc := &ServiceImpl{
		store:    store,
		queries:  q,
		commands: c,
		resolver: resolver,
		hydra:    hydra,
	}
	return svc, hydra, fg
}

// Issue #967 regression test:
// When a ghost policy (e.g. "awgm0") is created on the router by hrneo at boot,
// rules bound to "awgm0" must NOT flip to policy mode. They must remain in interface mode.
func TestHRRuleToDomainList_GhostPolicyDoesNotFlipInterfaceMode(t *testing.T) {
	resolver := &stubResolver{
		kernelByTunnel: map[string]string{"awgm0": "awgm0", "awg10": "awgm0"},
	}
	svc, hydra, fg := newHRTestSvcWithNDMS(t, resolver)
	ctx := context.Background()

	// Seed /show/rc/ip/policy with both a ghost policy "awgm0" and a valid user policy "Streaming"
	fg.SetJSON("/show/rc/ip/policy", `{
		"awgm0": {"description": "awgm0"},
		"Streaming": {"description": "Media Policy"}
	}`)

	// 1. Direct rules created in domain.conf
	_, err := hydra.CreateRule(hydraroute.HRRule{
		Name:    "AwgRule",
		Domains: []string{"awg-target.com"},
		Target:  "awgm0",
	})
	if err != nil {
		t.Fatalf("CreateRule AwgRule: %v", err)
	}

	_, err = hydra.CreateRule(hydraroute.HRRule{
		Name:    "StreamRule",
		Domains: []string{"netflix.com"},
		Target:  "Streaming",
	})
	if err != nil {
		t.Fatalf("CreateRule StreamRule: %v", err)
	}

	// 2. List rules through dnsroute Service
	lists, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(lists) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(lists))
	}

	var awgList, streamList *DomainList
	for i := range lists {
		if lists[i].Name == "AwgRule" {
			awgList = &lists[i]
		} else if lists[i].Name == "StreamRule" {
			streamList = &lists[i]
		}
	}

	if awgList == nil || streamList == nil {
		t.Fatalf("missing expected rules: awgList=%v, streamList=%v", awgList, streamList)
	}

	// AwgRule must be in interface mode despite "awgm0" being in policySet!
	if awgList.HRRouteMode != "interface" {
		t.Errorf("AwgRule HRRouteMode = %q, want 'interface'", awgList.HRRouteMode)
	}
	if awgList.HRPolicyName != "" {
		t.Errorf("AwgRule HRPolicyName = %q, want empty", awgList.HRPolicyName)
	}
	if len(awgList.Routes) != 1 || awgList.Routes[0].Interface != "awgm0" {
		t.Errorf("AwgRule Routes = %+v, want [{Interface: awgm0}]", awgList.Routes)
	}

	// StreamRule must be in policy mode
	if streamList.HRRouteMode != "policy" {
		t.Errorf("StreamRule HRRouteMode = %q, want 'policy'", streamList.HRRouteMode)
	}
	if streamList.HRPolicyName != "Streaming" {
		t.Errorf("StreamRule HRPolicyName = %q, want 'Streaming'", streamList.HRPolicyName)
	}
	if len(streamList.Routes) != 0 {
		t.Errorf("StreamRule Routes = %+v, want empty", streamList.Routes)
	}

	// 3. Create a new rule with interface mode for awgm0
	created, err := svc.Create(ctx, DomainList{
		Name:          "NewAwgRule",
		Backend:       "hydraroute",
		ManualDomains: []string{"new-awg.com"},
		HRRouteMode:   "interface",
		Routes:        []RouteTarget{{TunnelID: "awgm0"}},
	})
	if err != nil {
		t.Fatalf("Create NewAwgRule: %v", err)
	}
	if created.HRRouteMode != "interface" {
		t.Errorf("created rule HRRouteMode = %q, want 'interface'", created.HRRouteMode)
	}
	if len(created.Routes) != 1 || created.Routes[0].Interface != "awgm0" {
		t.Errorf("created rule Routes = %+v, want Interface: awgm0", created.Routes)
	}

	// 4. Update the rule with interface mode for awgm0
	updated, err := svc.Update(ctx, DomainList{
		ID:            created.ID,
		Name:          "NewAwgRule",
		Backend:       "hydraroute",
		ManualDomains: []string{"updated-awg.com"},
		HRRouteMode:   "interface",
		Routes:        []RouteTarget{{TunnelID: "awgm0"}},
	})
	if err != nil {
		t.Fatalf("Update NewAwgRule: %v", err)
	}
	if updated.HRRouteMode != "interface" {
		t.Errorf("updated rule HRRouteMode = %q, want 'interface'", updated.HRRouteMode)
	}
	if len(updated.Routes) != 1 || updated.Routes[0].Interface != "awgm0" {
		t.Errorf("updated rule Routes = %+v, want Interface: awgm0", updated.Routes)
	}

	// 5. Attempting to create a policy-mode rule using the ghost policy name "awgm0" must be rejected
	_, err = svc.Create(ctx, DomainList{
		Name:          "InvalidGhostPolicyRule",
		Backend:       "hydraroute",
		ManualDomains: []string{"invalid.com"},
		HRRouteMode:   "policy",
		HRPolicyName:  "awgm0",
	})
	if err == nil {
		t.Fatal("expected error creating policy-mode rule with ghost policy name 'awgm0', got nil")
	}
}

func TestHRRuleToDomainList_TargetClassification(t *testing.T) {
	// Various router interfaces and policies in policySet
	policySet := map[string]bool{
		"awgm0":      true, // ghost policy
		"nwg0":       true, // ghost policy
		"nwg1":       true, // ghost policy for system tunnel interface
		"opkgtun10":  true, // ghost policy
		"Wireguard0": true, // ghost policy
		"PPPoE0":     true, // ghost policy
		"OpenVPN0":   true, // ghost policy
		"Policy0":    true, // system policy
		"Streaming":  true, // genuine user policy
		"Work":       true, // genuine user policy
	}
	systemByIface := map[string]string{
		"nwg1": "system:Wireguard0",
	}

	cases := []struct {
		target     string
		wantMode   string
		wantPol    string
		wantIface  string
		wantTunnel string
	}{
		{target: "awgm0", wantMode: "interface", wantIface: "awgm0", wantTunnel: "awgm0"},
		{target: "nwg0", wantMode: "interface", wantIface: "nwg0", wantTunnel: "nwg0"},
		{target: "nwg1", wantMode: "interface", wantIface: "nwg1", wantTunnel: "system:Wireguard0"},
		{target: "opkgtun10", wantMode: "interface", wantIface: "opkgtun10", wantTunnel: "opkgtun10"},
		{target: "Wireguard0", wantMode: "interface", wantIface: "Wireguard0", wantTunnel: "Wireguard0"},
		{target: "PPPoE0", wantMode: "interface", wantIface: "PPPoE0", wantTunnel: "PPPoE0"},
		{target: "OpenVPN0", wantMode: "interface", wantIface: "OpenVPN0", wantTunnel: "OpenVPN0"},
		{target: "Policy0", wantMode: "interface", wantIface: "Policy0", wantTunnel: "Policy0"},
		{target: "wan:eth0", wantMode: "interface", wantIface: "wan:eth0", wantTunnel: "wan:eth0"},
		{target: "wan:PPPoE0", wantMode: "interface", wantIface: "wan:PPPoE0", wantTunnel: "wan:PPPoE0"},
		{target: "eth0.100", wantMode: "interface", wantIface: "eth0.100", wantTunnel: "eth0.100"},
		{target: "Streaming", wantMode: "policy", wantPol: "Streaming"},
		{target: "Work", wantMode: "policy", wantPol: "Work"},
		{target: "NonExistentPolicy", wantMode: "interface", wantIface: "NonExistentPolicy", wantTunnel: "NonExistentPolicy"},
		{target: "", wantMode: "interface", wantIface: "", wantTunnel: ""},
	}

	for _, tc := range cases {
		rule := hydraroute.HRRule{
			Name:    "rule_" + tc.target,
			Domains: []string{"example.com"},
			Target:  tc.target,
		}
		dl := hrRuleToDomainList(rule, policySet, systemByIface)
		if dl.HRRouteMode != tc.wantMode {
			t.Errorf("target %q: HRRouteMode = %q, want %q", tc.target, dl.HRRouteMode, tc.wantMode)
		}
		if dl.HRPolicyName != tc.wantPol {
			t.Errorf("target %q: HRPolicyName = %q, want %q", tc.target, dl.HRPolicyName, tc.wantPol)
		}
		if tc.wantMode == "interface" {
			if len(dl.Routes) != 1 {
				t.Fatalf("target %q: expected 1 route, got %d", tc.target, len(dl.Routes))
			}
			if dl.Routes[0].Interface != tc.wantIface {
				t.Errorf("target %q: Route Interface = %q, want %q", tc.target, dl.Routes[0].Interface, tc.wantIface)
			}
			if dl.Routes[0].TunnelID != tc.wantTunnel {
				t.Errorf("target %q: Route TunnelID = %q, want %q", tc.target, dl.Routes[0].TunnelID, tc.wantTunnel)
			}
		} else {
			if len(dl.Routes) != 0 {
				t.Errorf("target %q: policy mode expected empty Routes, got %+v", tc.target, dl.Routes)
			}
		}
	}

	// Nil map robustness: policySet or systemByIface being nil must not panic
	nilDl := hrRuleToDomainList(hydraroute.HRRule{Name: "nil_test", Target: "awgm0"}, nil, nil)
	if nilDl.HRRouteMode != "interface" || nilDl.Routes[0].Interface != "awgm0" || nilDl.Routes[0].TunnelID != "awgm0" {
		t.Errorf("nil maps returned unexpected domain list: %+v", nilDl)
	}
}

