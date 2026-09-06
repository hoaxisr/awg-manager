package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// McpKeyDTO is one MCP key as listed (no secret material).
type McpKeyDTO struct {
	ID         string     `json:"id" example:"3f9c1a2b4d5e6f70"`
	Name       string     `json:"name" example:"laptop"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// McpKeysListData is the payload of GET /mcp/keys.
type McpKeysListData struct {
	Keys []McpKeyDTO `json:"keys"`
}

// McpKeysListResponse is the envelope of GET /mcp/keys.
type McpKeysListResponse struct {
	Success bool            `json:"success" example:"true"`
	Data    McpKeysListData `json:"data"`
}

// McpKeyCreateRequest is the body of POST /mcp/keys/create.
type McpKeyCreateRequest struct {
	Name string `json:"name" example:"laptop"`
}

// McpKeyCreatedData is the payload of POST /mcp/keys/create. Key is the
// plaintext, returned exactly once.
type McpKeyCreatedData struct {
	ID        string    `json:"id" example:"3f9c1a2b4d5e6f70"`
	Name      string    `json:"name" example:"laptop"`
	CreatedAt time.Time `json:"createdAt"`
	Key       string    `json:"key" example:"awgm_Q3VyaW91cz8gVGhpcyBpcyBqdXN0IGFuIGV4YW1wbGU"`
}

// McpKeyCreatedResponse is the envelope of POST /mcp/keys/create.
type McpKeyCreatedResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    McpKeyCreatedData `json:"data"`
}

// McpKeyRevokeRequest is the body of POST /mcp/keys/revoke.
type McpKeyRevokeRequest struct {
	ID string `json:"id" example:"3f9c1a2b4d5e6f70"`
}

// McpKeyRevokedData is the payload of POST /mcp/keys/revoke.
type McpKeyRevokedData struct {
	Revoked bool `json:"revoked" example:"true"`
}

// McpKeyRevokedResponse is the envelope of POST /mcp/keys/revoke.
type McpKeyRevokedResponse struct {
	Success bool              `json:"success" example:"true"`
	Data    McpKeyRevokedData `json:"data"`
}

// McpKeysHandler manages bearer keys for the /mcp endpoint.
type McpKeysHandler struct {
	store *storage.McpKeyStore
	log   *logging.ScopedLogger
	bus   *events.Bus
}

// SetEventBus wires the SSE bus; nil is fine (tests).
func (h *McpKeysHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// NewMcpKeysHandler creates the handler. appLogger may be nil in tests.
func NewMcpKeysHandler(store *storage.McpKeyStore, appLogger logging.AppLogger) *McpKeysHandler {
	return &McpKeysHandler{store: store, log: logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubMcp)}
}

func toKeyDTO(k storage.McpKey) McpKeyDTO {
	dto := McpKeyDTO{ID: k.ID, Name: k.Name, CreatedAt: k.CreatedAt}
	if !k.LastUsedAt.IsZero() {
		t := k.LastUsedAt
		dto.LastUsedAt = &t
	}
	return dto
}

// List returns all MCP keys without secret material.
//
//	@Summary		List MCP keys
//	@Description	Names and timestamps of the bearer keys accepted by the /mcp endpoint. Hashes and plaintexts are never returned.
//	@Tags			mcp
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	McpKeysListResponse
//	@Failure		405	{object}	APIErrorEnvelope
//	@Router			/mcp/keys [get]
func (h *McpKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	keys := h.store.List()
	out := make([]McpKeyDTO, 0, len(keys))
	for _, k := range keys {
		out = append(out, toKeyDTO(k))
	}
	response.Success(w, McpKeysListData{Keys: out})
}

// Create mints a new MCP key and returns its plaintext once.
//
//	@Summary		Create MCP key
//	@Description	Generates a new `awgm_…` bearer key for the /mcp endpoint. The plaintext is returned only in this response; store it in the MCP client configuration.
//	@Tags			mcp
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		McpKeyCreateRequest	true	"Key name (1..64 chars)"
//	@Success		200		{object}	McpKeyCreatedResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/mcp/keys/create [post]
func (h *McpKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[McpKeyCreateRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	key, plaintext, err := h.store.Create(req.Name)
	if err != nil {
		if errors.Is(err, storage.ErrMcpKeyInvalidName) {
			response.ErrorWithStatus(w, http.StatusBadRequest, err.Error(), "MCP_KEY_INVALID_NAME")
			return
		}
		// The client gets a fixed message; the cause (a read-only store
		// after a failed load, an unwritable flash) goes to the journal,
		// where the admin will look for it.
		h.log.Error("key-create", req.Name, "Failed to create MCP key: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, "failed to create key", "MCP_KEY_CREATE_ERROR")
		return
	}
	h.log.Info("key-create", key.Name, "MCP key created")
	h.bus.PublishInvalidated(events.ResourceMcpKeys, "created")
	response.Success(w, McpKeyCreatedData{ID: key.ID, Name: key.Name, CreatedAt: key.CreatedAt, Key: plaintext})
}

// Revoke deletes an MCP key by id.
//
//	@Summary		Revoke MCP key
//	@Description	Deletes a key; clients using it are rejected on their next request.
//	@Tags			mcp
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		McpKeyRevokeRequest	true	"Key id"
//	@Success		200		{object}	McpKeyRevokedResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
//	@Router			/mcp/keys/revoke [post]
func (h *McpKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[McpKeyRevokeRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if req.ID == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "id is required", "MCP_KEY_ID_REQUIRED")
		return
	}
	if err := h.store.Revoke(req.ID); err != nil {
		if errors.Is(err, storage.ErrMcpKeyNotFound) {
			response.ErrorWithStatus(w, http.StatusNotFound, "key not found", "MCP_KEY_NOT_FOUND")
			return
		}
		h.log.Error("key-revoke", req.ID, "Failed to revoke MCP key: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, "failed to revoke key", "MCP_KEY_REVOKE_ERROR")
		return
	}
	h.log.Info("key-revoke", req.ID, "MCP key revoked")
	h.bus.PublishInvalidated(events.ResourceMcpKeys, "revoked")
	response.Success(w, McpKeyRevokedData{Revoked: true})
}
