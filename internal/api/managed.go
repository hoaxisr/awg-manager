package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── Response DTOs ────────────────────────────────────────────────

// ManagedPeerDTO mirrors frontend ManagedPeer.
type ManagedPeerDTO struct {
	PublicKey    string `json:"publicKey" example:"HHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHH="`
	PrivateKey   string `json:"privateKey" example:"IIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIII="`
	PresharedKey string `json:"presharedKey" example:"JJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJ="`
	Description  string `json:"description" example:"My Phone"`
	TunnelIP     string `json:"tunnelIP" example:"10.10.0.2"`
	DNS          string `json:"dns,omitempty" example:"8.8.8.8"`
	Enabled      bool   `json:"enabled" example:"true"`
}

// ManagedServerDTO mirrors frontend ManagedServer.
type ManagedServerDTO struct {
	InterfaceName string           `json:"interfaceName" example:"Wireguard1"`
	Address       string           `json:"address" example:"10.10.0.1"`
	Mask          string           `json:"mask" example:"255.255.255.0"`
	ListenPort    int              `json:"listenPort" example:"51821"`
	Endpoint      string           `json:"endpoint,omitempty" example:"203.0.113.42:51821"`
	DNS           string           `json:"dns,omitempty" example:"8.8.8.8"`
	MTU           int              `json:"mtu,omitempty" example:"1420"`
	NatEnabled    bool             `json:"natEnabled,omitempty" example:"true"`
	Policy        string           `json:"policy" example:"default"`
	Peers         []ManagedPeerDTO `json:"peers"`
}

// ManagedServerResponse is the envelope for GET /managed-server.
type ManagedServerResponse struct {
	Success bool             `json:"success" example:"true"`
	Data    ManagedServerDTO `json:"data"`
}

// CreateServerRequestDTO is the swagger-visible body for POST /managed-servers.
type CreateServerRequestDTO struct {
	Address     string `json:"address" example:"10.10.0.1"`
	Mask        string `json:"mask" example:"255.255.255.0"`
	ListenPort  int    `json:"listenPort" example:"51821"`
	Description string `json:"description,omitempty" example:"My WG Server"`
	Endpoint    string `json:"endpoint,omitempty" example:"203.0.113.42:51821"`
	DNS         string `json:"dns,omitempty" example:"8.8.8.8"`
	MTU         int    `json:"mtu,omitempty" example:"1420"`
}

// UpdateServerRequestDTO is the swagger-visible body for PUT /managed-servers/{id}.
type UpdateServerRequestDTO struct {
	Address     string  `json:"address" example:"10.10.0.1"`
	Mask        string  `json:"mask" example:"255.255.255.0"`
	ListenPort  int     `json:"listenPort" example:"51821"`
	Description *string `json:"description,omitempty" example:"My WG Server"`
	Endpoint    *string `json:"endpoint,omitempty" example:"203.0.113.42:51821"`
	DNS         *string `json:"dns,omitempty" example:"8.8.8.8"`
	MTU         *int    `json:"mtu,omitempty" example:"1420"`
}

// isValidWGKey checks that a string is a valid WireGuard key (44-char base64, 32 bytes decoded).
func isValidWGKey(key string) bool {
	if len(key) != 44 || key[43] != '=' {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(decoded) == 32
}

// managedServerResponse is a safe DTO that strips private keys from peers.
type managedServerResponse struct {
	InterfaceName string              `json:"interfaceName"`
	Description   string              `json:"description,omitempty"`
	Address       string              `json:"address"`
	Mask          string              `json:"mask"`
	ListenPort    int                 `json:"listenPort"`
	Endpoint      string              `json:"endpoint,omitempty"`
	DNS           string              `json:"dns,omitempty"`
	MTU           int                 `json:"mtu,omitempty"`
	NATEnabled    bool                `json:"natEnabled"`
	Policy        string              `json:"policy"`
	Peers         []managedPeerPublic `json:"peers"`
}

// managedPeerPublic is a peer DTO without private/preshared keys.
type managedPeerPublic struct {
	PublicKey   string `json:"publicKey"`
	Description string `json:"description"`
	TunnelIP    string `json:"tunnelIP"`
	DNS         string `json:"dns,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// toManagedServerResponse converts storage model to a safe response DTO.
func toManagedServerResponse(s *storage.ManagedServer) *managedServerResponse {
	peers := make([]managedPeerPublic, len(s.Peers))
	for i, p := range s.Peers {
		peers[i] = managedPeerPublic{
			PublicKey:   p.PublicKey,
			Description: p.Description,
			TunnelIP:    p.TunnelIP,
			DNS:         p.DNS,
			Enabled:     p.Enabled,
		}
	}
	return &managedServerResponse{
		InterfaceName: s.InterfaceName,
		Description:   s.Description,
		Address:       s.Address,
		Mask:          s.Mask,
		ListenPort:    s.ListenPort,
		Endpoint:      s.Endpoint,
		DNS:           s.DNS,
		MTU:           s.MTU,
		NATEnabled:    s.NATEnabled,
		Policy:        s.Policy,
		Peers:         peers,
	}
}

// ManagedServerHandler handles managed WireGuard server operations.
type ManagedServerHandler struct {
	svc     managed.ManagedServerService
	servers *ServersHandler // for shared server:updated publishing
}

// SetServersHandler sets the servers handler for shared SSE publishing.
func (h *ManagedServerHandler) SetServersHandler(s *ServersHandler) { h.servers = s }

// publishServerUpdated delegates to ServersHandler to broadcast a
// resource:invalidated hint so servers polling subscribers refetch.
func (h *ManagedServerHandler) publishServerUpdated() {
	if h.servers != nil {
		h.servers.publishServerInvalidated("managed-mutation")
	}
}

// writeServersSnapshot delegates the composite ServersSnapshot response
// to ServersHandler.writeAll with a nil guard. Mutation handlers use this
// so an isolated-test construction (NewManagedServerHandler without
// SetServersHandler) falls back to a safe error response instead of a
// nil pointer panic.
func (h *ManagedServerHandler) writeServersSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.servers == nil {
		response.Error(w, "servers handler not initialized", "INTERNAL_ERROR")
		return
	}
	h.servers.writeAll(w, r)
}

// NewManagedServerHandler creates a new managed server handler.
func NewManagedServerHandler(svc managed.ManagedServerService) *ManagedServerHandler {
	return &ManagedServerHandler{svc: svc}
}

// getManagedList builds the list of managed server DTOs for the composite
// servers snapshot. Always returns a non-nil slice so callers can json-marshal
// it as `[]` rather than `null`.
func (h *ManagedServerHandler) getManagedList() []*managedServerResponse {
	servers := h.svc.List()
	out := make([]*managedServerResponse, 0, len(servers))
	for i := range servers {
		out = append(out, toManagedServerResponse(&servers[i]))
	}
	return out
}

// getManagedStatsMap builds a {id: stats} map for the composite servers
// snapshot. Always returns a non-nil map so callers can json-marshal it
// as `{}` rather than `null`. Errors per-server are skipped (best-effort
// — a single bad server should not blank out the whole snapshot).
func (h *ManagedServerHandler) getManagedStatsMap(ctx context.Context) map[string]*managed.ManagedServerStats {
	servers := h.svc.List()
	out := make(map[string]*managed.ManagedServerStats, len(servers))
	for _, sv := range servers {
		stats, err := h.svc.GetStats(ctx, sv.InterfaceName)
		if err != nil {
			continue
		}
		out[sv.InterfaceName] = stats
	}
	return out
}

// splitPath strips prefix from the escaped URL path, splits the remainder on
// '/', and percent-decodes each segment. The caller MUST pass
// r.URL.EscapedPath() (NOT r.URL.Path) — Go's net/http already
// percent-decodes r.URL.Path, which would let a literal '/' inside a
// segment value (e.g. a base64 WireGuard pubkey containing '/') split the
// path mid-segment and silently truncate downstream values.
//
// Returns ok=false on:
//   - any segment that fails percent-decoding
//   - any decoded segment exactly equal to "." or ".." (path-traversal guard)
//   - any empty segment (e.g. consecutive slashes)
//
// Empty result + ok=true means the path was exactly the prefix (or prefix+slash).
func splitPath(escaped, prefix string) ([]string, bool) {
	rest := strings.TrimPrefix(escaped, prefix)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil, true
	}
	raw := strings.Split(rest, "/")
	parts := make([]string, 0, len(raw))
	for _, seg := range raw {
		if seg == "" {
			return nil, false
		}
		d, err := url.PathUnescape(seg)
		if err != nil {
			return nil, false
		}
		if d == "." || d == ".." {
			return nil, false
		}
		parts = append(parts, d)
	}
	return parts, true
}

// validateID enforces a WireguardN-shaped id and writes the error response
// on failure. Returns true when the id is acceptable.
func (h *ManagedServerHandler) validateID(w http.ResponseWriter, id string) bool {
	if id == "" {
		response.Error(w, "missing managed server id", "MISSING_ID")
		return false
	}
	if !isValidWireguardName(id) {
		response.Error(w, "invalid managed server id", "INVALID_ID")
		return false
	}
	return true
}

// validatePubkey enforces a 44-char base64 WG pubkey and writes the error
// response on failure. Returns true when the pubkey is acceptable.
func (h *ManagedServerHandler) validatePubkey(w http.ResponseWriter, pubkey string) bool {
	if pubkey == "" {
		response.Error(w, "missing pubkey", "MISSING_PUBKEY")
		return false
	}
	if !isValidWGKey(pubkey) {
		response.Error(w, "invalid pubkey format", "INVALID_PUBKEY")
		return false
	}
	return true
}

// Collection dispatches the exact /api/managed-servers endpoint:
//   - GET  → List
//   - POST → Create
func (h *ManagedServerHandler) Collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.List(w, r)
	case http.MethodPost:
		h.Create(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}

// Subtree dispatches every URL under /api/managed-servers/, splitting on '/'
// to extract the id and any further sub-path. See the documented route
// table in this package's package doc / Task 5 plan for the full mapping.
func (h *ManagedServerHandler) Subtree(w http.ResponseWriter, r *http.Request) {
	parts, ok := splitPath(r.URL.EscapedPath(), "/api/managed-servers/")
	if !ok {
		response.Error(w, "invalid path", "INVALID_PATH")
		return
	}
	if len(parts) == 0 {
		// Trailing-slash hit on the collection root — fall through to List/Create.
		h.Collection(w, r)
		return
	}

	// Collection-level non-id endpoints.
	switch parts[0] {
	case "suggest-address":
		if len(parts) != 1 {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		h.SuggestAddress(w, r)
		return
	case "policies":
		if len(parts) != 1 {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		h.GetPolicies(w, r)
		return
	}

	// Everything past this point is an id-scoped path: parts[0] is the id.
	id := parts[0]
	if !h.validateID(w, id) {
		return
	}

	switch len(parts) {
	case 1:
		// /api/managed-servers/{id}
		switch r.Method {
		case http.MethodGet:
			h.Get(w, r, id)
		case http.MethodPut:
			h.Update(w, r, id)
		case http.MethodDelete:
			h.Delete(w, r, id)
		default:
			response.MethodNotAllowed(w)
		}
		return
	case 2:
		// /api/managed-servers/{id}/<sub>
		sub := parts[1]
		switch sub {
		case "stats":
			h.Stats(w, r, id)
		case "policy":
			h.SetPolicy(w, r, id)
		case "nat":
			h.NAT(w, r, id)
		case "enabled":
			h.SetEnabled(w, r, id)
		case "asc":
			h.ASC(w, r, id)
		case "peers":
			h.AddPeer(w, r, id)
		default:
			response.Error(w, "unknown path", "UNKNOWN_PATH")
		}
		return
	case 3:
		// /api/managed-servers/{id}/peers/{pubkey}
		if parts[1] != "peers" {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		pubkey := parts[2]
		if !h.validatePubkey(w, pubkey) {
			return
		}
		switch r.Method {
		case http.MethodPut:
			h.UpdatePeer(w, r, id, pubkey)
		case http.MethodDelete:
			h.DeletePeer(w, r, id, pubkey)
		default:
			response.MethodNotAllowed(w)
		}
		return
	case 4:
		// /api/managed-servers/{id}/peers/{pubkey}/{leaf}
		if parts[1] != "peers" {
			response.Error(w, "unknown path", "UNKNOWN_PATH")
			return
		}
		pubkey := parts[2]
		if !h.validatePubkey(w, pubkey) {
			return
		}
		switch parts[3] {
		case "conf":
			h.PeerConf(w, r, id, pubkey)
		case "toggle":
			h.TogglePeer(w, r, id, pubkey)
		default:
			response.Error(w, "unknown path", "UNKNOWN_PATH")
		}
		return
	}

	response.Error(w, "unknown path", "UNKNOWN_PATH")
}

// List returns every managed server. Always emits a JSON array (never null).
// GET /api/managed-servers
//
//	@Summary		List managed servers
//	@Description	Returns every managed server (id, address, listen port, peers, ASC, NAT, enabled flag, ...). Always a JSON array, never null.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Router			/managed-servers [get]
func (h *ManagedServerHandler) List(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.getManagedList())
}

// SuggestAddress returns a free private /24 for the create-server UI.
// GET /api/managed-servers/suggest-address
//
//	@Summary		Suggest managed-server address
//	@Description	Returns a free private /24 (address, mask) for the create-server UI. Avoids collisions with already-used managed-server subnets.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/managed-servers/suggest-address [get]
func (h *ManagedServerHandler) SuggestAddress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	addr, mask, err := h.svc.SuggestAddress(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "SUGGEST_FAILED")
		return
	}
	response.Success(w, map[string]string{"address": addr, "mask": mask})
}

// Get returns a single managed server by id.
// GET /api/managed-servers/{id}
//
//	@Summary		Get managed server
//	@Description	Returns a single managed server by id (Wireguard{N}).
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	path		string	true	"Server id (e.g. Wireguard0)"
//	@Success		200	{object}	map[string]interface{}
//	@Failure		404	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Router			/managed-servers/{id} [get]
func (h *ManagedServerHandler) Get(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	server, err := h.svc.Get(id)
	if err != nil {
		response.Error(w, err.Error(), "NOT_FOUND")
		return
	}
	response.Success(w, toManagedServerResponse(server))
}

// Stats returns runtime statistics for one managed server's peers.
// GET /api/managed-servers/{id}/stats
//
//	@Summary		Get managed-server peer stats
//	@Description	Returns per-peer runtime stats (handshake, rx/tx) for the named managed server.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	path		string	true	"Server id"
//	@Success		200	{object}	map[string]interface{}
//	@Failure		404	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/stats [get]
func (h *ManagedServerHandler) Stats(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	stats, err := h.svc.GetStats(r.Context(), id)
	if err != nil {
		response.Error(w, err.Error(), "STATS_ERROR")
		return
	}
	response.Success(w, stats)
}

// Create creates a new managed WireGuard server. The id is allocated by the
// service (next free Wireguard{N} slot) — the request body has no id.
// POST /api/managed-servers
//
//	@Summary		Create managed server
//	@Description	Creates a new managed WireGuard server. The id is allocated server-side (next free Wireguard{N} slot).
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			body	body		CreateServerRequestDTO	true	"Address, mask, port, ASC, name"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers [post]
func (h *ManagedServerHandler) Create(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[managed.CreateServerRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	server, err := h.svc.Create(r.Context(), req)
	if err != nil {
		response.Error(w, err.Error(), "CREATE_FAILED")
		return
	}
	response.Success(w, toManagedServerResponse(server))
	h.publishServerUpdated()
}

// Update updates a managed server's address and/or listen port.
// PUT /api/managed-servers/{id}
//
//	@Summary		Update managed server
//	@Description	Updates a managed server's address, listen port, name, and/or other top-level fields. Returns the fresh ServersSnapshot.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string						true	"Server id"
//	@Param			body	body		UpdateServerRequestDTO	true	"Update payload"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		404		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id} [put]
func (h *ManagedServerHandler) Update(w http.ResponseWriter, r *http.Request, id string) {
	req, ok := parseJSON[managed.UpdateServerRequest](w, r, http.MethodPut)
	if !ok {
		return
	}
	if err := h.svc.Update(r.Context(), id, req); err != nil {
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// Delete removes a managed server and all its peers.
// DELETE /api/managed-servers/{id}
//
//	@Summary		Delete managed server
//	@Description	Removes the named managed server along with all its peers. Returns the fresh ServersSnapshot.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id	path		string	true	"Server id"
//	@Success		200	{object}	map[string]interface{}
//	@Failure		404	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/managed-servers/{id} [delete]
func (h *ManagedServerHandler) Delete(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		response.Error(w, err.Error(), "DELETE_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// enabledToggle is the shared request body for NAT and SetEnabled.
type enabledToggle struct {
	Enabled bool `json:"enabled"`
}

// setPolicyRequest is the request body for /api/managed-servers/{id}/policy.
type setPolicyRequest struct {
	Policy string `json:"policy"`
}

// SetPolicy updates the ip hotspot policy for one managed server interface.
// POST /api/managed-servers/{id}/policy
//
//	@Summary		Set managed-server IP policy
//	@Description	Updates the IP hotspot policy bound to the managed server interface. The policy must exist (see /managed-servers/policies).
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string					true	"Server id"
//	@Param			body	body		map[string]interface{}	true	"{policy: string}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/policy [post]
func (h *ManagedServerHandler) SetPolicy(w http.ResponseWriter, r *http.Request, id string) {
	req, ok := parseJSON[setPolicyRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.SetPolicy(r.Context(), id, req.Policy); err != nil {
		response.Error(w, err.Error(), "POLICY_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// GetPolicies returns every IP Policy profile available on the router.
// This is a global catalog — not per-server — so it lives at the
// collection level even though the consumer is a per-server dropdown.
// GET /api/managed-servers/policies
//
//	@Summary		List IP Policy profiles
//	@Description	Returns the global router catalog of IP Policy profiles for use by the per-server policy dropdown. Always a JSON array, never null.
//	@Tags			managed-servers
//	@Produce		json
//	@Security		CookieAuth
//	@Success		200	{object}	map[string]interface{}
//	@Failure		405	{object}	map[string]interface{}
//	@Failure		500	{object}	map[string]interface{}
//	@Router			/managed-servers/policies [get]
func (h *ManagedServerHandler) GetPolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	opts, err := h.svc.ListPolicies(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "POLICIES_FAILED")
		return
	}
	if opts == nil {
		opts = []managed.PolicyOption{}
	}
	response.Success(w, opts)
}

// NAT enables or disables NAT on one managed server interface.
// POST /api/managed-servers/{id}/nat
//
//	@Summary		Toggle managed-server NAT
//	@Description	Enables or disables NAT (masquerade) on the named managed server interface.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string					true	"Server id"
//	@Param			body	body		map[string]interface{}	true	"{enabled: bool}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/nat [post]
func (h *ManagedServerHandler) NAT(w http.ResponseWriter, r *http.Request, id string) {
	req, ok := parseJSON[enabledToggle](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.SetNAT(r.Context(), id, req.Enabled); err != nil {
		response.Error(w, err.Error(), "NAT_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// SetEnabled enables or disables one managed server interface.
// POST /api/managed-servers/{id}/enabled
//
//	@Summary		Toggle managed-server enabled
//	@Description	Brings the named managed server interface up or down (and persists the desired state).
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string					true	"Server id"
//	@Param			body	body		map[string]interface{}	true	"{enabled: bool}"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/enabled [post]
func (h *ManagedServerHandler) SetEnabled(w http.ResponseWriter, r *http.Request, id string) {
	req, ok := parseJSON[enabledToggle](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.SetEnabled(r.Context(), id, req.Enabled); err != nil {
		response.Error(w, err.Error(), "SET_ENABLED_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// ASC handles GET/PUT for ASC parameters of a managed server.
//
// GET /api/managed-servers/{id}/asc — read params
// PUT /api/managed-servers/{id}/asc — write params
//
//	@Summary		Get/set managed-server ASC params
//	@Description	GET reads, PUT writes the AWG signature/obfuscation params for the named managed server. PUT body is the raw ASC JSON object.
//	@Tags			managed-servers
//	@Accept			json
//	@Produce		json
//	@Security		CookieAuth
//	@Param			id		path		string					true	"Server id"
//	@Param			body	body		map[string]interface{}	false	"ASC params (PUT only)"
//	@Success		200		{object}	map[string]interface{}
//	@Failure		400		{object}	map[string]interface{}
//	@Failure		500		{object}	map[string]interface{}
//	@Router			/managed-servers/{id}/asc [get]
//	@Router			/managed-servers/{id}/asc [put]
func (h *ManagedServerHandler) ASC(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		params, err := h.svc.GetASCParams(r.Context(), id)
		if err != nil {
			response.Error(w, err.Error(), "GET_ASC_FAILED")
			return
		}
		response.Success(w, params)
	case http.MethodPut:
		var params json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			response.Error(w, "invalid request body", "INVALID_BODY")
			return
		}
		if err := h.svc.SetASCParams(r.Context(), id, params); err != nil {
			response.Error(w, err.Error(), "SET_ASC_FAILED")
			return
		}
		h.publishServerUpdated()
		h.writeServersSnapshot(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}
