package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// HealthHandler serves GET /api/health. A cheap liveness check that
// does no I/O, no NDMS calls — used by the frontend 5-second poller
// to decide when to show the full-screen "backend offline" overlay
// independently of SSE connection state.
type HealthHandler struct {
	version string
}

// NewHealthHandler constructs a HealthHandler that reports the given
// build version. The version is set via ldflags at build time and
// propagated from cmd/awg-manager/main.go through server.Config.Version.
func NewHealthHandler(version string) *HealthHandler {
	return &HealthHandler{version: version}
}

// ServeHTTP responds to GET with { ok: true, version: "..." }. Any
// other method returns 405 Method Not Allowed.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, map[string]any{
		"ok":      true,
		"version": h.version,
	})
}
