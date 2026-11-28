// Lifecycle around the mandatory explicit tunnel (BACKLOG section 3): without
// a chosen targetInterface the engine stays idle and the status reports
// setupRequired for the wizard; choosing a tunnel starts the working engine,
// clearing it stops the engine without touching AWGM rules.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
)

// TestStartWithoutTunnelStaysIdleForWizard proves the idle contract of an
// unconfigured autolist: no capture, no checks, no writes, user actions
// rejected, and the status carries setupRequired with no stale tunnel.
func TestStartWithoutTunnelStaysIdleForWizard(t *testing.T) {
	h := newSetupHarness(t, nil)
	if h.engine.Running() {
		t.Fatal("engine running without a tunnel")
	}
	status := h.engine.Status()
	if status.Running || !status.SetupRequired {
		t.Fatalf("status=%+v, want idle with setupRequired", status)
	}
	if status.TargetInterface != "" || status.TargetOutbound != "" {
		t.Fatalf("stale tunnel displayed: %+v", status)
	}
	if got := h.captureStarts(); got != 0 {
		t.Fatalf("capture started %d times without a tunnel", got)
	}
	if got := h.coverage.callsCount(); got != 0 {
		t.Fatalf("coverage ran without a tunnel: %d", got)
	}
	if err := h.engine.Add(context.Background(), "domain|example.com|tcp|443"); !errors.Is(err, ErrStopped) {
		t.Fatalf("add on an idle engine: %v", err)
	}
	if err := h.engine.Recheck(context.Background(), "domain|example.com|tcp|443"); !errors.Is(err, ErrStopped) {
		t.Fatalf("recheck on an idle engine: %v", err)
	}
}

// TestPatchConfigEmptyToExplicitStartsEngine proves the wizard completion:
// saving the first tunnel launches the working engine and suspects flow
// through the whole pipeline again.
func TestPatchConfigEmptyToExplicitStartsEngine(t *testing.T) {
	h := newSetupHarness(t, nil)
	if _, err := h.engine.PatchConfig([]byte(`{"targetInterface":"awg0"}`)); err != nil {
		t.Fatal(err)
	}
	if !h.engine.Running() {
		t.Fatal("engine did not start after the wizard chose a tunnel")
	}
	if status := h.engine.Status(); status.SetupRequired {
		t.Fatalf("status=%+v, want setupRequired=false", status)
	}
	if got := h.captureStarts(); got == 0 {
		t.Fatal("capture did not start")
	}

	target := h.tcpTarget("wizard.example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
}

// TestPatchConfigExplicitToEmptyStopsEngineAndKeepsRules proves the stop
// contract: the engine halts, setupRequired is reported again, no refresh is
// ever called with an empty interface, no rule is removed, and the stored
// candidate survives untouched.
func TestPatchConfigExplicitToEmptyStopsEngineAndKeepsRules(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if err := h.engine.Add(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if len(h.writer.adds) != 1 {
		t.Fatalf("writer adds=%v", h.writer.adds)
	}
	starts := h.captureStarts()

	if _, err := h.engine.PatchConfig([]byte(`{"targetInterface":""}`)); err != nil {
		t.Fatal(err)
	}
	if h.engine.Running() {
		t.Fatal("engine still running with a cleared tunnel")
	}
	status := h.engine.Status()
	if !status.SetupRequired {
		t.Fatalf("status=%+v, want setupRequired", status)
	}
	if status.TargetInterface != "" {
		t.Fatalf("stale tunnel displayed after the stop: %q", status.TargetInterface)
	}
	if got := h.captureStarts(); got != starts {
		t.Fatalf("capture restarted on the stop: %d -> %d", starts, got)
	}
	if len(h.writer.refreshes) != 0 {
		t.Fatalf("refresh called with a cleared tunnel: %v", h.writer.refreshes)
	}
	if len(h.writer.removes) != 0 {
		t.Fatalf("rules removed on the setupRequired transition: %v", h.writer.removes)
	}
	candidate, ok := h.candidate(key)
	if !ok || candidate.State != model.CandidateAdded || candidate.TargetInterface != "awg0" {
		t.Fatalf("candidate changed: state=%s ok=%v iface=%q", candidate.State, ok, candidate.TargetInterface)
	}
	if saved := h.config.Config().TargetInterface; saved != "" {
		t.Fatalf("saved targetInterface=%q, want empty", saved)
	}

	// Idle again: no observation, no checks while setup is required.
	coverageCalls := h.coverage.callsCount()
	h.capture.suspect(target, "tcp-rst")
	if got := h.coverage.callsCount(); got != coverageCalls {
		t.Fatalf("coverage ran while setup is required: %d -> %d", coverageCalls, got)
	}

	// A second wizard round restarts the engine on the new tunnel without
	// re-pointing the untouched rules.
	if _, err := h.engine.PatchConfig([]byte(`{"targetInterface":"nwg0"}`)); err != nil {
		t.Fatal(err)
	}
	if !h.engine.Running() {
		t.Fatal("engine did not restart after a new tunnel choice")
	}
	if len(h.writer.refreshes) != 0 {
		t.Fatalf("empty->explicit must not refresh route objects: %v", h.writer.refreshes)
	}
	if got := h.captureStarts(); got <= starts {
		t.Fatalf("capture did not start on the wizard restart: %d -> %d", starts, got)
	}
}

// TestPatchConfigWhileSetupRequiredStartsNothing proves that operational
// patches before the wizard completes only persist configuration: no capture
// generation, no WAN monitor (the idle engine has no base context), the
// engine keeps waiting for the tunnel.
func TestPatchConfigWhileSetupRequiredStartsNothing(t *testing.T) {
	h := newSetupHarness(t, nil)
	if _, err := h.engine.PatchConfig([]byte(`{"udpEnabled":true,"wanMonitorEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if h.engine.Running() {
		t.Fatal("engine started by an operational patch without a tunnel")
	}
	status := h.engine.Status()
	if !status.SetupRequired || !status.UDPEnabled {
		t.Fatalf("status=%+v, want setupRequired with the persisted UDP flag", status)
	}
	if got := h.captureStarts(); got != 0 {
		t.Fatalf("capture started %d times without a tunnel", got)
	}
}

// TestPatchConfigExplicitSwitchKeepsEngineRunning proves that a direct A->B
// switch only re-points the route objects: the engine keeps running and the
// status never reports setupRequired.
func TestPatchConfigExplicitSwitchKeepsEngineRunning(t *testing.T) {
	h := newHarness(t, nil)
	if _, err := h.engine.PatchConfig([]byte(`{"targetInterface":"nwg0"}`)); err != nil {
		t.Fatal(err)
	}
	status := h.engine.Status()
	if !status.Running || status.SetupRequired {
		t.Fatalf("status=%+v, want running without setupRequired", status)
	}
	if len(h.writer.refreshes) != 1 || h.writer.refreshes[0] != "nwg0" {
		t.Fatalf("refreshes=%v, want [nwg0]", h.writer.refreshes)
	}
}

// TestSetupRequiredStatusJSON proves the setupRequired field is part of the
// operational status contract in both states.
func TestSetupRequiredStatusJSON(t *testing.T) {
	for name, want := range map[string]bool{"setup harness": true, "running harness": false} {
		t.Run(name, func(t *testing.T) {
			h := newSetupHarness(t, nil)
			if !want {
				if _, err := h.engine.PatchConfig([]byte(`{"targetInterface":"awg0"}`)); err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(h.engine.Status())
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			got, ok := fields["setupRequired"]
			if !ok {
				t.Fatalf("missing setupRequired in %s", data)
			}
			if got != want {
				t.Fatalf("setupRequired=%v, want %v (%s)", got, want, data)
			}
		})
	}
}

func TestProbeLogDetailIncludesFailureStageTimings(t *testing.T) {
	detail := probeLogDetail(probe.Result{
		Verdict: model.VerdictError, Detail: "boom",
		TimeDNS: time.Millisecond, TimeConnect: 2 * time.Millisecond, TimeTLS: 3 * time.Millisecond,
	})
	for _, want := range []string{"boom", "dns=1ms", "connect=2ms", "tls=3ms"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail=%q, missing %q", detail, want)
		}
	}
	if got := probeLogDetail(probe.Result{Verdict: model.VerdictReachable}); got != "" {
		t.Fatalf("reachable detail=%q", got)
	}
}
