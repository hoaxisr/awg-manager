package aiassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// isChatCompletionsToolProvider returns true for providers that support the
// standard OpenAI Chat Completions tool calling format. Ollama only qualifies
// when the configured base URL ends with /v1 (OpenAI-compatible mode).
func isChatCompletionsToolProvider(cfg ModelConfig) bool {
	switch cfg.Provider {
	case "deepseek", "openrouter", "custom":
		return true
	case "ollama":
		base := strings.TrimRight(cfg.BaseURL, "/")
		if base == "" {
			base = DefaultOllamaBaseURL
		}
		return strings.HasSuffix(base, "/v1")
	default:
		return false
	}
}

func (c *ResponsesClient) AnalyzeWithTools(
	ctx context.Context,
	cfg ModelConfig,
	question string,
	diagnostic []byte,
	definitions []ToolDefinition,
	history []ModelToolExchange,
	toolChoice ...string,
) (ModelToolTurn, error) {
	choice := "auto"
	if len(toolChoice) > 0 && toolChoice[0] != "" {
		choice = toolChoice[0]
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = resolveEndpoint(cfg)
	}
	var body []byte
	var err error
	switch {
	case cfg.Provider == "google":
		body, err = buildGeminiToolRequest(cfg, question, diagnostic, definitions, history, choice)
	case cfg.Provider == "openai":
		body, err = buildOpenAIResponsesToolRequest(cfg, question, diagnostic, definitions, history, choice)
	case isChatCompletionsToolProvider(cfg):
		body, err = buildChatCompletionsToolRequest(cfg, question, diagnostic, definitions, history, choice)
	default:
		return ModelToolTurn{}, fmt.Errorf("native tools are unsupported for provider %q", cfg.Provider)
	}
	if err != nil {
		return ModelToolTurn{}, err
	}
	raw, err := c.doRequest(ctx, cfg, endpoint, body)
	if err != nil {
		return ModelToolTurn{}, err
	}
	switch {
	case cfg.Provider == "google":
		return parseGeminiToolTurn(raw, definitions)
	case cfg.Provider == "openai":
		return parseOpenAIResponsesToolTurn(raw, definitions)
	default:
		return parseChatCompletionsToolTurn(raw, definitions)
	}
}

func providerToolName(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

func originalToolName(name string, definitions []ToolDefinition) string {
	for _, definition := range definitions {
		if providerToolName(definition.Name) == name {
			return definition.Name
		}
	}
	return name
}

func toolUserPrompt(question string, diagnostic []byte) string {
	prompt := fmt.Sprintf("Вопрос: %s\nДиагностика: %s", sanitizeModelText(question), diagnostic)
	if knownErr := LookupKnownError(question); knownErr != nil {
		prompt += fmt.Sprintf("\n\n[БАЗА ЗНАНИЙ СБОЕВ РОУТЕРА]:\n- Название сбоя: %s (код: %s, категория: %s)\n- Что произошло: %s\n- Причина: %s\n- Пошаговые действия для пользователя («Куда тыкнуть мышкой»):\n%s\n- Совет: %s\nИспользуй эти точные факты при формулировании ответа пользователю!",
			knownErr.Title, knownErr.Code, knownErr.Category, knownErr.Summary, knownErr.Cause,
			strings.Join(knownErr.ActionSteps, "\n"), knownErr.Tip)
	} else if heuristic, ok := FallbackHeuristicAnalysis(question); ok {
		prompt += fmt.Sprintf("\n\n[АНАЛИЗ КОДА СБОЯ]:\n%s\nИспользуй эти данные при ответе!", heuristic)
	}
	return prompt
}

func buildOpenAIResponsesToolRequest(cfg ModelConfig, question string, diagnostic []byte, definitions []ToolDefinition, history []ModelToolExchange, choice string) ([]byte, error) {
	input := []any{map[string]any{"role": "user", "content": toolUserPrompt(question, diagnostic)}}
	for _, exchange := range history {
		if len(exchange.ProviderState) > 0 {
			var output []any
			if err := json.Unmarshal(exchange.ProviderState, &output); err != nil {
				return nil, fmt.Errorf("decode OpenAI tool history: %w", err)
			}
			input = append(input, output...)
		}
		for _, result := range exchange.Results {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": result.ID, "output": result.Output})
		}
	}
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"type": "function", "name": providerToolName(definition.Name),
			"description": definition.Description, "parameters": strictOpenAISchema(definition.InputSchema), "strict": true,
		})
	}
	if choice == "none" {
		input = append(input, map[string]any{"role": "user", "content": "Диагностика завершена. Больше не вызывай инструментов. Напиши понятный и подробный ответ пользователю на русском языке: в чём точная причина проблемы, что показали проверки и как её решить."})
	}
	req := map[string]any{
		"model": cfg.Model, "instructions": modelSystemPrompt + modelAnswerPolicy, "input": input,
		"store": false, "max_output_tokens": 3000,
	}
	if len(tools) > 0 {
		req["tools"] = tools
		req["tool_choice"] = choice
		req["parallel_tool_calls"] = false
	}
	return json.Marshal(req)
}

func strictOpenAISchema(input map[string]any) map[string]any {
	raw, _ := json.Marshal(input)
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	normalizeOpenAIObjectSchema(schema)
	return schema
}

func normalizeOpenAIObjectSchema(schema map[string]any) {
	if schema["type"] == "object" {
		schema["additionalProperties"] = false
		properties, _ := schema["properties"].(map[string]any)
		required := make(map[string]bool)
		for _, name := range stringSlice(schema["required"]) {
			required[name] = true
		}
		allRequired := make([]string, 0, len(properties))
		for name, property := range properties {
			allRequired = append(allRequired, name)
			propertySchema, _ := property.(map[string]any)
			if !required[name] {
				makeSchemaNullable(propertySchema)
			}
			normalizeOpenAIObjectSchema(propertySchema)
		}
		if len(allRequired) > 0 {
			sort.Strings(allRequired)
			schema["required"] = allRequired
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		normalizeOpenAIObjectSchema(items)
	}
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	if typed, ok := value.([]string); ok {
		return append(result, typed...)
	}
	return result
}

func makeSchemaNullable(schema map[string]any) {
	typeName, ok := schema["type"].(string)
	if ok && typeName != "null" {
		schema["type"] = []string{typeName, "null"}
	}
	if values, ok := schema["enum"].([]any); ok {
		for _, value := range values {
			if value == nil {
				return
			}
		}
		schema["enum"] = append(values, nil)
	}
}

func parseOpenAIResponsesToolTurn(raw []byte, definitions []ToolDefinition) (ModelToolTurn, error) {
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ModelToolTurn{}, fmt.Errorf("decode OpenAI tool response: %w", err)
	}
	state, _ := json.Marshal(response.Output)
	turn := ModelToolTurn{ProviderState: state}
	var texts []string
	for _, itemRaw := range response.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(itemRaw, &item) != nil {
			continue
		}
		if item.Type == "function_call" {
			var arguments map[string]any
			if err := json.Unmarshal([]byte(item.Arguments), &arguments); err != nil {
				return ModelToolTurn{}, fmt.Errorf("decode OpenAI tool arguments: %w", err)
			}
			turn.Calls = append(turn.Calls, ModelToolCall{ID: item.CallID, Name: originalToolName(item.Name, definitions), Arguments: arguments})
		}
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				texts = append(texts, strings.TrimSpace(content.Text))
			}
		}
	}
	turn.Text = strings.Join(texts, "\n")
	return turn, nil
}

func buildGeminiToolRequest(cfg ModelConfig, question string, diagnostic []byte, definitions []ToolDefinition, history []ModelToolExchange, choice string) ([]byte, error) {
	contents := []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": toolUserPrompt(question, diagnostic)}}}}
	for _, exchange := range history {
		if len(exchange.ProviderState) > 0 {
			var content any
			if err := json.Unmarshal(exchange.ProviderState, &content); err != nil {
				return nil, fmt.Errorf("decode Gemini tool history: %w", err)
			}
			contents = append(contents, content)
		}
		parts := make([]any, 0, len(exchange.Results))
		for _, result := range exchange.Results {
			var structuredResult any = result.Output
			_ = json.Unmarshal([]byte(result.Output), &structuredResult)
			parts = append(parts, map[string]any{"functionResponse": map[string]any{
				"id": result.ID, "name": providerToolName(result.Name), "response": map[string]any{"result": structuredResult},
			}})
		}
		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
	}
	if choice == "none" {
		contents = append(contents, map[string]any{
			"role":  "user",
			"parts": []any{map[string]any{"text": "Диагностика завершена. Больше не вызывай инструментов. Напиши понятный и подробный ответ пользователю на русском языке: в чём точная причина проблемы, что показали проверки и как её решить."}},
		})
	}
	declarations := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		declarations = append(declarations, map[string]any{
			"name": providerToolName(definition.Name), "description": definition.Description, "parameters": geminiSchema(definition.InputSchema),
		})
	}
	req := map[string]any{
		"system_instruction": map[string]any{"parts": []map[string]string{{"text": modelSystemPrompt + modelAnswerPolicy}}},
		"contents":           contents,
		"generationConfig":   geminiGenerationConfig(cfg.Model, 3000),
	}
	if len(declarations) > 0 {
		req["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
		mode := "AUTO"
		if choice == "none" {
			mode = "NONE"
		}
		req["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": mode}}
	}
	return json.Marshal(req)
}

// Gemini accepts an OpenAPI-style schema subset rather than the complete JSON
// Schema shape used by MCP/OpenAI. In particular additionalProperties is
// rejected as an unknown field. Keep one canonical tool catalog and strip only
// unsupported schema keywords at the provider boundary.
func geminiSchema(input map[string]any) map[string]any {
	raw, _ := json.Marshal(input)
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	normalizeGeminiSchema(schema)
	return schema
}

func normalizeGeminiSchema(schema map[string]any) {
	delete(schema, "additionalProperties")
	delete(schema, "$schema")
	delete(schema, "$id")
	if enumRaw, ok := schema["enum"].([]string); ok {
		clean := make([]string, 0, len(enumRaw))
		for _, e := range enumRaw {
			if strings.TrimSpace(e) != "" {
				clean = append(clean, e)
			}
		}
		if len(clean) > 0 {
			schema["enum"] = clean
		} else {
			delete(schema, "enum")
		}
	} else if enumRaw, ok := schema["enum"].([]any); ok {
		clean := make([]any, 0, len(enumRaw))
		for _, e := range enumRaw {
			if s, ok := e.(string); ok && strings.TrimSpace(s) == "" {
				continue
			}
			clean = append(clean, e)
		}
		if len(clean) > 0 {
			schema["enum"] = clean
		} else {
			delete(schema, "enum")
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, property := range properties {
			if child, ok := property.(map[string]any); ok {
				normalizeGeminiSchema(child)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		normalizeGeminiSchema(items)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if variants, ok := schema[key].([]any); ok {
			for _, variant := range variants {
				if child, ok := variant.(map[string]any); ok {
					normalizeGeminiSchema(child)
				}
			}
		}
	}
}

func parseGeminiToolTurn(raw []byte, definitions []ToolDefinition) (ModelToolTurn, error) {
	var response struct {
		Candidates []struct {
			Content json.RawMessage `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || len(response.Candidates) == 0 {
		return ModelToolTurn{}, fmt.Errorf("decode Gemini tool response")
	}
	turn := ModelToolTurn{ProviderState: append(json.RawMessage(nil), response.Candidates[0].Content...)}
	var content struct {
		Parts []struct {
			Text         string `json:"text"`
			FunctionCall *struct {
				ID   string         `json:"id"`
				Name string         `json:"name"`
				Args map[string]any `json:"args"`
			} `json:"functionCall"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(response.Candidates[0].Content, &content); err != nil {
		return ModelToolTurn{}, fmt.Errorf("decode Gemini candidate: %w", err)
	}
	var texts []string
	for _, part := range content.Parts {
		if strings.TrimSpace(part.Text) != "" {
			texts = append(texts, strings.TrimSpace(part.Text))
		}
		if part.FunctionCall != nil {
			turn.Calls = append(turn.Calls, ModelToolCall{
				ID: part.FunctionCall.ID, Name: originalToolName(part.FunctionCall.Name, definitions), Arguments: part.FunctionCall.Args,
			})
		}
	}
	turn.Text = strings.Join(texts, "\n")
	return turn, nil
}

// buildChatCompletionsToolRequest builds an OpenAI Chat Completions tool request
// for providers that support the standard /chat/completions tool calling format
// (deepseek, openrouter, ollama with /v1, custom endpoints).
func buildChatCompletionsToolRequest(cfg ModelConfig, question string, diagnostic []byte, definitions []ToolDefinition, history []ModelToolExchange, choice string) ([]byte, error) {
	messages := []any{
		map[string]any{"role": "system", "content": modelSystemPrompt + modelAnswerPolicy},
		map[string]any{"role": "user", "content": toolUserPrompt(question, diagnostic)},
	}
	for _, exchange := range history {
		// Append assistant turn with tool_calls if present in provider state.
		if len(exchange.ProviderState) > 0 {
			var assistantMsg map[string]any
			if err := json.Unmarshal(exchange.ProviderState, &assistantMsg); err != nil {
				return nil, fmt.Errorf("decode chat completions tool history: %w", err)
			}
			messages = append(messages, assistantMsg)
		}
		// Append one tool result message per call.
		for _, result := range exchange.Results {
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": result.ID,
				"content":      result.Output,
			})
		}
	}
	if choice == "none" {
		messages = append(messages, map[string]any{
			"role":    "user",
			"content": "Диагностика завершена. Больше не вызывай инструментов. Напиши понятный и подробный ответ пользователю на русском языке: в чём точная причина проблемы, что показали проверки и как её решить.",
		})
	}
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        providerToolName(definition.Name),
				"description": definition.Description,
				"parameters":  chatCompletionsSchema(definition.InputSchema),
			},
		})
	}
	req := map[string]any{
		"model":      cfg.Model,
		"messages":   messages,
		"max_tokens": 3000,
	}
	if len(tools) > 0 {
		req["tools"] = tools
		req["tool_choice"] = choice
		req["parallel_tool_calls"] = false
	}
	return json.Marshal(req)
}

// chatCompletionsSchema converts our canonical tool schema to the subset
// accepted by standard OpenAI-compatible Chat Completions endpoints. Unlike
// the strict OpenAI Responses API, most compatible providers do not support
// strict mode or require all properties to be required, so we use a simpler
// pass-through with only additionalProperties preserved.
func chatCompletionsSchema(input map[string]any) map[string]any {
	raw, _ := json.Marshal(input)
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	return schema
}

// parseChatCompletionsToolTurn parses an OpenAI Chat Completions response that
// may contain tool_calls in the assistant message. The full assistant message
// (with tool_calls) is stored in ProviderState so the next turn can replay it.
func parseChatCompletionsToolTurn(raw []byte, definitions []ToolDefinition) (ModelToolTurn, error) {
	var response struct {
		Choices []struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &response); err != nil || len(response.Choices) == 0 {
		return ModelToolTurn{}, fmt.Errorf("decode chat completions tool response")
	}
	msg := response.Choices[0].Message
	turn := ModelToolTurn{}

	// Store the full assistant message as provider state for history replay.
	// The message role must be "assistant" and must include tool_calls for
	// proper multi-turn conversation support.
	assistantMsg := map[string]any{
		"role":    "assistant",
		"content": msg.Content,
	}
	if len(msg.ToolCalls) > 0 {
		toolCallItems := make([]map[string]any, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			toolCallItems = append(toolCallItems, map[string]any{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]any{
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				},
			})
		}
		assistantMsg["tool_calls"] = toolCallItems
	}
	stateBytes, _ := json.Marshal(assistantMsg)
	turn.ProviderState = stateBytes

	// Parse tool calls into ModelToolCall entries.
	for _, tc := range msg.ToolCalls {
		if tc.Type != "function" {
			continue
		}
		var arguments map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &arguments); err != nil {
			return ModelToolTurn{}, fmt.Errorf("decode chat completions tool arguments: %w", err)
		}
		turn.Calls = append(turn.Calls, ModelToolCall{
			ID:        tc.ID,
			Name:      originalToolName(tc.Function.Name, definitions),
			Arguments: arguments,
		})
	}
	turn.Text = strings.TrimSpace(msg.Content)
	return turn, nil
}
