package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/proc"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

const maxBodySize = 1 << 20 // 1 MB

// validTunnelID matches safe tunnel identifiers: starts with a letter,
// followed by up to 31 alphanumeric characters, hyphens, or underscores.
var validTunnelID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,31}$`)

// isValidTunnelID reports whether id is a safe tunnel identifier.
func isValidTunnelID(id string) bool {
	return validTunnelID.MatchString(id)
}

// stateToStatus converts a tunnel State to the status string sent to the frontend.
// Covers all v2 states: running, starting, broken, needs_start, needs_stop, disabled.
// Unknown/legacy states default to "stopped".
func stateToStatus(s tunnel.State) string {
	switch s {
	case tunnel.StateRunning:
		return "running"
	case tunnel.StateStarting:
		return "starting"
	case tunnel.StateBroken:
		return "broken"
	case tunnel.StateNeedsStart:
		return "needs_start"
	case tunnel.StateNeedsStop:
		return "needs_stop"
	case tunnel.StateDisabled:
		return "disabled"
	default:
		return "stopped"
	}
}

// formatHandshake converts time to human-readable format.
func formatHandshake(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// writeConfigFile writes config content to file.
func writeConfigFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0600)
}

// AppLogger defines interface for application logging.
type AppLogger interface {
	Log(category, action, target, message string)
	LogWarn(category, action, target, message string)
	LogError(category, action, target, message, errMsg string)
}

// TunnelService defines the interface for tunnel operations used by API handlers.
type TunnelService interface {
	// CRUD
	List(ctx context.Context) ([]service.TunnelWithStatus, error)
	Get(ctx context.Context, tunnelID string) (*service.TunnelWithStatus, error)
	Create(ctx context.Context, tunnelID, name string, cfg tunnel.Config) error
	Update(ctx context.Context, tunnelID string, cfg tunnel.Config) error
	Delete(ctx context.Context, tunnelID string) error

	// Lifecycle
	Start(ctx context.Context, tunnelID string) error
	Stop(ctx context.Context, tunnelID string) error
	Restart(ctx context.Context, tunnelID string) error

	// Validation
	CheckAddressConflicts(ctx context.Context, tunnelID string) []string

	// State
	GetState(ctx context.Context, tunnelID string) tunnel.StateInfo

	// Settings
	SetEnabled(ctx context.Context, tunnelID string, enabled bool) error
	SetDefaultRoute(ctx context.Context, tunnelID string, enabled bool) error

	// Import
	Import(ctx context.Context, confContent, name string) (*service.TunnelWithStatus, error)

	// Reconcile
	ReconcileInterface(ctx context.Context, ndmsName, layer, level string) error

	// WAN events
	HandleWANUp(ctx context.Context, iface string)
	HandleWANDown(ctx context.Context, iface string)

	// WAN state model
	WANModel() *wan.Model

	// Resolved ISP for auto-mode tunnels
	GetResolvedISP(tunnelID string) string

	// Backend switch
	TeardownForBackendSwitch(ctx context.Context) error
}

// TunnelsHandler handles tunnel CRUD operations.
type TunnelsHandler struct {
	svc           TunnelService
	store         *storage.AWGTunnelStore
	settingsStore *storage.SettingsStore
	pingCheck     PingCheckService
	logger        AppLogger
	traffic       *traffic.History
}

// NewTunnelsHandler creates a new tunnels handler.
func NewTunnelsHandler(svc TunnelService, store *storage.AWGTunnelStore) *TunnelsHandler {
	return &TunnelsHandler{svc: svc, store: store}
}

// SetLoggingService sets the logging service for the handler.
func (h *TunnelsHandler) SetLoggingService(logger LoggingService) {
	h.logger = logger
}

// SetSettingsStore sets the settings store for reading defaults.
func (h *TunnelsHandler) SetSettingsStore(store *storage.SettingsStore) {
	h.settingsStore = store
}

// SetPingCheckService sets the ping check service for monitoring control.
func (h *TunnelsHandler) SetPingCheckService(svc PingCheckService) {
	h.pingCheck = svc
}

// SetTrafficHistory sets the traffic history accumulator.
func (h *TunnelsHandler) SetTrafficHistory(th *traffic.History) {
	h.traffic = th
}

// BuildTunnelResponse builds a consistent tunnel response with stored data.
// Exported so Import and External handlers can reuse the same response format.
func BuildTunnelResponse(r *http.Request, svc TunnelService, store *storage.AWGTunnelStore, id string) (map[string]interface{}, error) {
	t, err := svc.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}

	stored, _ := store.Get(id)

	resp := map[string]interface{}{
		"id":                t.ID,
		"name":              t.Name,
		"type":              "awg",
		"enabled":           t.Enabled,
		"defaultRoute": t.DefaultRoute,
		"ispInterface":      t.ISPInterface,
		"interfaceName":     t.InterfaceName,
		"configPreview":     t.ConfigPreview,
		"state":             t.State.String(),
		"stateInfo":         t.StateInfo,
	}

	if stored != nil {
		resp["interface"] = stored.Interface
		resp["peer"] = stored.Peer
		resp["pingCheck"] = stored.PingCheck
		resp["ispInterfaceLabel"] = stored.ISPInterfaceLabel
	}

	return resp, nil
}

// List returns all tunnels.
func (h *TunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.svc.List(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	type tunnelItem struct {
		ID                        string `json:"id"`
		Name                      string `json:"name"`
		Type                      string `json:"type"`
		Status                    string `json:"status"`
		Enabled                   bool   `json:"enabled"`
		DefaultRoute              bool   `json:"defaultRoute"`
		ISPInterface              string `json:"ispInterface,omitempty"`
		ISPInterfaceLabel         string `json:"ispInterfaceLabel,omitempty"`
		ResolvedISPInterface      string `json:"resolvedIspInterface,omitempty"`
		ResolvedISPInterfaceLabel string `json:"resolvedIspInterfaceLabel,omitempty"`
		Endpoint                  string `json:"endpoint"`
		Address                   string `json:"address"`
		InterfaceName             string `json:"interfaceName"`
		IsDeadByMonitoring        bool   `json:"isDeadByMonitoring"`
		NextRestartAt             string `json:"nextRestartAt,omitempty"`
		HasAddressConflict        bool   `json:"hasAddressConflict"`
		RxBytes                   int64  `json:"rxBytes"`
		TxBytes                   int64  `json:"txBytes"`
		LastHandshake             string `json:"lastHandshake"`
		BackendType               string `json:"backendType,omitempty"`
		AWGVersion                string `json:"awgVersion"`
		MTU                       int    `json:"mtu"`
		StartedAt                 string `json:"startedAt,omitempty"`
	}

	// Build set of addresses used by running tunnels (for conflict detection)
	runningAddresses := make(map[string]string) // address -> tunnelID
	for _, t := range tunnels {
		if t.State == tunnel.StateRunning {
			if stored, _ := h.store.Get(t.ID); stored != nil && stored.Interface.Address != "" {
				runningAddresses[stored.Interface.Address] = t.ID
			}
		}
	}

	items := make([]tunnelItem, 0, len(tunnels))
	for _, t := range tunnels {
		// Get stored tunnel for additional fields
		stored, _ := h.store.Get(t.ID)

		isDeadByMonitoring := false
		var nextRestartAt string
		awgVersion := "wg"
		var endpoint, address string
		var ispInterface, ispInterfaceLabel string
		var resolvedISPInterface, resolvedISPInterfaceLabel string
		var mtu int
		if stored != nil {
			if stored.PingCheck != nil {
				isDeadByMonitoring = stored.PingCheck.IsDeadByMonitoring
				// Compute next forced restart time for dead tunnels
				if isDeadByMonitoring && stored.PingCheck.DeadSince != nil {
					if deadSince, err := time.Parse(time.RFC3339, *stored.PingCheck.DeadSince); err == nil {
						deadInterval := 120 // default
						if h.settingsStore != nil {
							if settings, err := h.settingsStore.Get(); err == nil && settings.PingCheck.Defaults.DeadInterval > 0 {
								deadInterval = settings.PingCheck.Defaults.DeadInterval
							}
						}
						if stored.PingCheck.UseCustomSettings && stored.PingCheck.DeadInterval > 0 {
							deadInterval = stored.PingCheck.DeadInterval
						}
						nextRestartAt = deadSince.Add(time.Duration(deadInterval) * time.Second).Format(time.RFC3339)
					}
				}
			}
			endpoint = stored.Peer.Endpoint
			address = stored.Interface.Address
			mtu = stored.Interface.MTU
			awgVersion = config.ClassifyAWGVersion(&stored.Interface)
			ispInterface = stored.ISPInterface
			ispInterfaceLabel = stored.ISPInterfaceLabel

			// For running tunnels, resolve actual WAN from in-memory tracking
			if t.State == tunnel.StateRunning {
				if resolved := h.svc.GetResolvedISP(t.ID); resolved != "" {
					resolvedISPInterface = resolved
					resolvedISPInterfaceLabel = h.svc.WANModel().GetLabel(resolved)
					if resolvedISPInterfaceLabel == "" {
						// Non-WAN interface (bridge mode etc.) — use stored label from routing page
						resolvedISPInterfaceLabel = ispInterfaceLabel
					}
					if resolvedISPInterfaceLabel == "" {
						// Last resort — show kernel interface name
						resolvedISPInterfaceLabel = resolved
					}
				}
			}
		}

		// Detect address conflict: another running tunnel uses the same address
		hasConflict := false
		if address != "" && t.State != tunnel.StateRunning {
			if conflictID, ok := runningAddresses[address]; ok && conflictID != t.ID {
				hasConflict = true
			}
		}

		var startedAt string
		if stored != nil && stored.StartedAt != "" {
			startedAt = stored.StartedAt
		}
		// Fallback: derive start time from process for running tunnels
		// without persisted StartedAt (e.g. after upgrade, reconcile).
		if startedAt == "" && t.State == tunnel.StateRunning && t.StateInfo.ProcessPID > 0 {
			startedAt = proc.ProcessStartTime(t.StateInfo.ProcessPID)
		}

		items = append(items, tunnelItem{
			ID:                        t.ID,
			Name:                      t.Name,
			Type:                      "awg",
			Status:                    stateToStatus(t.State),
			Enabled:                   t.Enabled,
			DefaultRoute:              t.DefaultRoute,
			ISPInterface:              ispInterface,
			ISPInterfaceLabel:         ispInterfaceLabel,
			ResolvedISPInterface:      resolvedISPInterface,
			ResolvedISPInterfaceLabel: resolvedISPInterfaceLabel,
			Endpoint:                  endpoint,
			Address:             address,
			InterfaceName:       t.InterfaceName,
			IsDeadByMonitoring:  isDeadByMonitoring,
			NextRestartAt:       nextRestartAt,
			HasAddressConflict:  hasConflict,
			RxBytes:             t.StateInfo.RxBytes,
			TxBytes:             t.StateInfo.TxBytes,
			LastHandshake:       formatHandshake(t.StateInfo.LastHandshake),
			BackendType:         t.StateInfo.BackendType,
			AWGVersion:          awgVersion,
			MTU:                 mtu,
			StartedAt:           startedAt,
		})
	}

	// Feed traffic history for running tunnels.
	if h.traffic != nil {
		for _, item := range items {
			if item.Status == "running" {
				h.traffic.Feed(item.ID, item.RxBytes, item.TxBytes)
			}
		}
	}

	response.Success(w, items)
}

// TrafficHistory returns rate history for a tunnel.
// GET /api/tunnels/traffic-history?id=xxx&period=1h
func (h *TunnelsHandler) TrafficHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	period := r.URL.Query().Get("period")
	var since time.Duration
	switch period {
	case "3h":
		since = 3 * time.Hour
	case "24h":
		since = 24 * time.Hour
	default:
		since = time.Hour
	}

	const maxPoints = 360

	if h.traffic == nil {
		response.Success(w, []traffic.Point{})
		return
	}

	pts := h.traffic.Get(id, since, maxPoints)
	if pts == nil {
		pts = []traffic.Point{}
	}
	response.Success(w, pts)
}

// Get returns a single tunnel by ID.
func (h *TunnelsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id)
	if err != nil {
		response.Error(w, err.Error(), "NOT_FOUND")
		return
	}
	response.Success(w, resp)
}

// Create creates a new tunnel.
func (h *TunnelsHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req storage.AWGTunnel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}

	// Generate ID if not provided
	tunnelID := req.ID
	if tunnelID == "" {
		var err error
		tunnelID, err = h.store.NextAvailableID()
		if err != nil {
			response.Error(w, "failed to generate tunnel ID", "CREATE_FAILED")
			return
		}
	} else if !isValidTunnelID(tunnelID) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	// Prepare tunnel data
	req.ID = tunnelID
	req.Type = "awg"
	req.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	req.Status = "stopped"
	if !req.Enabled {
		req.Enabled = true
	}
	req.ISPInterface = "" // auto mode: NDMS picks default gateway
	req.ISPInterfaceLabel = "Определяет роутер"

	// Create NDMS/system resources via service (OS5: OpkgTun, OS4: no-op).
	// Must be called before store.Save so the service's Exists check passes.
	cfg := tunnel.Config{
		ID:      tunnelID,
		Name:    req.Name,
		Address: req.Interface.Address,
		MTU:     req.Interface.MTU,
	}
	if err := h.svc.Create(r.Context(), tunnelID, req.Name, cfg); err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "create", req.Name, "Service create failed", err.Error())
		}
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}

	// If global ping check is enabled and tunnel has no config, add defaults
	if req.PingCheck == nil && h.pingCheck != nil && h.pingCheck.IsEnabled() {
		if defaults := h.getPingCheckDefaults(); defaults != nil {
			req.PingCheck = defaults
		}
	}

	// Save to storage
	if err := h.store.Save(&req); err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "create", req.Name, "Failed to save tunnel", err.Error())
		}
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}

	// Write config file
	confPath := "/opt/etc/awg-manager/" + tunnelID + ".conf"
	confContent := config.Generate(&req)
	if err := writeConfigFile(confPath, confContent); err != nil {
		_ = h.store.Delete(tunnelID)
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "create", req.Name, "Tunnel created")
	}

	// Return the created tunnel
	resp, err := BuildTunnelResponse(r, h.svc, h.store, tunnelID)
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, resp)
}

// Update updates an existing tunnel.
func (h *TunnelsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req storage.AWGTunnel
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}

	// Get existing tunnel
	existing, err := h.store.Get(id)
	if err != nil {
		response.Error(w, "tunnel not found", "NOT_FOUND")
		return
	}

	// Detect changes before merge
	oldPingCheckEnabled := existing.PingCheck != nil && existing.PingCheck.Enabled
	newPingCheckEnabled := req.PingCheck != nil && req.PingCheck.Enabled
	oldISPInterface := existing.ISPInterface

	// Merge changes — preserve fields not sent by partial updates (e.g. routing page).
	req.ID = existing.ID
	req.CreatedAt = existing.CreatedAt
	req.Type = existing.Type
	req.Enabled = existing.Enabled
	req.Status = "stopped" // runtime-only field, always "stopped" in persisted JSON
	req.ResolvedEndpointIP = existing.ResolvedEndpointIP
	req.ActiveWAN = existing.ActiveWAN
	if req.Name == "" {
		req.Name = existing.Name
	}
	if req.Interface.PrivateKey == "" {
		req.Interface = existing.Interface
	}
	if req.Peer.PublicKey == "" {
		req.Peer = existing.Peer
	}
	if !req.DefaultRouteSet {
		req.DefaultRoute = existing.DefaultRoute
		req.DefaultRouteSet = existing.DefaultRouteSet
	}
	if req.ISPInterface == tunnel.ISPInterfaceAuto {
		// Routing page explicitly set "auto-detect" — normalize to empty string.
		req.ISPInterface = ""
		req.ISPInterfaceLabel = ""
	} else if req.ISPInterface == "" {
		// Field not sent (partial update from edit page) — preserve existing.
		req.ISPInterface = existing.ISPInterface
		req.ISPInterfaceLabel = existing.ISPInterfaceLabel
	}
	if req.PingCheck == nil {
		req.PingCheck = existing.PingCheck
		newPingCheckEnabled = oldPingCheckEnabled // no change
	}

	// Update service config before store.Save — service detects name change
	// by comparing cfg.Name against the old name still in the store.
	cfg := tunnel.Config{
		ID:      id,
		Name:    req.Name,
		Address: req.Interface.Address,
		MTU:     req.Interface.MTU,
	}
	_ = h.svc.Update(r.Context(), id, cfg)

	// Save updated tunnel
	if err := h.store.Save(&req); err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "update", req.Name, "Failed to update tunnel", err.Error())
		}
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}

	// Handle pingCheck changes
	if h.pingCheck != nil && h.pingCheck.IsEnabled() {
		stateInfo := h.svc.GetState(r.Context(), id)
		isRunning := stateInfo.State == tunnel.StateRunning

		if oldPingCheckEnabled != newPingCheckEnabled {
			// Toggle: start or stop monitoring
			if newPingCheckEnabled && isRunning {
				h.pingCheck.StartMonitoring(id, req.Name)
			} else if !newPingCheckEnabled {
				h.pingCheck.StopMonitoring(id)
			}
		}
		// Settings-only changes (method, interval, threshold) are picked up
		// automatically by the monitor loop on each tick via getCheckConfig().
	}

	// Regenerate config file
	confPath := "/opt/etc/awg-manager/" + id + ".conf"
	confContent := config.Generate(&req)
	if err := writeConfigFile(confPath, confContent); err != nil {
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}

	// Handle primary connection / ISP interface route changes for running tunnels.
	// Routing is only applied during Start, so restart the tunnel to pick up changes.
	routeChanged := req.ISPInterface != oldISPInterface
	if routeChanged {
		stateInfo := h.svc.GetState(r.Context(), id)
		if stateInfo.State == tunnel.StateRunning {
			if err := h.svc.Restart(r.Context(), id); err != nil {
				if h.logger != nil {
					h.logger.LogWarn(logging.CategoryTunnel, "update", req.Name,
						"Restart for routing changes failed: "+err.Error())
				}
			} else if h.logger != nil {
				h.logger.Log(logging.CategoryTunnel, "update", req.Name,
					"Tunnel restarted to apply routing changes")
			}
		}
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "update", req.Name, "Tunnel updated")
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id)
	if err != nil {
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}
	if warnings := h.svc.CheckAddressConflicts(r.Context(), id); len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

// Delete deletes a tunnel.
func (h *TunnelsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		response.Error(w, "missing id parameter", "MISSING_ID")
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	// Get tunnel name for logging before delete
	var tunnelName string
	if h.logger != nil {
		if t, err := h.svc.Get(r.Context(), id); err == nil {
			tunnelName = t.Name
		}
	}

	// Delete via service (handles stop + config file + store + NDMS cleanup)
	if err := h.svc.Delete(r.Context(), id); err != nil {
		if h.logger != nil {
			h.logger.LogError(logging.CategoryTunnel, "delete", tunnelName, "Failed to delete tunnel", err.Error())
		}
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "DELETE_FAILED")
		return
	}

	// Stop monitoring for deleted tunnel
	if h.pingCheck != nil {
		h.pingCheck.StopMonitoring(id)
	}

	// Clear traffic history for deleted tunnel
	if h.traffic != nil {
		h.traffic.Clear(id)
	}

	if h.logger != nil {
		h.logger.Log(logging.CategoryTunnel, "delete", tunnelName, "Tunnel deleted")
	}

	response.Success(w, map[string]interface{}{
		"success":  true,
		"tunnelId": id,
		"verified": true,
	})
}

// getPingCheckDefaults returns default PingCheck config from global settings.
func (h *TunnelsHandler) getPingCheckDefaults() *storage.TunnelPingCheck {
	if h.settingsStore == nil {
		return nil
	}
	settings, err := h.settingsStore.Get()
	if err != nil {
		return nil
	}
	defaults := settings.PingCheck.Defaults
	return &storage.TunnelPingCheck{
		Enabled:           true,
		UseCustomSettings: false,
		Method:            defaults.Method,
		Target:            defaults.Target,
		Interval:          defaults.Interval,
		DeadInterval:      defaults.DeadInterval,
		FailThreshold:     defaults.FailThreshold,
	}
}
