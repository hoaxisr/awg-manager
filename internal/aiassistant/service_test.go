package aiassistant

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/diagnostics"
)

type fakeModel struct {
	diagnostic []byte
	question   string
}

func (f *fakeModel) Analyze(_ context.Context, _ ModelConfig, question string, diagnostic []byte) (string, error) {
	f.diagnostic = append([]byte(nil), diagnostic...)
	f.question = question
	return "Ответ модели", nil
}

func TestServiceChatDoesNotRunDiagnostics(t *testing.T) {
	runner := &countingRunner{}
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	svc := NewService(runner)
	svc.SetTools(NewToolRegistry())
	svc.SetModel(store, model)
	if err := svc.Start("Привет!"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status == "done" {
			if runner.called.Load() {
				t.Fatal("chat unexpectedly ran diagnostics")
			}
			if state.Intent.Kind != "chat" || len(state.Messages) != 2 {
				t.Fatalf("state = %+v", state)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("chat did not finish")
}

type targetedToolStub struct{}

func (targetedToolStub) Plan(string) (Intent, []ToolCall) {
	return Intent{Kind: "domain.inspect", Entity: map[string]string{"domain": "ya.ru"}}, []ToolCall{{Name: "domain.inspect"}}
}

func (targetedToolStub) Execute(context.Context, ToolCall) ToolStep {
	return ToolStep{Name: "domain.inspect", Title: "Домен", Status: "passed", Summary: "Маршрут найден", ReadOnly: true}
}

type countingRunner struct{ called atomic.Bool }

func (r *countingRunner) RunWithStream(context.Context, diagnostics.RunOptions) (<-chan diagnostics.DiagEvent, error) {
	r.called.Store(true)
	ch := make(chan diagnostics.DiagEvent)
	close(ch)
	return ch, nil
}

func (*countingRunner) Result() ([]byte, error) { return nil, nil }

func TestServiceTargetedIntentSkipsUnrelatedFullDiagnostics(t *testing.T) {
	runner := &countingRunner{}
	svc := NewService(runner)
	svc.SetTools(targetedToolStub{})
	if err := svc.Start("Проверь маршрут ya.ru"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status == "done" {
			if runner.called.Load() {
				t.Fatal("targeted request unexpectedly ran the full diagnostics suite")
			}
			if state.Intent.Kind != "domain.inspect" || len(state.ToolSteps) != 1 || state.Stats.Passed != 1 {
				t.Fatalf("unexpected targeted state: %+v", state)
			}
			if state.Engine != "typed-tools" || !strings.Contains(state.ModelAnswer, "Маршрут найден") {
				t.Fatalf("targeted answer must come from trusted tool evidence: %+v", state)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("targeted diagnostics did not finish")
}

type fakeRunner struct {
	report diagnostics.Report
}

func (f *fakeRunner) RunWithStream(context.Context, diagnostics.RunOptions) (<-chan diagnostics.DiagEvent, error) {
	ch := make(chan diagnostics.DiagEvent, 2)
	ch <- diagnostics.DiagEvent{Type: "phase", Label: "Проверка маршрутов…"}
	ch <- diagnostics.DiagEvent{Type: "done"}
	close(ch)
	return ch, nil
}

func (f *fakeRunner) Result() ([]byte, error) { return json.Marshal(f.report) }

func TestServiceAnalyzesSanitizedDiagnostics(t *testing.T) {
	svc := NewService(&fakeRunner{report: diagnostics.Report{Tests: []diagnostics.TestResult{
		{Name: "wan_connectivity", Description: "WAN", Status: diagnostics.StatusFail, Detail: "Нет default route"},
		{Name: "ndms_health", Description: "NDMS", Status: diagnostics.StatusPass},
	}}})
	if err := svc.Start("Почему нет интернета?"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := svc.Status()
		if state.Status == "done" {
			if state.Stats.Failed != 1 || state.Stats.Passed != 1 {
				t.Fatalf("unexpected stats: %+v", state.Stats)
			}
			if len(state.Findings) != 1 || state.Findings[0].Severity != "critical" {
				t.Fatalf("unexpected findings: %+v", state.Findings)
			}
			if !state.ReadOnly || state.Engine != "local-diagnostics" {
				t.Fatalf("unexpected safety flags: %+v", state)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("diagnostics did not finish")
}

func TestServiceRejectsLongQuestion(t *testing.T) {
	svc := NewService(&fakeRunner{})
	question := make([]rune, 1001)
	for i := range question {
		question[i] = 'я'
	}
	if err := svc.Start(string(question)); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestServiceModelReceivesReducedPrivateSnapshot(t *testing.T) {
	runner := &fakeRunner{report: diagnostics.Report{
		System: diagnostics.SystemInfo{AppVersion: "test", Arch: "arm64"},
		Tests: []diagnostics.TestResult{{
			Name: "route_leak_check", Description: "Маршрут", Status: diagnostics.StatusFail,
			Detail: "via 192.168.1.1", TunnelID: "secret-id", TunnelName: "Домашний VPN",
		}},
		Tunnels: []diagnostics.TunnelInfo{{Name: "Личный сервер", Status: "running", Backend: "nativewg"}},
	}}
	store, err := NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ConfigUpdate{Enabled: true, Provider: "openai", Model: "test", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	model := &fakeModel{}
	svc := NewService(runner)
	svc.SetModel(store, model)
	if err := svc.Start("проверь"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if svc.Status().Status == "done" {
			raw := string(model.diagnostic)
			for _, secret := range []string{"192.168.1.1", "secret-id", "Домашний VPN", "Личный сервер"} {
				if strings.Contains(raw, secret) {
					t.Fatalf("model diagnostic leaks %q: %s", secret, raw)
				}
			}
			if svc.Status().ModelAnswer != "Ответ модели" {
				t.Fatalf("model answer missing: %+v", svc.Status())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("diagnostics did not finish")
}
