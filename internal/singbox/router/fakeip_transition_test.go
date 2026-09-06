package router

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ---------------------------------------------------------------------------
// Transition harness — a service that can Enable/Disable BOTH tproxy and
// fakeip-tun, with a subscribed events.Bus capturing every transition event.
//
// The fakeip path reuses the recording fakes from service_fakeip_test.go. The
// tproxy path is wired with stubbed IPTables + WAN collector + netfilter
// preflight, DeviceMode="all" (no policy needed). Enable success/failure for
// each mode is toggled via injectable seams (failTproxy / fakeip failAt).
// ---------------------------------------------------------------------------

type transitionHarness struct {
	svc   *ServiceImpl
	store *storage.SettingsStore
	dir   string
	bus   *events.Bus
	// captured transition events, in publish order.
	events []TransitionEvent
	// wanErr, when set, makes a tproxy Enable fail at WAN-IP collection.
	wan *fakeWANIPCollector
}

func newTransitionHarness(t *testing.T) *transitionHarness {
	t.Helper()
	svc, dir := newOrchedTestService(t)

	// Seed a router config so loadRouterConfig returns a usable egress for both
	// tproxy (inbound ensure) and fakeip (proxy outbound + final).
	routerCfg := `{"outbounds":[{"tag":"proxy-out","type":"socks","server":"1.2.3.4"},{"tag":"direct","type":"direct"}],"route":{"final":"proxy-out","rules":[]}}`
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(routerCfg), 0644); err != nil {
		t.Fatalf("write router cfg: %v", err)
	}

	singbox := newTestSingbox(t)
	singbox.dir = dir
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc.deps.Singbox = singbox

	// fakeip deps.
	log := &callLog{}
	svc.deps.OpkgTun = &recOpkgTun{log: log}
	svc.deps.StaticRoutes = &recStaticRoutes{log: log}
	svc.deps.OpkgTunIndices = &recIndices{live: map[int]bool{}}
	svc.deps.FakeIPTun = DefaultFakeIPTunParams()
	svc.deps.CacheDBPath = func() string { return filepath.Join(dir, "cache.db") }

	// policy-tun deps (общий с fakeip OpkgTun/индексы + парковка дефолта).
	svc.deps.DefaultRoute = &recDefaultRoute{log: log}

	// tproxy deps.
	wan := &fakeWANIPCollector{ips: []string{"203.0.113.1/32"}}
	svc.deps.WANIPCollector = wan
	svc.deps.IPTables = newStubIPTables(func(context.Context, string) error { return nil })
	svc.deps.Policies = &fakeAccessPolicyProvider{mark: "0xffffaaa"}
	svc.deps.NetfilterPreflight = func(context.Context) error { return nil }

	// Events bus + subscription. The bus delivers synchronously within Publish,
	// so draining after each call is enough — but we drain lazily in capture().
	bus := events.NewBus()
	svc.deps.Events = bus

	h := &transitionHarness{svc: svc, store: svc.deps.Settings, dir: dir, bus: bus, wan: wan}

	// Readiness seams: tproxy listening probe + fakeip tun/DNS probes all green.
	stubListeningProbe(t, func() bool { return true })
	stubTunReadyProbe(t, func(string) bool { return true })
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool { return true })
	stubLinkAbsent(t)
	oldFlush := fakeIPAddrFlush
	fakeIPAddrFlush = func(context.Context, string) error { return nil }
	t.Cleanup(func() { fakeIPAddrFlush = oldFlush })

	// Drain the fakeip drain-schedule synchronously so Disable tests don't leak
	// goroutines and the reject route gets removed deterministically.
	oldDrain := fakeIPScheduleDrain
	fakeIPScheduleDrain = func(removeReject func()) { removeReject() }
	t.Cleanup(func() { fakeIPScheduleDrain = oldDrain })

	return h
}

// subscribe starts capturing transition events. Must be called before the
// switch under test (the bus only delivers to active subscribers).
func (h *transitionHarness) subscribe(t *testing.T) func() {
	t.Helper()
	_, ch, unsub := h.bus.Subscribe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			if ev.Type != transitionEventType {
				continue
			}
			te, ok := ev.Data.(TransitionEvent)
			if !ok {
				continue
			}
			h.events = append(h.events, te)
		}
	}()
	return func() {
		unsub()
		<-done
	}
}

func (h *transitionHarness) seedState(t *testing.T, mode string, enabled bool) {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	all.SingboxRouter = storage.SingboxRouterSettings{
		RoutingMode:   mode,
		Enabled:       enabled,
		DeviceMode:    "all", // tproxy: no policy required
		WANAutoDetect: true,
		FakeIPPool6:   DefaultFakeIPTunParams().Inet6Range,
	}
	if err := h.store.Update(func(cur *storage.Settings) error { *cur = *all; return nil }); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func (h *transitionHarness) state(t *testing.T) (mode string, enabled bool) {
	t.Helper()
	all, err := h.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return all.SingboxRouter.RoutingMode, all.SingboxRouter.Enabled
}

// steps returns the ordered (step,status) pairs of captured transition events.
func (h *transitionHarness) steps() []string {
	out := make([]string, 0, len(h.events))
	for _, e := range h.events {
		out = append(out, e.Step.Step+":"+e.Step.Status)
	}
	return out
}

// terminal returns the last (Done) transition event, or false if none.
func (h *transitionHarness) terminal() (TransitionEvent, bool) {
	for i := len(h.events) - 1; i >= 0; i-- {
		if h.events[i].Done {
			return h.events[i], true
		}
	}
	return TransitionEvent{}, false
}

// ---------------------------------------------------------------------------
// Validation + no-op
// ---------------------------------------------------------------------------

func TestSwitch_InvalidTarget(t *testing.T) {
	h := newTransitionHarness(t)
	if err := h.svc.SwitchRoutingMode(context.Background(), "bogus"); err == nil {
		t.Fatal("expected error for invalid target")
	}
}

func TestSwitch_NoOpSameState(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateFakeIPTun, true)
	stop := h.subscribe(t)

	if err := h.svc.SwitchRoutingMode(context.Background(), stateFakeIPTun); err != nil {
		t.Fatalf("no-op switch: %v", err)
	}
	stop()

	if got := h.steps(); len(got) != 1 || got[0] != "ready:done" {
		t.Fatalf("no-op should emit a single ready:done event, got %v", got)
	}
	term, ok := h.terminal()
	if !ok || term.FinalState != stateFakeIPTun {
		t.Fatalf("terminal finalState = %q want fakeip-tun (ok=%v)", term.FinalState, ok)
	}
}

// ---------------------------------------------------------------------------
// Happy-path directions
// ---------------------------------------------------------------------------

func TestSwitch_OffToFakeIP(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateTProxy, false) // off (Enabled=false), mode irrelevant
	stop := h.subscribe(t)

	if err := h.svc.SwitchRoutingMode(context.Background(), stateFakeIPTun); err != nil {
		t.Fatalf("off→fakeip: %v", err)
	}
	stop()

	mode, enabled := h.state(t)
	if mode != stateFakeIPTun || !enabled {
		t.Fatalf("persisted = %q/%v want fakeip-tun/true", mode, enabled)
	}
	// No teardown (source=off): start → provision → readiness → ready.
	want := []string{"start:current", "provision:current", "provision:done", "readiness:done", "ready:done"}
	assertSteps(t, h.steps(), want)
	term, _ := h.terminal()
	if term.FinalState != stateFakeIPTun {
		t.Fatalf("finalState = %q want fakeip-tun", term.FinalState)
	}
}

func TestSwitch_TproxyToFakeIP(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateTProxy, true)
	stop := h.subscribe(t)

	if err := h.svc.SwitchRoutingMode(context.Background(), stateFakeIPTun); err != nil {
		t.Fatalf("tproxy→fakeip: %v", err)
	}
	stop()

	mode, enabled := h.state(t)
	if mode != stateFakeIPTun || !enabled {
		t.Fatalf("persisted = %q/%v want fakeip-tun/true", mode, enabled)
	}
	// Teardown tproxy THEN provision fakeip.
	want := []string{"start:current", "teardown:current", "teardown:done", "provision:current", "provision:done", "readiness:done", "ready:done"}
	assertSteps(t, h.steps(), want)
}

func TestSwitch_FakeIPToTproxy(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateFakeIPTun, true)
	// Provision fakeip first so the teardown has real persist to drain.
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("seed Enable(fakeip): %v", err)
	}
	stop := h.subscribe(t)

	if err := h.svc.SwitchRoutingMode(context.Background(), stateTProxy); err != nil {
		t.Fatalf("fakeip→tproxy: %v", err)
	}
	stop()

	mode, enabled := h.state(t)
	if mode != stateTProxy || !enabled {
		t.Fatalf("persisted = %q/%v want tproxy/true", mode, enabled)
	}
	want := []string{"start:current", "teardown:current", "teardown:done", "readiness:current", "provision:done", "readiness:done", "ready:done"}
	assertSteps(t, h.steps(), want)
}

func TestSwitch_FakeIPToOff(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateFakeIPTun, true)
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("seed Enable(fakeip): %v", err)
	}
	// fakeip persist must exist before teardown.
	if all, _ := h.store.Load(); all.OpkgTun == nil || !all.OpkgTun.Provisioned {
		t.Fatalf("expected provisioned fakeip persist before switch")
	}
	stop := h.subscribe(t)

	if err := h.svc.SwitchRoutingMode(context.Background(), stateOff); err != nil {
		t.Fatalf("fakeip→off: %v", err)
	}
	stop()

	_, enabled := h.state(t)
	if enabled {
		t.Fatalf("expected disabled after →off")
	}
	if all, _ := h.store.Load(); all.OpkgTun != nil {
		t.Fatalf("fakeip persist not cleared: %+v", all.OpkgTun)
	}
	want := []string{"start:current", "teardown:current", "teardown:done", "ready:done"}
	assertSteps(t, h.steps(), want)
	term, _ := h.terminal()
	if term.FinalState != stateOff {
		t.Fatalf("finalState = %q want off", term.FinalState)
	}
}

// ---------------------------------------------------------------------------
// Rollback directions
// ---------------------------------------------------------------------------

func TestSwitch_RollbackTproxyToFakeIP(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateTProxy, true)
	// Make the fakeip Enable fail at iface creation.
	h.svc.deps.OpkgTun = &recOpkgTun{log: &callLog{}, failAt: "Create"}
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), stateFakeIPTun)
	stop()
	if err == nil {
		t.Fatal("expected error from failed fakeip Enable")
	}

	// Rollback must restore tproxy.
	mode, enabled := h.state(t)
	if mode != stateTProxy || !enabled {
		t.Fatalf("after rollback persisted = %q/%v want tproxy/true", mode, enabled)
	}
	term, ok := h.terminal()
	if !ok || term.FinalState != stateTProxy {
		t.Fatalf("finalState = %q want tproxy", term.FinalState)
	}
	if !hasStep(h.steps(), "error:error") || !hasStep(h.steps(), "rollback:current") {
		t.Fatalf("expected error + rollback steps, got %v", h.steps())
	}
}

func TestSwitch_RollbackOffToFakeIP(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateTProxy, false) // off
	h.svc.deps.OpkgTun = &recOpkgTun{log: &callLog{}, failAt: "Create"}
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), stateFakeIPTun)
	stop()
	if err == nil {
		t.Fatal("expected error from failed fakeip Enable")
	}

	_, enabled := h.state(t)
	if enabled {
		t.Fatalf("off→fakeip rollback must leave disabled, enabled=%v", enabled)
	}
	term, _ := h.terminal()
	if term.FinalState != stateOff {
		t.Fatalf("finalState = %q want off", term.FinalState)
	}
}

func TestSwitch_RollbackFakeIPToTproxy(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateFakeIPTun, true)
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("seed Enable(fakeip): %v", err)
	}
	// Make the tproxy Enable fail (WAN collector error after teardown).
	h.wan.err = errContext("wan boom")
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), stateTProxy)
	stop()
	if err == nil {
		t.Fatal("expected error from failed tproxy Enable")
	}

	// Must NOT restore fakeip — must land OFF.
	_, enabled := h.state(t)
	if enabled {
		t.Fatalf("fakeip→tproxy rollback must leave disabled, enabled=%v", enabled)
	}
	term, _ := h.terminal()
	if term.FinalState != stateOff {
		t.Fatalf("finalState = %q want off", term.FinalState)
	}
	if term.Error == "" {
		t.Fatalf("expected explicit error message in terminal event")
	}
}

// ---------------------------------------------------------------------------
// policy-tun: цель перехода + rollback-матрица
// ---------------------------------------------------------------------------

func TestValidTransitionTarget_PolicyTun(t *testing.T) {
	if !validTransitionTarget(statePolicyTun) {
		t.Fatal("policy-tun must be a valid transition target")
	}
}

// singboxFailFirstEnable роняет ПЕРВЫЙ Enable на ClearManualStop. Сеанс отказа
// mode-agnostic (срабатывает в enableLocked до диспетчеризации по режиму), так
// что цель может быть любой, а rollback-Enable источника проходит нормально.
type singboxFailFirstEnable struct {
	*fakeSingbox
	failed bool
}

func (f *singboxFailFirstEnable) ClearManualStop() error {
	if !f.failed {
		f.failed = true
		return errContext("enable boom")
	}
	return f.fakeSingbox.ClearManualStop()
}

// failNextEnable подменяет Singbox обёрткой, роняющей ближайший Enable.
func (h *transitionHarness) failNextEnable(t *testing.T) {
	t.Helper()
	sb, ok := h.svc.deps.Singbox.(*fakeSingbox)
	if !ok {
		t.Fatalf("harness Singbox = %T want *fakeSingbox", h.svc.deps.Singbox)
	}
	h.svc.deps.Singbox = &singboxFailFirstEnable{fakeSingbox: sb}
}

func TestSwitch_RollbackTproxyToPolicyTun(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateTProxy, true)
	h.failNextEnable(t)
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), statePolicyTun)
	stop()
	if err == nil {
		t.Fatal("expected error from failed policy-tun Enable")
	}

	// Rollback must restore tproxy (slot-20 пара).
	mode, enabled := h.state(t)
	if mode != stateTProxy || !enabled {
		t.Fatalf("after rollback persisted = %q/%v want tproxy/true", mode, enabled)
	}
	term, ok := h.terminal()
	if !ok || term.FinalState != stateTProxy {
		t.Fatalf("finalState = %q want tproxy (ok=%v)", term.FinalState, ok)
	}
}

func TestSwitch_RollbackPolicyTunToTproxy(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, statePolicyTun, true)
	h.failNextEnable(t)
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), stateTProxy)
	stop()
	if err == nil {
		t.Fatal("expected error from failed tproxy Enable")
	}

	mode, enabled := h.state(t)
	if mode != statePolicyTun || !enabled {
		t.Fatalf("after rollback persisted = %q/%v want policy-tun/true", mode, enabled)
	}
	term, ok := h.terminal()
	if !ok || term.FinalState != statePolicyTun {
		t.Fatalf("finalState = %q want policy-tun (ok=%v)", term.FinalState, ok)
	}
}

func TestSwitch_RollbackPolicyTunToFakeIP(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, statePolicyTun, true)
	h.failNextEnable(t)
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), stateFakeIPTun)
	stop()
	if err == nil {
		t.Fatal("expected error from failed fakeip Enable")
	}

	// Ресурсы policy-tun уже освобождены teardown'ом → OFF, не восстановление.
	_, enabled := h.state(t)
	if enabled {
		t.Fatalf("policy-tun→fakeip rollback must leave disabled, enabled=%v", enabled)
	}
	term, _ := h.terminal()
	if term.FinalState != stateOff {
		t.Fatalf("finalState = %q want off", term.FinalState)
	}
	if !strings.Contains(term.Step.Message, "after policy-tun teardown") {
		t.Fatalf("terminal message = %q want explicit post-teardown reason", term.Step.Message)
	}
}

func TestSwitch_RollbackFakeIPToPolicyTun(t *testing.T) {
	h := newTransitionHarness(t)
	h.seedState(t, stateFakeIPTun, true)
	if err := h.svc.Enable(context.Background()); err != nil {
		t.Fatalf("seed Enable(fakeip): %v", err)
	}
	h.failNextEnable(t)
	stop := h.subscribe(t)

	err := h.svc.SwitchRoutingMode(context.Background(), statePolicyTun)
	stop()
	if err == nil {
		t.Fatal("expected error from failed policy-tun Enable")
	}

	_, enabled := h.state(t)
	if enabled {
		t.Fatalf("fakeip→policy-tun rollback must leave disabled, enabled=%v", enabled)
	}
	term, _ := h.terminal()
	if term.FinalState != stateOff {
		t.Fatalf("finalState = %q want off", term.FinalState)
	}
	if !strings.Contains(term.Step.Message, "after fakeip-tun teardown") {
		t.Fatalf("terminal message = %q want explicit post-teardown reason", term.Step.Message)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type errString string

func (e errString) Error() string { return string(e) }
func errContext(s string) error   { return errString(s) }

func assertSteps(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("step sequence:\n got=%v\nwant=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("step[%d] = %q want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

func hasStep(steps []string, want string) bool {
	for _, s := range steps {
		if s == want {
			return true
		}
	}
	return false
}
