package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// Сетевые сокеты: список, кто занимает порт, завершение процесса.
// GET /api/system/ports/list
// @Summary PortsList (Expert only)
// @Description PortsList (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemPortsListResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/ports/list [get]
func (h *SystemToolsHandler) PortsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	items, err := h.ports.List()
	if err != nil {
		response.Error(w, err.Error(), "PORTS_ERROR")
		return
	}
	response.Success(w, items)
}

// GET /api/system/ports/inspect?port=&proto=
// @Summary PortsInspect (Expert only)
// @Description PortsInspect (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemPortInspectResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/ports/inspect [get]
func (h *SystemToolsHandler) PortsInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	portStr := r.URL.Query().Get("port")
	if strings.TrimSpace(portStr) == "" {
		response.Error(w, "port parameter required", "INVALID_PORT")
		return
	}
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil || port <= 0 || port > 65535 {
		response.Error(w, "invalid port number (1-65535)", "INVALID_PORT")
		return
	}
	proto := r.URL.Query().Get("proto")
	items, err := h.ports.InspectPort(port, proto)
	if err != nil {
		response.Error(w, err.Error(), "PORTS_ERROR")
		return
	}
	response.Success(w, SystemPortInspectData{Port: port, Proto: proto, Bindings: items, Occupied: len(items) > 0})
}

type portKillRequest struct {
	PID    int    `json:"pid"`
	Signal string `json:"signal,omitempty"` // "SIGTERM" or "SIGKILL"
	Port   int    `json:"port,omitempty"`
	Proto  string `json:"proto,omitempty"`
}

// POST /api/system/ports/kill
// @Summary PortsKill (Expert only)
// @Description PortsKill (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemKillResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/ports/kill [post]
func (h *SystemToolsHandler) PortsKill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req portKillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.PID <= 0 {
		response.Error(w, "valid PID required", "INVALID_PID")
		return
	}
	sig := req.Signal
	if strings.TrimSpace(sig) == "" {
		sig = "SIGTERM"
	}
	if err := h.ports.KillProcess(req.PID, sig); err != nil {
		h.log.Error("kill_process", fmt.Sprintf("PID %d (port %d)", req.PID, req.Port), err.Error())
		response.Error(w, err.Error(), "KILL_ERROR")
		return
	}
	h.emitEvent("kill_process", fmt.Sprintf("PID %d (signal %s, port %d)", req.PID, sig, req.Port), "process terminated")
	response.Success(w, SystemKillData{PID: req.PID, Signal: sig, OK: true})
}
