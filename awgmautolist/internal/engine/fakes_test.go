// Fakes for engine tests. Every dependency is deterministic and observable;
// probes can be held open to exercise pause, cancellation and stop races.
package engine

import (
	"context"
	"encoding/json"
	"log"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

// fakeCapture records lifecycle transitions; Start is counted so tests can
// assert fresh generations after pause/resume.
type fakeCapture struct {
	mu      sync.Mutex
	udpAll  bool
	emit    func(capture.Suspect)
	starts  int
	paused  bool
	stopped bool
	running bool
}

func (f *fakeCapture) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.paused = false
	f.stopped = false
	f.running = true
	return nil
}

func (f *fakeCapture) Pause() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paused = true
	f.running = false
	return nil
}

func (f *fakeCapture) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.paused = true
	f.running = false
	return nil
}

func (f *fakeCapture) Status() capture.CaptureStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return capture.CaptureStatus{Running: f.running}
}

func (f *fakeCapture) suspect(target model.Target, reason string) {
	f.mu.Lock()
	emit := f.emit
	f.mu.Unlock()
	emit(capture.Suspect{
		Target: target, Reason: reason, ClientIP: "192.168.1.50",
		ClientMAC: "aa:bb:cc:dd:ee:ff", FirstAt: time.Now(), LastAt: time.Now(),
	})
}

func (f *fakeCapture) captureStarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// hold waits until released or the context is cancelled. Each hold call adds
// one permit to the target's gate; probes block until the gate opens.
type holdGate struct {
	mu      sync.Mutex
	open    map[string]bool
	waiting map[string][]chan struct{}
}

func newHoldGate() *holdGate {
	return &holdGate{open: map[string]bool{}, waiting: map[string][]chan struct{}{}}
}

// hold closes the gate for key: probes on this key block until unhold.
func (g *holdGate) hold(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open[key] = false
}

func (g *holdGate) unhold(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open[key] = true
	for _, ch := range g.waiting[key] {
		close(ch)
	}
	g.waiting[key] = nil
}

// wait blocks while the gate for key is closed and ctx is alive. A key that
// was never held is open.
func (g *holdGate) wait(ctx context.Context, key string) {
	g.mu.Lock()
	open, held := g.open[key]
	if !held || open {
		g.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	g.waiting[key] = append(g.waiting[key], ch)
	g.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
} // fakeProbes returns canned verdicts; held targets block until released so
// tests can serialize in-flight checks against user actions. The direct and
// tunnel checks use separate gates: holdDirect blocks the parallel direct+WAN
// stage, holdTunnel blocks only the tunnel probe.
type fakeProbes struct {
	mu         sync.Mutex
	direct     map[string]model.Verdict
	tunnel     map[string]model.Verdict
	wan        model.Verdict
	wanNow     model.Verdict // verdict used only for the fresh pre-commit check
	directGate *holdGate
	tunnelGate *holdGate
}

func newFakeProbes() *fakeProbes {
	return &fakeProbes{
		direct: map[string]model.Verdict{}, tunnel: map[string]model.Verdict{},
		directGate: newHoldGate(), tunnelGate: newHoldGate(),
	}
}

func (f *fakeProbes) setDirect(key string, verdict model.Verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.direct[key] = verdict
}

func (f *fakeProbes) setTunnel(key string, verdict model.Verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tunnel[key] = verdict
}

func (f *fakeProbes) setWAN(verdict model.Verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wan = verdict
	f.wanNow = verdict
}

// setWANSplit sets the parallel WAN verdict and the fresh pre-commit verdict
// independently.
func (f *fakeProbes) setWANSplit(parallel, fresh model.Verdict) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wan = parallel
	f.wanNow = fresh
}

// hold blocks the direct+WAN stage for the key until unhold.
func (f *fakeProbes) hold(t *testing.T, key string) {
	t.Helper()
	f.directGate.hold(key)
}

// holdTunnel blocks only the tunnel check for the key until unhold.
func (f *fakeProbes) holdTunnel(t *testing.T, key string) {
	t.Helper()
	f.tunnelGate.hold(key)
}

func (f *fakeProbes) unhold(key string) { f.directGate.unhold(key) }
func (f *fakeProbes) unholdTunnel(key string) {
	f.directGate.unhold(key)
	f.tunnelGate.unhold(key)
}

func (f *fakeProbes) verdict(table map[string]model.Verdict, target model.Target, fallback model.Verdict) model.Verdict {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := table[target.CandidateKey()]; ok {
		return v
	}
	if v, ok := table[target.ProbeKey()]; ok {
		return v
	}
	return fallback
}

func (f *fakeProbes) Check(ctx context.Context, target model.Target, _ probe.Path) probe.Result {
	f.tunnelGate.wait(ctx, target.CandidateKey())
	return probe.Result{
		Verdict: f.verdict(f.tunnel, target, model.VerdictBlocked),
		Bytes:   128, Duration: 10 * time.Millisecond, Detail: "fake",
	}
}

func (f *fakeProbes) DirectAndWAN(ctx context.Context, target model.Target, _ string, _ probe.Path) probe.DirectAndWANResult {
	f.directGate.wait(ctx, target.CandidateKey())
	parallel := f.wan
	if parallel == "" {
		parallel = model.VerdictReachable
	}
	return probe.DirectAndWANResult{
		Direct: probe.Result{Verdict: f.verdict(f.direct, target, model.VerdictReachable), Bytes: 256, Duration: 20 * time.Millisecond, Detail: "fake"},
		WAN:    probe.Result{Verdict: parallel, Duration: 5 * time.Millisecond, Detail: "HTTP 200"},
	}
}

func (f *fakeProbes) CheckWAN(context.Context, string, probe.Path) probe.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	fresh := f.wanNow
	if fresh == "" {
		fresh = model.VerdictReachable
	}
	return probe.Result{Verdict: fresh, Duration: 5 * time.Millisecond, Detail: "HTTP 200"}
}

// fakeCoverage fails on demand and counts calls.
type fakeCoverage struct {
	mu       sync.Mutex
	err      error
	covered  bool
	excluded bool
	calls    int
}

func (f *fakeCoverage) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeCoverage) Check(context.Context, capture.Suspect, config.Config) (CoverageOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	outcome := CoverageOutcome{Evidence: model.CoverageEvidence{Covered: f.covered, Excluded: f.excluded, Detail: "fake coverage"}}
	if f.err != nil {
		return outcome, f.err
	}
	return outcome, nil
}

func (f *fakeCoverage) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCoverage) setCovered(covered bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.covered = covered
}

func (f *fakeCoverage) setExcluded(excluded bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.excluded = excluded
}

// fakeRoutes resolves to a fixed direct path.
type fakeRoutes struct {
	mu       sync.Mutex
	err      error
	path     awgm.DirectPath
	resolves int
}

func (f *fakeRoutes) Resolve(context.Context) (awgm.DirectPath, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolves++
	if f.err != nil {
		return awgm.DirectPath{}, f.err
	}
	return f.path, nil
}

// fakeWriter records adds/removes and can fail or duplicate.
type fakeWriter struct {
	mu             sync.Mutex
	existing       map[string]bool
	err            error
	stubbornRemove bool
	adds           []string
	removes        []string
}

func (f *fakeWriter) Add(_ context.Context, target model.Target, iface string) (WriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return WriteResult{}, f.err
	}
	key := iface + "|" + target.CandidateKey()
	f.adds = append(f.adds, key)
	if f.existing[key] {
		return WriteResult{Outbound: iface, Persisted: false}, nil
	}
	f.existing[key] = true
	return WriteResult{Outbound: iface, Persisted: true, NewEntry: true}, nil
}

func (f *fakeWriter) Remove(_ context.Context, target model.Target, iface string) (WriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := iface + "|" + target.CandidateKey()
	f.removes = append(f.removes, key)
	// stubbornRemove simulates a rule the writer cannot remove: the entry
	// stays in existing so a later Add reports a duplicate.
	if !f.existing[key] || f.stubbornRemove {
		return WriteResult{Outbound: iface, Persisted: false}, nil
	}
	delete(f.existing, key)
	return WriteResult{Outbound: iface, Persisted: true}, nil
}

// testHarness wires fakes into a running engine with deterministic settings.
type testHarness struct {
	t        *testing.T
	engine   *Engine
	config   *config.Store
	state    *store.Store
	capture  *fakeCapture
	probes   *fakeProbes
	coverage *fakeCoverage
	routes   *fakeRoutes
	writer   *fakeWriter
}

func newHarness(t *testing.T, configure func(*config.Config)) *testHarness {
	t.Helper()
	cfgStore := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := cfgStore.Load(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if configure != nil {
		configure(&cfg)
	}
	// Tests that do not care about the tunnel get the fixed interface; tests
	// that pass an explicit TargetInterface (including empty) keep theirs.
	if configure == nil {
		cfg.TargetInterface = "awg0"
	}
	patch, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfgStore.Patch(patch); err != nil {
		t.Fatal(err)
	}
	st := store.New("")
	if err := st.Load(); err != nil {
		t.Fatal(err)
	}
	cap := &fakeCapture{}
	probes := newFakeProbes()
	coverage := &fakeCoverage{}
	routes := &fakeRoutes{path: awgm.DirectPath{Outbound: "direct"}}
	writer := &fakeWriter{existing: map[string]bool{}}
	engineDeps := Dependencies{
		Config:   cfgStore,
		State:    st,
		Coverage: coverage,
		Probes:   probes,
		Routes:   routes,
		Writer:   writer,
		CaptureFactory: func(udpAll bool, emit func(capture.Suspect)) Capture {
			cap.udpAll = udpAll
			cap.emit = emit
			return cap
		},
		Logger: log.New(&discardWriter{}, "", 0),
		DefaultTunnel: func(context.Context) (awgm.Tunnel, error) {
			return awgm.Tunnel{Interface: "awg0", Outbound: "awg0-out", Enabled: true}, nil
		},
	}
	eng := New(engineDeps)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	return &testHarness{
		t: t, engine: eng, config: cfgStore, state: st, capture: cap,
		probes: probes, coverage: coverage, routes: routes, writer: writer,
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (h *testHarness) tcpTarget(domain string) model.Target {
	h.t.Helper()
	target, err := model.NewObservedTarget(domain, "203.0.113.10", model.NetworkTCP, 443)
	if err != nil {
		h.t.Fatal(err)
	}
	return target
}

func (h *testHarness) ipTarget(ip string, port uint16) model.Target {
	h.t.Helper()
	// Engine IP-only fixtures exercise the still-supported UDP STUN/TURN path.
	// Unnamed TCP is rejected at admission by the domain-only TCP policy.
	target, err := model.NewObservedTarget("", ip, model.NetworkUDP, port)
	if err != nil {
		h.t.Fatal(err)
	}
	return target
}

func (h *testHarness) candidate(key string) (model.Candidate, bool) {
	h.t.Helper()
	candidate, ok := h.state.Snapshot(time.Now()).Candidates[key]
	return candidate, ok
}

func (h *testHarness) waitFor(fn func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	h.t.Fatal("condition not reached in 2s")
}

func (h *testHarness) captureStarts() int { return h.capture.captureStarts() }
