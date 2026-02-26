package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// DiagnosticsRunner is the interface for running diagnostics.
type DiagnosticsRunner interface {
	Run(ctx context.Context) error
	Status() diagnostics.RunStatus
	Result() ([]byte, error)
}

// DiagnosticsHandler handles diagnostics API endpoints.
type DiagnosticsHandler struct {
	runner DiagnosticsRunner
}

// NewDiagnosticsHandler creates a new diagnostics handler.
func NewDiagnosticsHandler(runner DiagnosticsRunner) *DiagnosticsHandler {
	return &DiagnosticsHandler{runner: runner}
}

// Run starts a background diagnostic run.
// POST /api/diagnostics/run
func (h *DiagnosticsHandler) Run(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	if err := h.runner.Run(r.Context()); err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "DIAGNOSTICS_RUNNING")
		return
	}

	response.Success(w, map[string]interface{}{
		"status": "running",
	})
}

// Status returns the current diagnostic run status.
// GET /api/diagnostics/status
func (h *DiagnosticsHandler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	response.Success(w, h.runner.Status())
}

// Result returns the last completed diagnostics report as a JSON file download.
// GET /api/diagnostics/result
func (h *DiagnosticsHandler) Result(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	data, err := h.runner.Result()
	if err != nil {
		response.Error(w, err.Error(), "NO_REPORT")
		return
	}

	filename := fmt.Sprintf("awg-diagnostics-%s.json", time.Now().Format("2006-01-02_15-04-05"))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Write(data)
}
