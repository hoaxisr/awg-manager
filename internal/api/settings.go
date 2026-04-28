package api

import (
	"crypto/rand"
	"fmt"
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
//
//	@Summary		Get settings
//	@Description	Returns the full Settings object (server, pingCheck, logging, dnsRoute, managed, apiKey, ...).
//	@Tags			settings
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/settings [get]
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
//
//	@Summary		Update settings
//	@Description	Persists Settings. Sub-structs (server, pingCheck, logging, dnsRoute, managedServers, ...) preserved when zero/nil. ApiKey preserved when empty (rotate via /settings/regenerate-api-key). Top-level bool flags (authEnabled, disableMemorySaving, onboardingCompleted) MUST be sent on every save.
//	@Tags			settings
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"Settings"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/settings [post]
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	settings, ok := parseJSON[storage.Settings](w, r, http.MethodPost)
	if !ok {
		return
	}

	// Get current settings to detect pingCheck toggle change
	oldSettings, err := h.store.Get()
	if err != nil {
		response.Error(w, err.Error(), "SETTINGS_LOAD_ERROR")
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
	if settings.DNSRoute == (storage.DNSRouteSettings{}) {
		settings.DNSRoute = oldSettings.DNSRoute
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
	if settings.ManagedServers == nil {
		settings.ManagedServers = oldSettings.ManagedServers
	}
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = oldSettings.SchemaVersion
	}
	// ApiKey is omitempty: a payload that omits the field decodes to "".
	// Preserve the existing key in that case so a partial update can't
	// silently revoke API access. To ROTATE the key the caller sends a new
	// non-empty value; to CLEAR it the caller currently has no path —
	// matches the behavior of other secret fields.
	if settings.ApiKey == "" {
		settings.ApiKey = oldSettings.ApiKey
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

// RegenerateApiKey generates a fresh UUID v4 server-side, persists it
// into Settings.ApiKey, and returns the updated Settings. Lives on the
// backend (not in browser via crypto.randomUUID) because the UI is
// served over plain HTTP and the WebCrypto API is unavailable in
// non-secure contexts.
//
//	@Summary		Regenerate API key
//	@Description	Generates a fresh UUID v4 via crypto/rand, stores it into Settings.ApiKey, and returns the updated Settings. The new key takes effect immediately as a `Authorization: Bearer <key>` substitute for the session cookie.
//	@Tags			settings
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/settings/regenerate-api-key [post]
func (h *SettingsHandler) RegenerateApiKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.ErrorWithStatus(w, http.StatusMethodNotAllowed, "Method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	key, err := generateUUIDv4()
	if err != nil {
		response.Error(w, "failed to generate key: "+err.Error(), "API_KEY_GENERATE_ERROR")
		return
	}

	settings, err := h.store.Get()
	if err != nil {
		response.Error(w, err.Error(), "SETTINGS_LOAD_ERROR")
		return
	}
	settings.ApiKey = key
	if err := h.store.Save(settings); err != nil {
		response.Error(w, err.Error(), "SETTINGS_SAVE_ERROR")
		return
	}

	h.log.Info("api-key", "", "API key regenerated")
	response.Success(w, settings)
}

// generateUUIDv4 produces an RFC 4122 v4 UUID using crypto/rand.
// Format: 8-4-4-4-12 lowercase hex.
func generateUUIDv4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
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
