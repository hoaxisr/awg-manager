package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
)

// Службы Entware (init.d): список, старт/стоп, чтение и правка скриптов.
// GET /api/system/services/list
// @Summary ServicesList (Expert only)
// @Description ServicesList (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServicesListResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/list [get]
func (h *SystemToolsHandler) ServicesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	items, err := h.services.List()
	if err != nil {
		response.Error(w, err.Error(), "SERVICES_ERROR")
		return
	}
	response.Success(w, items)
}

type serviceActionRequest struct {
	Script string `json:"script"`
	Action string `json:"action"`
}

// POST /api/system/services/action
// @Summary ServicesAction (Expert only)
// @Description ServicesAction (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceActionResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/action [post]
func (h *SystemToolsHandler) ServicesAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	out, err := h.services.RunAction(req.Script, req.Action)
	h.emitEvent(req.Action, req.Script, out)
	if err != nil {
		response.Success(w, SystemServiceActionData{Output: out, OK: false, Error: err.Error()})
		return
	}
	response.Success(w, SystemServiceActionData{Output: out, OK: true})
}

// GET /api/system/services/get?script=/opt/etc/init.d/S90name
// @Summary ServicesGetScript (Expert only)
// @Description ServicesGetScript (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceScriptResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/get [get]
func (h *SystemToolsHandler) ServicesGetScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	script := r.URL.Query().Get("script")
	if script == "" {
		response.Error(w, "missing script parameter", "INVALID_PARAMS")
		return
	}
	content, err := h.services.ReadScript(script)
	if err != nil {
		response.Error(w, err.Error(), "READ_ERROR")
		return
	}
	response.Success(w, SystemServiceScriptData{Script: script, Content: content})
}

type serviceSaveRequest struct {
	ScriptName string `json:"scriptName"` // e.g. "S90my-daemon"
	Content    string `json:"content"`    // shell script body
}

// POST /api/system/services/save
// @Summary ServicesSaveScript (Expert only)
// @Description ServicesSaveScript (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceSavedResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/save [post]
func (h *SystemToolsHandler) ServicesSaveScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.ScriptName == "" {
		response.Error(w, "scriptName is required", "INVALID_PARAMS")
		return
	}
	if req.Content == "" {
		response.Error(w, "content cannot be empty", "INVALID_PARAMS")
		return
	}

	fullPath, err := h.services.SaveScript(req.ScriptName, req.Content)
	if err != nil {
		response.Error(w, err.Error(), "SAVE_ERROR")
		return
	}

	h.emitEvent("save", req.ScriptName, fullPath)
	response.Success(w, SystemServiceSavedData{OK: true, Script: fullPath})
}

type serviceDeleteRequest struct {
	Script string `json:"script"`
}

// POST /api/system/services/delete
// @Summary ServicesDeleteScript (Expert only)
// @Description ServicesDeleteScript (Expert only)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemOKFlagResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/delete [post]
func (h *SystemToolsHandler) ServicesDeleteScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.Script == "" {
		response.Error(w, "script is required", "INVALID_PARAMS")
		return
	}

	if err := h.services.DeleteScript(req.Script); err != nil {
		response.Error(w, err.Error(), "DELETE_ERROR")
		return
	}

	h.emitEvent("delete", req.Script, "service deleted")
	response.Success(w, SystemOKFlagData{OK: true})
}

type serviceToggleEnableRequest struct {
	Script  string `json:"script"`
	Enabled bool   `json:"enabled"`
}

// POST /api/system/services/toggle-enable
// @Summary ServicesToggleEnable (Expert only)
// @Description Toggle autostart for a service (rename Sxx <-> Kxx)
// @Tags system,expert
// @Accept json
// @Produce json
// @Security CookieAuth
// @Success 200 {object} SystemServiceToggleEnableResponse
// @Failure 400 {object} APIErrorEnvelope
// @Failure 403 {object} APIErrorEnvelope
// @Failure 500 {object} APIErrorEnvelope
// @Router /system/services/toggle-enable [post]
func (h *SystemToolsHandler) ServicesToggleEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	var req serviceToggleEnableRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid JSON", "INVALID_JSON")
		return
	}
	if req.Script == "" {
		response.Error(w, "script is required", "INVALID_PARAMS")
		return
	}

	newScript, err := h.services.ToggleEnable(req.Script, req.Enabled)
	if err != nil {
		response.Error(w, err.Error(), "TOGGLE_ERROR")
		return
	}

	h.emitEvent("toggle_enable", req.Script, fmt.Sprintf("enabled=%v newPath=%s", req.Enabled, newScript))
	response.Success(w, SystemServiceToggleEnableData{
		OK:        true,
		NewScript: newScript,
		Enabled:   req.Enabled,
	})
}
