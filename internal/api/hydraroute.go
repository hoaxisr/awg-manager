package api

import (
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/response"
)

// HydraRouteHandler handles HydraRoute Neo settings API endpoints.
type HydraRouteHandler struct {
	svc *hydraroute.Service
	bus *events.Bus
}

// NewHydraRouteHandler creates a new HydraRoute settings handler.
func NewHydraRouteHandler(svc *hydraroute.Service) *HydraRouteHandler {
	return &HydraRouteHandler{svc: svc}
}

// SetEventBus wires the SSE bus so HR Neo mutations that touch the DNS
// route list (policy order, native rule import, config write) can emit
// resource:invalidated hints for `routing.dnsRoutes`, and so HR daemon
// state changes publish `routing.hydrarouteStatus` hints.
func (h *HydraRouteHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// GetConfig returns the current HydraRoute config.
func (h *HydraRouteHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	cfg, err := h.svc.ReadConfig()
	if err != nil {
		response.Error(w, err.Error(), "CONFIG_READ_ERROR")
		return
	}

	response.Success(w, cfg)
}

// UpdateConfig writes the HydraRoute config.
//
//	@Summary		Update HydraRoute config
//	@Description	Persists the HydraRoute (HrNeo) config and schedules a neo restart. The cached status becomes stale and is invalidated via SSE.
//	@Tags			hydraroute
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		map[string]interface{}	true	"hydraroute.Config"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/hydraroute/config/update [put]
func (h *HydraRouteHandler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}

	var cfg hydraroute.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		response.Error(w, "invalid request body: "+err.Error(), "BAD_REQUEST")
		return
	}

	if err := h.svc.WriteConfig(&cfg); err != nil {
		response.Error(w, err.Error(), "CONFIG_WRITE_ERROR")
		return
	}

	// WriteConfig schedules a neo restart that flips the running flag,
	// so the cached hydraroute status becomes stale.
	publishInvalidated(h.bus, ResourceRoutingHydrarouteStatus, "config-write")
	response.Success(w, cfg)
}

// ListGeoFiles returns all tracked geo data files.
func (h *HydraRouteHandler) ListGeoFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	gds := h.svc.GetGeoData()
	if gds == nil {
		response.Success(w, []hydraroute.GeoFileEntry{})
		return
	}

	response.Success(w, response.MustNotNil(gds.List()))
}

// AddGeoFile downloads and registers a new geo data file.
func (h *HydraRouteHandler) AddGeoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body: "+err.Error(), "BAD_REQUEST")
		return
	}

	gds := h.svc.GetGeoData()
	if gds == nil {
		response.Error(w, "geo data store not initialized", "NOT_INITIALIZED")
		return
	}

	entry, err := gds.Download(req.Type, req.URL)
	if err != nil {
		response.Error(w, err.Error(), "GEO_DOWNLOAD_ERROR")
		return
	}

	if err := h.svc.SyncGeoFilesToConfig(); err != nil {
		response.Error(w, "downloaded but failed to sync config: "+err.Error(), "SYNC_ERROR")
		return
	}

	response.Success(w, entry)
}

// DeleteGeoFile removes a tracked geo data file.
//
//	@Summary		Delete HydraRoute geo file
//	@Description	Removes the tracked geo data file at the given path and re-syncs the geo file list to config.
//	@Tags			hydraroute
//	@Produce		json
//	@Security		CookieAuth
//	@Param			path	query		string	true	"Filesystem path of the geo file"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/hydraroute/geo-files/delete [delete]
func (h *HydraRouteHandler) DeleteGeoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		response.Error(w, "path query parameter is required", "BAD_REQUEST")
		return
	}

	gds := h.svc.GetGeoData()
	if gds == nil {
		response.Error(w, "geo data store not initialized", "NOT_INITIALIZED")
		return
	}

	if err := gds.Delete(path); err != nil {
		response.Error(w, err.Error(), "GEO_DELETE_ERROR")
		return
	}

	if err := h.svc.SyncGeoFilesToConfig(); err != nil {
		response.Error(w, "deleted but failed to sync config: "+err.Error(), "SYNC_ERROR")
		return
	}

	response.Success(w, nil)
}

// UpdateGeoFile re-downloads a geo data file (or all files if path is empty).
func (h *HydraRouteHandler) UpdateGeoFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body: "+err.Error(), "BAD_REQUEST")
		return
	}

	gds := h.svc.GetGeoData()
	if gds == nil {
		response.Error(w, "geo data store not initialized", "NOT_INITIALIZED")
		return
	}

	if req.Path == "" {
		count, err := gds.UpdateAll()
		if err != nil {
			response.Error(w, err.Error(), "GEO_UPDATE_ERROR")
			return
		}

		if err := h.svc.SyncGeoFilesToConfig(); err != nil {
			response.Error(w, "updated but failed to sync config: "+err.Error(), "SYNC_ERROR")
			return
		}

		response.Success(w, map[string]int{"updated": count})
		return
	}

	entry, err := gds.Update(req.Path)
	if err != nil {
		response.Error(w, err.Error(), "GEO_UPDATE_ERROR")
		return
	}

	if err := h.svc.SyncGeoFilesToConfig(); err != nil {
		response.Error(w, "updated but failed to sync config: "+err.Error(), "SYNC_ERROR")
		return
	}

	response.Success(w, entry)
}

// GetGeoTags returns the tag list for a specific geo data file.
func (h *HydraRouteHandler) GetGeoTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		response.Error(w, "path query parameter is required", "BAD_REQUEST")
		return
	}

	gds := h.svc.GetGeoData()
	if gds == nil {
		response.Error(w, "geo data store not initialized", "NOT_INITIALIZED")
		return
	}

	tags, err := gds.GetTags(path)
	if err != nil {
		response.Error(w, err.Error(), "GEO_TAGS_ERROR")
		return
	}

	response.Success(w, response.MustNotNil(tags))
}

// GetIpsetUsage returns the current ipset usage per kernel interface.
func (h *HydraRouteHandler) GetIpsetUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	usage, err := h.svc.CalculateIpsetUsage()
	if err != nil {
		response.Error(w, err.Error(), "IPSET_USAGE_ERROR")
		return
	}

	response.Success(w, usage)
}

// SetPolicyOrder updates the PolicyOrder in hrneo.conf.
func (h *HydraRouteHandler) SetPolicyOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	var req struct {
		Order []string `json:"order"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body: "+err.Error(), "BAD_REQUEST")
		return
	}

	if err := h.svc.SetPolicyOrder(req.Order); err != nil {
		response.Error(w, err.Error(), "POLICY_ORDER_ERROR")
		return
	}

	publishInvalidated(h.bus, ResourceRoutingDnsRoutes, "policy-order")
	// Policy-order changes trigger a neo restart too.
	publishInvalidated(h.bus, ResourceRoutingHydrarouteStatus, "policy-order")
	response.Success(w, map[string][]string{"order": req.Order})
}

// GetOversizedTags returns the list of geoip tags HR Neo excluded plus
// the current IpsetMaxElem so the frontend can render the 'Отключённые
// теги' pane and enforce picker limits.
func (h *HydraRouteHandler) GetOversizedTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	status := h.svc.GetStatus()
	if !status.Installed {
		response.Success(w, map[string]interface{}{
			"installed": false,
			"maxelem":   0,
			"tags":      []hydraroute.OversizedTag{},
		})
		return
	}

	cfg, err := h.svc.ReadConfig()
	if err != nil {
		response.Error(w, err.Error(), "CONFIG_READ_ERROR")
		return
	}

	tags, err := h.svc.OversizedTags(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "OVERSIZED_ERROR")
		return
	}

	response.Success(w, map[string]interface{}{
		"installed": true,
		"maxelem":   cfg.EffectiveMaxElem(),
		"tags":      response.MustNotNil(tags),
	})
}
