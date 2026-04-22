package api

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// PingCheckService defines the interface for ping check operations.
// Uses pingcheck types directly — no adapter needed.
type PingCheckService interface {
	GetStatus() []pingcheck.TunnelStatus
	GetLogs() []pingcheck.LogEntry
	GetTunnelLogs(tunnelID string) []pingcheck.LogEntry
	ClearLogs()
	CheckAllNow()
	IsEnabled() bool
	StartMonitoringAllRunning()
	StopMonitoringAll()
	Stop()
	// Per-tunnel monitoring control
	StartMonitoring(tunnelID, tunnelName string, skipConfigure ...bool)
	StopMonitoring(tunnelID string)
	GetTunnelPingStatus(tunnelID string) pingcheck.TunnelPingInfo
}

// PingCheckHandler handles ping check API endpoints.
type PingCheckHandler struct {
	service PingCheckService
	tunnels *storage.AWGTunnelStore
	nwgOp   *nwg.OperatorNativeWG
	log     *logging.ScopedLogger
	bus     *events.Bus
}

// NewPingCheckHandler creates a new ping check handler.
func NewPingCheckHandler(service PingCheckService, tunnels *storage.AWGTunnelStore, nwgOp *nwg.OperatorNativeWG, appLogger logging.AppLogger) *PingCheckHandler {
	return &PingCheckHandler{
		service: service,
		tunnels: tunnels,
		nwgOp:   nwgOp,
		log:     logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubPingcheck),
	}
}

// SetEventBus sets the event bus for SSE invalidation hints.
func (h *PingCheckHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// PublishSnapshot publishes a resource:invalidated hint for pingcheck so
// subscribed polling stores refetch. The legacy `snapshot:pingcheck` event
// was removed (Task 12) — the frontend status list is now a polling store.
// Logs are still pushed via `pingcheck:log` stream, untouched.
func (h *PingCheckHandler) PublishSnapshot() {
	publishInvalidated(h.bus, ResourcePingcheck, "snapshot")
}

// GetStatus returns the current status of all monitored tunnels.
func (h *PingCheckHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	statuses := h.service.GetStatus()
	if statuses == nil {
		statuses = []pingcheck.TunnelStatus{}
	}
	response.Success(w, map[string]interface{}{
		"enabled": h.service.IsEnabled(),
		"tunnels": statuses,
	})
}

// GetLogs returns ping check logs.
func (h *PingCheckHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	tunnelID := r.URL.Query().Get("tunnelId")
	if tunnelID != "" && !isValidTunnelID(tunnelID) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	var logs []pingcheck.LogEntry
	if tunnelID != "" {
		logs = h.service.GetTunnelLogs(tunnelID)
	} else {
		logs = h.service.GetLogs()
	}
	if logs == nil {
		logs = []pingcheck.LogEntry{}
	}

	response.Success(w, logs)
}

// ClearLogs removes all ping check log entries.
func (h *PingCheckHandler) ClearLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	h.service.ClearLogs()
	h.log.Info("pingcheck", "", "Ping check logs cleared")
	h.PublishSnapshot()

	response.Success(w, map[string]string{"message": "Logs cleared"})
}

// CheckNow triggers an immediate check on all tunnels.
func (h *PingCheckHandler) CheckNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	if h.service == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "Ping check service not available", "SERVICE_UNAVAILABLE")
		return
	}

	h.service.CheckAllNow()
	h.log.Info("check-now", "", "Manual ping check triggered")

	response.Success(w, map[string]string{"message": "Check triggered"})
}

// GetTunnelPingCheckStatus returns NDMS ping-check status for a single nativewg tunnel.
// GET /api/tunnels/pingcheck?id=xxx
func (h *PingCheckHandler) GetTunnelPingCheckStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" || !isValidTunnelID(id) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid or missing tunnel ID", "INVALID_ID")
		return
	}

	if h.nwgOp == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "NativeWG not available", "NWG_UNAVAILABLE")
		return
	}

	stored, err := h.tunnels.Get(id)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, "tunnel not found", "NOT_FOUND")
		return
	}

	// Skip NDMS query if pingcheck is not configured for this tunnel
	if stored.PingCheck == nil || !stored.PingCheck.Enabled {
		response.Success(w, map[string]bool{"exists": false})
		return
	}

	status, err := h.nwgOp.GetPingCheckStatus(r.Context(), stored)
	if err != nil {
		response.Error(w, err.Error(), "PINGCHECK_STATUS_ERROR")
		return
	}
	// NDMS /show/ping-check may omit restart flag in status payload.
	// Use persisted tunnel config so UI toggle reflects the actual saved intent.
	if stored.PingCheck != nil {
		status.Restart = stored.PingCheck.Restart
	}

	response.Success(w, status)
}

// ConfigureTunnelPingCheck creates/updates NDMS ping-check for a nativewg tunnel.
// POST /api/tunnels/pingcheck?id=xxx
func (h *PingCheckHandler) ConfigureTunnelPingCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" || !isValidTunnelID(id) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid or missing tunnel ID", "INVALID_ID")
		return
	}

	if h.nwgOp == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "NativeWG not available", "NWG_UNAVAILABLE")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var cfg ndms.PingCheckConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid JSON", "INVALID_JSON")
		return
	}

	if cfg.Host == "" {
		response.ErrorWithStatus(w, http.StatusBadRequest, "host is required", "MISSING_HOST")
		return
	}

	stored, err := h.tunnels.Get(id)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, "tunnel not found", "NOT_FOUND")
		return
	}

	// Apply to NDMS first — only persist to storage after success
	if err := h.nwgOp.ConfigurePingCheck(r.Context(), stored, cfg); err != nil {
		response.Error(w, err.Error(), "PINGCHECK_CONFIGURE_ERROR")
		return
	}

	// Save config to storage after NDMS success
	stored.PingCheck = &storage.TunnelPingCheck{
		Enabled:       true,
		Method:        cfg.Mode,
		Target:        cfg.Host,
		Interval:      cfg.UpdateInterval,
		FailThreshold: cfg.MaxFails,
		MinSuccess:    cfg.MinSuccess,
		Timeout:       cfg.Timeout,
		Port:          cfg.Port,
		Restart:       cfg.Restart,
	}
	if err := h.tunnels.Save(stored); err != nil {
		response.Error(w, "failed to save config", "SAVE_ERROR")
		return
	}

	// Start NativeWG poll-based monitor for log generation.
	// NDMS config already applied above — skip redundant configure.
	h.service.StartMonitoring(id, stored.Name, true)

	h.log.Info("ping-check-configure", id, "Ping-check configured: host="+cfg.Host)
	h.PublishSnapshot()

	response.Success(w, map[string]bool{"success": true})
}

// RemoveTunnelPingCheck removes NDMS ping-check for a nativewg tunnel.
// POST /api/tunnels/pingcheck/remove?id=xxx
func (h *PingCheckHandler) RemoveTunnelPingCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" || !isValidTunnelID(id) {
		response.ErrorWithStatus(w, http.StatusBadRequest, "invalid or missing tunnel ID", "INVALID_ID")
		return
	}

	if h.nwgOp == nil {
		response.ErrorWithStatus(w, http.StatusServiceUnavailable, "NativeWG not available", "NWG_UNAVAILABLE")
		return
	}

	stored, err := h.tunnels.Get(id)
	if err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, "tunnel not found", "NOT_FOUND")
		return
	}

	// Remove from NDMS
	if err := h.nwgOp.RemovePingCheck(r.Context(), stored); err != nil {
		response.Error(w, err.Error(), "PINGCHECK_REMOVE_ERROR")
		return
	}

	// Stop NativeWG poll-based monitor.
	h.service.StopMonitoring(id)

	// Update storage
	if stored.PingCheck != nil {
		stored.PingCheck.Enabled = false
	}
	_ = h.tunnels.Save(stored)

	h.log.Info("ping-check-remove", id, "Ping-check removed")
	h.PublishSnapshot()

	response.Success(w, map[string]bool{"success": true})
}
