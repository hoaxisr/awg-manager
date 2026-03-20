package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// DiagnosticsRunner is the interface for running diagnostics.
type DiagnosticsRunner interface {
	Run(ctx context.Context) error
	RunWithStream(ctx context.Context, opts diagnostics.RunOptions) (<-chan diagnostics.DiagEvent, error)
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

// Stream starts a diagnostic run and streams results via SSE.
// GET /api/diagnostics/stream?mode=quick&restart=false
func (h *DiagnosticsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		response.Error(w, "streaming not supported", "SSE_NOT_SUPPORTED")
		return
	}

	mode := diagnostics.RunMode(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = diagnostics.ModeQuick
	}
	restart := r.URL.Query().Get("restart") == "true"
	opts := diagnostics.RunOptions{
		Mode:           mode,
		IncludeRestart: restart,
	}

	ch, err := h.runner.RunWithStream(r.Context(), opts)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusConflict, err.Error(), "DIAGNOSTICS_RUNNING")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}
