package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

// writeConfigFile writes config content to file.
func writeConfigFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0600)
}

// List returns all tunnels.
//
//	@Summary		List tunnels
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	TunnelListResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/list [get]
func (h *TunnelsHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	items, err := h.listItems(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	response.Success(w, items)
}

// GetAll returns the composite tunnels snapshot ({tunnels, external,
// system}) the frontend polls instead of listening to a legacy
// snapshot SSE event.
// GET /api/tunnels/all
//
//	@Summary		Composite tunnels snapshot
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	TunnelsAllResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/all [get]
func (h *TunnelsHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	h.writeAll(w, r)
}

// parseTrafficPeriod maps the period query value to a duration.
func parseTrafficPeriod(raw string) (time.Duration, bool) {
	switch raw {
	case "5m":
		return 5 * time.Minute, true
	case "10m":
		return 10 * time.Minute, true
	case "30m":
		return 30 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "3h":
		return 3 * time.Hour, true
	case "6h":
		return 6 * time.Hour, true
	case "12h":
		return 12 * time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

// Traffic returns rate history + aggregates for a single tunnel.
// GET /api/tunnels/traffic?id=<tunnelID>&period=5m|10m|30m|1h|3h|6h|12h|24h
//
// Only a fixed set of short/long-range presets is accepted — anything
// else returns 400. 1h is what the card chart fetches on mount to
// backfill before SSE takes over; the detail modal can request any of
// the supported presets.
//
// data.stats.volumeRx and data.stats.volumeTx are byte estimates for the
// selected window from raw in-memory samples (zero if fewer than two samples).
//
//	@Summary		Tunnel traffic history
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		query	string	true	"Tunnel id"
//	@Param			period	query	string	true	"5m, 10m, 30m, 1h, 3h, 6h, 12h, or 24h"
//	@Success		200	{object}	TunnelTrafficResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/traffic [get]
func (h *TunnelsHandler) Traffic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	// Read-only handler reading an in-memory map: tolerate non-AWG ids
	// (singbox subscription tags include emoji/spaces). Sanity-check still
	// rejects binary garbage and oversized ids. Unknown id → 200 + empty.
	if len(id) > 256 || !utf8.ValidString(id) || strings.ContainsFunc(id, func(r rune) bool { return r < 0x20 }) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	since, ok := parseTrafficPeriod(r.URL.Query().Get("period"))
	if !ok {
		response.Error(w, "period must be one of: 5m, 10m, 30m, 1h, 3h, 6h, 12h, 24h", "INVALID_PERIOD")
		return
	}

	const maxPoints = 360

	resp := map[string]any{
		"points": []traffic.Point{},
		"stats":  traffic.Stats{},
	}
	if h.traffic != nil {
		pts := h.traffic.Get(id, since, maxPoints)
		if pts == nil {
			pts = []traffic.Point{}
		}
		resp["points"] = pts
		resp["stats"] = h.traffic.Stats(id, since)
	}
	response.Success(w, resp)
}

// Get returns a single tunnel by ID.
//
//	@Summary		Get tunnel
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelDetailResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/get [get]
func (h *TunnelsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	if stored, err := h.store.Get(id); err == nil && stored != nil && stored.Backend == backendWdttRaw {
		response.Success(w, h.buildWdttRawResponse(stored))
		return
	}

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id, h.quiescentFor(id))
	if err != nil {
		response.Error(w, err.Error(), "NOT_FOUND")
		return
	}
	response.Success(w, resp)
}

// Create creates a new tunnel.
//
//	@Summary		Create tunnel
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/create [post]
func (h *TunnelsHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[storage.AWGTunnel](w, r, http.MethodPost)
	if !ok {
		return
	}

	if err := config.ValidateKeepaliveForBackend(req.Peer.PersistentKeepalive, req.Backend); err != nil {
		response.Error(w, err.Error(), "INVALID_KEEPALIVE")
		return
	}

	// Validate endpoint resolves
	if err := config.ValidateAWG3(&req.Interface.AWGObfuscation); err != nil {
		response.Error(w, err.Error(), "INVALID_AWG3")
		return
	}
	if req.Peer.Endpoint != "" {
		if _, _, err := netutil.ResolveEndpoint(req.Peer.Endpoint); err != nil {
			response.Error(w, "endpoint не резолвится: "+err.Error(), "INVALID_ENDPOINT")
			return
		}
	}

	// Generate ID if not provided
	tunnelID := req.ID
	if tunnelID == "" {
		var err error
		tunnelID, err = h.store.NextAvailableID(req.Backend)
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
	// Gate from before the NDMS Create call through publishTunnelList so
	// the hook-driven snapshot rebroadcast sees the finalized store state.
	// Only relevant for NativeWG (kernel backend doesn't touch NDMS at
	// Create time), but always entering is cheap and keeps the flow
	// symmetric. The final publishTunnelList at the bottom triggers its
	// own snapshot refresh AFTER gate exit.
	if h.selfCreateGate != nil {
		h.selfCreateGate.EnterSelfCreate()
		defer h.selfCreateGate.ExitSelfCreate()
	}
	if err := h.svc.Create(r.Context(), tunnelID, req.Name, cfg, &req); err != nil {
		h.log.Warn("create", req.Name, "Service create failed: "+err.Error())
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}

	// Add per-tunnel ping check defaults if not specified
	if req.PingCheck == nil && h.pingCheck != nil {
		req.PingCheck = &storage.TunnelPingCheck{
			Enabled:       false,
			Method:        "icmp",
			Target:        "8.8.8.8",
			Interval:      45,
			DeadInterval:  120,
			FailThreshold: 3,
			MinSuccess:    1,
			Timeout:       5,
			Restart:       true,
		}
	}

	// Save to storage
	if err := h.store.Save(&req); err != nil {
		h.log.Warn("create", req.Name, "Failed to save tunnel: "+err.Error())
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

	h.log.Info("create", req.Name, "Tunnel created")
	h.publishTunnelList(r.Context())

	// Return the created tunnel
	resp, err := BuildTunnelResponse(r, h.svc, h.store, tunnelID, h.quiescentFor(tunnelID))
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, resp)
}

// Update updates an existing tunnel.
//
//	@Summary		Update tunnel
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/update [post]
func (h *TunnelsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}
	req, ok := parseJSON[storage.AWGTunnel](w, r, http.MethodPost)
	if !ok {
		return
	}

	// Get existing tunnel
	existing, err := h.store.Get(id)
	if err != nil {
		response.Error(w, "tunnel not found", "NOT_FOUND")
		return
	}

	if existing.Backend == backendWdttRaw {
		if req.Name != "" && req.Name != existing.Name {
			// Имя зеркальной записи — производная конфига инстанса: зеркало
			// перезаписывает его на каждом объявлении (exitreg/mirror.go:105).
			// Принять переименование здесь значит подтвердить правку, которую
			// ближайшее объявление молча откатит.
			response.Error(w, "имя raw-записи задаётся инстансом WDTT — переименуйте инстанс", "WDTT_RAW_NAME_READONLY")
			return
		}
		updated := *existing
		if req.ConnectivityCheck != nil {
			updated.ConnectivityCheck = req.ConnectivityCheck
			if updated.ConnectivityCheck.Method == "" {
				updated.ConnectivityCheck.Method = "http"
			}
		}
		if err := h.store.Save(&updated); err != nil {
			response.Error(w, err.Error(), "UPDATE_FAILED")
			return
		}
		h.log.Info("update", updated.Name, "WDTT raw metadata updated")
		h.publishTunnelList(r.Context())
		response.Success(w, h.buildWdttRawResponse(&updated))
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
	req.ActiveWAN = existing.ActiveWAN
	req.Backend = existing.Backend
	req.NWGIndex = existing.NWGIndex
	if req.Name == "" {
		req.Name = existing.Name
	}
	mergeInterfaceWhitelist(&req, existing)
	mergePeerWhitelist(&req, existing)
	if err := config.ValidateKeepaliveForBackend(req.Peer.PersistentKeepalive, req.Backend); err != nil {
		response.Error(w, err.Error(), "INVALID_KEEPALIVE")
		return
	}
	if err := config.ValidateAWG3(&req.Interface.AWGObfuscation); err != nil {
		response.Error(w, err.Error(), "INVALID_AWG3")
		return
	}
	// Кэш резолва валиден только для endpoint'а, под которым был получен:
	// перенос через смену endpoint'а подставлял бы DNS-фолбэкам адрес
	// ПРЕЖНЕГО имени. Сравнение — после mergePeerWhitelist (partial-update
	// без Peer наследует endpoint из existing).
	if req.Peer.Endpoint == existing.Peer.Endpoint {
		req.ResolvedEndpointIP = existing.ResolvedEndpointIP
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
	// NativeWG: convert ISPInterface to NDMS name for "connect via".
	// Frontend sends kernel names (from WAN model), but NDMS needs NDMS IDs.
	if req.Backend == "nativewg" && req.ISPInterface != "" {
		if tunnel.IsTunnelRoute(req.ISPInterface) {
			// Tunnel chaining: resolve parent tunnel's NDMS interface name.
			parentID := tunnel.TunnelRouteID(req.ISPInterface)
			if parent, err := h.store.Get(parentID); err == nil {
				if parent.Backend == "nativewg" {
					req.ISPInterface = nwg.NewNWGNames(parent.NWGIndex).NDMSName
				} else {
					req.ISPInterface = tunnel.NewNames(parentID).NDMSName
				}
			}
		} else if ndmsID := h.svc.WANModel().IDFor(req.ISPInterface); ndmsID != "" {
			req.ISPInterface = ndmsID
		}
	}

	if req.PingCheck == nil {
		req.PingCheck = existing.PingCheck
		newPingCheckEnabled = oldPingCheckEnabled // no change
	}
	if req.ConnectivityCheck == nil {
		req.ConnectivityCheck = existing.ConnectivityCheck
	} else if req.ConnectivityCheck.Method == "" && (req.ConnectivityCheck.PingTarget == "" || req.ConnectivityCheck.Method != "ping") {
		// Если поля пустые или метод не "ping", использовать существующие настройки
		if existing.ConnectivityCheck != nil {
			req.ConnectivityCheck = existing.ConnectivityCheck
		}
	}

	// Validate endpoint resolves (only if changed)
	if req.Peer.Endpoint != existing.Peer.Endpoint {
		if _, _, err := netutil.ResolveEndpoint(req.Peer.Endpoint); err != nil {
			response.Error(w, "endpoint не резолвится: "+err.Error(), "INVALID_ENDPOINT")
			return
		}
	}

	// Service handles runtime RCI based on the diff between existing
	// (pre-merge snapshot) and req (post-merge state). Storage save
	// happens AFTER service runs — handler is the sole writer. Fail-closed:
	// if the service can't apply the change to the running interface,
	// we don't persist it either, otherwise on-disk state would diverge
	// from the live state.
	if err := h.svc.Update(r.Context(), existing, &req); err != nil {
		h.log.Warn("update", req.Name, "Service update failed: "+err.Error())
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}

	// Save updated tunnel
	if err := h.store.Save(&req); err != nil {
		h.log.Warn("update", req.Name, "Failed to update tunnel: "+err.Error())
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}

	// Sync orchestrator's in-memory cache with the new storage state
	// before we hit StopMonitoring / RestartEvent etc. — decide() reads
	// the cache, and a stale PingCheck flag here causes later events to
	// emit phantom ActionRemovePingCheck that warns NDMS.
	if h.orch != nil {
		h.orch.RefreshTunnelState(id)
	}

	// Handle pingCheck changes
	if h.pingCheck != nil {
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
			if err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
				Type: orchestrator.EventRestart, Tunnel: id,
			}); err != nil {
				h.log.Warn("update", req.Name, "Restart for routing changes failed: "+err.Error())
			} else {
				h.log.Info("update", req.Name, "Tunnel restarted to apply routing changes")
			}
		}
	}

	h.log.Info("update", req.Name, "Tunnel updated")
	h.publishTunnelList(r.Context())

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id, h.quiescentFor(id))
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
//
//	@Summary		Delete tunnel
//	@Tags			tunnels
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	TunnelDeleteResponse
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		409	{object}	TunnelReferencedResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/delete [post]
func (h *TunnelsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	if stored, err := h.store.Get(id); err == nil && stored != nil && stored.Backend == backendWdttRaw {
		tunnelName := stored.Name
		if err := h.store.Delete(id); err != nil {
			response.Error(w, err.Error(), "DELETE_FAILED")
			return
		}
		if h.traffic != nil {
			h.traffic.Clear(id)
		}
		h.log.Info("delete", tunnelName, "WDTT raw registry entry removed")
		h.publishTunnelList(r.Context())
		response.Success(w, map[string]interface{}{
			"success":  true,
			"tunnelId": id,
			"verified": true,
		})
		return
	}

	// Get tunnel name for logging before delete
	var tunnelName string
	if t, err := h.svc.Get(r.Context(), id); err == nil {
		tunnelName = t.Name
	}

	// Route through svc.Delete so the refuse-on-delete check fires
	// (returns ErrTunnelReferenced if the tunnel's awg-{id} tag is
	// referenced by deviceproxy selector or any router rule).
	if err := h.svc.Delete(r.Context(), id); err != nil {
		var refErr service.ErrTunnelReferenced
		if errors.As(err, &refErr) {
			h.log.Info("delete", tunnelName, "Refused: "+refErr.Error())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(TunnelReferencedResponse{
				Error: "tunnel_referenced",
				Details: TunnelReferencedDetails{
					TunnelID:    refErr.TunnelID,
					DeviceProxy: refErr.DeviceProxy,
					RouterRules: refErr.RouterRules,
					RouterOther: refErr.RouterOther,
				},
			})
			return
		}
		h.log.Warn("delete", tunnelName, "Failed to delete tunnel: "+err.Error())
		response.ErrorWithStatus(w, http.StatusInternalServerError, err.Error(), "DELETE_FAILED")
		return
	}

	// Clear traffic history for deleted tunnel
	if h.traffic != nil {
		h.traffic.Clear(id)
	}

	h.log.Info("delete", tunnelName, "Tunnel deleted")
	h.publishTunnelList(r.Context())

	response.Success(w, map[string]interface{}{
		"success":  true,
		"tunnelId": id,
		"verified": true,
	})
}

// Export returns a single tunnel config as a downloadable .conf file.
//
//	@Summary		Export tunnel config
//	@Tags			tunnels
//	@Produce		plain
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{file}	binary
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/export [get]
func (h *TunnelsHandler) Export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}

	stored, err := h.store.Get(id)
	if err != nil {
		response.Error(w, "tunnel not found", "NOT_FOUND")
		return
	}

	content := config.GenerateForExport(stored)
	filename := stored.Name + ".conf"

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Write([]byte(content))
}

// ExportAll returns all tunnel configs as a downloadable ZIP archive.
//
//	@Summary		Export all tunnels (zip)
//	@Tags			tunnels
//	@Produce		application/zip
//	@Security		CookieAuth
//	@Success		200	{file}	binary
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/export-all [get]
func (h *TunnelsHandler) ExportAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	tunnels, err := h.store.List()
	if err != nil {
		response.Error(w, "failed to list tunnels", "LIST_FAILED")
		return
	}

	if len(tunnels) == 0 {
		response.Error(w, "no tunnels to export", "NO_TUNNELS")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, t := range tunnels {
		stored, err := h.store.Get(t.ID)
		if err != nil {
			continue
		}
		content := config.GenerateForExport(stored)
		fw, err := zw.Create(stored.Name + ".conf")
		if err != nil {
			continue
		}
		fw.Write([]byte(content))
	}

	zw.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"awg-tunnels.zip\"")
	w.Write(buf.Bytes())
}

// ReplaceConf replaces a tunnel's configuration from a new .conf file.
// If the tunnel is running, it is stopped before replacement and restarted after.
//
//	@Summary		Replace tunnel from conf
//	@Tags			tunnels
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	query	string	true	"Tunnel id"
//	@Success		200	{object}	APIEnvelope
//	@Failure		400	{object}	APIErrorEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/tunnels/replace [post]
func (h *TunnelsHandler) ReplaceConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	id, ok := requireQueryID(w, r)
	if !ok {
		return
	}
	if !isValidTunnelID(id) {
		response.Error(w, "invalid tunnel ID", "INVALID_ID")
		return
	}
	req, ok := parseJSON[struct {
		Content string `json:"content"`
		Name    string `json:"name"`
	}](w, r, http.MethodPost)
	if !ok {
		return
	}

	if req.Content == "" {
		response.BadRequest(w, "missing config content")
		return
	}

	// Check tunnel exists
	if _, err := h.store.Get(id); err != nil {
		response.ErrorWithStatus(w, http.StatusNotFound, "tunnel not found", "NOT_FOUND")
		return
	}

	// Check if running — need to stop before replacing config
	stateInfo := h.svc.GetState(r.Context(), id)
	wasRunning := stateInfo.State == tunnel.StateRunning

	if wasRunning {
		if err := h.svc.Stop(r.Context(), id); err != nil {
			response.InternalError(w, "failed to stop tunnel before config replace: "+err.Error())
			return
		}
	}

	// Replace config
	var warnings []string
	if err := h.svc.ReplaceConfig(r.Context(), id, req.Content, req.Name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			response.ErrorWithStatus(w, http.StatusNotFound, err.Error(), "NOT_FOUND")
			return
		}
		if strings.Contains(err.Error(), "parse conf") {
			response.BadRequest(w, err.Error())
			return
		}
		response.InternalError(w, err.Error())
		return
	}

	// Restart if was running
	if wasRunning {
		if err := h.svc.Start(r.Context(), id); err != nil {
			warnings = append(warnings, "tunnel config replaced but failed to restart: "+err.Error())
		}
	}

	h.publishTunnelList(r.Context())

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id, h.quiescentFor(id))
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	if conflicts := h.svc.CheckAddressConflicts(r.Context(), id); len(conflicts) > 0 {
		warnings = append(warnings, conflicts...)
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	response.Success(w, resp)
}

// mergeInterfaceWhitelist applies the edit-form whitelist on top of
// existing.Interface. Address, MTU, DNS, and the AmneziaWG obfuscation
// block (Qlen, Jc, Jmin, Jmax, S1-S4, H1-H4, I1-I5) are taken from req;
// PrivateKey is taken from req only when non-empty so a save without a
// fresh key keeps the existing one.
//
// Partial-update safety net: when req.Interface.Address is empty the
// entire Interface is treated as missing (routing-page calls that only
// touch ispInterface) and fully preserved from existing. Callers that
// send Address MUST send the rest of the interface body too, otherwise
// the empty fields will overwrite existing values — the frontend's
// buildUpdatePayload spreads ...tunnel.interface for that reason.
func mergeInterfaceWhitelist(req *storage.AWGTunnel, existing *storage.AWGTunnel) {
	if req.Interface.Address == "" {
		req.Interface = existing.Interface
		return
	}
	preserved := existing.Interface
	preserved.Address = req.Interface.Address
	preserved.MTU = req.Interface.MTU
	preserved.DNS = req.Interface.DNS
	if req.Interface.PrivateKey != "" {
		preserved.PrivateKey = req.Interface.PrivateKey
	}
	// AWG obfuscation block (issue #131): editable in the full edit form,
	// so req is the source of truth — including explicit clears (i1 -> "").
	preserved.AWGObfuscation = req.Interface.AWGObfuscation
	req.Interface = preserved
}

// mergePeerWhitelist applies the edit-form whitelist on top of
// existing.Peer. Five fields (PublicKey, PresharedKey, Endpoint,
// AllowedIPs, PersistentKeepalive) are taken from req when PublicKey
// is non-empty; otherwise the entire Peer preserves from existing.
func mergePeerWhitelist(req *storage.AWGTunnel, existing *storage.AWGTunnel) {
	if req.Peer.PublicKey == "" {
		req.Peer = existing.Peer
		return
	}
	preserved := existing.Peer
	preserved.PublicKey = req.Peer.PublicKey
	preserved.PresharedKey = req.Peer.PresharedKey
	preserved.Endpoint = req.Peer.Endpoint
	preserved.AllowedIPs = req.Peer.AllowedIPs
	preserved.PersistentKeepalive = req.Peer.PersistentKeepalive
	req.Peer = preserved
}
