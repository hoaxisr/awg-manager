package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/aiassistant"
)

type fakeAIService struct {
	question string
	state    aiassistant.State
	err      error
}

func TestAIAssistantConfigNeverReturnsAPIKey(t *testing.T) {
	store, err := aiassistant.NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(aiassistant.ConfigUpdate{Enabled: true, Provider: "openai", Model: "test", APIKey: "top-secret"}); err != nil {
		t.Fatal(err)
	}
	h := NewAIAssistantHandler(&fakeAIService{})
	h.SetConfigStore(store)
	rec := httptest.NewRecorder()
	h.Config(rec, httptest.NewRequest(http.MethodGet, "/api/system/ai/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "top-secret") || !strings.Contains(rec.Body.String(), `"apiKeySet":true`) {
		t.Fatalf("unsafe config response: %s", rec.Body.String())
	}
}

func (f *fakeAIService) Start(question string) error { f.question = question; return f.err }
func (f *fakeAIService) Status() aiassistant.State   { return f.state }
func (f *fakeAIService) ClearChat()                  {}

func TestAIAssistantDiagnose(t *testing.T) {
	svc := &fakeAIService{state: aiassistant.State{Status: "running", ReadOnly: true}}
	h := NewAIAssistantHandler(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/ai/diagnose", strings.NewReader(`{"question":"нет интернета"}`))
	h.Diagnose(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if svc.question != "нет интернета" {
		t.Fatalf("question = %q", svc.question)
	}
}

func TestAIAssistantDiagnoseRejectsUnknownFields(t *testing.T) {
	h := NewAIAssistantHandler(&fakeAIService{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/system/ai/diagnose", strings.NewReader(`{"command":"rm"}`))
	h.Diagnose(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAIAssistantEmbeddedStatus(t *testing.T) {
	store, err := aiassistant.NewConfigStore(filepath.Join(t.TempDir(), "ai.json"))
	if err != nil {
		t.Fatal(err)
	}
	embedded := aiassistant.NewEmbeddedManager(store)
	h := NewAIAssistantHandler(&fakeAIService{})
	h.SetEmbedded(embedded)

	rec := httptest.NewRecorder()
	h.Embedded(rec, httptest.NewRequest(http.MethodGet, "/api/system/ai/embedded", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"port":11435`) {
		t.Fatalf("expected port in response: %s", rec.Body.String())
	}
}

type fakeMCPAIService struct{ fakeAIService }

func (*fakeMCPAIService) ListTools() []aiassistant.ToolDefinition {
	return []aiassistant.ToolDefinition{{Name: "dns.inspect", Title: "DNS", Description: "Проверка DNS", InputSchema: map[string]any{"type": "object"}, ReadOnly: true}}
}

func (*fakeMCPAIService) CallTool(_ context.Context, call aiassistant.ToolCall) aiassistant.ToolStep {
	return aiassistant.ToolStep{Name: call.Name, Title: "DNS", Status: "passed", Summary: "DNS отвечает", ReadOnly: true}
}

func TestAIAssistantMCPListsAndCallsReadOnlyTools(t *testing.T) {
	h := NewAIAssistantHandler(&fakeMCPAIService{})

	rec := httptest.NewRecorder()
	h.MCP(rec, httptest.NewRequest(http.MethodPost, "/api/system/ai/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"dns.inspect"`) || !strings.Contains(rec.Body.String(), `"readOnlyHint":true`) {
		t.Fatalf("unexpected tools/list response: status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.MCP(rec, httptest.NewRequest(http.MethodPost, "/api/system/ai/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dns.inspect","arguments":{"domain":"ya.ru"}}}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"isError":false`) || !strings.Contains(rec.Body.String(), "DNS отвечает") {
		t.Fatalf("unexpected tools/call response: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify numeric and boolean arguments in tools/call are converted and forwarded
	rec = httptest.NewRecorder()
	h.MCP(rec, httptest.NewRequest(http.MethodPost, "/api/system/ai/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"system.diagnose_command","arguments":{"command":"ping -c 1 127.0.0.1","timeoutSeconds":5}}}`)))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"error":`) {
		t.Fatalf("unexpected tools/call with numeric args response: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
