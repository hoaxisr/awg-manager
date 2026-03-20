package api

import (
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/testing"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
)

// ServersHandler handles VPN server interface operations.
type ServersHandler struct {
	ndms     ndms.Client
	settings *storage.SettingsStore
	awgStore *storage.AWGTunnelStore
}

// NewServersHandler creates a new servers handler.
func NewServersHandler(ndmsClient ndms.Client, settings *storage.SettingsStore, awgStore *storage.AWGTunnelStore) *ServersHandler {
	return &ServersHandler{ndms: ndmsClient, settings: settings, awgStore: awgStore}
}

func (h *ServersHandler) validateName(w http.ResponseWriter, name string) bool {
	if name == "" {
		response.Error(w, "missing name parameter", "MISSING_NAME")
		return false
	}
	if !ndms.IsValidWireguardName(name) {
		response.Error(w, "invalid interface name", "INVALID_NAME")
		return false
	}
	return true
}

// List returns all server WireGuard interfaces (built-in VPN Server + user-marked).
// GET /api/servers
func (h *ServersHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}

	all, err := h.ndms.ListAllWireguardServers(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "LIST_FAILED")
		return
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

	// Exclude managed server interface (it's shown separately)
	var managedServerIface string
	if ms := h.settings.GetManagedServer(); ms != nil {
		managedServerIface = ms.InterfaceName
	}

	var servers []ndms.WireguardServer
	for _, s := range all {
		if managedSet[s.ID] || s.ID == managedServerIface {
			continue
		}
		isBuiltIn := s.Description == "Wireguard VPN Server"
		isMarked := serverSet[s.ID]
		if isBuiltIn || isMarked {
			servers = append(servers, s)
		}
	}

	response.Success(w, response.MustNotNil(servers))
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
	server, err := h.ndms.GetWireguardServer(r.Context(), name)
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
	config, err := h.ndms.GetWireguardServerConfig(r.Context(), name)
	if err != nil {
		response.Error(w, err.Error(), "GET_CONFIG_FAILED")
		return
	}
	response.Success(w, config)
}

// Mark handles mark/unmark operations for server interfaces.
// POST /api/servers/mark?name=Wireguard0 — mark as server
// DELETE /api/servers/mark?name=Wireguard0 — unmark (return to tunnels)
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
		response.Success(w, map[string]bool{"ok": true})
	case http.MethodDelete:
		if err := h.settings.UnmarkServerInterface(name); err != nil {
			response.Error(w, err.Error(), "UNMARK_FAILED")
			return
		}
		response.Success(w, map[string]bool{"ok": true})
	default:
		response.MethodNotAllowed(w)
	}
}

// WANIP returns the external WAN IP for .conf generation.
// GET /api/servers/wan-ip
func (h *ServersHandler) WANIP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	ip, err := testing.GetWANIP(r.Context())
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
