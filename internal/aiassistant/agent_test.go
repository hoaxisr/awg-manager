package aiassistant

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseModelToolCall(t *testing.T) {
	call, ok := parseModelToolCall(`TOOL_CALL: {"name":"dns.inspect","arguments":{"domain":"ya.ru"}}`)
	if !ok || call.Name != "dns.inspect" || call.Arguments["domain"] != "ya.ru" {
		t.Fatalf("unexpected call: %+v, ok=%v", call, ok)
	}
}

type scriptedAgentModel struct {
	mu        sync.Mutex
	responses []string
	questions []string
}

type nativeAgentModel struct {
	mu      sync.Mutex
	turns   int
	history []ModelToolExchange
	legacy  int
}

type nativeProposalModel struct{ turns int }

func (m *nativeProposalModel) Analyze(context.Context, ModelConfig, string, []byte) (string, error) {
	return "legacy path must not be used", nil
}

func (m *nativeProposalModel) AnalyzeWithTools(context.Context, ModelConfig, string, []byte, []ToolDefinition, []ModelToolExchange, ...string) (ModelToolTurn, error) {
	m.turns++
	if m.turns == 1 {
		return ModelToolTurn{Calls: []ModelToolCall{{
			ID: "proposal-1", Name: "remediation.propose",
			Arguments: map[string]any{"action": "tunnel.restart", "target": "awg1"},
		}}}, nil
	}
	return ModelToolTurn{Text: "Туннель следует перезапустить после подтверждения."}, nil
}

func (m *nativeAgentModel) Analyze(_ context.Context, _ ModelConfig, _ string, _ []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.legacy++
	return "legacy path must not be used", nil
}

func (m *nativeAgentModel) AnalyzeWithTools(
	_ context.Context,
	_ ModelConfig,
	_ string,
	_ []byte,
	_ []ToolDefinition,
	history []ModelToolExchange,
	_ ...string,
) (ModelToolTurn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns++
	m.history = append([]ModelToolExchange(nil), history...)
	if m.turns == 1 {
		return ModelToolTurn{
			Calls:         []ModelToolCall{{ID: "call-1", Name: "dns.inspect", Arguments: map[string]any{"domain": "ya.ru"}}},
			ProviderState: []byte(`[{"type":"function_call","call_id":"call-1"}]`),
		}, nil
	}
	return ModelToolTurn{Text: "DNS проверен нативным инструментом."}, nil
}

func (m *scriptedAgentModel) Analyze(_ context.Context, _ ModelConfig, question string, _ []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.questions = append(m.questions, question)
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type agentToolStub struct{ calls []ToolCall }

func (t *agentToolStub) Plan(string) (Intent, []ToolCall) { return Intent{Kind: "chat"}, nil }
func (t *agentToolStub) Tools() []ToolDefinition {
	return []ToolDefinition{{Name: "dns.inspect", Title: "DNS", Description: "DNS test", InputSchema: map[string]any{"type": "object"}, ReadOnly: true, Risk: "none"}}
}

type proposalToolStub struct{ registry *ToolRegistry }

func (t *proposalToolStub) Plan(string) (Intent, []ToolCall) { return Intent{Kind: "chat"}, nil }
func (t *proposalToolStub) Tools() []ToolDefinition          { return t.registry.Tools() }
func (t *proposalToolStub) Execute(ctx context.Context, call ToolCall) ToolStep {
	return t.registry.Execute(ctx, call)
}
func (t *agentToolStub) Execute(_ context.Context, call ToolCall) ToolStep {
	t.calls = append(t.calls, call)
	return ToolStep{Name: call.Name, Title: "DNS", Status: "passed", Summary: "DNS отвечает", ReadOnly: true, StartedAt: time.Now()}
}

func TestServiceLetsModelChooseToolAndContinueConversation(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	model := &scriptedAgentModel{responses: []string{
		`TOOL_CALL: {"name":"dns.inspect","arguments":{"domain":"ya.ru"}}`,
		"DNS роутера отвечает; проблема не в разрешении имени.",
	}}
	tools := &agentToolStub{}
	svc := NewService(&countingRunner{})
	svc.SetModel(store, model)
	svc.SetTools(tools)
	if err := svc.Start("Посмотри состояние подключения"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status != "done" {
			time.Sleep(time.Millisecond)
			continue
		}
		if len(tools.calls) != 1 || tools.calls[0].Name != "dns.inspect" {
			t.Fatalf("tool calls = %+v", tools.calls)
		}
		if len(state.ToolSteps) != 1 || !strings.Contains(state.ModelAnswer, "DNS роутера отвечает") {
			t.Fatalf("unexpected state: %+v", state)
		}
		if len(model.questions) != 2 || !strings.Contains(model.questions[0], "Каталог инструментов") {
			t.Fatalf("agent prompt was not supplied: %+v", model.questions)
		}
		return
	}
	t.Fatal("agent conversation did not finish")
}

func TestServiceRunsNativeProviderToolLoop(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "google", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	model := &nativeAgentModel{}
	tools := &agentToolStub{}
	svc := NewService(&countingRunner{})
	svc.SetModel(store, model)
	svc.SetTools(tools)
	if err := svc.Start("Проверь DNS"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status != "done" {
			time.Sleep(time.Millisecond)
			continue
		}
		if model.legacy != 0 || model.turns != 2 {
			t.Fatalf("native turns=%d legacy calls=%d", model.turns, model.legacy)
		}
		if len(model.history) != 1 || len(model.history[0].Results) != 1 {
			t.Fatalf("native history = %+v", model.history)
		}
		result := model.history[0].Results[0]
		if result.ID != "call-1" || result.Name != "dns.inspect" || !strings.Contains(result.Output, `"status":"passed"`) {
			t.Fatalf("native tool result = %+v", result)
		}
		if len(tools.calls) != 1 || len(state.ToolSteps) != 1 || state.ModelAnswer != "DNS проверен нативным инструментом." {
			t.Fatalf("unexpected final state: %+v; calls=%+v", state, tools.calls)
		}
		return
	}
	t.Fatal("native agent conversation did not finish")
}

type eagerPlannerToolStub struct {
	agentToolStub
	planned bool
}

func (t *eagerPlannerToolStub) Plan(string) (Intent, []ToolCall) {
	t.planned = true
	return Intent{Kind: "diagnostics.general"}, []ToolCall{{Name: "engine.status"}}
}

func TestNativeAgentChoosesToolsBeforeLocalPlanner(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "google", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	model := &nativeAgentModel{}
	tools := &eagerPlannerToolStub{}
	svc := NewService(&countingRunner{})
	svc.SetModel(store, model)
	svc.SetTools(tools)
	if err := svc.Start("Проверь работу Mihomo"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status != "done" {
			time.Sleep(time.Millisecond)
			continue
		}
		if tools.planned {
			t.Fatal("local keyword planner ran before the native agent")
		}
		if state.Intent.Kind != "agent" {
			t.Fatalf("intent = %q, want agent", state.Intent.Kind)
		}
		if len(tools.calls) != 1 || tools.calls[0].Name != "dns.inspect" {
			t.Fatalf("model-selected calls = %+v", tools.calls)
		}
		return
	}
	t.Fatal("native agent did not finish")
}

func TestServiceKeepsStructuredRemediationProposalForConfirmation(t *testing.T) {
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "google", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	model := &nativeProposalModel{}
	svc := NewService(nil)
	svc.SetModel(store, model)
	svc.SetTools(&proposalToolStub{registry: NewToolRegistry()})
	if err := svc.Start("Что предлагаешь сделать?"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status != "done" {
			time.Sleep(time.Millisecond)
			continue
		}
		if state.Proposal == nil || state.Proposal.Action != "tunnel.restart" || state.Proposal.Target != "awg1" || state.Proposal.Status != "pending" {
			t.Fatalf("structured proposal was not preserved: %+v", state.Proposal)
		}
		if model.turns != 2 {
			t.Fatalf("native turns = %d", model.turns)
		}
		return
	}
	t.Fatal("structured proposal conversation did not finish")
}
