package aiassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// A real investigation commonly needs a broad snapshot followed by two or
// three focused checks and, optionally, a remediation proposal. Sixteen calls
// provide deep diagnostic visibility while keeping execution bounded.
const (
	maxAgentToolTurns = 24
)

type ModelToolCall struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type ModelToolResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Output string `json:"output"`
}

type ModelToolExchange struct {
	// ProviderState contains the complete provider model-output item(s). This is
	// required for OpenAI reasoning items and Gemini thought signatures.
	ProviderState json.RawMessage   `json:"-"`
	Results       []ModelToolResult `json:"results"`
}

type ModelToolTurn struct {
	Text          string          `json:"text,omitempty"`
	Calls         []ModelToolCall `json:"calls,omitempty"`
	ProviderState json.RawMessage `json:"-"`
}

type ToolAwareModel interface {
	AnalyzeWithTools(context.Context, ModelConfig, string, []byte, []ToolDefinition, []ModelToolExchange, ...string) (ModelToolTurn, error)
}

// agentPrompt gives every supported provider the same MCP-shaped catalog.
// Providers with native function calling can consume the same definitions in
// a future adapter; the text envelope keeps Gemini, OpenAI-compatible APIs and
// small local models interoperable today.
func agentPrompt(question string, definitions []ToolDefinition) string {
	if len(definitions) == 0 {
		return question
	}
	raw, _ := json.Marshal(definitions)
	return fmt.Sprintf(`%s

У тебя есть инструменты AWG Manager. Сам реши, нужен ли инструмент для ответа.
Каталог инструментов (совместимая с MCP схема): %s
Если нужны данные, ответь ТОЛЬКО одной строкой без Markdown:
TOOL_CALL: {"name":"имя","arguments":{"ключ":"значение"}}
Вызывай по одному инструменту за шаг. Не выдумывай результат инструмента. Если данных достаточно, дай обычный ответ пользователю.`, question, raw)
}

func parseModelToolCall(text string) (ToolCall, bool) {
	const marker = "TOOL_CALL:"
	idx := strings.Index(text, marker)
	if idx < 0 {
		return ToolCall{}, false
	}
	payload := strings.TrimSpace(text[idx+len(marker):])
	if end := strings.IndexByte(payload, '\n'); end >= 0 {
		payload = strings.TrimSpace(payload[:end])
	}
	payload = strings.Trim(payload, "`")
	var wire struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(payload), &wire); err != nil || strings.TrimSpace(wire.Name) == "" {
		return ToolCall{}, false
	}
	args := make(map[string]string, len(wire.Arguments))
	for key, value := range wire.Arguments {
		switch typed := value.(type) {
		case string:
			args[key] = typed
		case float64, bool:
			args[key] = fmt.Sprint(typed)
		}
	}
	return ToolCall{Name: strings.TrimSpace(wire.Name), Arguments: args}, true
}

func toolCallKey(call ToolCall) string {
	raw, _ := json.Marshal(call)
	return string(raw)
}
