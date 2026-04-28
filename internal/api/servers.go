package api

import (
	"context"
	"net/http"
	"regexp"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// wireguardNamePattern matches valid Keenetic WG interface names (WireguardN).
// Local copy of the legacy ndms.IsValidWireguardName regex — kept here so this
// file no longer depends on the legacy tunnel/ndms package.
var wireguardNamePattern = regexp.MustCompile(`^Wireguard\d+$`)

// isValidWireguardName checks that the name matches "WireguardN" pattern.
// Used to prevent command injection in ndmc/RCI calls.
func isValidWireguardName(name string) bool {
	return wireguardNamePattern.MatchString(name)
}

// ServersHandler handles VPN server interface operations.
// Frontend now polls GET /api/servers/all; this handler only emits
// resource:invalidated hints on mark/unmark and poller metrics ticks so
// subscribers refetch immediately instead of waiting for the next poll.
type ServersHandler struct {
	queries  *query.Queries
	settings *storage.SettingsStore
	awgStore *storage.AWGTunnelStore
	bus      *events.Bus
	managed  *ManagedServerHandler
}

// SetEventBus sets the event bus used for SSE publishing.
func (h *ServersHandler) SetEventBus(bus *events.Bus) {
	h.bus = bus
}

// SetManagedHandler sets the managed server handler for shared publishing.
func (h *ServersHandler) SetManagedHandler(m *ManagedServerHandler) { h.managed = m }

// PublishServerSnapshot broadcasts a resource:invalidated hint. Kept
// as a method on *ServersHandler because ndms/metrics.Poller calls it
// through the ServerSnapshotPublisher interface.
func (h *ServersHandler) PublishServerSnapshot(ctx context.Context) {
	publishInvalidated(h.bus, ResourceServers, "metrics-tick")
}

// publishServerInvalidated broadcasts a resource:invalidated hint for
// servers. Used by ManagedServerHandler after managed CRUD so its
// subscribers refetch immediately.
func (h *ServersHandler) publishServerInvalidated(reason string) {
	publishInvalidated(h.bus, ResourceServers, reason)
}

// NewServersHandler creates a new servers handler.
func NewServersHandler(queries *query.Queries, settings *storage.SettingsStore, awgStore *storage.AWGTunnelStore) *ServersHandler {
	return &ServersHandler{queries: queries, settings: settings, awgStore: awgStore}
}

func (h *ServersHandler) validateName(w http.ResponseWriter, name string) bool {
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return false
	}
	if !isValidWireguardName(name) {
		response.Error(w, "invalid interface name", "INVALID_NAME")
		return false
	}
	return true
}

// listServers builds the filtered server list for API response and SSE snapshots.
func (h *ServersHandler) listServers(ctx context.Context) ([]ndms.WireguardServer, error) {
	all, err := h.queries.WGServers.List(ctx)
	if err != nil {
		return nil, err
	}

	serverIDs := h.settings.GetServerInterfaces()
	serverSet := make(map[string]bool, len(serverIDs))
	for _, id := range serverIDs {
		serverSet[id] = true
	}

	// Exclude AWG Manager-managed NativeWG tunnels
	managedNWG := managedNativeWGNames(h.awgStore)
	managedSet := make(map[string]bool, len(managedNWG))
	for _, id := range managedNWG {
		managedSet[id] = true
	}

	// Exclude managed server interfaces (they're shown separately)
	managedServerIfaces := h.settings.GetManagedServers()
	managedServerSet := make(map[string]bool, len(managedServerIfaces))
	for _, ms := range managedServerIfaces {
		if ms.InterfaceName != "" {
			managedServerSet[ms.InterfaceName] = true
		}
	}

	var servers []ndms.WireguardServer
	for _, s := range all {
		if managedSet[s.ID] || managedServerSet[s.ID] {
			continue
		}
		isBuiltIn := s.Description == "Wireguard VPN Server"
		isMarked := serverSet[s.ID]
		if isBuiltIn || isMarked {
			servers = append(servers, s)
		}
	}

	if servers == nil {
		servers = []ndms.WireguardServer{}
	}
	return servers, nil
}

// List returns all server WireGuard interfaces (built-in VPN Server + user-marked).
// GET /api/servers
func (h *ServersHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	servers, err := h.listServers(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}

	response.Success(w, servers)
}

// writeAll writes the composite servers snapshot. Used by GetAll (REST)
// and by Mark/Unmark so mutations return fresh state inline.
//
// `managed` is always an array (never null) and `managedStats` is always
// a `{id: stats}` map (never null). The frontend types depend on this:
// returning null for an empty managed-server set would force every
// consumer to handle the null case.
func (h *ServersHandler) writeAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := h.listServers(ctx)
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
	}
	managedList := []*managedServerResponse{}
	managedStats := map[string]*managed.ManagedServerStats{}
	if h.managed != nil {
		managedList = h.managed.getManagedList()
		managedStats = h.managed.getManagedStatsMap(ctx)
	}
	payload := map[string]any{
		"servers":      list,
		"managed":      managedList,
		"managedStats": managedStats,
		"wanIP":        "",
	}
	if ip, err := testing.GetWANIPWithFallback(ctx, h.queries.WANInterfaceAddress); err == nil {
		payload["wanIP"] = ip
	}
	response.Success(w, payload)
}

// GetAll returns the composite servers snapshot (list + managed + stats + wanIP).
// Replaces the snapshot:servers SSE event — the frontend polls this.
// GET /api/servers/all
func (h *ServersHandler) GetAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	h.writeAll(w, r)
}

// Get returns a single server with all peers.
// GET /api/servers/get?name=Wireguard0
func (h *ServersHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	server, err := h.queries.WGServers.Get(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return
	}
	response.Success(w, server)
}

// Config returns RC configuration for .conf generation.
// GET /api/servers/config?name=Wireguard0
func (h *ServersHandler) Config(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}
	config, err := h.queries.WGServers.GetConfig(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_CONFIG_FAILED")
		return
	}
	response.Success(w, config)
}

// Mark handles mark/unmark operations for server interfaces.
// POST /api/servers/mark?name=Wireguard0 — mark as server
// DELETE /api/servers/mark?name=Wireguard0 — unmark (return to tunnels)
// Both return the fresh ServersSnapshot as body.
//
//	@Summary		Mark/unmark interface as server
//	@Description	POST marks the named WG interface as a server (visible under Servers, hidden from Tunnels). DELETE unmarks (returns it to the Tunnels list). Both return the fresh ServersSnapshot.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query		string	true	"Interface name (e.g. Wireguard0)"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/servers/mark [post]
//	@Router			/servers/mark [delete]
func (h *ServersHandler) Mark(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}

	switch r.Method {
	case http.MethodPost:
		if err := h.settings.MarkServerInterface(name); err != nil {
			response.Error(w, err.Error(), "MARK_FAILED")
			return
		}
	case http.MethodDelete:
		if err := h.settings.UnmarkServerInterface(name); err != nil {
			response.Error(w, err.Error(), "UNMARK_FAILED")
			return
		}
	default:
		response.MethodNotAllowed(w)
		return
	}

	publishInvalidated(h.bus, ResourceServers, "mark-changed")
	h.writeAll(w, r)
}

// WANIP returns the external WAN IP for .conf generation.
// GET /api/servers/wan-ip
func (h *ServersHandler) WANIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ip, err := testing.GetWANIPWithFallback(r.Context(), h.queries.WANInterfaceAddress)
	if err != nil {
		response.Error(w, err.Error(), "WAN_IP_FAILED")
		return
	}
	response.Success(w, map[string]string{"ip": ip})
}

// Marked returns the list of server interface IDs.
// GET /api/servers/marked
func (h *ServersHandler) Marked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ids := h.settings.GetServerInterfaces()
	if ids == nil {
		ids = []string{}
	}
	response.Success(w, ids)
}
