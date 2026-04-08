package api

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// PingCheckToggleService defines the interface for ping check toggle operations.
type PingCheckToggleService interface {
	StartMonitoringAllRunning()
	StopMonitoringAll()
}

// SettingsHandler handles settings API endpoints.
type SettingsHandler struct {
	store             *storage.SettingsStore
	tunnels           *storage.AWGTunnelStore
	pingCheck         PingCheckToggleService
	pingCheckSnapshot func()
	logsSnapshot      func()
	log               *logging.ScopedLogger
}

// NewSettingsHandler creates a new settings handler.
func NewSettingsHandler(store *storage.SettingsStore, appLogger logging.AppLogger) *SettingsHandler {
	return &SettingsHandler{
		store: store,
		log:   logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubSettings),
	}
}

// SetTunnelStore sets the tunnel store for ping check toggle logic.
func (h *SettingsHandler) SetTunnelStore(tunnels *storage.AWGTunnelStore) {
	h.tunnels = tunnels
}

// SetPingCheckService sets the ping check service for toggle operations.
func (h *SettingsHandler) SetPingCheckService(svc PingCheckToggleService) {
	h.pingCheck = svc
}

// SetPingCheckSnapshot sets the function that publishes a pingcheck snapshot.
func (h *SettingsHandler) SetPingCheckSnapshot(fn func()) { h.pingCheckSnapshot = fn }

// SetLogsSnapshot sets the function that publishes a logs snapshot.
func (h *SettingsHandler) SetLogsSnapshot(fn func()) { h.logsSnapshot = fn }

// Get returns current settings.
func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	settings, err := h.store.Get()
	if err != nil {
		response.Error(w, err.Error(), "SETTINGS_LOAD_ERROR")
		return
	}

	response.Success(w, settings)
}

// Update saves settings.
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	// Get current settings to detect pingCheck toggle change
	oldSettings, err := h.store.Get()
	if err != nil {
		response.Error(w, err.Error(), "SETTINGS_LOAD_ERROR")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var settings storage.Settings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		response.ErrorWithStatus(w, http.StatusBadRequest, "Invalid JSON", "INVALID_JSON")
		return
	}

	// Defense-in-depth for partial updates: Go's json decoder cannot
	// distinguish "field absent" from "field present with zero value", so a
	// payload missing any top-level field decodes to zero. Without preserve
	// logic Save(&settings) would wipe every omitted section (server,
	// pingCheck, logging, etc.). Frontend currently sends full objects via
	// spread, but a single forgotten spread would silently nuke the config.
	//
	// Policy: for every top-level sub-struct or slice field, restore from
	// existing if the incoming value is zero. Top-level bool flags
	// (AuthEnabled, DisableMemorySaving, OnboardingCompleted) cannot be
	// defended this way — "false" and "not sent" are indistinguishable —
	// so the caller is expected to always send the full object.
	if settings.Server == (storage.ServerSettings{}) {
		settings.Server = oldSettings.Server
	}
	if settings.PingCheck == (storage.PingCheckSettings{}) {
		settings.PingCheck = oldSettings.PingCheck
	}
	if settings.Logging == (storage.LoggingSettings{}) {
		settings.Logging = oldSettings.Logging
	}
	if settings.Updates == (storage.UpdateSettings{}) {
		settings.Updates = oldSettings.Updates
	}
	if settings.DNSRoute == (storage.DNSRouteSettings{}) {
		settings.DNSRoute = oldSettings.DNSRoute
	}
	if settings.HiddenSystemTunnels == nil {
		settings.HiddenSystemTunnels = oldSettings.HiddenSystemTunnels
	}
	if settings.ServerInterfaces == nil {
		settings.ServerInterfaces = oldSettings.ServerInterfaces
	}
	if settings.ManagedPolicies == nil {
		settings.ManagedPolicies = oldSettings.ManagedPolicies
	}
	if settings.ManagedServer == nil {
		settings.ManagedServer = oldSettings.ManagedServer
	}
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = oldSettings.SchemaVersion
	}

	// Detect ping check toggle change before saving
	pingCheckWasEnabled := oldSettings.PingCheck.Enabled
	pingCheckNowEnabled := settings.PingCheck.Enabled
	toggleEnabled := !pingCheckWasEnabled && pingCheckNowEnabled
	toggleDisabled := pingCheckWasEnabled && !pingCheckNowEnabled

	// Detect logging toggle change
	loggingWasEnabled := oldSettings.Logging.Enabled
	loggingNowEnabled := settings.Logging.Enabled

	// Update tunnel configs if enabling
	if h.tunnels != nil && toggleEnabled {
		if err := h.enablePingCheckOnAllTunnels(&settings); err != nil {
			response.Error(w, err.Error(), "TOGGLE_ENABLE_ERROR")
			return
		}
	}

	// Save settings BEFORE starting monitoring (so service reads new values)
	if err := h.store.Save(&settings); err != nil {
		response.Error(w, err.Error(), "SETTINGS_SAVE_ERROR")
		return
	}

	// Handle ping check toggle AFTER settings are saved
	if h.tunnels != nil {
		if toggleEnabled {
			if h.pingCheck != nil {
				h.pingCheck.StartMonitoringAllRunning()
			}
		} else if toggleDisabled {
			if h.pingCheck != nil {
				h.pingCheck.StopMonitoringAll()
			}
			if err := h.disablePingCheckOnAllTunnels(); err != nil {
				response.Error(w, err.Error(), "TOGGLE_DISABLE_ERROR")
				return
			}
		}
	}

	// Log specific changes
	if loggingNowEnabled && !loggingWasEnabled {
		h.log.Info("logging", "", "Logging enabled")
	} else if loggingWasEnabled && !loggingNowEnabled {
		h.log.Info("logging", "", "Logging disabled")
	}

	if toggleEnabled {
		h.log.Info("pingcheck", "", "Ping Check enabled")
	} else if toggleDisabled {
		h.log.Info("pingcheck", "", "Ping Check disabled")
	}

	if oldSettings.Server.Port != settings.Server.Port {
		h.log.Info("update", "", "Server port changed")
	}
	if oldSettings.AuthEnabled != settings.AuthEnabled {
		if settings.AuthEnabled {
			h.log.Info("auth", "", "Authentication enabled")
		} else {
			h.log.Warn("auth", "", "Authentication disabled")
		}
	}
	if oldSettings.DisableMemorySaving != settings.DisableMemorySaving {
		if settings.DisableMemorySaving {
			h.log.Info("memory-saving", "", "Memory saving disabled")
		} else {
			h.log.Info("memory-saving", "", "Memory saving enabled")
		}
	}

	if h.pingCheckSnapshot != nil && (toggleEnabled || toggleDisabled) {
		h.pingCheckSnapshot()
	}
	if h.logsSnapshot != nil && loggingNowEnabled != loggingWasEnabled {
		h.logsSnapshot()
	}

	response.Success(w, settings)
}

// enablePingCheckOnAllTunnels adds pingCheck config with defaults to all tunnels.
func (h *SettingsHandler) enablePingCheckOnAllTunnels(settings *storage.Settings) error {
	tunnels, err := h.tunnels.List()
	if err != nil {
		return err
	}

	defaults := settings.PingCheck.Defaults
	for i := range tunnels {
		tunnel := &tunnels[i]
		if tunnel.PingCheck == nil {
			tunnel.PingCheck = &storage.TunnelPingCheck{
				Enabled:       true,
				Method:        defaults.Method,
				Target:        defaults.Target,
				Interval:      defaults.Interval,
				DeadInterval:  defaults.DeadInterval,
				FailThreshold: defaults.FailThreshold,
				MinSuccess:    1,
				Timeout:       5,
				Restart:       true,
			}
		} else {
			tunnel.PingCheck.Enabled = true
		}
		if err := h.tunnels.Save(tunnel); err != nil {
			return err
		}
	}
	return nil
}

// disablePingCheckOnAllTunnels sets pingCheck.enabled=false on all tunnels.
func (h *SettingsHandler) disablePingCheckOnAllTunnels() error {
	tunnels, err := h.tunnels.List()
	if err != nil {
		return err
	}

	for i := range tunnels {
		tunnel := &tunnels[i]
		if tunnel.PingCheck != nil {
			tunnel.PingCheck.Enabled = false
			if err := h.tunnels.Save(tunnel); err != nil {
				return err
			}
		}
	}
	return nil
}
