package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
)

// wireguardNamePattern matches valid Keenetic WG interface names (WireguardN).
// Local copy of the legacy ndms.IsValidWireguardName regex — kept here so this
// file no longer depends on the legacy tunnel/ndms package.
var wireguardNamePattern = regexp.MustCompile(`^Wireguard\d+$`)

// ── Response DTOs ────────────────────────────────────────────────

// WireguardServerPeerDTO mirrors frontend WireguardServerPeer.
type WireguardServerPeerDTO struct {
	PublicKey     string   `json:"publicKey" example:"DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD="`
	Description   string   `json:"description" example:"Phone"`
	Endpoint      string   `json:"endpoint" example:"1.2.3.4:12345"`
	AllowedIPs    []string `json:"allowedIPs" example:"10.0.1.2/32"`
	RxBytes       int64    `json:"rxBytes" example:"1048576"`
	TxBytes       int64    `json:"txBytes" example:"524288"`
	LastHandshake string   `json:"lastHandshake" example:"2024-01-15T10:30:00Z"`
	Online        bool     `json:"online" example:"true"`
	Enabled       bool     `json:"enabled" example:"true"`
	ConfAvailable bool     `json:"confAvailable,omitempty" example:"true"`
}

// WireguardServerDTO mirrors frontend WireguardServer.
type WireguardServerDTO struct {
	ID            string                   `json:"id" example:"Wireguard0"`
	InterfaceName string                   `json:"interfaceName" example:"Wireguard0"`
	Description   string                   `json:"description" example:"Wireguard VPN Server"`
	Status        string                   `json:"status" example:"up"`
	Connected     bool                     `json:"connected" example:"true"`
	MTU           int                      `json:"mtu" example:"1420"`
	Address       string                   `json:"address" example:"10.0.1.1"`
	Mask          string                   `json:"mask" example:"255.255.255.0"`
	PublicKey     string                   `json:"publicKey" example:"EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE="`
	ListenPort    int                      `json:"listenPort" example:"51820"`
	Peers         []WireguardServerPeerDTO `json:"peers"`
	NATEnabled    bool                     `json:"natEnabled,omitempty" example:"true"`
	NATMode       string                   `json:"natMode,omitempty" example:"full"`
	Policy        string                   `json:"policy,omitempty" example:"Policy0"`
	KeenDNSDomain string                   `json:"keenDnsDomain,omitempty" example:"home.keenetic.pro"`
	// Endpoint is the user-configured connect host for client .conf files.
	// Empty = WAN IP at generation time.
	Endpoint string `json:"endpoint,omitempty" example:"203.0.113.42"`
	BuiltIn  bool   `json:"builtIn,omitempty" example:"true"`
	// NATModeKnown/PolicyKnown are false when the corresponding NDMS read
	// failed (e.g. transient router error). The frontend must render an
	// "unknown" state instead of trusting the zero-valued NATMode/Policy,
	// which would otherwise read as a fabricated "none".
	NATModeKnown bool `json:"natModeKnown" example:"true"`
	PolicyKnown  bool `json:"policyKnown" example:"true"`
	// Enabled reflects NDMS admin intent (summary.layer.conf == "running").
	// Status/connected alone are unreliable for the on/off toggle.
	Enabled      bool `json:"enabled" example:"true"`
	EnabledKnown bool `json:"enabledKnown" example:"true"`
}

// ManagedPeerStatsDTO mirrors frontend ManagedPeerStats.
type ManagedPeerStatsDTO struct {
	PublicKey     string `json:"publicKey" example:"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF="`
	Endpoint      string `json:"endpoint" example:"5.6.7.8:54321"`
	RxBytes       int64  `json:"rxBytes" example:"2097152"`
	TxBytes       int64  `json:"txBytes" example:"1048576"`
	LastHandshake string `json:"lastHandshake" example:"2024-01-15T10:30:00Z"`
	Online        bool   `json:"online" example:"true"`
}

// ManagedServerStatsDTO mirrors frontend ManagedServerStats.
type ManagedServerStatsDTO struct {
	Status string                `json:"status" example:"up"`
	Peers  []ManagedPeerStatsDTO `json:"peers"`
}

// ServersAllData is the composite payload for GET /servers/all.
type ServersAllData struct {
	Servers      []WireguardServerDTO   `json:"servers"`
	ManagedStats *ManagedServerStatsDTO `json:"managedStats"`
	WANIP        string                 `json:"wanIP" example:"203.0.113.42"`
}

// ServersAllResponse is the envelope for GET /servers/all.
type ServersAllResponse struct {
	Success bool           `json:"success" example:"true"`
	Data    ServersAllData `json:"data"`
}

// WANIPData is the data for GET /servers/wan-ip.
type WANIPData struct {
	IP string `json:"ip" example:"203.0.113.42"`
}

// WANIPResponse is the envelope for GET /servers/wan-ip.
type WANIPResponse struct {
	Success bool      `json:"success" example:"true"`
	Data    WANIPData `json:"data"`
}

// WireguardServerConfigPeerDTO mirrors frontend WireguardServerPeerConfig.
type WireguardServerConfigPeerDTO struct {
	PublicKey    string   `json:"publicKey" example:"DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD="`
	Description  string   `json:"description" example:"Phone"`
	PresharedKey string   `json:"presharedKey" example:"GGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGGG="`
	AllowedIPs   []string `json:"allowedIPs" example:"10.0.1.2/32"`
	Address      string   `json:"address" example:"10.0.1.2"`
}

// WireguardServerConfigDTO mirrors frontend WireguardServerConfig.
type WireguardServerConfigDTO struct {
	PublicKey  string                         `json:"publicKey" example:"EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE="`
	ListenPort int                            `json:"listenPort" example:"51820"`
	MTU        int                            `json:"mtu" example:"1420"`
	Address    string                         `json:"address" example:"10.0.1.1"`
	Peers      []WireguardServerConfigPeerDTO `json:"peers"`
}

// ServerConfigResponse is the envelope for GET /servers/config.
type ServerConfigResponse struct {
	Success bool                     `json:"success" example:"true"`
	Data    WireguardServerConfigDTO `json:"data"`
}

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
	queries    *query.Queries
	commands   *ndmscommand.Commands
	settings   *storage.SettingsStore
	awgStore   *storage.AWGTunnelStore
	bus        *events.Bus
	managed    *ManagedServerHandler
	managedSvc *managed.Service
	log        *logging.ScopedLogger
}

// SetEventBus sets the event bus used for SSE publishing.
func (h *ServersHandler) SetEventBus(bus *events.Bus) {
	h.bus = bus
}

// SetManagedHandler sets the managed server handler for shared publishing.
func (h *ServersHandler) SetManagedHandler(m *ManagedServerHandler) { h.managed = m }

// SetManagedService wires managed.Service for system-server NAT/policy RCI.
func (h *ServersHandler) SetManagedService(svc *managed.Service) { h.managedSvc = svc }

// SetCommands wires NDMS interface commands used by server up/down/restart
// controls. Kept as a setter so tests using NewServersHandler do not need to
// construct the full command registry.
func (h *ServersHandler) SetCommands(commands *ndmscommand.Commands) { h.commands = commands }

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
func NewServersHandler(queries *query.Queries, settings *storage.SettingsStore, awgStore *storage.AWGTunnelStore, appLogger logging.AppLogger) *ServersHandler {
	return &ServersHandler{
		queries:  queries,
		settings: settings,
		awgStore: awgStore,
		log:      logging.NewScopedLogger(appLogger, logging.GroupServer, logging.SubWan),
	}
}

type serverEnabledRequest struct {
	Enabled bool `json:"enabled"`
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

func (h *ServersHandler) getListedServer(ctx context.Context, name string) (*ndms.WireguardServer, error) {
	servers, err := h.listServers(ctx)
	if err != nil {
		return nil, err
	}

	for i := range servers {
		server := servers[i]
		if server.ID == name || server.InterfaceName == name {
			return &server, nil
		}
	}

	return nil, nil
}

func (h *ServersHandler) serverIsUp(ctx context.Context, server *ndms.WireguardServer) bool {
	if server == nil {
		return false
	}
	if enabled, known := h.readSystemServerEnabled(ctx, server.ID); known {
		return enabled
	}
	return server.Status == "up"
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
	enriched := make([]WireguardServerDTO, len(list))
	for i, srv := range list {
		enriched[i] = h.enrichServerDTO(ctx, srv)
	}
	managedList := []*managedServerResponse{}
	managedStats := map[string]*managed.ManagedServerStats{}
	if h.managed != nil {
		managedList = h.managed.getManagedList()
		managedStats = h.managed.getManagedStatsMap(ctx)
	}
	payload := map[string]any{
		"servers":      enriched,
		"managed":      managedList,
		"managedStats": managedStats,
	}
	response.Success(w, payload)
}

// GetAll returns the composite servers snapshot (list + managed + stats + wanIP).
// Replaces the snapshot:servers SSE event — the frontend polls this.
// GET /api/servers/all
//
//	@Summary		Get all servers snapshot
//	@Description	Returns the composite servers snapshot: WireGuard servers list, managed server, stats and WAN IP.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	ServersAllResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/servers/all [get]
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
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		500		{object}	APIErrorEnvelope
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

// SetEnabled enables or disables a built-in/marked WireGuard server interface.
// POST /api/servers/enabled?name=Wireguard0
//
//	@Summary		Toggle WireGuard server enabled state
//	@Description	Brings a built-in or marked WireGuard server interface up or down.
//	@Tags			servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query	string				true	"Interface name"
//	@Param			body	body	serverEnabledRequest	true	"Enabled flag"
//	@Success		200		{object}	ServersAllResponse
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Router			/servers/enabled [post]
func (h *ServersHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.commands == nil || h.commands.Interfaces == nil {
		response.Error(w, "ndms commands not initialized", "INTERNAL_ERROR")
		return
	}

	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}

	var req serverEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, "invalid request body", "INVALID_BODY")
		return
	}

	server, err := h.getListedServer(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return
	}
	if server == nil {
		response.Error(w, "server not found", "NOT_FOUND")
		return
	}

	if req.Enabled {
		if err := h.commands.Interfaces.InterfaceUp(r.Context(), name); err != nil {
			response.Error(w, err.Error(), "INTERFACE_UP_FAILED")
			return
		}
	} else {
		if err := h.commands.Interfaces.InterfaceDown(r.Context(), name); err != nil {
			response.Error(w, err.Error(), "INTERFACE_DOWN_FAILED")
			return
		}
	}

	if h.queries != nil && h.queries.WGServers != nil {
		h.queries.WGServers.Invalidate(name)
	}
	publishInvalidated(h.bus, ResourceServers, "server-enabled-changed")
	h.writeAll(w, r)
}

// Restart accepts a restart/start command for a built-in/marked WireGuard server.
// POST /api/servers/restart?name=Wireguard0
//
//	@Summary		Restart or start WireGuard server
//	@Description	If the server interface is up, restarts it with down -> pause -> up. If it is down, starts it. The command is accepted quickly and executed in background so a client connected through this server does not cancel the operation.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			name	query	string	true	"Interface name"
//	@Success		200		{object}	APIEnvelope
//	@Failure		400		{object}	APIErrorEnvelope
//	@Failure		404		{object}	APIErrorEnvelope
//	@Failure		405		{object}	APIErrorEnvelope
//	@Router			/servers/restart [post]
func (h *ServersHandler) Restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if h.commands == nil || h.commands.Interfaces == nil {
		response.Error(w, "ndms commands not initialized", "INTERNAL_ERROR")
		return
	}

	name := r.URL.Query().Get("name")
	if !h.validateName(w, name) {
		return
	}

	server, err := h.getListedServer(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_FAILED")
		return
	}
	if server == nil {
		response.Error(w, "server not found", "NOT_FOUND")
		return
	}

	wasUp := h.serverIsUp(r.Context(), server)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)

	go func() {
		defer cancel()

		// Give the accepted response a chance to reach the browser before we
		// drop the server interface.
		time.Sleep(300 * time.Millisecond)

		if wasUp {
			if err := h.commands.Interfaces.InterfaceDown(ctx, name); err != nil {
				return
			}
			time.Sleep(1200 * time.Millisecond)
		}

		if err := h.commands.Interfaces.InterfaceUp(ctx, name); err != nil {
			return
		}

		publishInvalidated(h.bus, ResourceServers, "server-restart")
	}()

	response.Success(w, map[string]any{
		"id":       name,
		"accepted": true,
	})
}

// WANIP returns the external WAN IP for .conf generation.
// GET /api/servers/wan-ip
//
//	@Summary		Get WAN IP
//	@Description	Returns the router's external WAN IP address.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	WANIPResponse
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/servers/wan-ip [get]
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
//
//	@Summary		Get marked server interfaces
//	@Description	Returns the list of interface IDs that have been marked as servers.
//	@Tags			servers
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	APIEnvelope
//	@Failure		500	{object}	APIErrorEnvelope
//	@Router			/servers/marked [get]
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
