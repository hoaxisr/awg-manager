package aiassistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeActionExecutor struct {
	calls        atomic.Int32
	action       string
	target       string
	verification *ActionVerification
}

func (f *fakeActionExecutor) Apply(_ context.Context, action, target string) error {
	f.calls.Add(1)
	f.action = action
	f.target = target
	return nil
}

func (f *fakeActionExecutor) Verify(_ context.Context, action, target string) (*ActionVerification, error) {
	if f.verification != nil {
		return f.verification, nil
	}
	return &ActionVerification{Status: "passed", Summary: "all good"}, nil
}

func TestApplyActionDoesNotClaimSuccessWhenVerificationFails(t *testing.T) {
	svc := NewService(nil)
	exec := &fakeActionExecutor{verification: &ActionVerification{Status: "failed", Summary: "Mihomo is still stopped"}}
	svc.SetActions(exec)
	proposal := validatedRemediationProposal("mihomo.restart", "")
	svc.state.Proposal = proposal

	if err := svc.ApplyAction(proposal.ID); err != nil {
		t.Fatal(err)
	}
	state := svc.Status()
	if state.Proposal == nil || state.Proposal.Status != "verification_failed" {
		t.Fatalf("proposal = %+v", state.Proposal)
	}
	if len(state.Messages) == 0 || !strings.Contains(state.Messages[len(state.Messages)-1].Content, "проблема не устранена") {
		t.Fatalf("messages = %+v", state.Messages)
	}
}

type transactionalActionExecutor struct {
	fakeActionExecutor
	rolledBack bool
}

func (f *transactionalActionExecutor) Snapshot(context.Context, string, string) (*ActionSnapshot, error) {
	return &ActionSnapshot{Action: "routing.switch_engine", Value: "sing-box"}, nil
}

func (f *transactionalActionExecutor) Rollback(_ context.Context, snapshot ActionSnapshot) error {
	f.rolledBack = snapshot.Value == "sing-box"
	return nil
}

func TestApplyActionRollsBackFailedTransactionalVerification(t *testing.T) {
	svc := NewService(nil)
	exec := &transactionalActionExecutor{fakeActionExecutor: fakeActionExecutor{
		verification: &ActionVerification{Status: "failed", Summary: "Mihomo did not start"},
	}}
	svc.SetActions(exec)
	proposal := validatedRemediationProposal("routing.switch_engine", "mihomo")
	if proposal == nil {
		t.Fatal("proposal is nil")
	}
	svc.state.Proposal = proposal

	if err := svc.ApplyAction(proposal.ID); err != nil {
		t.Fatal(err)
	}
	state := svc.Status()
	if !exec.rolledBack || state.Proposal == nil || state.Proposal.Status != "rolled_back" || !state.Proposal.RolledBack {
		t.Fatalf("proposal=%+v rolledBack=%v", state.Proposal, exec.rolledBack)
	}
}

func TestSwitchEngineProposalAcceptsOnlyKnownEngines(t *testing.T) {
	if validatedRemediationProposal("routing.switch_engine", "mihomo") == nil {
		t.Fatal("mihomo target rejected")
	}
	if validatedRemediationProposal("routing.switch_engine", "sing-box") == nil {
		t.Fatal("sing-box target rejected")
	}
	if validatedRemediationProposal("routing.switch_engine", "other") != nil {
		t.Fatal("unknown engine accepted")
	}
}

func TestRoutingModeTransactionSnapshotsAndRestores(t *testing.T) {
	current := "tproxy"
	registry := NewActionRegistry(ActionHandlers{
		CurrentRoutingMode: func(context.Context) (string, error) { return current, nil },
		SwitchRoutingMode: func(_ context.Context, mode string) error {
			current = mode
			return nil
		},
	})
	snapshot, err := registry.Snapshot(context.Background(), "routing.switch_mode", "fakeip-tun")
	if err != nil || snapshot == nil || snapshot.Value != "tproxy" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if err := registry.Apply(context.Background(), "routing.switch_mode", "fakeip-tun"); err != nil {
		t.Fatal(err)
	}
	if current != "fakeip-tun" {
		t.Fatalf("current=%q", current)
	}
	if err := registry.Rollback(context.Background(), *snapshot); err != nil {
		t.Fatal(err)
	}
	if current != "tproxy" {
		t.Fatalf("rollback current=%q", current)
	}
}

func TestSwitchModeProposalAcceptsOnlyKnownModes(t *testing.T) {
	for _, mode := range []string{"off", "tproxy", "fakeip-tun", "policy-tun"} {
		if validatedRemediationProposal("routing.switch_mode", mode) == nil {
			t.Fatalf("mode %q rejected", mode)
		}
	}
	if validatedRemediationProposal("routing.switch_mode", "shell") != nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestSystemActionTargetsAreStrictlyValidated(t *testing.T) {
	for _, tc := range []struct{ action, target string }{
		{"service.restart", "S90demo"},
		{"service.start", "K50worker"},
		{"opkg.install", "curl"},
		{"opkg.upgrade", "ca-bundle"},
		{"opkg.remove", "demo_pkg"},
		{"opkg.update", ""},
	} {
		if validatedRemediationProposal(tc.action, tc.target) == nil {
			t.Fatalf("valid proposal rejected: %+v", tc)
		}
	}
	for _, tc := range []struct{ action, target string }{
		{"service.restart", "../../S90demo"},
		{"service.stop", "dropbear"},
		{"opkg.install", "curl; reboot"},
		{"opkg.update", "unexpected"},
	} {
		if validatedRemediationProposal(tc.action, tc.target) != nil {
			t.Fatalf("unsafe proposal accepted: %+v", tc)
		}
	}
}

func TestSystemMutationsAreNeverAutoFixed(t *testing.T) {
	for _, action := range []string{"service.start", "service.stop", "service.restart", "opkg.update", "opkg.install", "opkg.upgrade", "opkg.remove", "routing.switch_engine", "routing.switch_mode"} {
		if autoFixActionAllowed(action) {
			t.Fatalf("unsafe auto-fix action allowed: %s", action)
		}
	}
	if !autoFixActionAllowed("mihomo.reload") {
		t.Fatal("known reversible low-risk action should remain eligible for auto-fix")
	}
}

func TestRemediationForSingboxProxyPortRequiresConfirmation(t *testing.T) {
	proposal := remediationForFindings([]Finding{{Source: "singbox_proxy_port"}}, "")
	if proposal == nil || proposal.Action != "singbox.restart" || proposal.Status != "pending" || proposal.ID == "" {
		t.Fatalf("proposal = %+v", proposal)
	}
	exec := &fakeActionExecutor{}
	svc := NewService(nil)
	svc.SetActions(exec)
	svc.state.Proposal = proposal
	if err := svc.ApplyAction(proposal.ID); err != nil {
		t.Fatal(err)
	}
	if exec.calls.Load() != 1 || svc.Status().Proposal.Status != "applied" {
		t.Fatalf("state = %+v calls = %d", svc.Status().Proposal, exec.calls.Load())
	}
	if svc.Status().ReadOnly {
		t.Fatal("state must record that a confirmed mutation was attempted")
	}
	if svc.Status().Proposal.Verification == nil || svc.Status().Proposal.Verification.Status != "passed" {
		t.Fatalf("verification = %+v", svc.Status().Proposal.Verification)
	}
	if err := svc.ApplyAction(proposal.ID); err == nil {
		t.Fatal("proposal was applied twice")
	}
}

func TestRemediationFromModelAction(t *testing.T) {
	answer := "Я обнаружил, что туннель nwg0 потерял соединение.\nACTION: {\"action\": \"tunnel.restart\", \"target\": \"nwg0\", \"title\": \"Перезапустить nwg0\", \"description\": \"Перезапуск туннеля\", \"risk\": \"low\"}"
	proposal := remediationForFindings(nil, answer)
	if proposal == nil || proposal.Action != "tunnel.restart" || proposal.Target != "nwg0" {
		t.Fatalf("proposal = %+v", proposal)
	}
}

func TestRemediationFromModelUsesServerMetadata(t *testing.T) {
	answer := `ACTION: {"action":"mihomo.reload","title":"Безопасная проверка","description":"ничего не изменится","risk":"none"}`
	proposal := remediationForFindings(nil, answer)
	if proposal == nil {
		t.Fatal("proposal is nil")
	}
	if proposal.Title != "Перезагрузить конфигурацию Mihomo" || proposal.Risk != "low" {
		t.Fatalf("model metadata escaped allowlist: %+v", proposal)
	}
}

func TestRemediationFromModelRejectsUnknownOrUnsafeAction(t *testing.T) {
	for _, answer := range []string{
		`ACTION: {"action":"shell.run","target":"reboot"}`,
		`ACTION: {"action":"tunnel.restart","target":"nwg0; reboot"}`,
		`ACTION: {"action":"tunnel.restart"}`,
		`ACTION: {"action":"singbox.restart","target":"nwg0"}`,
	} {
		if proposal := remediationForFindings(nil, answer); proposal != nil {
			t.Fatalf("unsafe proposal accepted: %+v", proposal)
		}
	}
}

func TestRemediationFromStructuredToolStep(t *testing.T) {
	proposal := remediationFromToolSteps([]ToolStep{{
		Name: "remediation.propose", Status: "passed",
		Evidence: []string{`{"action":"subscription.update","target":"sub-123","risk":"none","title":"fake"}`},
	}})
	if proposal == nil || proposal.Action != "subscription.update" || proposal.Target != "sub-123" || proposal.Risk != "low" {
		t.Fatalf("proposal = %+v", proposal)
	}
}

func TestAutoFixAppliesAutomatically(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir + "/ai.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, AutoFix: true, Provider: "openai", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}

	exec := &fakeActionExecutor{}
	svc := NewService(nil)
	svc.SetModel(store, &fakeModel{})
	svc.SetActions(exec)

	// Simulate completeDiagnosis with auto-fixable finding
	svc.completeDiagnosis(Stats{}, []Finding{{Source: "tunnel_handshake_stale", Detail: "tunnel-test"}}, "Модель советует перезапуск", nil, "openai")

	if exec.calls.Load() != 1 || exec.action != "tunnel.restart" || exec.target != "tunnel-test" {
		t.Fatalf("exec = %+v calls=%d", exec, exec.calls.Load())
	}
	state := svc.Status()
	if state.Proposal == nil || state.Proposal.Status != "applied" || !state.Proposal.AutoApplied {
		t.Fatalf("state.Proposal = %+v", state.Proposal)
	}
}

func TestExpiredRemediationIsRejected(t *testing.T) {
	svc := NewService(nil)
	svc.SetActions(&fakeActionExecutor{})
	svc.state.Proposal = &RemediationProposal{ID: "old", Action: "singbox.restart", Status: "pending", CreatedAt: time.Now().Add(-11 * time.Minute)}
	if err := svc.ApplyAction("old"); err == nil || svc.Status().Proposal.Status != "expired" {
		t.Fatalf("error = %v proposal = %+v", err, svc.Status().Proposal)
	}
}

type retryLoopModel struct {
	mu        sync.Mutex
	turn      int
	questions []string
}

func (m *retryLoopModel) Analyze(context.Context, ModelConfig, string, []byte) (string, error) {
	return "legacy path", nil
}

func (m *retryLoopModel) AnalyzeWithTools(
	_ context.Context,
	_ ModelConfig,
	question string,
	_ []byte,
	_ []ToolDefinition,
	_ []ModelToolExchange,
	_ ...string,
) (ModelToolTurn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turn++
	m.questions = append(m.questions, question)
	switch m.turn {
	case 1:
		return ModelToolTurn{Calls: []ModelToolCall{{
			ID:   "call-1",
			Name: "remediation.propose",
			Arguments: map[string]any{
				"symptom":           "gateway offline",
				"diagnosis":         "tunnel failed",
				"remediationAction": "tunnel.restart",
				"remediationTarget": "awg1",
				"action":            "tunnel.restart",
				"target":            "awg1",
			},
		}}}, nil
	case 2:
		return ModelToolTurn{Text: "Предлагаю перезапустить туннель awg1."}, nil
	case 3:
		// Turn 3 (continuation after failure): propose alternative action
		return ModelToolTurn{Calls: []ModelToolCall{{
			ID:   "call-2",
			Name: "remediation.propose",
			Arguments: map[string]any{
				"symptom":           "gateway offline",
				"diagnosis":         "core stuck",
				"remediationAction": "mihomo.reload",
				"action":            "mihomo.reload",
			},
		}}}, nil
	default:
		return ModelToolTurn{Text: "Предлагаю перезагрузить конфигурацию Mihomo."}, nil
	}
}

type mockSnapshotManager struct {
	taken    atomic.Int32
	restored atomic.Int32
}

func (m *mockSnapshotManager) TakeSnapshot(_ context.Context) (*SystemSnapshot, error) {
	m.taken.Add(1)
	return &SystemSnapshot{ID: "test-snap", IPTables: "*filter\nCOMMIT", CreatedAt: time.Now()}, nil
}

func (m *mockSnapshotManager) RestoreSnapshot(_ context.Context, snap *SystemSnapshot) error {
	m.restored.Add(1)
	return nil
}

type failingActionExecutor struct {
	applyCalls atomic.Int32
	failAction string
	rolledBack atomic.Bool
}

func (f *failingActionExecutor) Apply(_ context.Context, action, target string) error {
	f.applyCalls.Add(1)
	if f.failAction == "" || f.failAction == action {
		return errors.New("simulated apply failure: command exited with code 1")
	}
	return nil
}

func (f *failingActionExecutor) Verify(_ context.Context, action, target string) (*ActionVerification, error) {
	return &ActionVerification{Status: "passed", Summary: "ok"}, nil
}

func (f *failingActionExecutor) Snapshot(context.Context, string, string) (*ActionSnapshot, error) {
	return &ActionSnapshot{Action: "tunnel.restart", Target: "awg1", Value: "stopped"}, nil
}

func (f *failingActionExecutor) Rollback(_ context.Context, _ ActionSnapshot) error {
	f.rolledBack.Store(true)
	return nil
}

func TestApplyFailureTriggersAutonomousRetryLoop(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir + "/ai.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "google", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}

	model := &retryLoopModel{}
	svc := NewService(nil)
	svc.SetModel(store, model)
	svc.SetTools(&proposalToolStub{registry: NewToolRegistry()})

	exec := &failingActionExecutor{failAction: "tunnel.restart"}
	svc.SetActions(exec)

	snapMgr := &mockSnapshotManager{}
	svc.SetSnapshotManager(snapMgr)

	if err := svc.Start("У меня отвалился шлюз"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status == "done" && state.Proposal != nil && state.Proposal.Status == "pending" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	initialState := svc.Status()
	if initialState.Proposal == nil || initialState.Proposal.Action != "tunnel.restart" {
		t.Fatalf("expected initial proposal tunnel.restart, got: %+v", initialState.Proposal)
	}

	activeRun := svc.ActiveRun()
	if activeRun == nil || activeRun.Iteration != 1 {
		t.Fatalf("expected active run iteration 1, got: %+v", activeRun)
	}

	propID := initialState.Proposal.ID
	_ = svc.ApplyAction(propID)

	if !exec.rolledBack.Load() {
		t.Fatal("expected rollback to be called on apply failure")
	}
	if snapMgr.restored.Load() == 0 {
		t.Fatal("expected snapshot manager RestoreSnapshot to be called")
	}

	// Wait for continuation turn (turn 2) to complete and produce the new proposal (mihomo.reload)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status == "done" && state.Proposal != nil && state.Proposal.Status == "pending" && state.Proposal.Action == "mihomo.reload" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	updatedState := svc.Status()
	if updatedState.Proposal == nil || updatedState.Proposal.Action != "mihomo.reload" {
		t.Fatalf("expected new proposal mihomo.reload after retry loop, got: %+v", updatedState.Proposal)
	}

	updatedRun := svc.ActiveRun()
	if updatedRun == nil || updatedRun.Iteration != 2 {
		t.Fatalf("expected active run iteration 2, got: %+v", updatedRun)
	}
	if len(updatedRun.Journal) == 0 {
		t.Fatal("expected journal entries in active run")
	}
}

func TestRetryLoopRespectsMaxIterations(t *testing.T) {
	run := NewAgentRun("critical failure")
	run.MaxIter = 2
	run.Iteration = 2

	if run.CanRetry() {
		t.Fatal("expected CanRetry to return false when Iteration == MaxIter")
	}
}

func TestStaleProposalRejected(t *testing.T) {
	svc := NewService(nil)
	svc.SetActions(&fakeActionExecutor{})

	run := NewAgentRun("stale test")
	svc.SetActiveRun(run)

	// Case 1: Mismatched RunID
	svc.state.Proposal = &RemediationProposal{
		ID:        "prop_other",
		RunID:     "different_run_id",
		Action:    "tunnel.restart",
		Target:    "awg1",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	err := svc.ApplyProposal(context.Background(), run.ID, "prop_other")
	if err == nil || !strings.Contains(err.Error(), "inactive or expired run") {
		t.Fatalf("expected inactive or expired run error, got: %v", err)
	}

	// Case 2: Expired TTL
	svc.state.Proposal = &RemediationProposal{
		ID:        "prop_expired",
		RunID:     run.ID,
		Action:    "tunnel.restart",
		Target:    "awg1",
		Status:    "pending",
		CreatedAt: time.Now().Add(-15 * time.Minute),
	}
	err = svc.ApplyProposal(context.Background(), run.ID, "prop_expired")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got: %v", err)
	}

	// Case 3: Already applied status
	svc.state.Proposal = &RemediationProposal{
		ID:        "prop_already_applied",
		RunID:     run.ID,
		Action:    "tunnel.restart",
		Target:    "awg1",
		Status:    "applied",
		CreatedAt: time.Now(),
	}
	err = svc.ApplyProposal(context.Background(), run.ID, "prop_already_applied")
	if err == nil || !strings.Contains(err.Error(), "unavailable or expired") {
		t.Fatalf("expected unavailable or expired error for non-pending proposal, got: %v", err)
	}
}

type verificationFailingExecutor struct {
	rolledBack atomic.Bool
}

func (v *verificationFailingExecutor) Apply(context.Context, string, string) error {
	return nil
}

func (v *verificationFailingExecutor) Verify(context.Context, string, string) (*ActionVerification, error) {
	return &ActionVerification{Status: "failed", Summary: "service did not respond"}, nil
}

func (v *verificationFailingExecutor) Snapshot(context.Context, string, string) (*ActionSnapshot, error) {
	return &ActionSnapshot{Action: "service.restart", Target: "S99singbox", Value: "stopped"}, nil
}

func (v *verificationFailingExecutor) Rollback(context.Context, ActionSnapshot) error {
	v.rolledBack.Store(true)
	return nil
}

func TestRollbackOnVerificationFailureWithRetry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewConfigStore(dir + "/ai.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "google", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}

	model := &retryLoopModel{}
	svc := NewService(nil)
	svc.SetModel(store, model)
	svc.SetTools(&proposalToolStub{registry: NewToolRegistry()})

	exec := &verificationFailingExecutor{}
	svc.SetActions(exec)

	run := NewAgentRun("verification fail test")
	svc.SetActiveRun(run)

	prop := &RemediationProposal{
		ID:        "prop_vf",
		RunID:     run.ID,
		Action:    "service.restart",
		Target:    "S99singbox",
		Title:     "Restart singbox service",
		Risk:      "medium",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	svc.state.Proposal = prop

	_ = svc.ApplyProposal(context.Background(), run.ID, "prop_vf")

	if !exec.rolledBack.Load() {
		t.Fatal("expected rollback to be called on verification failure")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.ActiveRun() != nil && svc.ActiveRun().Iteration == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if svc.ActiveRun().Iteration != 2 {
		t.Fatalf("expected active run iteration 2 after verification failure, got: %d", svc.ActiveRun().Iteration)
	}
}

func TestKeeneticNdmcProposalValidation(t *testing.T) {
	// Valid commands
	validCmds := []string{
		"interface SSTP0 security-level private",
		"ip route 192.168.90.0 255.255.255.0 172.16.3.53 auto",
		"no ip access-list _WEBADMIN_SSTP0",
	}
	for _, cmd := range validCmds {
		p := validatedRemediationProposal("keenetic.ndmc", cmd)
		if p == nil {
			t.Fatalf("expected proposal for %q, got nil", cmd)
		}
		if p.Action != "keenetic.ndmc" || p.Target != cmd {
			t.Fatalf("unexpected proposal fields: %+v", p)
		}
		if p.Risk != "medium" {
			t.Fatalf("expected medium risk, got %s", p.Risk)
		}
	}

	// Banned / destructive commands
	bannedCmds := []string{
		"system default-config",
		"format storage",
		"erase nvram",
		"system reboot",
		"cleanup all",
		"",
	}
	for _, cmd := range bannedCmds {
		p := validatedRemediationProposal("keenetic.ndmc", cmd)
		if p != nil {
			t.Fatalf("expected banned command %q to return nil proposal, got %+v", cmd, p)
		}
	}
}

func TestKeeneticNdmcActionApplyAndVerify(t *testing.T) {
	var executedCmd string
	handlers := ActionHandlers{
		ExecKeenetic: func(ctx context.Context, cmd string) error {
			executedCmd = cmd
			return nil
		},
	}
	registry := NewActionRegistry(handlers)
	cmd := "interface SSTP0 security-level private"

	err := registry.Apply(context.Background(), "keenetic.ndmc", cmd)
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	if executedCmd != cmd {
		t.Fatalf("expected command %q, got %q", cmd, executedCmd)
	}

	ver, err := registry.Verify(context.Background(), "keenetic.ndmc", cmd)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if ver == nil || ver.Status != "passed" {
		t.Fatalf("expected passed verification, got %+v", ver)
	}
}
