package api

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
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

// TunnelService defines the interface for tunnel operations used by API handlers.
type TunnelService interface {
	// CRUD
	List(ctx context.Context) ([]service.TunnelWithStatus, error)
	Get(ctx context.Context, tunnelID string) (*service.TunnelWithStatus, error)
	Create(ctx context.Context, tunnelID, name string, cfg tunnel.Config, stored *storage.AWGTunnel) error
	Update(ctx context.Context, tunnelID string, cfg tunnel.Config) error
	Delete(ctx context.Context, tunnelID string) error

	// Lifecycle (delegated to orchestrator)
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
	Import(ctx context.Context, confContent, name, backend string) (*service.TunnelWithStatus, error)

	// ReplaceConfig replaces a tunnel's config from a new .conf file.
	ReplaceConfig(ctx context.Context, tunnelID, confContent, newName string) error

	// WAN state model
	WANModel() *wan.Model

	// Resolved ISP for auto-mode tunnels
	GetResolvedISP(tunnelID string) string

	// SetSelfCreateGate wires the gate used by import/create paths to
	// suppress hook-driven snapshot refreshes while an NDMS interface is
	// being created but our store.Save hasn't run yet.
	SetSelfCreateGate(g tunnel.SelfCreateGater)
}

// TunnelsHandler handles tunnel CRUD operations.
type TunnelsHandler struct {
	svc               TunnelService
	orch              *orchestrator.Orchestrator
	store             *storage.AWGTunnelStore
	settingsStore     *storage.SettingsStore
	pingCheck         PingCheckService
	bus               *events.Bus
	catalog           routing.Catalog
	log               *logging.ScopedLogger
	traffic           *traffic.History
	pingCheckSnapshot func()
	// snapshotRefresh (optional) republishes the full snapshot:tunnels
	// SSE event (managed + external + system lists with fresh dedup).
	// Called after publishTunnelList so UIs see a consistent state after
	// any managed-list-modifying operation (create / import / delete /
	// start / stop / etc.) — without it the frontend systemTunnels array
	// stays stale and the only guard against ghost duplicates is the
	// frontend-level interfaceName filter.
	snapshotRefresh func(ctx context.Context)
	// selfCreateGate (optional) suppresses the hook-driven snapshot
	// refresh while awg-manager is itself in the middle of creating an
	// NDMS interface. See tunnel.SelfCreateGater / api.HookHandler for
	// the contract.
	selfCreateGate tunnel.SelfCreateGater
	// buildTunnelsSnapshot (optional) assembles the composite
	// {tunnels, external, system} payload used by GetAll and by
	// mutation handlers that return fresh state. Injected by server.go
	// so TunnelsHandler doesn't need direct references to External /
	// System tunnel handlers. Falls back to managed-only when nil.
	buildTunnelsSnapshot func(ctx context.Context) map[string]interface{}
}

// NewTunnelsHandler creates a new tunnels handler.
func NewTunnelsHandler(svc TunnelService, store *storage.AWGTunnelStore, appLogger logging.AppLogger) *TunnelsHandler {
	return &TunnelsHandler{
		svc:   svc,
		store: store,
		log:   logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
	}
}

// SetEventBus sets the event bus for SSE publishing.
func (h *TunnelsHandler) SetEventBus(bus *events.Bus) { h.bus = bus }

// SetCatalog sets the routing catalog for tunnel list updates.
func (h *TunnelsHandler) SetCatalog(cat routing.Catalog) { h.catalog = cat }

// PublishTunnelList emits resource:invalidated hints for tunnels and
// routing.tunnels so polling stores refetch immediately. Exported for
// cross-handler use (Import, ExternalAdopt, Control).
func (h *TunnelsHandler) PublishTunnelList(ctx context.Context) { h.publishTunnelList(ctx) }

// publishTunnelList emits resource:invalidated hints after any mutation
// that changes the managed-tunnel list (Create / Update / Delete /
// Start / Stop / Restart / Import / Adopt / Replace).
//
//   - ResourceTunnels         — the {tunnels, external, system} snapshot
//                               now served by /api/tunnels/all.
//   - ResourceRoutingTunnels  — the routing-page catalog (Task 11 will
//                               migrate the store; the hint fires now
//                               so the future store picks it up.)
//
// Also still refreshes the pingcheck snapshot + rebroadcasts the
// legacy snapshot:tunnels SSE for any subscribers that haven't migrated
// yet (hookHandler shares the same refresher).
func (h *TunnelsHandler) publishTunnelList(ctx context.Context) {
	if h.bus == nil {
		return
	}
	publishInvalidated(h.bus, ResourceTunnels, "list-changed")
	if h.catalog != nil {
		publishInvalidated(h.bus, ResourceRoutingTunnels, "list-changed")
		// Task 11 will migrate the routing.tunnels store to polling; until
		// then, keep the legacy SSE payload too so the routing page
		// dropdown refreshes on tunnel CRUD.
		h.bus.Publish("routing:tunnels-updated", h.catalog.ListAll(ctx))
	}

	// Also refresh pingcheck (new/deleted tunnels appear/disappear on monitoring page)
	if h.pingCheckSnapshot != nil {
		h.pingCheckSnapshot()
	}

	// Keep the legacy snapshot rebroadcast wired — hookHandler calls the
	// same refresher on ifcreated/ifdestroyed; dropping it here would
	// require a separate refactor sweep. It is a no-op when no SSE
	// client listens to snapshot:tunnels.
	if h.snapshotRefresh != nil {
		h.snapshotRefresh(ctx)
	}
}

// SetSnapshotRefresher wires the legacy snapshot:tunnels refresher
// shared with hookHandler (fires on ifcreated/ifdestroyed). Kept for
// backward compat with NDMS hook-driven UI refresh; can be removed
// once the hook fires resource:invalidated directly.
func (h *TunnelsHandler) SetSnapshotRefresher(fn func(ctx context.Context)) {
	h.snapshotRefresh = fn
}

// SetTunnelsSnapshotBuilder wires the composer used by GetAll and
// mutation handlers that return fresh snapshot state. Server.go
// typically injects SnapshotBuilder.BuildTunnelsSnapshot.
func (h *TunnelsHandler) SetTunnelsSnapshotBuilder(fn func(ctx context.Context) map[string]interface{}) {
	h.buildTunnelsSnapshot = fn
}

// SetSelfCreateGate wires the gate used to suppress hook-driven snapshot
// refreshes while the handler itself is creating an NDMS interface
// (manual Create path — import path gates inside ServiceImpl directly).
func (h *TunnelsHandler) SetSelfCreateGate(g tunnel.SelfCreateGater) {
	h.selfCreateGate = g
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

// SetOrchestrator sets the orchestrator for lifecycle operations.
func (h *TunnelsHandler) SetOrchestrator(orch *orchestrator.Orchestrator) {
	h.orch = orch
}

// SetPingCheckSnapshot sets the function that publishes a pingcheck snapshot.
func (h *TunnelsHandler) SetPingCheckSnapshot(fn func()) { h.pingCheckSnapshot = fn }

// BuildTunnelResponse builds a consistent tunnel response with stored data.
// Exported so Import and External handlers can reuse the same response format.
func BuildTunnelResponse(r *http.Request, svc TunnelService, store *storage.AWGTunnelStore, id string) (map[string]interface{}, error) {
	t, err := svc.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}

	stored, _ := store.Get(id)

	ispIface := t.ISPInterface
	// NativeWG stores NDMS IDs (e.g. "ISP"), but frontend uses kernel names (e.g. "eth3").
	if stored != nil && stored.Backend == "nativewg" && ispIface != "" && ispIface != "auto" {
		if kernelName := svc.WANModel().NameForID(ispIface); kernelName != "" {
			ispIface = kernelName
		}
	}

	resp := map[string]interface{}{
		"id":                t.ID,
		"name":              t.Name,
		"type":              "awg",
		"enabled":           t.Enabled,
		"defaultRoute": t.DefaultRoute,
		"ispInterface":      ispIface,
		"interfaceName":     t.InterfaceName,
		"ndmsName":          t.NDMSName,
		"configPreview":     t.ConfigPreview,
		"state":             t.State.String(),
		"stateInfo":         t.StateInfo,
	}

	if stored != nil {
		resp["interface"] = stored.Interface
		resp["peer"] = stored.Peer
		resp["pingCheck"] = stored.PingCheck
		resp["connectivityCheck"] = stored.ConnectivityCheck
		resp["ispInterfaceLabel"] = stored.ISPInterfaceLabel
		backend := stored.Backend
		if backend == "" {
			backend = "kernel"
		}
		resp["backend"] = backend
	}

	return resp, nil
}

// tunnelItem is the list-item DTO returned by List and used by SSE snapshots.
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
	NDMSName                  string `json:"ndmsName,omitempty"`
	HasAddressConflict        bool   `json:"hasAddressConflict"`
	RxBytes                   int64  `json:"rxBytes"`
	TxBytes                   int64  `json:"txBytes"`
	LastHandshake             string `json:"lastHandshake"`
	Backend                   string `json:"backend"`
	BackendType               string `json:"backendType,omitempty"`
	AWGVersion                string `json:"awgVersion"`
	MTU                       int    `json:"mtu"`
	StartedAt                 string                  `json:"startedAt,omitempty"`
	PingCheck                 pingcheck.TunnelPingInfo `json:"pingCheck"`
}

// listItems builds the tunnel list items for API response and SSE snapshots.
func (h *TunnelsHandler) listItems(ctx context.Context) ([]tunnelItem, error) {
	tunnels, err := h.svc.List(ctx)
	if err != nil {
		return nil, err
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

		awgVersion := "wg"
		var endpoint, address string
		var ispInterface, ispInterfaceLabel string
		var resolvedISPInterface, resolvedISPInterfaceLabel string
		var mtu int
		if stored != nil {
			endpoint = stored.Peer.Endpoint
			address = stored.Interface.Address
			mtu = stored.Interface.MTU
			awgVersion = config.ClassifyAWGVersion(&stored.Interface)
			ispInterface = stored.ISPInterface
			ispInterfaceLabel = stored.ISPInterfaceLabel

			// NativeWG stores NDMS IDs (e.g. "ISP"), but frontend uses kernel names (e.g. "eth3").
			// Convert back so the dropdown can match the stored value.
			if stored.Backend == "nativewg" && ispInterface != "" && ispInterface != "auto" {
				if kernelName := h.svc.WANModel().NameForID(ispInterface); kernelName != "" {
					ispInterface = kernelName
				}
			}

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

				// NativeWG: resolve actual WAN from NDMS peer "via" field
				if resolvedISPInterface == "" && stored.Backend == "nativewg" {
					if via := t.StateInfo.PeerVia; via != "" {
						wanModel := h.svc.WANModel()
						if kernelName := wanModel.NameForID(via); kernelName != "" {
							resolvedISPInterface = kernelName
							resolvedISPInterfaceLabel = wanModel.GetLabel(kernelName)
						}
						if resolvedISPInterfaceLabel == "" {
							resolvedISPInterfaceLabel = via
						}
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

		backend := "kernel"
		if stored != nil && stored.Backend == "nativewg" {
			backend = "nativewg"
		}

		var startedAt string
		if t.StateInfo.ConnectedAt != "" {
			// Use NDMS uptime as source of truth (both kernel and NativeWG)
			startedAt = t.StateInfo.ConnectedAt
		} else if stored != nil && stored.StartedAt != "" {
			startedAt = stored.StartedAt // fallback to storage
		}

		var pcInfo pingcheck.TunnelPingInfo
		if h.pingCheck != nil {
			pcInfo = h.pingCheck.GetTunnelPingStatus(t.ID)
		} else {
			pcInfo = pingcheck.TunnelPingInfo{Status: "disabled"}
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
			NDMSName:            t.NDMSName,
			Backend:             backend,
			HasAddressConflict:  hasConflict,
			RxBytes:             t.StateInfo.RxBytes,
			TxBytes:             t.StateInfo.TxBytes,
			LastHandshake:       formatHandshake(t.StateInfo.LastHandshake),
			BackendType:         t.StateInfo.BackendType,
			AWGVersion:          awgVersion,
			MTU:                 mtu,
			StartedAt:           startedAt,
			PingCheck:           pcInfo,
		})
	}

	return items, nil
}

// List returns all tunnels.
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
// system}) the frontend polls instead of listening to the legacy
// snapshot:tunnels SSE event.
// GET /api/tunnels/all
func (h *TunnelsHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	h.writeAll(w, r)
}

// writeAll writes the composite tunnels snapshot. Used by GetAll
// (REST poll) and by any mutation that wants to return fresh state
// inline (see Task spec — current Create/Update/Delete return a single
// entity instead, so this is reserved for future callers).
func (h *TunnelsHandler) writeAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.buildTunnelsSnapshot != nil {
		if payload := h.buildTunnelsSnapshot(ctx); payload != nil {
			response.Success(w, payload)
			return
		}
	}
	// Fallback: managed-only (no external / system lists wired).
	items, err := h.listItems(ctx)
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}
	response.Success(w, map[string]interface{}{
		"tunnels":  items,
		"external": []interface{}{},
		"system":   []interface{}{},
	})
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
	req, ok := parseJSON[storage.AWGTunnel](w, r, http.MethodPost)
	if !ok {
		return
	}

	// Validate endpoint resolves
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

	// Detect changes before merge
	oldPingCheckEnabled := existing.PingCheck != nil && existing.PingCheck.Enabled
	newPingCheckEnabled := req.PingCheck != nil && req.PingCheck.Enabled
	oldISPInterface := existing.ISPInterface

	// Merge changes — preserve fields not sent by partial updates (e.g. routing page).
	req.ID = existing.ID
	req.CreatedAt = existing.CreatedAt
	req.Type = existing.Type
	req.Enabled = existing.Enabled
	req.ResolvedEndpointIP = existing.ResolvedEndpointIP
	req.ActiveWAN = existing.ActiveWAN
	req.Backend = existing.Backend
	req.NWGIndex = existing.NWGIndex
	if req.Name == "" {
		req.Name = existing.Name
	}
	if req.Interface.PrivateKey == "" {
		if req.Interface.Address == "" {
			// No interface data sent (partial update like ISP change) — preserve everything.
			req.Interface = existing.Interface
		} else {
			// Interface data sent without private key (edit page) — preserve only the key.
			req.Interface.PrivateKey = existing.Interface.PrivateKey
		}
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
		h.log.Warn("update", req.Name, "Failed to update tunnel: "+err.Error())
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
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
	if t, err := h.svc.Get(r.Context(), id); err == nil {
		tunnelName = t.Name
	}

	// Delete via orchestrator (handles stop + config file + store + NDMS cleanup + monitoring)
	if err := h.orch.HandleEvent(r.Context(), orchestrator.Event{
		Type: orchestrator.EventDelete, Tunnel: id,
	}); err != nil {
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
func (h *TunnelsHandler) Export(w http.ResponseWriter, r *http.Request) {
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
func (h *TunnelsHandler) ReplaceConf(w http.ResponseWriter, r *http.Request) {
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

	resp, err := BuildTunnelResponse(r, h.svc, h.store, id)
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

