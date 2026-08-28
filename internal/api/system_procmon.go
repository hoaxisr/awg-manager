package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// Монитор процессов: снимок CPU/памяти/процессов и завершение процесса.
// ProcSnapshot returns current CPU, RAM, and process top list.
// @Summary ProcSnapshot (Expert only)
// @Description ProcSnapshot (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemProcSnapshotResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/proc/snapshot [get]
func (h *SystemToolsHandler) ProcSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	snap, err := h.procmon.Snapshot()
	if err != nil {
		response.InternalError(w, fmt.Sprintf("proc snapshot failed: %v", err))
		return
	}

	response.Success(w, snap)
}

// ProcKill terminates a process by PID with signal.
// @Summary ProcKill (Expert only)
// @Description ProcKill (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemKillResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/proc/kill [post]
func (h *SystemToolsHandler) ProcKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		PID    int    `json:"pid"`
		Signal string `json:"signal"` // "SIGTERM" or "SIGKILL"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.BadRequest(w, "invalid JSON payload")
		return
	}
	if req.PID <= 1 {
		response.BadRequest(w, "invalid process PID")
		return
	}
	if req.Signal == "" {
		req.Signal = "SIGTERM"
	}

	if err := h.procmon.KillProcess(req.PID, req.Signal); err != nil {
		response.Error(w, err.Error(), "KILL_FAILED")
		return
	}

	h.emitEvent("proc_kill", strconv.Itoa(req.PID), fmt.Sprintf("Killed PID %d with %s", req.PID, req.Signal))
	response.Success(w, SystemKillData{PID: req.PID, Signal: req.Signal, OK: true})
}
