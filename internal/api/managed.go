package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/hoaxisr/awg-manager/internal/managed"
	"github.com/hoaxisr/awg-manager/internal/response"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

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
// to ServersHandler.writeAll with a nil guard. All 8 mutation handlers
// use this so an isolated-test construction (NewManagedServerHandler
// without SetServersHandler) falls back to a safe error response
// instead of a nil pointer panic.
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

// getManaged builds the managed server response for API and SSE snapshots.
func (h *ManagedServerHandler) getManaged() interface{} {
	ms := h.svc.Get()
	if ms == nil {
		return nil
	}
	return toManagedServerResponse(ms)
}

// getManagedStats builds the managed server stats for API and SSE snapshots.
func (h *ManagedServerHandler) getManagedStats(ctx context.Context) interface{} {
	stats, err := h.svc.GetStats(ctx)
	if err != nil {
		return nil
	}
	return stats
}

// SuggestAddress returns a free private /24 for the create-server UI.
// GET /api/managed-server/suggest-address
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

// Get returns the managed server with runtime data, or null if not created.
// GET /api/managed-server
func (h *ManagedServerHandler) Get(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	response.Success(w, h.getManaged())
}

// Stats returns runtime statistics for the managed server peers.
// GET /api/managed-server/stats
func (h *ManagedServerHandler) Stats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	stats, err := h.svc.GetStats(r.Context())
	if err != nil {
		response.Error(w, err.Error(), "STATS_ERROR")
		return
	}
	response.Success(w, stats)
}

// Create creates a new managed WireGuard server.
// POST /api/managed-server/create
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

// Update updates the managed server's address and/or listen port.
// PUT /api/managed-server/update
func (h *ManagedServerHandler) Update(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[managed.UpdateServerRequest](w, r, http.MethodPut)
	if !ok {
		return
	}
	if err := h.svc.Update(r.Context(), req); err != nil {
		response.Error(w, err.Error(), "UPDATE_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// Delete removes the managed server and all peers.
// DELETE /api/managed-server/delete
func (h *ManagedServerHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	if err := h.svc.Delete(r.Context()); err != nil {
		response.Error(w, err.Error(), "DELETE_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// AddPeer adds a new peer to the managed server.
// POST /api/managed-server/peers
func (h *ManagedServerHandler) AddPeer(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[managed.AddPeerRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	peer, err := h.svc.AddPeer(r.Context(), req)
	if err != nil {
		response.Error(w, err.Error(), "ADD_PEER_FAILED")
		return
	}
	response.Success(w, peer)
	h.publishServerUpdated()
}

// UpdatePeer updates an existing peer.
// PUT /api/managed-server/peers/update?pubkey=X
func (h *ManagedServerHandler) UpdatePeer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		response.MethodNotAllowed(w)
		return
	}
	pubkey := r.URL.Query().Get("pubkey")
	if pubkey == "" {
		response.Error(w, "missing pubkey parameter", "MISSING_PUBKEY")
		return
	}
	if !isValidWGKey(pubkey) {
		response.Error(w, "invalid pubkey format", "INVALID_PUBKEY")
		return
	}
	req, ok := parseJSON[managed.UpdatePeerRequest](w, r, http.MethodPut)
	if !ok {
		return
	}
	if err := h.svc.UpdatePeer(r.Context(), pubkey, req); err != nil {
		response.Error(w, err.Error(), "UPDATE_PEER_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// DeletePeer removes a peer from the managed server.
// DELETE /api/managed-server/peers?pubkey=X
func (h *ManagedServerHandler) DeletePeer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		response.MethodNotAllowed(w)
		return
	}
	pubkey := r.URL.Query().Get("pubkey")
	if pubkey == "" {
		response.Error(w, "missing pubkey parameter", "MISSING_PUBKEY")
		return
	}
	if !isValidWGKey(pubkey) {
		response.Error(w, "invalid pubkey format", "INVALID_PUBKEY")
		return
	}
	if err := h.svc.DeletePeer(r.Context(), pubkey); err != nil {
		response.Error(w, err.Error(), "DELETE_PEER_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// TogglePeer enables or disables a peer.
// POST /api/managed-server/peers/toggle
func (h *ManagedServerHandler) TogglePeer(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[managed.TogglePeerRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if req.PublicKey == "" {
		response.Error(w, "missing publicKey", "MISSING_PUBKEY")
		return
	}
	if !isValidWGKey(req.PublicKey) {
		response.Error(w, "invalid publicKey format", "INVALID_PUBKEY")
		return
	}
	if err := h.svc.TogglePeer(r.Context(), req.PublicKey, req.Enabled); err != nil {
		response.Error(w, err.Error(), "TOGGLE_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// PeerConf generates and returns a .conf file for a peer.
// GET /api/managed-server/peers/conf?pubkey=X
func (h *ManagedServerHandler) PeerConf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	pubkey := r.URL.Query().Get("pubkey")
	if pubkey == "" {
		response.Error(w, "missing pubkey parameter", "MISSING_PUBKEY")
		return
	}
	if !isValidWGKey(pubkey) {
		response.Error(w, "invalid pubkey format", "INVALID_PUBKEY")
		return
	}
	conf, err := h.svc.GenerateConf(r.Context(), pubkey)
	if err != nil {
		response.Error(w, err.Error(), "CONF_FAILED")
		return
	}
	response.Success(w, map[string]string{"conf": conf})
}

// enabledToggle is the shared request body for NAT and SetEnabled.
type enabledToggle struct {
	Enabled bool `json:"enabled"`
}

// setPolicyRequest is the request body for /api/managed-server/policy.
type setPolicyRequest struct {
	Policy string `json:"policy"`
}

// SetPolicy updates the ip hotspot policy for the managed server interface.
// POST /api/managed-server/policy
func (h *ManagedServerHandler) SetPolicy(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[setPolicyRequest](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.SetPolicy(r.Context(), req.Policy); err != nil {
		response.Error(w, err.Error(), "POLICY_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// GetPolicies returns every IP Policy profile available on the router,
// for the managed server's policy dropdown.
// GET /api/managed-server/policies
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

// NAT enables or disables NAT on the managed server interface.
// POST /api/managed-server/nat
func (h *ManagedServerHandler) NAT(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[enabledToggle](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.SetNAT(r.Context(), req.Enabled); err != nil {
		response.Error(w, err.Error(), "NAT_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// SetEnabled enables or disables the managed server interface.
// POST /api/managed-server/enabled
func (h *ManagedServerHandler) SetEnabled(w http.ResponseWriter, r *http.Request) {
	req, ok := parseJSON[enabledToggle](w, r, http.MethodPost)
	if !ok {
		return
	}
	if err := h.svc.SetEnabled(r.Context(), req.Enabled); err != nil {
		response.Error(w, err.Error(), "SET_ENABLED_FAILED")
		return
	}
	h.publishServerUpdated()
	h.writeServersSnapshot(w, r)
}

// ASC handles GET/POST for ASC parameters of the managed server.
// GET /api/managed-server/asc — get ASC params
// POST /api/managed-server/asc — set ASC params
func (h *ManagedServerHandler) ASC(w http.ResponseWriter, r *http.Request) {
	ifaceName := h.svc.GetInterfaceName()
	if ifaceName == "" {
		response.Error(w, "no managed server exists", "NO_SERVER")
		return
	}

	// Delegate to the system tunnel ASC handler pattern
	switch r.Method {
	case http.MethodGet:
		params, err := h.svc.GetASCParams(r.Context())
		if err != nil {
			response.Error(w, err.Error(), "GET_ASC_FAILED")
			return
		}
		response.Success(w, params)
	case http.MethodPost:
		var params json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			response.Error(w, "invalid request body", "INVALID_BODY")
			return
		}
		if err := h.svc.SetASCParams(r.Context(), params); err != nil {
			response.Error(w, err.Error(), "SET_ASC_FAILED")
			return
		}
		h.publishServerUpdated()
		h.writeServersSnapshot(w, r)
	default:
		response.MethodNotAllowed(w)
	}
}
