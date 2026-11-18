package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func setRoutingEngineInStore(t *testing.T, store *storage.SettingsStore, engine string) {
	t.Helper()
	if err := store.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.RoutingEngine = engine
		return nil
	}); err != nil {
		t.Fatalf("set RoutingEngine=%q: %v", engine, err)
	}
}

func assertNoDeadInterceptionIssue(t *testing.T, st Status) {
	t.Helper()
	for _, iss := range st.Issues {
		if iss.Kind == "engine-dead-interception" {
			t.Fatalf("unexpected engine-dead-interception issue while parked/firc: %+v", iss)
		}
	}
}

func TestExternalEngine_ParkAndResumeRoundTrip_AllModes(t *testing.T) {
	ctx := context.Background()

	t.Run("tproxy", func(t *testing.T) {
		h := newTransitionHarness(t)
		h.seedState(t, stateOff, false)
		if err := h.svc.SwitchRoutingMode(ctx, stateTProxy); err != nil {
			t.Fatalf("SwitchRoutingMode(tproxy): %v", err)
		}
		if !h.svc.routerSlotEnabled() {
			t.Fatal("expected SlotRouter enabled after tproxy switch")
		}
		cfgPath := filepath.Join(h.dir, "20-router.json")
		disabledCfgPath := filepath.Join(h.dir, "disabled", "20-router.json")
		beforeCfg, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read 20-router.json: %v", err)
		}

		if err := h.svc.ParkForExternalEngine(ctx); err != nil {
			t.Fatalf("ParkForExternalEngine(tproxy): %v", err)
		}
		setRoutingEngineInStore(t, h.store, "firc")
		// Simulate sing-box stopped while FIRC holds routing ownership.
		h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return false, 0 }
		if h.svc.routerSlotEnabled() {
			t.Fatal("expected SlotRouter disabled/parked while firc is active")
		}

		sr, err := h.svc.GetSettings(ctx)
		if err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
		if !sr.Enabled || sr.RoutingMode != stateTProxy {
			t.Fatalf("park must preserve Enabled=true & RoutingMode=tproxy, got %+v", sr)
		}
		parkedCfg, err := os.ReadFile(disabledCfgPath)
		if err != nil || len(parkedCfg) == 0 {
			t.Fatalf("park must preserve user router config in disabled/20-router.json; err=%v len=%d", err, len(parkedCfg))
		}

		st, err := h.svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus(parked): %v", err)
		}
		if st.RoutingEngine != "firc" || !st.ParkedByExternalEngine {
			t.Fatalf("expected RoutingEngine=firc & ParkedByExternalEngine=true, got %+v", st)
		}
		assertNoDeadInterceptionIssue(t, st)

		// Resume blocked while firc is active.
		if err := h.svc.ResumeFromExternalEngine(ctx); !errors.Is(err, ErrExternalRoutingEngineActive) {
			t.Fatalf("ResumeFromExternalEngine while firc active: got %v, want ErrExternalRoutingEngineActive", err)
		}

		// Return engine to sing-box and resume.
		h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return true, 1234 }
		setRoutingEngineInStore(t, h.store, "sing-box")
		if err := h.svc.ResumeFromExternalEngine(ctx); err != nil {
			t.Fatalf("ResumeFromExternalEngine(tproxy): %v", err)
		}
		resumedCfg, err := os.ReadFile(cfgPath)
		if err != nil || string(resumedCfg) != string(beforeCfg) {
			t.Fatalf("resume must restore 20-router.json cleanly; err=%v before=%q after=%q", err, beforeCfg, resumedCfg)
		}
		st, err = h.svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus(resumed): %v", err)
		}
		if st.ParkedByExternalEngine || !st.Enabled || !h.svc.routerSlotEnabled() {
			t.Fatalf("expected resumed tproxy status & enabled router slot, got status=%+v slotEnabled=%v",
				st, h.svc.routerSlotEnabled())
		}
	})

	t.Run("fakeip-tun", func(t *testing.T) {
		h := newTransitionHarness(t)
		if err := h.svc.SwitchRoutingMode(ctx, stateFakeIPTun); err != nil {
			t.Fatalf("SwitchRoutingMode(fakeip-tun): %v", err)
		}
		beforeSt, err := h.svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus(before fakeip park): %v", err)
		}
		if beforeSt.FakeIPIface == "" {
			t.Fatalf("expected non-empty FakeIPIface before park, got %+v", beforeSt)
		}

		if err := h.svc.ParkForExternalEngine(ctx); err != nil {
			t.Fatalf("ParkForExternalEngine(fakeip-tun): %v", err)
		}
		setRoutingEngineInStore(t, h.store, "firc")
		h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return false, 0 }

		sr, err := h.svc.GetSettings(ctx)
		if err != nil {
			t.Fatalf("GetSettings: %v", err)
		}
		if !sr.Enabled || sr.RoutingMode != stateFakeIPTun {
			t.Fatalf("park must preserve Enabled=true & RoutingMode=fakeip-tun, got %+v", sr)
		}
		st, err := h.svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus(parked fakeip): %v", err)
		}
		if st.RoutingEngine != "firc" || !st.ParkedByExternalEngine || st.FakeIPIface != "" {
			t.Fatalf("expected RoutingEngine=firc, ParkedByExternalEngine=true, empty FakeIPIface, got %+v", st)
		}
		assertNoDeadInterceptionIssue(t, st)

		h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return true, 1234 }
		setRoutingEngineInStore(t, h.store, "sing-box")
		if err := h.svc.ResumeFromExternalEngine(ctx); err != nil {
			t.Fatalf("ResumeFromExternalEngine(fakeip-tun): %v", err)
		}
		st, err = h.svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus(resumed fakeip): %v", err)
		}
		if st.ParkedByExternalEngine || !st.Enabled || st.FakeIPIface != "opkgtun0" {
			t.Fatalf("expected resumed fakeip-tun status with FakeIPIface=opkgtun0, got %+v", st)
		}
	})

	t.Run("policy-tun", func(t *testing.T) {
		h := newPolicyTunEnableHarness(t, "")
		if err := h.svc.enable(ctx); err != nil {
			t.Fatalf("enable(policy-tun): %v", err)
		}
		allBefore, err := h.store.Load()
		if err != nil || allBefore.OpkgTun == nil || !allBefore.OpkgTun.Provisioned {
			t.Fatalf("expected provisioned policy-tun OpkgTun before park, got %#v err=%v", allBefore.OpkgTun, err)
		}
		heldIndex := allBefore.OpkgTun.Index

		if err := h.svc.ParkForExternalEngine(ctx); err != nil {
			t.Fatalf("ParkForExternalEngine(policy-tun): %v", err)
		}
		setRoutingEngineInStore(t, h.store, "firc")
		h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return false, 0 }

		allParked, err := h.store.Load()
		if err != nil {
			t.Fatalf("Load(parked): %v", err)
		}
		if !allParked.SingboxRouter.Enabled {
			t.Fatal("park must preserve SingboxRouter.Enabled=true for policy-tun")
		}
		if allParked.OpkgTun == nil || allParked.OpkgTun.Mode != storage.OpkgTunModePolicyTun ||
			allParked.OpkgTun.Index != heldIndex || allParked.OpkgTun.Provisioned {
			t.Fatalf("park must hold same OpkgTun index (%d) with Provisioned=false, got %#v", heldIndex, allParked.OpkgTun)
		}

		st, err := h.svc.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus(parked policy-tun): %v", err)
		}
		if st.RoutingEngine != "firc" || !st.ParkedByExternalEngine {
			t.Fatalf("expected RoutingEngine=firc & ParkedByExternalEngine=true, got %+v", st)
		}
		assertNoDeadInterceptionIssue(t, st)

		h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return true, 1234 }
		setRoutingEngineInStore(t, h.store, "sing-box")
		if err := h.svc.ResumeFromExternalEngine(ctx); err != nil {
			t.Fatalf("ResumeFromExternalEngine(policy-tun): %v", err)
		}
		allResumed, err := h.store.Load()
		if err != nil {
			t.Fatalf("Load(resumed): %v", err)
		}
		if allResumed.OpkgTun == nil || allResumed.OpkgTun.Index != heldIndex || !allResumed.OpkgTun.Provisioned {
			t.Fatalf("expected resumed policy-tun to reuse held index %d and Provisioned=true, got %#v", heldIndex, allResumed.OpkgTun)
		}
	})
}

func TestExternalEngine_GuardsAllReconcileSyncAndMutationPathsWhileFircActive(t *testing.T) {
	ctx := context.Background()
	h := newTransitionHarness(t)
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, stateTProxy); err != nil {
		t.Fatalf("SwitchRoutingMode(tproxy): %v", err)
	}
	if err := h.svc.ParkForExternalEngine(ctx); err != nil {
		t.Fatalf("ParkForExternalEngine: %v", err)
	}
	setRoutingEngineInStore(t, h.store, "firc")

	recKeenDNS := &recordingKeenDNSSync{}
	h.svc.keenDNSSync = recKeenDNS
	syncCallsBefore := len(recKeenDNS.calls)

	var iptMutateCalls int
	ipt := h.svc.deps.IPTables
	ipt.restoreNoflush = func(context.Context, string) error { iptMutateCalls++; return nil }
	ipt.runIPTables = func(context.Context, ...string) error { iptMutateCalls++; return nil }
	ipt.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }
	ipt.runIP = func(context.Context, ...string) error { iptMutateCalls++; return nil }
	ipt.runIPOut = func(context.Context, ...string) (string, error) { return "", nil }

	// Reconcile and maintenance loops must perform zero iptables mutations or KeenDNS syncs.
	if err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile while firc active: %v", err)
	}
	if err := h.svc.ReapOrphanedFakeIPTun(ctx); err != nil {
		t.Fatalf("ReapOrphanedFakeIPTun while firc active: %v", err)
	}
	h.svc.SyncKeenDNSPreset(ctx)
	h.svc.syncKeenDNSPreset(ctx, keenDNSPresetSettings(true))
	sched := NewScheduler(h.svc, h.store)
	sched.tickPolicySync(ctx)

	if len(recKeenDNS.calls) != syncCallsBefore {
		t.Fatalf("expected 0 KeenDNS sync calls while firc active, got %d", len(recKeenDNS.calls)-syncCallsBefore)
	}
	if iptMutateCalls != 0 {
		t.Fatalf("expected 0 iptables mutations during guarded ticks, got %d", iptMutateCalls)
	}

	// SwitchRoutingMode rejects non-"off" targets with ErrExternalRoutingEngineActive.
	for _, target := range []string{stateTProxy, stateFakeIPTun, statePolicyTun} {
		if err := h.svc.SwitchRoutingMode(ctx, target); !errors.Is(err, ErrExternalRoutingEngineActive) {
			t.Fatalf("SwitchRoutingMode(%s) while firc active: got %v, want ErrExternalRoutingEngineActive", target, err)
		}
	}

	// ResumeFromExternalEngine also refuses while firc owns routing.
	if err := h.svc.ResumeFromExternalEngine(ctx); !errors.Is(err, ErrExternalRoutingEngineActive) {
		t.Fatalf("ResumeFromExternalEngine while firc active: got %v, want ErrExternalRoutingEngineActive", err)
	}

	// UpdateSettings must preserve RoutingEngine="firc" and avoid live router side effects.
	sr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	sr.RoutingEngine = "sing-box"
	sr.BypassPresets = []string{"keendns"}
	if err := h.svc.UpdateSettings(ctx, sr); err != nil {
		t.Fatalf("UpdateSettings while firc active: %v", err)
	}
	gotSr, err := h.svc.GetSettings(ctx)
	if err != nil {
		t.Fatalf("GetSettings after UpdateSettings: %v", err)
	}
	if gotSr.RoutingEngine != "firc" {
		t.Fatalf("UpdateSettings must preserve RoutingEngine=firc, got %q", gotSr.RoutingEngine)
	}
	if len(gotSr.BypassPresets) != 1 || gotSr.BypassPresets[0] != "keendns" {
		t.Fatalf("UpdateSettings did not persist allowed fields: %+v", gotSr)
	}
	if len(recKeenDNS.calls) != syncCallsBefore || iptMutateCalls != 0 {
		t.Fatalf("UpdateSettings while firc active triggered live side effects: dns=%d ipt=%d", len(recKeenDNS.calls), iptMutateCalls)
	}

	// healDetachedTun must no-op when RoutingEngine == "firc".
	healerSvc, healerSB, carrier, tick := healerHarness(t)
	setRoutingEngineInStore(t, healerSvc.deps.Settings, "firc")
	*carrier = false
	tick(lastHealTick * 2)
	if healerSB.reloadCalls != 0 {
		t.Fatalf("expected healer to stay idle while firc active, got reloadCalls=%d", healerSB.reloadCalls)
	}

	// SwitchRoutingMode("off") is explicitly allowed while firc is active (lets user disable sing-box intent).
	if err := h.svc.SwitchRoutingMode(ctx, stateOff); err != nil {
		t.Fatalf("SwitchRoutingMode(off) while firc active: %v", err)
	}
	gotSr, _ = h.svc.GetSettings(ctx)
	if gotSr.Enabled {
		t.Fatal("expected SwitchRoutingMode(off) to set Enabled=false")
	}
}

func TestExternalEngine_ResumeHonoursSingboxManuallyStopped(t *testing.T) {
	ctx := context.Background()
	h := newTransitionHarness(t)
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, stateTProxy); err != nil {
		t.Fatalf("SwitchRoutingMode(tproxy): %v", err)
	}
	if err := h.svc.ParkForExternalEngine(ctx); err != nil {
		t.Fatalf("ParkForExternalEngine: %v", err)
	}
	// Reflect uninstalled iptables chains/jumps after ParkForExternalEngine.
	h.svc.deps.IPTables.runIPTablesOut = func(context.Context, ...string) (string, error) { return "", nil }

	if err := h.store.Update(func(cur *storage.Settings) error {
		cur.SingboxManuallyStopped = true
		cur.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	h.svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return false, 0 }

	if err := h.svc.ResumeFromExternalEngine(ctx); err != nil {
		t.Fatalf("ResumeFromExternalEngine: %v", err)
	}
	st, err := h.svc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Installed || st.Active || h.svc.routerSlotEnabled() {
		t.Fatalf("ResumeFromExternalEngine must not reactivate router when SingboxManuallyStopped=true: status=%+v slotEnabled=%v",
			st, h.svc.routerSlotEnabled())
	}
	if st.ParkedByExternalEngine {
		t.Fatalf("did not expect ParkedByExternalEngine=true once engine returned to sing-box")
	}
}

func TestExternalEngine_ConcurrentReconcileAndParkResume_Race(t *testing.T) {
	ctx := context.Background()
	h := newTransitionHarness(t)
	h.seedState(t, stateOff, false)
	if err := h.svc.SwitchRoutingMode(ctx, stateTProxy); err != nil {
		t.Fatalf("SwitchRoutingMode(tproxy): %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(iter int) {
			defer wg.Done()
			if iter%2 == 0 {
				_ = h.svc.ParkForExternalEngine(ctx)
				_ = h.store.Update(func(cur *storage.Settings) error {
					cur.SingboxRouter.RoutingEngine = "firc"
					return nil
				})
				_ = h.svc.Reconcile(ctx)
			} else {
				_ = h.store.Update(func(cur *storage.Settings) error {
					cur.SingboxRouter.RoutingEngine = "sing-box"
					return nil
				})
				_ = h.svc.ResumeFromExternalEngine(ctx)
				_ = h.svc.Reconcile(ctx)
			}
		}(i)
	}
	wg.Wait()
}

type statefulPolicyDouble struct {
	*fakeAccessPolicyProvider
	policyName string
	permits    []string // ordered interface list currently permitted in policyName
}

func (p *statefulPolicyDouble) PermitInterface(ctx context.Context, name, iface string, order int) error {
	if name == p.policyName {
		filtered := make([]string, 0, len(p.permits)+1)
		for _, cur := range p.permits {
			if cur != iface {
				filtered = append(filtered, cur)
			}
		}
		if order <= 0 {
			p.permits = append([]string{iface}, filtered...)
		} else if order >= len(filtered) {
			p.permits = append(filtered, iface)
		} else {
			p.permits = append(filtered[:order], append([]string{iface}, filtered[order:]...)...)
		}
	}
	return p.fakeAccessPolicyProvider.PermitInterface(ctx, name, iface, order)
}

func (p *statefulPolicyDouble) DenyInterface(ctx context.Context, name, iface string) error {
	if name == p.policyName {
		kept := p.permits[:0]
		for _, cur := range p.permits {
			if cur != iface {
				kept = append(kept, cur)
			}
		}
		p.permits = kept
	}
	return p.fakeAccessPolicyProvider.DenyInterface(ctx, name, iface)
}

func (p *statefulPolicyDouble) Lines(context.Context) ([]string, error) {
	lines := []string{"ip policy " + p.policyName}
	for _, iface := range p.permits {
		lines = append(lines, "    permit global "+iface)
	}
	lines = append(lines, "!")
	return lines, nil
}

func (p *statefulPolicyDouble) Fetch(ctx context.Context) ([]string, error) {
	return p.Lines(ctx)
}

// B-R11g-1 regression test: enabling policy-tun denies WAN permit on Policy2,
// ParkForExternalEngine holds OpkgTun0 DOWN and restores WAN permit (PPPoE0)
// without touching unrelated non-WAN permits (Wireguard0), and
// ResumeFromExternalEngine re-denies the WAN permit on Policy2.
func TestParkAndResume_PolicyTunRestoresAndReDeniesPolicyWANWithoutClobberingNonWAN(t *testing.T) {
	ctx := context.Background()
	h := newTransitionHarness(t)
	h.seedState(t, stateOff, false)
	if err := h.store.Update(func(cur *storage.Settings) error {
		cur.SingboxRouter.DeviceMode = "policy"
		cur.SingboxRouter.PolicyName = "Policy2"
		cur.SingboxRouter.WANAutoDetect = true
		return nil
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	pol := &statefulPolicyDouble{
		fakeAccessPolicyProvider: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		policyName:               "Policy2",
		permits:                  []string{"PPPoE0", "Wireguard0"},
	}
	h.svc.deps.Policies = pol
	h.svc.deps.WANInterfaces = fakeWAN{list: policyWANs}
	h.svc.deps.RunningConfig = pol
	// Present netdev so holdOpkgTun actually takes the InterfaceDown + ClearAddress hold path.
	stubOrphanNetdev(t, true)

	if err := h.svc.SwitchRoutingMode(ctx, statePolicyTun); err != nil {
		t.Fatalf("SwitchRoutingMode(policy-tun): %v", err)
	}
	if want := []string{"OpkgTun0", "Wireguard0"}; !slices.Equal(pol.permits, want) {
		t.Fatalf("after Enable(policy-tun): permits=%v, want %v (WAN PPPoE0 denied, Wireguard0 kept)", pol.permits, want)
	}
	if !slices.Contains(pol.fakeAccessPolicyProvider.denies, "Policy2:PPPoE0") {
		t.Fatalf("expected DenyInterface(Policy2, PPPoE0) on enable, got %v", pol.fakeAccessPolicyProvider.denies)
	}

	if err := h.svc.ParkForExternalEngine(ctx); err != nil {
		t.Fatalf("ParkForExternalEngine: %v", err)
	}
	if all, _ := h.store.Load(); all == nil || all.OpkgTun == nil || all.OpkgTun.Provisioned || all.OpkgTun.Mode != storage.OpkgTunModePolicyTun {
		var st any
		if all != nil {
			st = all.OpkgTun
		}
		t.Fatalf("expected held policy-tun record (Provisioned=false) after park, got %+v", st)
	}
	if !slices.Contains(pol.permits, "PPPoE0") {
		t.Fatalf("after ParkForExternalEngine: Policy2 lacks WAN permit PPPoE0: %v", pol.permits)
	}
	if !slices.Contains(pol.permits, "Wireguard0") {
		t.Fatalf("after ParkForExternalEngine: Policy2 lost custom non-WAN permit Wireguard0: %v", pol.permits)
	}

	// Second idempotent park (when st.Provisioned is already false) must still preserve WAN permit.
	if err := h.svc.ParkForExternalEngine(ctx); err != nil {
		t.Fatalf("second ParkForExternalEngine: %v", err)
	}
	if !slices.Contains(pol.permits, "PPPoE0") || !slices.Contains(pol.permits, "Wireguard0") {
		t.Fatalf("after second ParkForExternalEngine: unexpected permits=%v", pol.permits)
	}

	// Resume returns policy-tun to active state and denies Policy2 WAN permit again.
	pol.fakeAccessPolicyProvider.denies = nil
	if err := h.svc.ResumeFromExternalEngine(ctx); err != nil {
		t.Fatalf("ResumeFromExternalEngine: %v", err)
	}
	if slices.Contains(pol.permits, "PPPoE0") {
		t.Fatalf("after ResumeFromExternalEngine: WAN PPPoE0 must be denied again, permits=%v", pol.permits)
	}
	if !slices.Contains(pol.permits, "Wireguard0") {
		t.Fatalf("after ResumeFromExternalEngine: Wireguard0 must stay permitted, permits=%v", pol.permits)
	}
	if !slices.Contains(pol.fakeAccessPolicyProvider.denies, "Policy2:PPPoE0") {
		t.Fatalf("expected DenyInterface(Policy2, PPPoE0) on resume, got %v", pol.fakeAccessPolicyProvider.denies)
	}
	if !strings.Contains(strings.Join(pol.fakeAccessPolicyProvider.permits, ","), "Policy2:PPPoE0:") {
		t.Fatalf("expected PermitInterface(Policy2, PPPoE0, ...) during park, got %v", pol.fakeAccessPolicyProvider.permits)
	}
}
