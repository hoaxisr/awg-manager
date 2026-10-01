package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/aiassistant"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/response"
)

type AIAssistantService interface {
	Start(question string) error
	Status() aiassistant.State
	ClearChat()
}

type AIAssistantHandler struct {
	service  AIAssistantService
	config   *aiassistant.ConfigStore
	embedded *aiassistant.EmbeddedManager
	routes   *downloader.Service
	memory   *aiassistant.MemoryStore
	sentinel *aiassistant.Sentinel
}

func NewAIAssistantHandler(service AIAssistantService) *AIAssistantHandler {
	return &AIAssistantHandler{service: service}
}

func (h *AIAssistantHandler) SetConfigStore(config *aiassistant.ConfigStore) { h.config = config }
func (h *AIAssistantHandler) SetEmbedded(embedded *aiassistant.EmbeddedManager) {
	h.embedded = embedded
}
func (h *AIAssistantHandler) SetRoutes(routes *downloader.Service)           { h.routes = routes }
func (h *AIAssistantHandler) SetMemoryStore(memory *aiassistant.MemoryStore) { h.memory = memory }
func (h *AIAssistantHandler) SetSentinel(sentinel *aiassistant.Sentinel)     { h.sentinel = sentinel }

func (h *AIAssistantHandler) RegisterRoutes(mux *http.ServeMux, guarded func(http.HandlerFunc) http.HandlerFunc) {
	for _, prefix := range []string{"/api/ai", "/api/system/ai"} {
		mux.HandleFunc(prefix+"/diagnose", guarded(h.Diagnose))
		mux.HandleFunc(prefix+"/status", guarded(h.Status))
		mux.HandleFunc(prefix+"/chat/clear", guarded(h.ClearChat))
		mux.HandleFunc(prefix+"/action", guarded(h.ApplyAction))
		mux.HandleFunc(prefix+"/action/apply", guarded(h.ApplyAction))
		mux.HandleFunc(prefix+"/config", guarded(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				h.Config(w, r)
			} else {
				h.SaveConfig(w, r)
			}
		}))
		mux.HandleFunc(prefix+"/models", guarded(h.FetchModels))
		mux.HandleFunc(prefix+"/embedded", guarded(h.Embedded))
		mux.HandleFunc(prefix+"/embedded/stop", guarded(h.EmbeddedStop))
		mux.HandleFunc(prefix+"/mcp", guarded(h.MCP))
		mux.HandleFunc(prefix+"/errors", guarded(h.ListErrors))
		mux.HandleFunc(prefix+"/memory", guarded(h.Memory))
		mux.HandleFunc(prefix+"/memory/facts", guarded(h.MemoryFacts))
		mux.HandleFunc(prefix+"/memory/facts/", guarded(h.MemoryFactItem))
		mux.HandleFunc(prefix+"/memory/playbooks/", guarded(h.MemoryPlaybookItem))
		mux.HandleFunc(prefix+"/sentinel", guarded(h.SentinelHandler))
	}
	mux.HandleFunc("/api/system/errors", guarded(h.ListErrors))
}

// ListErrors returns the list of known router and component errors with optional search/filtering.
// GET /api/system/errors?q=...&category=...
func (h *AIAssistantHandler) ListErrors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	q := r.URL.Query().Get("q")
	cat := r.URL.Query().Get("category")

	results := aiassistant.SearchErrors(q, cat)
	categories := []string{"Все", "KeeneticOS (NDMS)", "AmneziaWG", "Mihomo", "Sing-box", "DNS", "Сеть", "Ядро Linux", "Система"}
	response.Success(w, map[string]any{
		"total":      len(results),
		"categories": categories,
		"errors":     results,
	})
}

type aiMCPService interface {
	ListTools() []aiassistant.ToolDefinition
	CallTool(context.Context, aiassistant.ToolCall) aiassistant.ToolStep
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// MCP implements the JSON-RPC core needed by MCP clients and by the in-app
// assistant: initialize, tools/list and tools/call. The route is protected by
// the normal AWG Manager session and currently exposes read-only tools only.
func (h *AIAssistantHandler) MCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	service, ok := h.service.(aiMCPService)
	if !ok {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", Error: &mcpError{Code: -32601, Message: "MCP tools are unavailable"}})
		return
	}
	var req mcpRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeMCP(w, mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32600, Message: "Invalid Request"}})
		return
	}
	result := mcpResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		result.Result = map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]string{"name": "awg-manager", "version": "1"},
		}
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	case "tools/list":
		definitions := service.ListTools()
		tools := make([]map[string]any, 0, len(definitions))
		for _, definition := range definitions {
			tools = append(tools, map[string]any{
				"name": definition.Name, "title": definition.Title,
				"description": definition.Description, "inputSchema": definition.InputSchema,
				"annotations": map[string]any{"readOnlyHint": definition.ReadOnly, "destructiveHint": false},
			})
		}
		result.Result = map[string]any{"tools": tools}
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
			result.Error = &mcpError{Code: -32602, Message: "Invalid tool arguments"}
			break
		}
		arguments := make(map[string]string, len(params.Arguments))
		for key, value := range params.Arguments {
			if text, ok := value.(string); ok {
				arguments[key] = text
			}
		}
		step := service.CallTool(r.Context(), aiassistant.ToolCall{Name: params.Name, Arguments: arguments})
		text := step.Summary
		if len(step.Evidence) > 0 {
			text += "\n" + strings.Join(step.Evidence, "\n")
		}
		result.Result = map[string]any{
			"content":           []map[string]string{{"type": "text", "text": text}},
			"structuredContent": step,
			"isError":           step.Status == "error",
		}
	default:
		result.Error = &mcpError{Code: -32601, Message: "Method not found"}
	}
	writeMCP(w, result)
}

func writeMCP(w http.ResponseWriter, payload mcpResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

type AIAssistantConfigResponse struct {
	Success bool                          `json:"success" example:"true"`
	Data    aiassistant.PublicModelConfig `json:"data"`
}

type aiDiagnoseRequest struct {
	Question string `json:"question"`
}

type AIAssistantStateResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    aiassistant.State `json:"data"`
}

type AIAssistantEmbeddedResponse struct {
	Success bool                       `json:"success" example:"true"`
	Data    aiassistant.EmbeddedStatus `json:"data"`
}

type aiActionRequest struct {
	ProposalID string `json:"proposalId"`
}

type aiActionApplier interface {
	ApplyAction(string) error
}

// Diagnose starts a read-only, sanitized system diagnostic run.
// POST /api/system/ai/diagnose
//
//	@Summary		Start safe AI diagnostics
//	@Description	Runs sanitized read-only diagnostics without applying changes
//	@Tags			system
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		aiDiagnoseRequest	false	"Diagnostic question"
//	@Success		202		{object}	AIAssistantStateResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		409		{object}	APIErrorEnvelope
//	@Router			/system/ai/diagnose [post]
func (h *AIAssistantHandler) Diagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.InternalError(w, "AI assistant is unavailable")
		return
	}

	var req aiDiagnoseRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(w, "invalid request")
		return
	}
	if err := h.service.Start(req.Question); err != nil {
		if errors.Is(err, aiassistant.ErrRunning) {
			response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "AI_DIAGNOSTICS_RUNNING")
			return
		}
		response.BadRequest(w, err.Error())
		return
	}
	response.Accepted(w, h.service.Status())
}

// Status returns the current or last assistant diagnostic result.
// GET /api/system/ai/status
//
//	@Summary		AI diagnostics status
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AIAssistantStateResponse
//	@Router			/system/ai/status [get]
func (h *AIAssistantHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.service == nil {
		response.InternalError(w, "AI assistant is unavailable")
		return
	}
	response.Success(w, h.service.Status())
}

func (h *AIAssistantHandler) ApplyAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	applier, ok := h.service.(aiActionApplier)
	if !ok {
		response.InternalError(w, "AI remediation is unavailable")
		return
	}
	var req aiActionRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.ProposalID == "" {
		response.BadRequest(w, "invalid remediation request")
		return
	}
	if err := applier.ApplyAction(req.ProposalID); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, h.service.Status())
}

// Config returns write-only model configuration metadata. The API key is
// represented only by apiKeySet and is never returned.
//
//	@Summary		AI model configuration
//	@Tags			system
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	AIAssistantConfigResponse
//	@Router			/system/ai/config [get]
func (h *AIAssistantHandler) Config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.config == nil {
		response.InternalError(w, "AI model configuration is unavailable")
		return
	}
	response.Success(w, h.config.Public())
}

// SaveConfig persists model configuration in a dedicated 0600 file.
//
//	@Summary		Save AI model configuration
//	@Tags			system
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			request	body		aiassistant.ConfigUpdate	true	"Model configuration"
//	@Success		200		{object}	AIAssistantConfigResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Router			/system/ai/config [put]
func (h *AIAssistantHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.config == nil {
		response.InternalError(w, "AI model configuration is unavailable")
		return
	}
	var req aiassistant.ConfigUpdate
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		response.BadRequest(w, "invalid request")
		return
	}
	if aiassistant.IsPrivateOrLocalURL(req.BaseURL) {
		req.RouteTag = "direct"
		req.RouteKind = "direct"
	}
	if h.routes != nil && req.Provider != "local_embedded" {
		if _, err := h.routes.ValidateRoute(r.Context(), &downloader.Route{Tag: req.RouteTag, Kind: req.RouteKind}); err != nil {
			response.BadRequest(w, err.Error())
			return
		}
	}
	public, err := h.config.Save(req)
	if err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, public)
}

// Embedded returns status of the local embedded llama-server engine.
// GET /api/system/ai/embedded
func (h *AIAssistantHandler) Embedded(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.embedded == nil {
		response.InternalError(w, "embedded AI manager is unavailable")
		return
	}
	response.Success(w, h.embedded.Status())
}

// EmbeddedStop stops the running llama-server process to free memory.
// POST /api/system/ai/embedded/stop
func (h *AIAssistantHandler) EmbeddedStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.embedded == nil {
		response.InternalError(w, "embedded AI manager is unavailable")
		return
	}
	if err := h.embedded.Stop(); err != nil {
		response.BadRequest(w, err.Error())
		return
	}
	response.Success(w, h.embedded.Status())
}

func (h *AIAssistantHandler) FetchModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	var req struct {
		Provider  string `json:"provider"`
		BaseURL   string `json:"baseUrl"`
		APIKey    string `json:"apiKey"`
		RouteTag  string `json:"routeTag"`
		RouteKind string `json:"routeKind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		response.Error(w, "invalid request body: "+err.Error(), "INVALID_ARGUMENT")
		return
	}

	cfg := aiassistant.ModelConfig{
		Provider:  req.Provider,
		BaseURL:   req.BaseURL,
		APIKey:    req.APIKey,
		RouteTag:  req.RouteTag,
		RouteKind: req.RouteKind,
		Enabled:   true,
	}

	if h.config != nil {
		saved := h.config.Get()
		if cfg.APIKey == "" {
			if saved.Provider == req.Provider && saved.APIKey != "" {
				cfg.APIKey = saved.APIKey
			} else if prof, ok := saved.Providers[req.Provider]; ok && prof.APIKey != "" {
				cfg.APIKey = prof.APIKey
			}
		}
		if cfg.BaseURL == "" {
			if saved.Provider == req.Provider && saved.BaseURL != "" {
				cfg.BaseURL = saved.BaseURL
			} else if prof, ok := saved.Providers[req.Provider]; ok && prof.BaseURL != "" {
				cfg.BaseURL = prof.BaseURL
			}
		}
	}

	client := aiassistant.NewRoutedResponsesClient(h.routes)
	models, err := client.FetchAvailableModels(r.Context(), cfg)
	if err != nil {
		response.Error(w, err.Error(), "FETCH_MODELS_FAILED")
		return
	}

	response.Success(w, models)
}

func (h *AIAssistantHandler) ClearChat(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		response.Error(w, "ai assistant service is unavailable", "SERVICE_UNAVAILABLE")
		return
	}
	h.service.ClearChat()
	response.Success(w, h.service.Status())
}

// GET /api/system/ai/memory
func (h *AIAssistantHandler) Memory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.memory == nil {
		response.Success(w, map[string]any{
			"facts":     []any{},
			"playbooks": []any{},
			"journal":   []any{},
			"settings":  aiassistant.SentinelSettings{},
			"sentinel":  aiassistant.SentinelStatus{},
		})
		return
	}
	var sentinelStatus any
	if h.sentinel != nil {
		sentinelStatus = h.sentinel.Status()
	}
	response.Success(w, map[string]any{
		"facts":     h.memory.ListFacts(""),
		"playbooks": h.memory.ListPlaybooks(),
		"journal":   h.memory.ListJournal(50),
		"settings":  h.memory.Settings(),
		"sentinel":  sentinelStatus,
	})
}

// POST /api/system/ai/memory/facts
func (h *AIAssistantHandler) MemoryFacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.memory == nil {
		response.Error(w, "AI memory store is unavailable", "MEMORY_UNAVAILABLE")
		return
	}
	var req struct {
		Category string `json:"category"`
		Content  string `json:"content"`
		Source   string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		response.BadRequest(w, "content is required")
		return
	}
	source := req.Source
	if source == "" {
		source = "user"
	}
	fact := h.memory.AddFact(req.Category, req.Content, source)
	response.Success(w, fact)
}

// DELETE /api/system/ai/memory/facts/{id}
func (h *AIAssistantHandler) MemoryFactItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.memory == nil {
		response.Error(w, "AI memory store is unavailable", "MEMORY_UNAVAILABLE")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 {
		response.BadRequest(w, "missing fact ID")
		return
	}
	id := parts[len(parts)-1]
	ok := h.memory.RemoveFact(id)
	response.Success(w, map[string]any{"deleted": ok, "id": id})
}

// DELETE /api/system/ai/memory/playbooks/{id}
func (h *AIAssistantHandler) MemoryPlaybookItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}
	if h.memory == nil {
		response.Error(w, "AI memory store is unavailable", "MEMORY_UNAVAILABLE")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 {
		response.BadRequest(w, "missing playbook ID")
		return
	}
	id := parts[len(parts)-1]
	ok := h.memory.RemovePlaybook(id)
	response.Success(w, map[string]any{"deleted": ok, "id": id})
}

// GET / POST /api/system/ai/sentinel
func (h *AIAssistantHandler) SentinelHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if h.sentinel == nil {
			response.Success(w, aiassistant.SentinelStatus{})
			return
		}
		response.Success(w, h.sentinel.Status())
		return
	}
	if r.Method == http.MethodPost {
		if h.memory == nil {
			response.Error(w, "AI memory store is unavailable", "MEMORY_UNAVAILABLE")
			return
		}
		var req aiassistant.SentinelSettings
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			response.BadRequest(w, "invalid request body: "+err.Error())
			return
		}
		if err := h.memory.UpdateSettings(req); err != nil {
			response.BadRequest(w, err.Error())
			return
		}
		var status aiassistant.SentinelStatus
		if h.sentinel != nil {
			status = h.sentinel.Status()
		}
		response.Success(w, status)
		return
	}
	response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
}
