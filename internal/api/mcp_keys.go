package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// McpKeysHandler is a placeholder: the real key-management endpoints (and
// their swagger annotations) land with the MCP settings UI task. It exists
// now only so registerMcpRoutes can be wired in one place.
type McpKeysHandler struct{}

// NewMcpKeysHandler builds the placeholder handler.
func NewMcpKeysHandler(*storage.McpKeyStore, logging.AppLogger) *McpKeysHandler {
	return &McpKeysHandler{}
}

func (h *McpKeysHandler) List(w http.ResponseWriter, r *http.Request)   { http.NotFound(w, r) }
func (h *McpKeysHandler) Create(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
func (h *McpKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
