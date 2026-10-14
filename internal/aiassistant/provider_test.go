package aiassistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/downloader"
)

func TestResponsesClientUsesStatelessReadOnlyPrompt(t *testing.T) {
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output_text":"Проверьте default route"}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	client.Endpoint = srv.URL + "/responses"
	answer, err := client.Analyze(context.Background(), ModelConfig{
		Enabled: true, Provider: "openai", Model: "test-model", APIKey: "test-secret",
	}, "нет интернета на 192.168.1.25, ключ sk-supersecret", []byte(`{"tests":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Проверьте default route" {
		t.Fatalf("answer = %q", answer)
	}
	if request["store"] != false {
		t.Fatalf("store = %#v, want false", request["store"])
	}
	if request["model"] != "test-model" {
		t.Fatalf("model = %#v", request["model"])
	}
	instructions, _ := request["instructions"].(string)
	if !strings.Contains(instructions, "недоверенные") {
		t.Fatalf("prompt injection guard is missing: %q", instructions)
	}
	input, _ := request["input"].(string)
	if strings.Contains(input, "192.168.1.25") || strings.Contains(input, "sk-supersecret") {
		t.Fatalf("question was not sanitized: %q", input)
	}
}

func TestResponsesClientDoesNotLeakProviderBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`api_key=test-secret raw private response`))
	}))
	defer srv.Close()
	client := NewResponsesClient()
	client.Endpoint = srv.URL
	_, err := client.Analyze(context.Background(), ModelConfig{Enabled: true, Model: "m", APIKey: "test-secret"}, "q", nil)
	if err == nil || err.Error() != "model returned HTTP 401: request failed" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResponsesClientChatCompletionsFormat(t *testing.T) {
	var request map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer deepseek-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Все в порядке"}}]}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	answer, err := client.Analyze(context.Background(), ModelConfig{
		Enabled:  true,
		Provider: "deepseek",
		BaseURL:  srv.URL + "/v1",
		Model:    "deepseek-chat",
		APIKey:   "deepseek-key",
	}, "вопрос", []byte(`{"tests":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Все в порядке" {
		t.Fatalf("answer = %q, want 'Все в порядке'", answer)
	}
}

func TestResponsesClientGoogleGeminiFormat(t *testing.T) {
	var request struct {
		SystemInstruction struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"system_instruction"`
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
		GenerationConfig struct {
			MaxOutputTokens int `json:"maxOutputTokens"`
			ThinkingConfig  any `json:"thinkingConfig"`
		} `json:"generationConfig"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "google-secret" {
			t.Errorf("x-goog-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected authorization header = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Проверьте DNS"},{"text":"и маршрут."}]}}]}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	client.Endpoint = srv.URL + "/v1beta/models/gemini-test:generateContent"
	answer, err := client.Analyze(context.Background(), ModelConfig{
		Enabled: true, Provider: "google", Model: "gemini-test", APIKey: "google-secret",
	}, "почему нет интернета", []byte(`{"tests":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Проверьте DNS\nи маршрут." {
		t.Fatalf("answer = %q", answer)
	}
	if len(request.SystemInstruction.Parts) != 1 || !strings.Contains(request.SystemInstruction.Parts[0].Text, "недоверенные") {
		t.Fatalf("system instruction missing: %+v", request.SystemInstruction)
	}
	if len(request.Contents) != 1 || request.Contents[0].Role != "user" {
		t.Fatalf("contents = %+v", request.Contents)
	}
	if request.GenerationConfig.MaxOutputTokens != 3000 {
		t.Fatalf("maxOutputTokens = %d", request.GenerationConfig.MaxOutputTokens)
	}
	if request.GenerationConfig.ThinkingConfig != nil {
		t.Fatalf("thinkingConfig must be omitted, got %#v", request.GenerationConfig.ThinkingConfig)
	}
}

func TestResolveGoogleEndpointEscapesModel(t *testing.T) {
	got := resolveEndpoint(ModelConfig{Provider: "google", Model: "gemini test"})
	want := DefaultGoogleBaseURL + "/models/gemini%20test:generateContent"
	if got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
}

func TestResponsesClientRetriesTransientGoogleFailure(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"high demand"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"Готово"}]}}]}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	client.Endpoint = srv.URL
	client.RetryBaseDelay = time.Millisecond
	answer, err := client.Analyze(context.Background(), ModelConfig{
		Enabled: true, Provider: "google", Model: "gemini-test", APIKey: "secret",
	}, "вопрос", nil)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "Готово" || attempts != 3 {
		t.Fatalf("answer = %q, attempts = %d", answer, attempts)
	}
}

func TestResponsesClientDoesNotRepeatSlowProviderOverload(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		time.Sleep(15 * time.Millisecond)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"high demand"}}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	client.Endpoint = srv.URL
	client.RetryBaseDelay = time.Millisecond
	client.RetrySlowResponseThreshold = 5 * time.Millisecond
	_, err := client.Analyze(context.Background(), ModelConfig{
		Enabled: true, Provider: "google", Model: "gemini-3.7-flash", APIKey: "secret",
	}, "привет", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("error = %v, want HTTP 503", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 for a slow overload response", attempts)
	}
}

func TestGeminiGenerationConfigUsesLowThinkingForInteractiveModels(t *testing.T) {
	got := geminiGenerationConfig("gemini-3.7-flash", 1000)
	thinking, ok := got["thinkingConfig"].(map[string]any)
	if !ok || thinking["thinkingLevel"] != "low" {
		t.Fatalf("thinkingConfig = %#v, want low", got["thinkingConfig"])
	}
	legacy := geminiGenerationConfig("gemini-2.5-flash", 1000)
	if _, ok := legacy["thinkingConfig"]; ok {
		t.Fatalf("legacy model must not receive thinkingLevel: %#v", legacy)
	}
}

type testOutboundsProvider struct{}

func (testOutboundsProvider) ListDownloadOutbounds(context.Context) []downloader.Outbound {
	return []downloader.Outbound{{Tag: "direct", Kind: "direct", Available: true}}
}

type testTransportResolver struct{}

func (testTransportResolver) Resolve(context.Context, downloader.Outbound) (downloader.TransportSpec, error) {
	return downloader.TransportSpec{}, nil
}

func (testTransportResolver) IsAvailable(context.Context, downloader.Outbound) bool {
	return true
}

func TestResponsesClientRejectsUnavailableSelectedRoute(t *testing.T) {
	client := NewRoutedResponsesClient(downloader.NewService(downloader.Deps{
		Outbounds:         testOutboundsProvider{},
		TransportResolver: testTransportResolver{},
	}))
	client.Endpoint = "https://api.example.com/v1/responses"
	_, err := client.Analyze(context.Background(), ModelConfig{
		Enabled: true, Provider: "google", Model: "gemini-test", APIKey: "secret",
		RouteTag: "missing-tunnel", RouteKind: "singbox",
	}, "question", nil)
	if err == nil || !strings.Contains(err.Error(), "model route") || !strings.Contains(err.Error(), "missing-tunnel") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIResponsesNativeToolRoundTrip(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","call_id":"call_1","name":"dns_inspect","arguments":"{\"domain\":\"ya.ru\"}"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"DNS работает"}]}]}`))
	}))
	defer srv.Close()
	client := NewResponsesClient()
	client.Endpoint = srv.URL + "/responses"
	cfg := ModelConfig{Enabled: true, Provider: "openai", Model: "test", APIKey: "secret"}
	definitions := []ToolDefinition{{Name: "dns.inspect", Description: "DNS", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"domain": map[string]any{"type": "string"}}, "required": []string{"domain"}, "additionalProperties": false}}}

	first, err := client.AnalyzeWithTools(context.Background(), cfg, "проверь DNS", nil, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Calls) != 1 || first.Calls[0].Name != "dns.inspect" || first.Calls[0].ID != "call_1" {
		t.Fatalf("first turn = %+v", first)
	}
	second, err := client.AnalyzeWithTools(context.Background(), cfg, "проверь DNS", nil, definitions, []ModelToolExchange{{
		ProviderState: first.ProviderState,
		Results:       []ModelToolResult{{ID: "call_1", Name: "dns.inspect", Output: `{"status":"passed"}`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "DNS работает" || len(requests) != 2 {
		t.Fatalf("second turn = %+v requests=%d", second, len(requests))
	}
	input, _ := requests[1]["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("second input must contain user, reasoning, call and result: %#v", input)
	}
	result, _ := input[3].(map[string]any)
	if result["type"] != "function_call_output" || result["call_id"] != "call_1" {
		t.Fatalf("function result = %#v", result)
	}
	tools, _ := requests[0]["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	parameters, _ := tool["parameters"].(map[string]any)
	if tool["strict"] != true || parameters["additionalProperties"] != false {
		t.Fatalf("strict OpenAI schema = %#v", tool)
	}
}

func TestStrictOpenAISchemaMakesOptionalPropertiesNullableAndRequired(t *testing.T) {
	schema := strictOpenAISchema(map[string]any{
		"type":       "object",
		"properties": map[string]any{"domain": map[string]any{"type": "string", "enum": []string{"a", "b"}}},
	})
	required, _ := schema["required"].([]string)
	properties, _ := schema["properties"].(map[string]any)
	domain, _ := properties["domain"].(map[string]any)
	types, _ := domain["type"].([]string)
	values, _ := domain["enum"].([]any)
	if len(required) != 1 || required[0] != "domain" || len(types) != 2 || types[1] != "null" || len(values) != 3 || values[2] != nil {
		t.Fatalf("normalized schema = %#v", schema)
	}
}

func TestGeminiNativeToolRoundTripPreservesThoughtSignature(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"fc_1","name":"routing_snapshot","args":{}},"thoughtSignature":"opaque-signature"}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Маршруты проверены"}]}}]}`))
	}))
	defer srv.Close()
	client := NewResponsesClient()
	client.Endpoint = srv.URL
	cfg := ModelConfig{Enabled: true, Provider: "google", Model: "gemini-test", APIKey: "secret"}
	definitions := []ToolDefinition{{Name: "routing.snapshot", Description: "Routes", InputSchema: map[string]any{"type": "object", "additionalProperties": false}}}

	first, err := client.AnalyzeWithTools(context.Background(), cfg, "проверь маршруты", nil, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Calls) != 1 || first.Calls[0].Name != "routing.snapshot" || first.Calls[0].ID != "fc_1" {
		t.Fatalf("first turn = %+v", first)
	}
	second, err := client.AnalyzeWithTools(context.Background(), cfg, "проверь маршруты", nil, definitions, []ModelToolExchange{{
		ProviderState: first.ProviderState,
		Results:       []ModelToolResult{{ID: "fc_1", Name: "routing.snapshot", Output: `{"status":"passed"}`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "Маршруты проверены" || len(requests) != 2 {
		t.Fatalf("second turn = %+v", second)
	}
	contents, _ := requests[1]["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("Gemini history = %#v", contents)
	}
	modelContent, _ := contents[1].(map[string]any)
	modelParts, _ := modelContent["parts"].([]any)
	modelPart, _ := modelParts[0].(map[string]any)
	if modelPart["thoughtSignature"] != "opaque-signature" {
		t.Fatalf("thought signature lost: %#v", modelPart)
	}
	userContent, _ := contents[2].(map[string]any)
	userParts, _ := userContent["parts"].([]any)
	responsePart, _ := userParts[0].(map[string]any)
	functionResponse, _ := responsePart["functionResponse"].(map[string]any)
	if functionResponse["id"] != "fc_1" || functionResponse["name"] != "routing_snapshot" {
		t.Fatalf("function response = %#v", functionResponse)
	}
	response, _ := functionResponse["response"].(map[string]any)
	result, _ := response["result"].(map[string]any)
	if result["status"] != "passed" {
		t.Fatalf("Gemini function result is not structured: %#v", response)
	}
	tools, _ := requests[0]["tools"].([]any)
	declarationsContainer, _ := tools[0].(map[string]any)
	declarations, _ := declarationsContainer["functionDeclarations"].([]any)
	declaration, _ := declarations[0].(map[string]any)
	parameters, _ := declaration["parameters"].(map[string]any)
	if _, exists := parameters["additionalProperties"]; exists {
		t.Fatalf("unsupported additionalProperties leaked into Gemini schema: %#v", parameters)
	}
}

func TestGeminiSchemaStripsUnsupportedKeywordsRecursively(t *testing.T) {
	schema := geminiSchema(map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"nested": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		},
	})
	if _, exists := schema["additionalProperties"]; exists {
		t.Fatalf("root keyword was not stripped: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	nested := properties["nested"].(map[string]any)
	if _, exists := nested["additionalProperties"]; exists {
		t.Fatalf("nested keyword was not stripped: %#v", nested)
	}
}

func TestChatCompletionsToolCallRoundTrip(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			// First turn: return a tool call.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"dns_inspect","arguments":"{\"domain\":\"ya.ru\"}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		// Second turn: return final answer.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"DNS работает исправно"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	client.Endpoint = srv.URL + "/v1/chat/completions"
	cfg := ModelConfig{Enabled: true, Provider: "deepseek", BaseURL: srv.URL + "/v1", Model: "deepseek-chat", APIKey: "test-key"}
	definitions := []ToolDefinition{{
		Name:        "dns.inspect",
		Description: "DNS lookup",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"domain": map[string]any{"type": "string"}},
			"required":   []string{"domain"},
		},
	}}

	// First turn: expect a tool call.
	first, err := client.AnalyzeWithTools(context.Background(), cfg, "проверь DNS ya.ru", nil, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Calls) != 1 || first.Calls[0].Name != "dns.inspect" || first.Calls[0].ID != "call_abc" {
		t.Fatalf("first turn calls = %+v", first.Calls)
	}
	if first.Calls[0].Arguments["domain"] != "ya.ru" {
		t.Fatalf("first turn argument = %+v", first.Calls[0].Arguments)
	}

	// Second turn: replay history and get final answer.
	second, err := client.AnalyzeWithTools(context.Background(), cfg, "проверь DNS ya.ru", nil, definitions, []ModelToolExchange{{
		ProviderState: first.ProviderState,
		Results:       []ModelToolResult{{ID: "call_abc", Name: "dns.inspect", Output: `{"status":"passed","latency":12}`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "DNS работает исправно" {
		t.Fatalf("second turn text = %q", second.Text)
	}
	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}

	// Verify second request contains history: system + user + assistant + tool.
	messages, _ := requests[1]["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("second request messages count = %d, want 4: %#v", len(messages), messages)
	}
	toolMsg, _ := messages[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_abc" {
		t.Fatalf("tool result message = %#v", toolMsg)
	}

	// Verify parallel_tool_calls is false.
	if requests[0]["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls = %#v, want false", requests[0]["parallel_tool_calls"])
	}

	// Verify tools are present and correctly named.
	tools, _ := requests[0]["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools count = %d", len(tools))
	}
	toolDef, _ := tools[0].(map[string]any)
	fn, _ := toolDef["function"].(map[string]any)
	if fn["name"] != "dns_inspect" {
		t.Fatalf("tool name = %q, want dns_inspect", fn["name"])
	}
}

func TestChatCompletionsPlainAnswerNoToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Всё в порядке, туннели активны"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	client := NewResponsesClient()
	client.Endpoint = srv.URL + "/v1/chat/completions"
	cfg := ModelConfig{Enabled: true, Provider: "custom", BaseURL: srv.URL + "/v1", Model: "gpt-4o-mini", APIKey: "test-key"}
	definitions := []ToolDefinition{{Name: "engines.status", Description: "Engine status", InputSchema: map[string]any{"type": "object"}}}

	turn, err := client.AnalyzeWithTools(context.Background(), cfg, "как дела?", nil, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Calls) != 0 {
		t.Fatalf("expected no tool calls, got %+v", turn.Calls)
	}
	if turn.Text != "Всё в порядке, туннели активны" {
		t.Fatalf("text = %q", turn.Text)
	}
}

func TestIsChatCompletionsToolProvider(t *testing.T) {
	cases := []struct {
		cfg  ModelConfig
		want bool
	}{
		{ModelConfig{Provider: "deepseek"}, true},
		{ModelConfig{Provider: "openrouter"}, true},
		{ModelConfig{Provider: "custom"}, true},
		{ModelConfig{Provider: "ollama", BaseURL: "http://localhost:11434/v1"}, true},
		{ModelConfig{Provider: "ollama", BaseURL: ""}, true}, // default URL ends with /v1
		{ModelConfig{Provider: "ollama", BaseURL: "http://localhost:11434"}, false},
		{ModelConfig{Provider: "google"}, false},
		{ModelConfig{Provider: "openai"}, false},
		{ModelConfig{Provider: "local_embedded"}, false},
	}
	for _, c := range cases {
		got := isChatCompletionsToolProvider(c.cfg)
		if got != c.want {
			t.Errorf("isChatCompletionsToolProvider(%+v) = %v, want %v", c.cfg, got, c.want)
		}
	}
}
