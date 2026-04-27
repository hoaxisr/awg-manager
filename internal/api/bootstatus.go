package api

import (
	"encoding/json"
	"net/http"
)

// BootStatusHandler serves GET /api/boot-status (public).
type BootStatusHandler struct {
	InstanceID string
}

// NewBootStatusHandler returns a handler that reports boot phase and instance id.
func NewBootStatusHandler(instanceID string) *BootStatusHandler {
	return &BootStatusHandler{InstanceID: instanceID}
}

// Get responds with boot readiness and instance id for frontend restart detection.
//
//	@Summary		Boot status
//	@Description	Public snapshot: initializing flag, phase, instance id.
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Router			/boot-status [get]
func (h *BootStatusHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"initializing":     false,
		"remainingSeconds": 0,
		"phase":            "ready",
		"instanceId":       h.InstanceID,
	})
}
